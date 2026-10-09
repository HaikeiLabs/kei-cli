package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

const (
	testOWUIInstallation    = "99999999-9999-9999-9999-999999999999"
	testOWUIUnknownHealth   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testOpenWebUIAdminToken = "sk-owui-admin-secret"
)

// openWebUIListConsole serves the org's runtime installations: one healthy
// Open WebUI, one whose status read fails, and a Teams one to filter out.
func openWebUIListConsole(t *testing.T) (*fakeConsole, *httptest.Server, *memoryCredentialStore) {
	t.Helper()
	heartbeat := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Path {
		case "/api/v1/organizations/org-1/runtime-installations":
			_, _ = w.Write([]byte(`{"runtime_installations":[
				{"id":"` + testOWUIInstallation + `","platform":"openwebui","display_name":"chat","status":"active","last_heartbeat_at":"` + heartbeat + `"},
				{"id":"` + testOWUIUnknownHealth + `","platform":"openwebui","display_name":"staging","status":"pending"},
				{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","platform":"teams","display_name":"teams-bot","status":"active"}]}`))
		case "/api/v1/organizations/org-1/runtime-installations/" + testOWUIInstallation:
			_, _ = w.Write([]byte(`{"id":"` + testOWUIInstallation + `","status":"active","binding_status":"bound","runtime_version":"kei-proxy 0.4.2","last_heartbeat_at":"` + heartbeat + `","policy_bundle":{"schema_version":1,"state":"current","policy_revision":17}}`))
		case "/api/v1/organizations/org-1/runtime-installations/" + testOWUIUnknownHealth:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store.token = testCLIToken
	return fake, server, store
}

func TestHarnessListShowsOpenWebUIInstallations(t *testing.T) {
	_, server, store := openWebUIListConsole(t)
	var stdout, stderr bytes.Buffer
	if code := runHarnessCommand([]string{"list"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "OPEN WEBUI") || strings.Contains(stdout.String(), "teams-bot") {
		t.Fatalf("output=%q", stdout.String())
	}
	if got := strings.Fields(lines[1]); strings.Join(got, " ") != "chat "+testOWUIInstallation+" active 2m ago kei-proxy 0.4.2 17 current" {
		t.Fatalf("healthy row = %q", lines[1])
	}
	if got := strings.Fields(lines[2]); strings.Join(got, " ") != "staging "+testOWUIUnknownHealth+" pending - - - -" {
		t.Fatalf("unknown-health row = %q", lines[2])
	}
}

func TestHarnessListJSONIncludesOpenWebUIInstallations(t *testing.T) {
	_, server, store := openWebUIListConsole(t)
	var stdout, stderr bytes.Buffer
	if code := runHarnessCommand([]string{"list", "--json"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out struct {
		OpenWebUI []harness.OpenWebUIInstallation `json:"openwebui_installations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.OpenWebUI) != 2 {
		t.Fatalf("json=%s err=%v", stdout.String(), err)
	}
	if got := out.OpenWebUI[0]; got.KeiProxyVersion == nil || *got.KeiProxyVersion != "kei-proxy 0.4.2" || got.BundleRevision == nil || *got.BundleRevision != 17 {
		t.Fatalf("installation = %+v", got)
	}
}

func runOpenWebUISync(t *testing.T, functions string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("OPENWEBUI_ADMIN_TOKEN", testOpenWebUIAdminToken)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/functions/" {
			t.Errorf("Open WebUI got %s %s; sync must be read-only", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testOpenWebUIAdminToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(functions))
	}))
	t.Cleanup(server.Close)
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand(append([]string{"sync", "--harness", "openwebui", "--url", server.URL}, args...), &stdout, &stderr, server.Client(), &memoryCredentialStore{})
	if strings.Contains(stdout.String()+stderr.String(), testOpenWebUIAdminToken) {
		t.Fatalf("admin token printed: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	return code, stdout.String(), stderr.String()
}

func TestHarnessSyncOpenWebUIReportsOK(t *testing.T) {
	code, stdout, stderr := runOpenWebUISync(t, `[{"id":"kei","is_active":true},{"id":"kei_guard","is_active":true,"is_global":true}]`)
	if code != 0 || !strings.Contains(stdout, "In sync") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestHarnessSyncOpenWebUIDriftExits1WithRestartHint(t *testing.T) {
	for name, functions := range map[string]string{
		"missing":  `[{"id":"kei","is_active":true}]`,
		"inactive": `[{"id":"kei","is_active":true},{"id":"kei_guard","is_active":false,"is_global":true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, _ := runOpenWebUISync(t, functions)
			if code != 1 || !strings.Contains(stdout, "kei_guard): "+name) || !strings.Contains(stdout, "restart the Open WebUI container") {
				t.Fatalf("exit=%d stdout=%q", code, stdout)
			}
		})
	}
}

func TestHarnessSyncOpenWebUIFlagValidation(t *testing.T) {
	t.Setenv("OPENWEBUI_ADMIN_TOKEN", testOpenWebUIAdminToken)
	for _, args := range [][]string{
		{"sync", "--harness", "openwebui"},
		{"sync", "--harness", "openwebui", "--url", "https://chat.example.com", "--check", "--json"},
		{"sync", "--harness", "claude_code", "--url", "https://chat.example.com"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runHarnessCommand(args, &stdout, &stderr, http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Errorf("%v: exit=%d stderr=%q", args, code, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), testOpenWebUIAdminToken) {
			t.Errorf("%v printed the admin token", args)
		}
	}
}

func TestHarnessAddOpenWebUIFailsFastWithoutServer(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
	})
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--kind", "openwebui", "--installation", testOWUIInstallation, "--agent", testOWUIUnknownHealth}, &stdout, &stderr, server.Client(), store)
	if code != 2 || fake.count() != 0 {
		t.Fatalf("exit=%d requests=%d", code, fake.count())
	}
	if want := "Open WebUI configures itself on deploy; use kei harness list/sync --harness openwebui"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
