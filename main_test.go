package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	err := login(context.Background(), server.URL, "kei-cli@test", &output, server.Client(), store, func(time.Duration) { sleeps++ })
	if err != nil {
		t.Fatal(err)
	}
	if store.token != "never-print-this-token" || store.server != server.URL {
		t.Fatalf("token was not saved to credential store: %#v", store)
	}
	if strings.Contains(output.String(), "never-print-this-token") || strings.Contains(output.String(), "device-secret") {
		t.Fatalf("login output exposed a secret: %q", output.String())
	}
	if sleeps != 2 {
		t.Fatalf("sleep count = %d, want 2", sleeps)
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

func TestUsageMarksAzureDeploymentAsDeprecated(t *testing.T) {
	var output bytes.Buffer
	printUsage(&output)
	if !strings.Contains(output.String(), "Azure deployment commands are deprecated") {
		t.Fatalf("usage does not explain Azure deprecation: %s", output.String())
	}
	if !strings.Contains(output.String(), "bot install azure") || !strings.Contains(output.String(), "(deprecated)") {
		t.Fatalf("usage does not mark Azure commands deprecated: %s", output.String())
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
	if err := initBot(context.Background(), server.URL, "agent-123", "teams", "customer-teams", &output, server.Client(), store); err != nil {
		t.Fatal(err)
	}
	if received.AgentID != "agent-123" || received.Platform != "teams" || received.DisplayName != "customer-teams" {
		t.Fatalf("unexpected installation request: %#v", received)
	}
	if strings.Contains(output.String(), "runtime_token") || strings.Contains(output.String(), "kh_live_") {
		t.Fatalf("init output exposed a credential: %q", output.String())
	}
}

func TestIdempotentInstallationRequestSetsReuseFlag(t *testing.T) {
	store := &memoryCredentialStore{server: "", token: "cli-session-token"}
	var received createRuntimeInstallationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(createRuntimeInstallationResponse{ID: "installation-123", Platform: "teams", DisplayName: "customer-teams"})
	}))
	defer server.Close()
	store.server = server.URL
	if _, err := createBotInstallationIdempotent(context.Background(), server.URL, "", "teams", "customer-teams", io.Discard, server.Client(), store); err != nil {
		t.Fatal(err)
	}
	if !received.ReuseExisting {
		t.Fatalf("reuse_existing = false, want true")
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
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotStatusCommand([]string{"--api-url", server.URL, "--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("status command exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "kh_live_") || !strings.Contains(stdout.String(), `"binding_status":"verified"`) {
		t.Fatalf("unsafe or incomplete status output: %s", stdout.String())
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
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotAgentsAdd([]string{"--api-url", server.URL, "--installation", installationID, "--agent", agentID, "--default"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("agent add exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), agentID) {
		t.Fatalf("agent add output did not include assignment: %s", stdout.String())
	}
}
