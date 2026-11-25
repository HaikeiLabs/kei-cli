package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/updatecheck"
)

// Keep every test in this package off the network and out of the real state
// dir: a test that needs the release check installs its own stubs.
func init() {
	updateCheckResolve = func(context.Context) (updatecheck.Versions, error) {
		return updatecheck.Versions{}, errors.New("release lookups are disabled in tests")
	}
	updateCheckStatePath = func() (string, error) {
		return filepath.Join(os.TempDir(), fmt.Sprintf("kei-update-check-test-%d.json", os.Getpid())), nil
	}
	updateCheckInstalledProxy = func() string { return "" }
	updateCheckConfigDisabled = func() bool { return false }
}

type updateCheckStubs struct {
	statePath string
	resolves  int
}

// stubUpdateCheck points the update check at a temp state file, a fixed
// latest release and installed kei-proxy, and a fake terminal.
func stubUpdateCheck(t *testing.T, latest updatecheck.Versions, resolveErr error, installedProxy string, env map[string]string) *updateCheckStubs {
	t.Helper()
	s := &updateCheckStubs{statePath: filepath.Join(t.TempDir(), "kei-update-check.json")}
	saved := struct {
		getenv   func(string) string
		path     func() (string, error)
		resolve  func(context.Context) (updatecheck.Versions, error)
		proxy    func() string
		disabled func() bool
		tty      func(any) bool
	}{updateCheckGetenv, updateCheckStatePath, updateCheckResolve, updateCheckInstalledProxy, updateCheckConfigDisabled, updateCheckStdoutTTY}
	t.Cleanup(func() {
		updateCheckGetenv, updateCheckStatePath, updateCheckResolve = saved.getenv, saved.path, saved.resolve
		updateCheckInstalledProxy, updateCheckConfigDisabled, updateCheckStdoutTTY = saved.proxy, saved.disabled, saved.tty
	})
	updateCheckGetenv = func(k string) string { return env[k] }
	updateCheckStatePath = func() (string, error) { return s.statePath, nil }
	updateCheckResolve = func(context.Context) (updatecheck.Versions, error) {
		s.resolves++
		return latest, resolveErr
	}
	updateCheckInstalledProxy = func() string { return installedProxy }
	updateCheckConfigDisabled = func() bool { return false }
	updateCheckStdoutTTY = func(any) bool { return true }
	return s
}

func runUpdateNotice(command string, args []string, version string) string {
	var stdout, stderr bytes.Buffer
	finish := startUpdateNotice(command, args, &stdout, &stderr, version)
	finish()
	return stderr.String()
}

func TestUpdateNoticePrintsOneStderrLine(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{Kei: "0.4.0", Proxy: "0.12.0"}, nil, "0.12.0", nil)
	got := runUpdateNotice("workspaces", []string{"list"}, "v0.3.7")
	if got != "kei 0.4.0 is available (you have 0.3.7) — run kei upgrade\n" {
		t.Fatalf("stderr = %q", got)
	}
	if again := runUpdateNotice("workspaces", []string{"list"}, "v0.3.7"); again != "" {
		t.Fatalf("second command the same day printed %q", again)
	}
}

func TestUpdateNoticeBundledProxyNewer(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{Kei: "0.3.7", Proxy: "0.12.0"}, nil, "0.9.1", nil)
	got := runUpdateNotice("bot", []string{"list"}, "0.3.7")
	if got != "kei-proxy 0.12.0 is available (you have 0.9.1) — run kei upgrade\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestUpdateNoticeSuppressed(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		env     map[string]string
		config  bool
		notTTY  bool
	}{
		{name: "KEI_NO_UPDATE_CHECK", command: "bot", args: []string{"list"}, env: map[string]string{"KEI_NO_UPDATE_CHECK": "1"}},
		{name: "config update_check false", command: "bot", args: []string{"list"}, config: true},
		{name: "json output", command: "bot", args: []string{"list", "--json"}},
		{name: "quiet output", command: "harness", args: []string{"sync", "--quiet"}},
		{name: "stdout not a terminal", command: "bot", args: []string{"list"}, notTTY: true},
		{name: "upgrade itself", command: "upgrade"},
		{name: "version", command: "--version"},
		{name: "help", command: "help"},
		{name: "doctor reports versions itself", command: "doctor"},
		{name: "login prints its own summary", command: "login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stubUpdateCheck(t, updatecheck.Versions{Kei: "0.4.0"}, nil, "", tt.env)
			updateCheckConfigDisabled = func() bool { return tt.config }
			if tt.notTTY {
				updateCheckStdoutTTY = func(any) bool { return false }
			}
			if got := runUpdateNotice(tt.command, tt.args, "0.3.7"); got != "" {
				t.Fatalf("stderr = %q, want nothing", got)
			}
			if s.resolves != 0 {
				t.Fatalf("release index consulted %d times while suppressed", s.resolves)
			}
		})
	}
}

func TestUpdateNoticeOfflineIsSilent(t *testing.T) {
	s := stubUpdateCheck(t, updatecheck.Versions{}, errors.New("dial tcp: no route to host"), "", nil)
	if got := runUpdateNotice("bot", []string{"list"}, "0.3.7"); got != "" {
		t.Fatalf("stderr = %q, want nothing offline", got)
	}
	runUpdateNotice("bot", []string{"list"}, "0.3.7")
	if s.resolves != 1 {
		t.Fatalf("offline lookup retried %d times within a day, want 1", s.resolves)
	}
}

func TestUpdateNoticeDebugLogOnly(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{}, errors.New("dial tcp: no route to host"), "", map[string]string{"KEI_DEBUG": "1"})
	got := runUpdateNotice("bot", []string{"list"}, "0.3.7")
	if !strings.HasPrefix(got, "kei debug: update check:") {
		t.Fatalf("stderr = %q, want a debug line with KEI_DEBUG set", got)
	}
}

func TestUpdateNoticeNeverWaitsPastTimeout(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{}, nil, "", nil)
	updateCheckResolve = func(ctx context.Context) (updatecheck.Versions, error) {
		<-ctx.Done()
		time.Sleep(5 * time.Second)
		return updatecheck.Versions{Kei: "9.9.9"}, nil
	}
	start := time.Now()
	got := runUpdateNotice("bot", []string{"list"}, "0.3.7")
	if elapsed := time.Since(start); elapsed > updateCheckTimeout+time.Second {
		t.Fatalf("notice waited %v, want at most about %v", elapsed, updateCheckTimeout)
	}
	if got != "" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestMainSkipsUpdateNoticeWithBufferedStdout(t *testing.T) {
	s := stubUpdateCheck(t, updatecheck.Versions{Kei: "0.4.0"}, nil, "", nil)
	updateCheckStdoutTTY = writerIsTerminalAny
	var stdout, stderr bytes.Buffer
	Main("0.3.7", "help", nil, &stdout, &stderr, strings.NewReader(""))
	Main("0.3.7", "bogus", nil, &stdout, &stderr, strings.NewReader(""))
	if strings.Contains(stderr.String(), "is available") || s.resolves != 0 {
		t.Fatalf("notice with non-terminal stdout: %q (resolves %d)", stderr.String(), s.resolves)
	}
}

func TestConfigUpdateCheckSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kei.yaml")
	if err := os.WriteFile(path, []byte("update_check: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.UpdateCheckDisabled {
		t.Fatal("update_check: false did not disable the update check")
	}
	if !configDisablesUpdateCheck(path) {
		t.Fatal("configDisablesUpdateCheck = false")
	}
	if err := os.WriteFile(path, []byte("update_check: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if configDisablesUpdateCheck(path) {
		t.Fatal("update_check: true disabled the check")
	}
	if configDisablesUpdateCheck(filepath.Join(t.TempDir(), "missing.yaml")) {
		t.Fatal("a missing config disabled the check")
	}
	if err := os.WriteFile(path, []byte("update_check: maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntimeConfig(path); err == nil {
		t.Fatal("invalid update_check value accepted")
	}
}

func TestSaveRuntimeConfigKeepsUpdateCheckSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kei.yaml")
	config := runtimeConfig{ControlPlaneURL: "https://runtime.example", RuntimeToken: "token", HarnessURL: "http://127.0.0.1:8088", UpdateCheckDisabled: true}
	if err := saveRuntimeConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UpdateCheckDisabled {
		t.Fatal("saved config lost update_check: false")
	}
	config.UpdateCheckDisabled = false
	if err := saveRuntimeConfig(path, config); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "update_check") {
		t.Fatalf("default config writes update_check:\n%s", data)
	}
}

func loginSummaryServer(t *testing.T, installationsStatus int, installations string, org string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/organizations/org-1/runtime-installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCLIToken {
			t.Errorf("installations request without the CLI token")
		}
		w.WriteHeader(installationsStatus)
		_, _ = w.Write([]byte(installations))
	})
	mux.HandleFunc("/api/v1/organizations/org-1", func(w http.ResponseWriter, r *http.Request) {
		if org == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(org))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestLoginUpdateSummary(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{Kei: "0.4.0", Proxy: "0.12.0"}, nil, "0.9.1", nil)
	installations, _ := json.Marshal(map[string]any{"runtime_installations": []map[string]any{
		{"id": "i-1", "display_name": "laptop", "runtime_version": "0.12.0", "below_minimum_version": false},
		{"id": "i-2", "display_name": "dvl-openwebui", "runtime_version": "0.10.1", "below_minimum_version": true},
		{"id": "i-3", "display_name": "pending", "below_minimum_version": false},
	}})
	server := loginSummaryServer(t, http.StatusOK, string(installations), `{"id":"org-1","name":"Acme","min_runtime_version":"0.12.0"}`)
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout bytes.Buffer
	writeLoginUpdateSummary(context.Background(), &stdout, server.Client(), server.URL, store, "v0.3.7")
	out := stdout.String()
	for _, want := range []string{
		"Versions: kei 0.3.7 installed, 0.4.0 latest; kei-proxy 0.9.1 installed, 0.12.0 latest\n",
		"kei 0.4.0 is available (you have 0.3.7; kei-proxy 0.9.1 -> 0.12.0) — run kei upgrade\n",
		"1 runtime installation is below the org minimum runtime version (0.12.0):\n",
		"  dvl-openwebui: kei-proxy 0.10.1, needs 0.12.0 or later; missing:",
		"  Upgrade: run kei upgrade on each runtime host",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testCLIToken) {
		t.Fatal("summary printed the CLI token")
	}
}

func TestLoginUpdateSummaryFailuresAreSilent(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{}, errors.New("offline"), "", nil)
	server := loginSummaryServer(t, http.StatusInternalServerError, `{"error":"boom"}`, "")
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout bytes.Buffer
	writeLoginUpdateSummary(context.Background(), &stdout, server.Client(), server.URL, store, "v0.3.7")
	if stdout.Len() != 0 {
		t.Fatalf("summary printed %q when every lookup failed", stdout.String())
	}
}

func TestLoginUpdateSummaryOrgMinimumUnknown(t *testing.T) {
	stubUpdateCheck(t, updatecheck.Versions{}, errors.New("offline"), "", nil)
	installations := `{"runtime_installations":[{"id":"i-2","display_name":"teams","runtime_version":"0.9.0","below_minimum_version":true}]}`
	server := loginSummaryServer(t, http.StatusOK, installations, "")
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout bytes.Buffer
	writeLoginUpdateSummary(context.Background(), &stdout, server.Client(), server.URL, store, "v0.3.7")
	if !strings.Contains(stdout.String(), "  teams: kei-proxy 0.9.0, needs the org minimum; missing:") {
		t.Fatalf("summary:\n%s", stdout.String())
	}
}

func TestLoginUpdateSummaryReleaseCheckDisabled(t *testing.T) {
	s := stubUpdateCheck(t, updatecheck.Versions{Kei: "0.4.0"}, nil, "", map[string]string{"KEI_NO_UPDATE_CHECK": "1"})
	server := loginSummaryServer(t, http.StatusOK, `{"runtime_installations":[]}`, "")
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout bytes.Buffer
	writeLoginUpdateSummary(context.Background(), &stdout, server.Client(), server.URL, store, "v0.3.7")
	if s.resolves != 0 || strings.Contains(stdout.String(), "Versions:") {
		t.Fatalf("release check ran although disabled: %q", stdout.String())
	}
	if stdout.String() != "Runtime installations: none below the org minimum runtime version\n" {
		t.Fatalf("summary = %q", stdout.String())
	}
}
