package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryCredentialStore struct {
	server string
	token  string
}

func (s *memoryCredentialStore) Save(serverURL, token string) error {
	s.server = serverURL
	s.token = token
	return nil
}

func (s *memoryCredentialStore) Load(serverURL string) (string, error) {
	if s.server != serverURL || s.token == "" {
		return "", errors.New("credential not found")
	}
	return s.token, nil
}

func (s *memoryCredentialStore) Delete(serverURL string) (bool, error) {
	if s.server != serverURL || s.token == "" {
		return false, nil
	}
	s.server = ""
	s.token = ""
	return true, nil
}

func TestLoginStoresTokenWithoutPrintingIt(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device/authorize":
			json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
				DeviceCode:      "device-secret",
				UserCode:        "ABCDE-23456",
				ExpiresAt:       time.Now().Add(time.Minute),
				IntervalSeconds: 1,
			})
		case "/api/cli/device/token":
			polls++
			if polls == 1 {
				json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "pending"})
				return
			}
			json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "approved", AccessToken: "never-print-this-token", OrgID: "org-123"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	sleeps := 0
	openedURL := ""
	err := login(context.Background(), server.URL, "kei@test", &output, server.Client(), store, func(time.Duration) { sleeps++ }, func(url string) error {
		openedURL = url
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.token != "never-print-this-token" || store.server != server.URL {
		t.Fatalf("token was not saved to credential store: %#v", store)
	}
	if strings.Contains(output.String(), "never-print-this-token") || strings.Contains(output.String(), "device-secret") {
		t.Fatalf("login output exposed a secret: %q", output.String())
	}
	if !strings.Contains(openedURL, "/cli/activate?user_code=ABCDE-23456") {
		t.Fatalf("browser URL = %q", openedURL)
	}
	if sleeps != 2 {
		t.Fatalf("sleep count = %d, want 2", sleeps)
	}
}

func TestLoginReportsBrowserOpenFailureAndKeepsURLVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device/authorize":
			json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
				DeviceCode: "device-code", UserCode: "ABCDE-23456",
				ExpiresAt: time.Now().Add(time.Minute), IntervalSeconds: 1,
			})
		case "/api/cli/device/token":
			json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "approved", AccessToken: "token", OrgID: "org-123"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "kei@test", &output, server.Client(), store, func(time.Duration) {}, func(string) error {
		return errors.New("no graphical session")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), server.URL+"/cli/activate?user_code=ABCDE-23456") {
		t.Fatalf("login output omitted fallback URL: %q", output.String())
	}
	if !strings.Contains(output.String(), "Could not open a browser automatically") {
		t.Fatalf("login output omitted browser fallback: %q", output.String())
	}
}

func TestNormalizedKeiWebURL(t *testing.T) {
	if _, err := normalizedKeiWebURL("not-a-url"); err == nil {
		t.Fatal("expected relative URL to be rejected")
	}
	if _, err := normalizedKeiWebURL("https://kei.example.test?bad=true"); err == nil {
		t.Fatal("expected URL query to be rejected")
	}
	if got, err := normalizedKeiWebURL("https://kei.example.test/"); err != nil || got != "https://kei.example.test" {
		t.Fatalf("normalized URL = %q, %v", got, err)
	}
}

func TestValidRuntimePlatform(t *testing.T) {
	for _, platform := range []string{"cli", "teams", "discord", "slack"} {
		if !validRuntimePlatform(platform) {
			t.Errorf("validRuntimePlatform(%q) = false", platform)
		}
	}
	for _, platform := range []string{"", "openwebui", "unknown"} {
		if validRuntimePlatform(platform) {
			t.Errorf("validRuntimePlatform(%q) = true", platform)
		}
	}
}

func TestBotCredentialSendsWorkspaceIDInBody(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/runtime-installations/"+installationID+"/credential" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cli-session-token" {
			t.Fatalf("missing CLI authorization")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("missing Content-Type header")
		}
		var body runtimeCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.WorkspaceID != workspaceID {
			t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, workspaceID)
		}
		json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_test_token"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", workspaceID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_test_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialRotateSendsWorkspaceIDInBody(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/runtime-installations/"+installationID+"/rotate" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body runtimeCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.WorkspaceID != workspaceID {
			t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, workspaceID)
		}
		json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_rotated_token"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", workspaceID, "--rotate"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential rotate command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_rotated_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialMissingWorkspaceExitsNonZero(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("credential command exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--workspace") {
		t.Fatalf("stderr should mention --workspace: %q", stderr.String())
	}
}

func TestBotCredentialMissingWorkspaceMakesNoRequest(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	hitServer := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitServer = true
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store)
	if hitServer {
		t.Fatal("request was made despite missing --workspace")
	}
}

func TestBotCredentialWorkspaceFromEnv(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceID := "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body runtimeCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.WorkspaceID != workspaceID {
			t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, workspaceID)
		}
		json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_env_token"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	t.Setenv("KEI_WORKSPACE_ID", workspaceID)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_env_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestInitCreatesPublicInstallationWithoutExposingCredential(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	var received createRuntimeInstallationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/runtime-installations" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(createRuntimeInstallationResponse{ID: "installation-123", Platform: "teams", DisplayName: "customer-teams", Status: "pending", BindingStatus: "unverified"})
	}))
	defer server.Close()
	store.server = server.URL

	var output bytes.Buffer
	if err := initBot(context.Background(), server.URL, "agent-123", "teams", "customer-teams", "", &output, server.Client(), store); err != nil {
		t.Fatal(err)
	}
	if received.AgentID != "agent-123" || received.Platform != "teams" || received.DisplayName != "customer-teams" {
		t.Fatalf("unexpected installation request: %#v", received)
	}
	if strings.Contains(output.String(), "runtime_token") || strings.Contains(output.String(), "kh_live_") {
		t.Fatalf("init output exposed a credential: %q", output.String())
	}
}

func TestBotStatusPrintsSafeInstallationMetadata(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/runtime-installations/"+installationID || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"12345678-1234-1234-1234-123456789012","platform":"teams","status":"active","binding_status":"verified","deployment":{"key_vault_name":"customerkeivault","runtime_secret_name":"kei-runtime-123"}}`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotStatusCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("status command exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "kh_live_") || !strings.Contains(stdout.String(), `"binding_status":"verified"`) {
		t.Fatalf("unsafe or incomplete status output: %s", stdout.String())
	}
}

func TestBotDeleteRequiresExplicitConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runBotDeleteCommand([]string{"--installation", "12345678-1234-1234-1234-123456789012"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("delete command exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("delete warning = %q", stderr.String())
	}
}

func TestBotDeleteRevokesInstallation(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/runtime-installations/"+installationID || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotDeleteCommand([]string{"--installation", installationID, "--yes"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("delete command exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "revoked its credential") {
		t.Fatalf("delete output = %q", stdout.String())
	}
}

func TestBotAgentsAddUsesCLIOrganizationScopedEndpoint(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	agentID := "22345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/cli/runtime-installations/" + installationID + "/agents"
		if r.URL.Path != wantPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["agent_id"] != agentID || body["default"] != true {
			t.Fatalf("unexpected assignment body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"installation_id":"` + installationID + `","agent_id":"` + agentID + `","is_default":true}`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotAgentsAdd([]string{"--installation", installationID, "--agent", agentID, "--default"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("agent add exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), agentID) {
		t.Fatalf("agent add output did not include assignment: %s", stdout.String())
	}
}

func TestDefaultKeiWebURLIsHTTPS(t *testing.T) {
	if defaultKeiWebURL != "https://app.haikeilabs.com" {
		t.Fatalf("defaultKeiWebURL = %q, want https://app.haikeilabs.com", defaultKeiWebURL)
	}
}

func TestNoApiURLHelpSurface(t *testing.T) {
	var buf bytes.Buffer
	PrintUsage(&buf)
	if strings.Contains(buf.String(), "--api-url") {
		t.Fatalf("PrintUsage still contains --api-url:\n%s", buf.String())
	}
}

func TestApiURLFlagRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{}
	if code := runLogoutCommand([]string{"--api-url", "https://example.com"}, &stdout, &stderr, store); code != 2 {
		t.Fatalf("expected exit code 2 for unknown --api-url flag, got %d", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -api-url") {
		t.Fatalf("expected unknown flag error, got: %s", stderr.String())
	}
}

func TestWorkspacesListJSON(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/workspaces" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
			{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod", IsAdmin: true},
			{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "staging", IsAdmin: false},
		}})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceListCommand([]string{"--json"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("workspaces list exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`) {
		t.Fatalf("JSON output missing workspace id:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"is_admin": true`) {
		t.Fatalf("JSON output missing is_admin:\n%s", stdout.String())
	}
}

func TestWorkspacesListTable(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
			{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod", IsAdmin: true},
		}})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceListCommand([]string{}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("workspaces list exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("table output missing workspace id:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "(admin)") {
		t.Fatalf("table output missing admin marker:\n%s", stdout.String())
	}
}

func TestWorkspacesListEmpty(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{}})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceListCommand([]string{}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("workspaces list exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No workspaces found.") {
		t.Fatalf("unexpected output for empty list:\n%s", stdout.String())
	}
}

func TestWorkspacesListRequiresLogin(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceListCommand([]string{}, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("expected login error, got: %s", stderr.String())
	}
}

func TestBotCredentialResolvesWorkspaceByName(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	workspaceName := "my-workspace"
	workspaceHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/workspaces":
			workspaceHits++
			json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
				{ID: workspaceUUID, Name: workspaceName, IsAdmin: true},
				{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "other", IsAdmin: false},
			}})
		case "/api/cli/runtime-installations/" + installationID + "/credential":
			var body runtimeCredentialRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.WorkspaceID != workspaceUUID {
				t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, workspaceUUID)
			}
			json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_resolved_token"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", workspaceName}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if workspaceHits != 1 {
		t.Fatalf("workspaces API called %d times, want 1", workspaceHits)
	}
	if stdout.String() != "kh_live_resolved_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotInitResolvesWorkspaceByName(t *testing.T) {
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	workspaceName := "my-workspace"
	var received createRuntimeInstallationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/workspaces":
			json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
				{ID: workspaceUUID, Name: workspaceName, IsAdmin: true},
			}})
		case "/api/cli/runtime-installations":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(createRuntimeInstallationResponse{ID: "installation-123", Platform: "cli", DisplayName: "test", Status: "pending", BindingStatus: "unverified"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotInitCommand([]string{"--platform", "cli", "--name", "test", "--workspace", workspaceName}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("bot init exit = %d, stderr = %s", code, stderr.String())
	}
	if received.WorkspaceID != workspaceUUID {
		t.Fatalf("workspace_id = %q, want %q", received.WorkspaceID, workspaceUUID)
	}
}

func TestBotInitWorkspaceNotRequired(t *testing.T) {
	var received createRuntimeInstallationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/runtime-installations" {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(createRuntimeInstallationResponse{ID: "installation-123", Platform: "cli", DisplayName: "test", Status: "pending", BindingStatus: "unverified"})
		}
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotInitCommand([]string{"--platform", "cli", "--name", "test"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("bot init exit = %d, stderr = %s", code, stderr.String())
	}
	if received.WorkspaceID != "" {
		t.Fatalf("workspace_id should be empty when --workspace not set, got %q", received.WorkspaceID)
	}
}

func TestBotCredentialWorkspaceNameNoMatch(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
			{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod", IsAdmin: true},
			{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "staging", IsAdmin: false},
		}})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "nonexistent"}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "nonexistent") || !strings.Contains(stderr.String(), "prod") {
		t.Fatalf("stderr should mention the name and available workspaces: %s", stderr.String())
	}
}

func TestBotCredentialWorkspaceNameMultipleMatches(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(workspaceListResponse{Workspaces: []workspaceInfo{
			{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "dup-name", IsAdmin: true},
			{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "dup-name", IsAdmin: false},
		}})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "dup-name"}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "multiple workspaces match") {
		t.Fatalf("stderr should mention multiple matches: %s", stderr.String())
	}
}

func TestBotCredentialWorkspaceByUUIDMakesNoDiscoveryCall(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	workspaceAPICalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/workspaces" {
			workspaceAPICalled = true
		}
		if r.URL.Path == "/api/cli/runtime-installations/"+installationID+"/credential" {
			var body runtimeCredentialRequest
			json.NewDecoder(r.Body).Decode(&body)
			if body.WorkspaceID != workspaceUUID {
				t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, workspaceUUID)
			}
			json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_uuid_token"})
		}
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", workspaceUUID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if workspaceAPICalled {
		t.Fatal("workspace list API was called despite --workspace being a UUID")
	}
	if stdout.String() != "kh_live_uuid_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}
