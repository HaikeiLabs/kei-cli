package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// skillsPromptStateFile remembers the last stale-skills prompt in the Kei
	// config dir, so the CLI asks at most once a day and a "no" sticks.
	skillsPromptStateFile = "kei-skills-prompt.json"
)

// agentEnvVars are set by the supported coding harnesses when the kei CLI runs
// inside an agent, where a person cannot answer the prompt.
var agentEnvVars = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CODEX_SANDBOX", "CURSOR_TRACE_ID", "OPENCODE", "AI_AGENT"}

// skillsPromptState is the remembered outcome of the stale-skills prompt.
type skillsPromptState struct {
	LastPromptAt time.Time `json:"last_prompt_at"`
	Declined     bool      `json:"declined"`
}

// skillsPromptStdin is where the stale-skills prompt reads the answer from; it
// is a var so tests drive the prompt, and the CLI reads os.Stdin.
var skillsPromptStdin io.Reader = os.Stdin

// skillsPromptGetenv reads the environment the prompt's CI and agent checks
// use; it is a var so tests are not affected by the environment they run in.
var skillsPromptGetenv = os.Getenv

func calledByAgent(getenv func(string) string) bool {
	for _, key := range agentEnvVars {
		if getenv(key) != "" {
			return true
		}
	}
	return false
}

func inCI(getenv func(string) string) bool {
	value := getenv("CI")
	return value != "" && value != "0" && !strings.EqualFold(value, "false")
}

func defaultSkillsPromptStatePath() (string, error) {
	configPath, err := defaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), skillsPromptStateFile), nil
}

func readSkillsPromptState(path string) skillsPromptState {
	var state skillsPromptState
	contents, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(contents, &state)
	return state
}

// writeSkillsPromptState is best effort: a state file that cannot be written
// only costs an extra prompt later.
func writeSkillsPromptState(path string, state skillsPromptState) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	body, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, body, 0o600)
}

// maybePromptSkillsUpdate offers to update stale Haikei skills after kei login
// and kei harness sync. It never changes the caller's exit code, never waits on
// a pipe, and stays quiet in CI, inside an agent (where it prints one line
// pointing at kei skills update) and when the user already said no. force
// (--install-skills) ignores the daily gate and a remembered no.
func maybePromptSkillsUpdate(stdin io.Reader, interactive bool, stdout, stderr io.Writer, client *http.Client, force bool) {
	statePath, err := defaultSkillsPromptStatePath()
	if err != nil {
		return
	}
	state := readSkillsPromptState(statePath)
	now := time.Now()
	if !force && (state.Declined || now.Sub(state.LastPromptAt) < skillsUpstreamCacheTTL) {
		return
	}
	agent := calledByAgent(skillsPromptGetenv)
	if !agent && !force && (inCI(skillsPromptGetenv) || !interactive) {
		return
	}
	cachePath, err := defaultSkillsUpstreamCachePath()
	if err != nil {
		return
	}
	stale, total, err := staleHaikeiSkills(context.Background(), client, cachePath, now)
	if err != nil || total == 0 || len(stale) == 0 {
		return
	}
	if agent || !interactive {
		fmt.Fprintf(stdout, "Your Haikei skills look out of date (%d of %d); run kei skills update.\n", len(stale), total)
		state.LastPromptAt = now
		writeSkillsPromptState(statePath, state)
		return
	}
	fmt.Fprintf(stdout, "Your Haikei skills look out of date (%d of %d). Update now? [Y/n] ", len(stale), total)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	state.LastPromptAt = now
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		state.Declined = false
		runSkillsUpdate(stdout, stderr, client)
	default:
		state.Declined = true
	}
	writeSkillsPromptState(statePath, state)
}
