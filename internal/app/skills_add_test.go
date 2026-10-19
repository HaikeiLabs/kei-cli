package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsAddRunsNpxForDetectedAgents(t *testing.T) {
	skillsHome(t) // creates ~/.claude, so Claude Code is the one detected harness
	t.Chdir(t.TempDir())
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"add"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	want := "-y skills add HaikeiLabs/skills -g --skill haikei --skill kei-cli --skill kei-harness-setup -a claude-code"
	invocations, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(invocations)); got != want {
		t.Fatalf("npx argv = %q, want %q", got, want)
	}
}

func TestSkillsAddOmitsAgentsWhenNoneAreInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("OPENCODE_CONFIG", "")
	t.Chdir(t.TempDir())
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"add"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	invocations, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(invocations)); got != "-y skills add HaikeiLabs/skills -g --skill haikei --skill kei-cli --skill kei-harness-setup" {
		t.Fatalf("npx argv = %q", got)
	}
}

func TestSkillsAddPrintsTheCommandWithoutNpx(t *testing.T) {
	skillsHome(t)
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no npx anywhere

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"add"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "npx -y skills add HaikeiLabs/skills -g --skill haikei --skill kei-cli --skill kei-harness-setup -a claude-code") {
		t.Fatalf("stdout does not print the command to run:\n%s", stdout.String())
	}
}

func TestSkillsAddRejectsBadUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"add", "extra"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("add with an argument exit=%d, want 2", code)
	}
	if code := runSkillsCommand([]string{"add", "--harness", "claude_code"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("add with a flag exit=%d, want 2", code)
	}
}
