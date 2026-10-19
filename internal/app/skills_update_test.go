package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeNpx puts a script named npx on PATH that logs its argv and exits with the
// given code, so no test runs the real skills CLI.
func fakeNpx(t *testing.T, exitCode int) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "npx.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\n"
	if exitCode != 0 {
		script += "echo 'skills CLI failed' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return logPath
}

func writeStaleHaikeiLock(t *testing.T, home string, hashes map[string]string) {
	t.Helper()
	entries := map[string]map[string]string{}
	for name, hash := range hashes {
		entries[name] = haikeiLockEntry(hash)
	}
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), entries)
}

func TestSkillsUpdateRunsNpxForStaleSkills(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new", "kei-cli": "same-hash"})
	haikeiSkillsAPI(t, server)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash", "kei-cli": "same-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"update"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, stderr.String())
	}
	invocations, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(invocations)); got != "-y skills update -g haikei -y" {
		t.Fatalf("npx argv = %q, want %q", got, "-y skills update -g haikei -y")
	}
	if !strings.Contains(stdout.String(), "1 Haikei skill") {
		t.Fatalf("stdout does not report the update:\n%s", stdout.String())
	}
}

func TestSkillsUpdatePrintsTheCommandWithoutNpx(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new"})
	haikeiSkillsAPI(t, server)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	t.Setenv("PATH", t.TempDir()) // no npx anywhere

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"update"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "npx -y skills update -g haikei -y") {
		t.Fatalf("stdout does not print the command to run:\n%s", stdout.String())
	}
}

func TestSkillsUpdateDoesNothingWhenUpToDate(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "same-hash"})
	haikeiSkillsAPI(t, server)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "same-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"update"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("npx ran when every skill was up to date")
	}
}

func TestSkillsUpdatePropagatesNpxFailure(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new"})
	haikeiSkillsAPI(t, server)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	fakeNpx(t, 3)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"update"}, &stdout, &stderr, server.Client()); code != 3 {
		t.Fatalf("update exit=%d, want the npx exit code 3", code)
	}
	if !strings.Contains(stderr.String(), "skills CLI failed") {
		t.Fatalf("npx stderr not passed through:\n%s", stderr.String())
	}
}

func TestSkillsUpdateRejectsBadUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"update", "extra"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("update with an argument exit=%d, want 2", code)
	}
}
