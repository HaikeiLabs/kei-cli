package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeHarnessConfig writes the runtime config Main's harness commands load.
func writeHarnessConfig(t *testing.T, path, controlPlaneURL string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "control_plane_url: " + controlPlaneURL + "\nruntime_token: test-token\nharness_url: " + controlPlaneURL + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMainHarnessSyncSendsXKeiClient checks the real Main wiring: every
// control-plane request from `kei harness sync --dry-run` carries the client
// header, with the dispatched subcommand path and no flags or values.
func TestMainHarnessSyncSendsXKeiClient(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"build version", "v1.2.3", "kei-cli/v1.2.3; cmd=harness.sync"},
		{"unset version", "", "kei-cli/dev; cmd=harness.sync"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			var mu sync.Mutex
			var headers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				headers = append(headers, r.Header.Get("X-Kei-Client"))
				mu.Unlock()
				if r.URL.Path == "/api/v1/runtime/whoami" {
					_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
					return
				}
				http.Error(w, "bundle unavailable", http.StatusInternalServerError)
			}))
			defer server.Close()
			t.Setenv("KEI_WEB_URL", server.URL)
			writeHarnessConfig(t, filepath.Join(home, ".config", "kei.yaml"), server.URL)

			var stdout, stderr bytes.Buffer
			Main(tt.version, "harness", []string{"sync", "--dry-run"}, &stdout, &stderr, strings.NewReader(""))

			mu.Lock()
			defer mu.Unlock()
			if len(headers) == 0 {
				t.Fatalf("no control-plane request was made; stderr=%s", stderr.String())
			}
			for i, got := range headers {
				if got != tt.want {
					t.Fatalf("request %d X-Kei-Client = %q, want %q; stderr=%s", i, got, tt.want, stderr.String())
				}
			}
		})
	}
}

func TestDispatchedCommandPathNeverCarriesArguments(t *testing.T) {
	tests := []struct {
		command string
		args    []string
		want    string
	}{
		{"harness", []string{"sync", "--dry-run"}, "harness.sync"},
		{"groups", []string{"members", "add", "11111111-1111-1111-1111-111111111111", "--workspace", "Main"}, "groups.members.add"},
		{"users", []string{"get", "22222222-2222-2222-2222-222222222222"}, "users.get"},
		{"bot", []string{"init", "--platform", "cli", "--name", "My Bot"}, "bot.init"},
		{"skills", []string{"status"}, "skills.status"},
		{"login", []string{"--no-browser"}, "login"},
	}
	for _, tt := range tests {
		if got := dispatchedCommandPath(tt.command, tt.args); got != tt.want {
			t.Errorf("dispatchedCommandPath(%q, %q) = %q, want %q", tt.command, tt.args, got, tt.want)
		}
	}
	if got := clientHeaderValue("", "harness.sync"); got != "kei-cli/dev; cmd=harness.sync" {
		t.Errorf("unset version header = %q", got)
	}
}

// TestMainSkillsStatusDoesNotSendXKeiClientToGitHub checks that a request to
// a non-Kei host (the GitHub contents API used by skills status) does not
// carry the control-plane header.
func TestMainSkillsStatusDoesNotSendXKeiClientToGitHub(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())

	var mu sync.Mutex
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("X-Kei-Client"))
		mu.Unlock()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	haikeiSkillsAPI(t, server)
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei": haikeiLockEntry("old-hash"),
	})

	var stdout, stderr bytes.Buffer
	if code := Main("v1.2.3", "skills", []string{"status"}, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("skills status exit=%d stderr=%s", code, stderr.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) == 0 {
		t.Fatal("skills status made no upstream request")
	}
	for i, got := range headers {
		if got != "" {
			t.Fatalf("request %d to non-Kei host carried X-Kei-Client = %q", i, got)
		}
	}
}
