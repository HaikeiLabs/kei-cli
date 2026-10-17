package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// recordingConnectSteps replaces every side-effecting step of kei connect with
// a recorder, so a test can assert the order and which steps a re-run skips.
// The setup step writes a real runtime config so the second run sees the
// credential already on disk.
func recordingConnectSteps(order *[]string) connectSteps {
	record := func(name string) func(*connectFlow) error {
		return func(*connectFlow) error { *order = append(*order, name); return nil }
	}
	return connectSteps{
		initInstallation: func(f *connectFlow) error {
			*order = append(*order, "init")
			f.installationID = "11111111-1111-1111-1111-111111111111"
			return nil
		},
		issueCredential: func(f *connectFlow) error {
			*order = append(*order, "credential")
			f.runtimeToken = "runtime-secret"
			return nil
		},
		setup: func(f *connectFlow) error {
			*order = append(*order, "setup")
			return saveRuntimeConfig(f.configPath, runtimeConfig{
				ControlPlaneURL: f.controlPlaneURL,
				RuntimeToken:    f.runtimeToken,
				HarnessURL:      "http://127.0.0.1:8088",
			})
		},
		bootstrap:      record("bootstrap"),
		installService: record("service"),
		addHarness:     record("add"),
		syncHarness:    record("sync"),
	}
}

// connectConsole answers the org and workspace lookups kei connect makes before
// it starts the steps.
func connectConsole(t *testing.T) (*httptest.Server, *memoryCredentialStore) {
	t.Helper()
	_, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Path {
		case "/api/v1/organizations":
			_, _ = w.Write([]byte(`[{"id":"org-1","name":"Acme Corp"}]`))
		case "/api/v1/organizations/org-1/workspaces":
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main","is_default":true}]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	return server, &memoryCredentialStore{server: server.URL, token: testCLIToken}
}

func TestConnectRunsStepsInOrderAndPrintsOrgAndWorkspace(t *testing.T) {
	server, store := connectConsole(t)
	t.Setenv("HOME", t.TempDir())

	var order []string
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, recordingConnectSteps(&order))
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if got := strings.Join(order, ","); got != "init,credential,setup,bootstrap,service,sync" {
		t.Fatalf("step order = %q, want init,credential,setup,bootstrap,service,sync", got)
	}
	if !strings.Contains(stdout.String(), "Acme Corp") {
		t.Fatalf("stdout missing org name: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Main") {
		t.Fatalf("stdout missing workspace name: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "runtime-secret") || strings.Contains(stderr.String(), "runtime-secret") {
		t.Fatalf("connect output leaked the credential: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestConnectCommandIsWired(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main("dev", "connect", nil, &stdout, &stderr, strings.NewReader(""))
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "connect requires exactly one harness") {
		t.Fatalf("stderr = %q; connect is not wired into Main", stderr.String())
	}
}

func TestConnectRerunSkipsCompletedSteps(t *testing.T) {
	server, store := connectConsole(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".config", "kei.yaml")
	if err := saveRuntimeConfig(configPath, runtimeConfig{
		ControlPlaneURL: "https://control.example.test",
		RuntimeToken:    "runtime-secret",
		HarnessURL:      "http://127.0.0.1:8088",
	}); err != nil {
		t.Fatal(err)
	}

	var order []string
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, recordingConnectSteps(&order))
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if got := strings.Join(order, ","); got != "bootstrap,service,sync" {
		t.Fatalf("re-run order = %q, want bootstrap,service,sync (init/credential/setup skipped)", got)
	}
}

func TestConnectWithoutYesStopsBeforeSteps(t *testing.T) {
	server, store := connectConsole(t)
	t.Setenv("HOME", t.TempDir())

	var order []string
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Main"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, recordingConnectSteps(&order))
	if code != 2 {
		t.Fatalf("exit = %d, want 2 on decline", code)
	}
	if !strings.Contains(stderr.String(), "Connect this machine to Kei?") {
		t.Fatalf("stderr missing the confirmation prompt: %q", stderr.String())
	}
	if len(order) != 0 {
		t.Fatalf("declined but steps ran: %v", order)
	}
	if strings.Contains(stdout.String(), "Connected") {
		t.Fatalf("declined but connect reported success: %q", stdout.String())
	}
}

func TestConnectYesAsksNothing(t *testing.T) {
	server, store := connectConsole(t)
	t.Setenv("HOME", t.TempDir())

	var order []string
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, recordingConnectSteps(&order))
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "Connect this machine to Kei?") {
		t.Fatalf("--yes still prompted: %q", stderr.String())
	}
	if len(order) != 6 {
		t.Fatalf("steps = %v, want all six", order)
	}
}

func TestConnectResolvesWorkspaceNameToID(t *testing.T) {
	var initBody, credBody map[string]any
	_, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Path == "/api/v1/organizations":
			_, _ = w.Write([]byte(`[{"id":"org-1","name":"Acme Corp"}]`))
		case r.Path == "/api/v1/organizations/org-1/workspaces":
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main","is_default":true}]`))
		case r.Method == http.MethodPost && r.Path == "/api/v1/organizations/org-1/runtime-installations":
			initBody = r.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"11111111-1111-1111-1111-111111111111","platform":"cli","status":"pending"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":issueCredential"):
			credBody = r.Body
			_, _ = w.Write([]byte(`{"runtime_token":"runtime-secret"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	t.Setenv("HOME", t.TempDir())
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}

	steps := recordingConnectSteps(&[]string{})
	steps.initInstallation = connectInitInstallation
	steps.issueCredential = connectIssueCredential
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"opencode", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, steps)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if initBody["workspace_id"] != testWorkspaceID {
		t.Fatalf("bot init workspace_id = %v, want resolved %s", initBody["workspace_id"], testWorkspaceID)
	}
	if credBody["workspace_id"] != testWorkspaceID {
		t.Fatalf("credential workspace_id = %v, want resolved %s", credBody["workspace_id"], testWorkspaceID)
	}
}

func TestConnectUsesDefaultWorkspaceWithoutFlag(t *testing.T) {
	_, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Path {
		case "/api/v1/organizations":
			_, _ = w.Write([]byte(`[{"id":"org-1","name":"Acme Corp"}]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	t.Setenv("HOME", t.TempDir())
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}

	var usedWorkspace string
	steps := recordingConnectSteps(&[]string{})
	steps.initInstallation = func(f *connectFlow) error {
		usedWorkspace = f.workspaceID
		f.installationID = "11111111-1111-1111-1111-111111111111"
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"codex", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, steps)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if usedWorkspace != testWorkspaceID {
		t.Fatalf("workspace id = %q, want the default %q", usedWorkspace, testWorkspaceID)
	}
	if !strings.Contains(stdout.String(), "Workspace: Main") {
		t.Fatalf("stdout = %q, want the default workspace name", stdout.String())
	}
}

func TestConnectUnknownWorkspaceStopsBeforeSteps(t *testing.T) {
	server, store := connectConsole(t)
	t.Setenv("HOME", t.TempDir())

	var order []string
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Nope", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, recordingConnectSteps(&order))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for an unknown workspace", code)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %q, want a not-found error", stderr.String())
	}
	if len(order) != 0 {
		t.Fatalf("unknown workspace but steps ran: %v", order)
	}
}

func TestConnectSetupRunsTheRealSetupPathAndVerifiesTheToken(t *testing.T) {
	const installationID = "11111111-1111-1111-1111-111111111111"
	var whoamiAuth string
	_, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Path == "/api/v1/organizations":
			_, _ = w.Write([]byte(`[{"id":"org-1","name":"Acme Corp"}]`))
		case r.Path == "/api/v1/organizations/org-1/workspaces":
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main","is_default":true}]`))
		case r.Method == http.MethodPost && r.Path == "/api/v1/organizations/org-1/runtime-installations":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + installationID + `","platform":"cli","status":"pending"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":issueCredential"):
			_, _ = w.Write([]byte(`{"runtime_token":"runtime-secret"}`))
		case r.Path == "/api/v1/runtime/whoami":
			whoamiAuth = r.Auth
			_, _ = w.Write([]byte(`{"id":"` + installationID + `","org_id":"org-1","platform":"cli"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}

	steps := recordingConnectSteps(&[]string{})
	steps.initInstallation = connectInitInstallation
	steps.issueCredential = connectIssueCredential
	steps.setup = connectSetup
	var stdout, stderr bytes.Buffer
	code := runConnectCommand([]string{"claude-code", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, steps)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if whoamiAuth != "Bearer runtime-secret" {
		t.Fatalf("setup did not verify the runtime token (whoami auth=%q)", whoamiAuth)
	}
	if strings.Contains(stdout.String(), "runtime-secret") || strings.Contains(stderr.String(), "runtime-secret") {
		t.Fatalf("connect leaked the credential: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	config, err := loadRuntimeConfig(filepath.Join(home, ".config", "kei.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeToken != "runtime-secret" {
		t.Fatalf("config runtime token was not saved")
	}
}
