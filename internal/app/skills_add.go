package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// haikeiSkillsRepo is the source the skills CLI installs Haikei's published
// skills from.
const haikeiSkillsRepo = "HaikeiLabs/skills"

// haikeiSkillNames are the Haikei skills `kei skills add` installs; haikei is
// always included.
var haikeiSkillNames = []string{"haikei", "kei-cli", "kei-harness-setup"}

// skillsAgentKinds maps a Kei harness kind to the agent id the skills CLI
// installs into. A harness with no mapping (custom, openwebui) is skipped.
var skillsAgentKinds = map[string]string{
	"claude_code": "claude-code",
	"codex":       "codex",
	"opencode":    "opencode",
}

// detectedSkillsAgents lists the skills CLI agent ids for the harnesses
// installed locally, in registry order.
func detectedSkillsAgents(registry *harness.Registry, env harness.Env) []string {
	var agents []string
	for _, h := range registry.Detected(env) {
		if agent, ok := skillsAgentKinds[h.Kind()]; ok {
			agents = append(agents, agent)
		}
	}
	return agents
}

// npxSkillsAddArgs is the skills CLI invocation that installs the Haikei
// skills globally, for the detected agents.
func npxSkillsAddArgs(agents []string) []string {
	args := []string{"-y", "skills", "add", haikeiSkillsRepo, "-g"}
	for _, name := range haikeiSkillNames {
		args = append(args, "--skill", name)
	}
	for _, agent := range agents {
		args = append(args, "-a", agent)
	}
	return args
}

// runSkillsAdd is `kei skills add`: it installs the Haikei skills with the
// skills CLI. There is no installer here — when npx is not on PATH it prints
// the command to run instead.
func runSkillsAdd(stdout, stderr io.Writer) int {
	args := npxSkillsAddArgs(detectedSkillsAgents(harness.Default, harness.OSEnv()))
	npxPath, err := exec.LookPath("npx")
	if err != nil {
		fmt.Fprintf(stdout, "npx is not on PATH; run: npx %s\n", strings.Join(args, " "))
		return 0
	}
	cmd := runCommand(context.Background(), npxPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(stderr, "skills add: %v\n", err)
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "skills add: %v\n", err)
		return 1
	}
	return 0
}
