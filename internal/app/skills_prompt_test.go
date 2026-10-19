package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func staleUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new"})
	haikeiSkillsAPI(t, server)
	return server
}

// skillsPromptTestEnv fixes the environment the prompt reads, so the
// environment the tests run in (an agent, CI) cannot change the outcome.
func skillsPromptTestEnv(t *testing.T, values map[string]string) {
	t.Helper()
	prev := skillsPromptGetenv
	skillsPromptGetenv = func(key string) string { return values[key] }
	t.Cleanup(func() { skillsPromptGetenv = prev })
}

func TestSkillsPromptOffersUpdateWhenStale(t *testing.T) {
	skillsPromptTestEnv(t, nil)
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &stdout, &stderr, server.Client(), false)
	if !strings.Contains(stdout.String(), "Your Haikei skills look out of date (1 of 1). Update now? [Y/n]") {
		t.Fatalf("prompt:\n%s", stdout.String())
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "-y skills update -g haikei -y") {
		t.Fatalf("the yes answer did not update:\n%s", invocations)
	}
}

func TestSkillsPromptRemembersNo(t *testing.T) {
	skillsPromptTestEnv(t, nil)
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("n\n"), true, &stdout, &stderr, server.Client(), false)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("npx ran after the user said no")
	}
	var again bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &again, &stderr, server.Client(), false)
	if strings.Contains(again.String(), "out of date") {
		t.Fatalf("asked again after no:\n%s", again.String())
	}
}

func TestSkillsPromptAsksAtMostOnceADay(t *testing.T) {
	skillsPromptTestEnv(t, nil)
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &stdout, &stderr, server.Client(), false)
	var again bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &again, &stderr, server.Client(), false)
	if strings.Contains(again.String(), "Update now?") {
		t.Fatalf("asked twice in one day:\n%s", again.String())
	}
	invocations, _ := os.ReadFile(log)
	if got := strings.Count(string(invocations), "-y skills update"); got != 1 {
		t.Fatalf("update ran %d times, want 1:\n%s", got, invocations)
	}
}

func TestSkillsPromptPrintsOneLineForAgents(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	skillsPromptTestEnv(t, map[string]string{"OPENCODE": "1"})
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader(""), false, &stdout, &stderr, server.Client(), false)
	if !strings.Contains(stdout.String(), "run kei skills update") {
		t.Fatalf("agent line:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "[Y/n]") {
		t.Fatalf("an agent was prompted:\n%s", stdout.String())
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("an agent answer ran npx")
	}
}

func TestSkillsPromptSkipsInCIAndWhenNotInteractive(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})

	skillsPromptTestEnv(t, map[string]string{"CI": "true"})
	var ciOut bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &ciOut, &bytes.Buffer{}, server.Client(), false)
	if ciOut.Len() != 0 {
		t.Fatalf("CI saw output:\n%s", ciOut.String())
	}

	skillsPromptTestEnv(t, nil)
	var pipeOut bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), false, &pipeOut, &bytes.Buffer{}, server.Client(), false)
	if pipeOut.Len() != 0 {
		t.Fatalf("non-interactive stdin saw output:\n%s", pipeOut.String())
	}
}

func TestSkillsPromptSilentWhenUpToDate(t *testing.T) {
	skillsPromptTestEnv(t, nil)
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{"haikei": "same-hash"})
	haikeiSkillsAPI(t, server)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "same-hash"})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &stdout, &stderr, server.Client(), false)
	if stdout.Len() != 0 {
		t.Fatalf("prompted with nothing to update:\n%s", stdout.String())
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("npx ran with nothing to update")
	}
}

func TestSkillsPromptForcedIgnoresRememberedNo(t *testing.T) {
	skillsPromptTestEnv(t, nil)
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})
	writeTestSkillsPromptState(t, home, skillsPromptState{LastPromptAt: time.Now(), Declined: true})
	log := fakeNpx(t, 0)

	var stdout, stderr bytes.Buffer
	maybePromptSkillsUpdate(strings.NewReader("y\n"), true, &stdout, &stderr, server.Client(), true)
	if !strings.Contains(stdout.String(), "Update now?") {
		t.Fatalf("--install-skills did not force the prompt:\n%s", stdout.String())
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "-y skills update -g haikei -y") {
		t.Fatalf("forced prompt did not update:\n%s", invocations)
	}
}

// writeTestSkillsPromptState writes the prompt state the way the CLI does.
func writeTestSkillsPromptState(t *testing.T, home string, state skillsPromptState) {
	t.Helper()
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", skillsPromptStateFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoginOffersSkillsUpdate keeps the login wiring honest: the prompt is
// called from runLoginCommand, so removing it fails here.
func TestLoginOffersSkillsUpdate(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	skillsPromptTestEnv(t, map[string]string{"OPENCODE": "1"}) // print one line instead of prompting
	upstream := staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode: "device", UserCode: "ABCDE-12345", ExpiresIn: 60, Interval: 1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: "cli-login-token"})
	})
	loginServer := httptest.NewServer(mux)
	t.Cleanup(loginServer.Close)
	t.Setenv("KEI_WEB_URL", loginServer.URL)

	var stdout, stderr bytes.Buffer
	if code := runLoginCommand("v0.0.1", []string{"--no-browser"}, &stdout, &stderr, loginServer.Client(), &memoryCredentialStore{}); code != 0 {
		t.Fatalf("login exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run kei skills update") {
		t.Fatalf("login did not point at kei skills update:\n%s", stdout.String())
	}
	_ = upstream
}

// TestHarnessSyncOffersSkillsUpdate keeps the sync wiring honest.
func TestHarnessSyncOffersSkillsUpdate(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	skillsPromptTestEnv(t, map[string]string{"OPENCODE": "1"})
	bundleServer := skillsBundleServer(t)
	config := writeSkillsConfig(t, bundleServer.URL, "")
	staleUpstream(t)
	writeStaleHaikeiLock(t, home, map[string]string{"haikei": "old-hash"})

	var stdout, stderr bytes.Buffer
	if code := runHarnessCommand([]string{"sync", "--config", config, "--harness", "claude_code"}, &stdout, &stderr, bundleServer.Client(), &memoryCredentialStore{}); code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run kei skills update") {
		t.Fatalf("harness sync did not point at kei skills update:\n%s", stdout.String())
	}
}
