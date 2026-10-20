package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	exchanges := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "AUTHORIZATION_PENDING", Message: "not yet approved"}})
			return
		}
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: "never-print-this-token"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	sleeps := 0
	openedURL := ""
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) { sleeps++ }, func(url string) error {
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
	if !strings.Contains(openedURL, "/activate?user_code=ABCDE-23456") {
		t.Fatalf("browser URL = %q", openedURL)
	}
	if sleeps != 2 {
		t.Fatalf("sleep count = %d, want 2", sleeps)
	}
}

func TestLoginReportsBrowserOpenFailureAndKeepsURLVisible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-code",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: "token"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error {
		return errors.New("no graphical session")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "http://example.com/activate?user_code=ABCDE-23456") {
		t.Fatalf("login output omitted fallback URL: %q", output.String())
	}
	if !strings.Contains(output.String(), "Could not open a browser automatically") {
		t.Fatalf("login output omitted browser fallback: %q", output.String())
	}
}

func TestLoginDoesNotFallbackWhenAIPEndpointIsMissing(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/deviceAuthorizations" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", io.Discard, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err == nil {
		t.Fatal("login succeeded without the AIP device-authorization endpoint")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the AIP request", requests)
	}
	if store.token != "" {
		t.Fatalf("token = %q, want no saved token", store.token)
	}
}

func TestLoginSlowDownIncreasesInterval(t *testing.T) {
	exchanges := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "SLOW-12345",
			VerificationURIComplete: "http://example.com/activate?user_code=SLOW-12345",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "SLOW_DOWN", Message: "too fast"}})
			return
		}
		// On second exchange the interval should have increased;
		// deny immediately so the test terminates quickly.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 403, Reason: "ACCESS_DENIED", Message: "nope"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	sleepDurations := make([]time.Duration, 0)
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(d time.Duration) { sleepDurations = append(sleepDurations, d) }, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected denied error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v, want 'denied'", err)
	}
	if len(sleepDurations) < 2 {
		t.Fatalf("expected at least 2 sleeps, got %d", len(sleepDurations))
	}
	// First interval from start response (1s). After SLOW_DOWN, interval should be 1s + 5s = 6s.
	if sleepDurations[1] < 5*time.Second {
		t.Fatalf("expected increased interval after SLOW_DOWN, got %v", sleepDurations[1])
	}
}

func TestLoginAccessDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "DENY-12345",
			VerificationURIComplete: "http://example.com/activate?user_code=DENY-12345",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 403, Reason: "ACCESS_DENIED", Message: "user denied"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected denied error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v, want 'denied'", err)
	}
}

func TestLoginExpiredToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "EXPR-12345",
			VerificationURIComplete: "http://example.com/activate?user_code=EXPR-12345",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "EXPIRED_TOKEN", Message: "timed out"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected expired error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %v, want 'expired'", err)
	}
}

func TestLoginDeviceCodeNeverInOutput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "super-secret-device-code-12345",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: "token"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "super-secret-device-code-12345") {
		t.Fatalf("device_code leaked into output: %q", output.String())
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
	for _, platform := range []string{"cli", "teams", "discord", "slack", "whatsapp", "openwebui"} {
		if !validRuntimePlatform(platform) {
			t.Errorf("validRuntimePlatform(%q) = false", platform)
		}
	}
	for _, platform := range []string{"", "unknown", "sms"} {
		if validRuntimePlatform(platform) {
			t.Errorf("validRuntimePlatform(%q) = true", platform)
		}
	}
}

func TestBotCredentialSendsWorkspaceIDInBody(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/organizations/org-1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"` + workspaceID + `","name":"prod"}]`))
			return
		}
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+installationID+":issueCredential" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testCLIToken {
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
	t.Setenv("HOME", t.TempDir())
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "prod"}, &stdout, &stderr, server.Client(), store); code != 0 {
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
		if r.URL.Path == "/api/v1/organizations/org-1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"` + workspaceID + `","name":"prod"}]`))
			return
		}
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+installationID+":rotateCredential" || r.Method != http.MethodPost {
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
	t.Setenv("HOME", t.TempDir())
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "prod", "--rotate"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential rotate command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_rotated_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialEmptyWorkspaceListFails(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	hitWorkspaces := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/organizations/org-1/workspaces" && r.Method == http.MethodGet {
			hitWorkspaces = true
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEI_WORKSPACE", "")
	t.Setenv("KEI_WORKSPACE_ID", "")
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("credential command exit = %d, want 1, stderr = %s", code, stderr.String())
	}
	if !hitWorkspaces {
		t.Fatal("expected a workspaces-list request to be made")
	}
	if !strings.Contains(stderr.String(), "no workspaces found") {
		t.Fatalf("stderr should mention no workspaces: %q", stderr.String())
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
	t.Setenv("KEI_WORKSPACE", "")
	t.Setenv("KEI_WORKSPACE_ID", workspaceID)
	t.Setenv("HOME", t.TempDir())
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "KEI_WORKSPACE_ID") || !strings.Contains(stderr.String(), "deprecated") {
		t.Fatalf("stderr should carry a one-line KEI_WORKSPACE_ID deprecation note: %q", stderr.String())
	}
	if stdout.String() != "kh_live_env_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestInitCreatesPublicInstallationWithoutExposingCredential(t *testing.T) {
	store := &memoryCredentialStore{token: testCLIToken}
	var received createRuntimeInstallationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testCLIToken {
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
	store := &memoryCredentialStore{token: testCLIToken}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+installationID || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testCLIToken {
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

func TestBotStatusRendersApprovedPolicyBundleHealth(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	// This active read projection is copied byte-for-byte from the canonical
	// kei-connector-contracts PR #14 fixture. State variants below retain its
	// exact field set and are shaped according to the same approved contract.
	canonicalFixture, err := os.ReadFile("testdata/runtime-policy-bundle-health-read-active.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(canonicalFixture, &canonical); err != nil {
		t.Fatal(err)
	}
	states := []struct {
		state      string
		reasonCode string
		cold       bool
	}{
		{state: "cold", reasonCode: "bundle_missing", cold: true},
		{state: "active"},
		{state: "stale_but_valid", reasonCode: "bundle_stale"},
		{state: "expired", reasonCode: "bundle_expired"},
		{state: "invalid", reasonCode: "integrity_rejected"},
		{state: "unsupported", reasonCode: "schema_unsupported"},
		{state: "revoked", reasonCode: "runtime_revoked"},
	}

	for _, tc := range states {
		t.Run(tc.state, func(t *testing.T) {
			var health map[string]any
			encoded, err := json.Marshal(canonical)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &health); err != nil {
				t.Fatal(err)
			}
			health["state"] = tc.state
			health["reason_code"] = nil
			if tc.reasonCode != "" {
				health["reason_code"] = tc.reasonCode
			}
			if tc.cold {
				health["bundle_version"] = nil
				health["policy_revision"] = nil
				health["bundle_digest"] = nil
				health["accepted_at"] = nil
				health["expires_at"] = nil
			}
			response, err := json.Marshal(map[string]any{
				"id": installationID, "status": "active", "binding_status": "verified", "policy_bundle": health,
			})
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryCredentialStore{server: "", token: testCLIToken}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+installationID {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+testCLIToken {
					t.Fatalf("Authorization = %q", got)
				}
				_, _ = w.Write(response)
			}))
			defer server.Close()
			t.Setenv("KEI_WEB_URL", server.URL)
			store.server = server.URL
			var stdout, stderr bytes.Buffer
			if code := runBotStatusCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
				t.Fatalf("status command exit = %d, stderr=%s", code, stderr.String())
			}
			var got runtimeInstallationStatus
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decode CLI output: %v; output=%s", err, stdout.String())
			}
			if got.Status != "active" || got.BindingStatus != "verified" {
				t.Fatalf("installation lifecycle fields changed: %#v", got)
			}
			if got.PolicyBundle == nil || got.PolicyBundle.SchemaVersion != 1 || got.PolicyBundle.State != tc.state || got.PolicyBundle.ReportedAt == nil {
				t.Fatalf("policy bundle health not rendered: %#v", got.PolicyBundle)
			}
			if tc.cold {
				if got.PolicyBundle.BundleVersion != nil || got.PolicyBundle.PolicyRevision != nil || got.PolicyBundle.BundleDigest != nil || got.PolicyBundle.ReasonCode == nil || *got.PolicyBundle.ReasonCode != "bundle_missing" {
					t.Fatalf("cold state fields do not match the approved contract: %#v", got.PolicyBundle)
				}
			} else if got.PolicyBundle.BundleVersion == nil || *got.PolicyBundle.BundleVersion != 42 || got.PolicyBundle.PolicyRevision == nil || *got.PolicyBundle.PolicyRevision != 7 {
				t.Fatalf("accepted bundle metadata was not rendered: %#v", got.PolicyBundle)
			}
			var rendered map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil {
				t.Fatal(err)
			}
			var healthFields map[string]json.RawMessage
			if err := json.Unmarshal(rendered["policy_bundle"], &healthFields); err != nil {
				t.Fatal(err)
			}
			wantFields := []string{"schema_version", "state", "bundle_version", "policy_revision", "bundle_digest", "checked_at", "accepted_at", "expires_at", "reason_code", "reported_at"}
			if len(healthFields) != len(wantFields) {
				t.Fatalf("policy_bundle fields = %v, want exactly %v", healthFields, wantFields)
			}
			for _, field := range wantFields {
				if _, ok := healthFields[field]; !ok {
					t.Errorf("policy_bundle missing approved field %q", field)
				}
			}
			if strings.Contains(stdout.String(), testCLIToken) || strings.Contains(stdout.String(), "runtime_token") {
				t.Fatalf("status output exposed a credential: %s", stdout.String())
			}
		})
	}
}

func TestBotStatusRendersNullWhenNoPolicyBundleReport(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: testCLIToken}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + installationID + `","status":"active","binding_status":"verified","policy_bundle":null}`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotStatusCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("status command exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"policy_bundle":null`) {
		t.Fatalf("missing report was not rendered as null: %s", stdout.String())
	}
}

func TestBotStatusNotFound(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: testCLIToken}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runBotStatusCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("status command exit = %d, want 1, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "404") {
		t.Fatalf("expected 404 in stderr, got: %s", stderr.String())
	}
}

func TestBotDeleteRequiresExplicitConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: testCLIToken}
	if code := runBotDeleteCommand([]string{"--installation", "12345678-1234-1234-1234-123456789012"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("delete command exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("delete warning = %q", stderr.String())
	}
}

func TestBotDeleteRevokesInstallation(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	store := &memoryCredentialStore{token: testCLIToken}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+installationID || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testCLIToken {
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
	store := &memoryCredentialStore{token: testCLIToken}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/organizations/org-1/runtime-installations/" + installationID + "/agents"
		if r.URL.Path != wantPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testCLIToken {
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
	if defaultKeiWebURL != "https://api.haikeilabs.com" {
		t.Fatalf("defaultKeiWebURL = %q, want https://api.haikeilabs.com", defaultKeiWebURL)
	}
}

func TestNoApiURLHelpSurface(t *testing.T) {
	var buf bytes.Buffer
	PrintUsage(&buf)
	if strings.Contains(buf.String(), "--api-url") {
		t.Fatalf("PrintUsage still contains --api-url:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "kei feedback --description") {
		t.Fatalf("PrintUsage omitted the feedback command:\n%s", buf.String())
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
	store := &memoryCredentialStore{token: workspaceTestToken()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/workspaces" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+workspaceTestToken() {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"prod"},{"id":"ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj","name":"staging"}]`))
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
}

func TestWorkspacesListTable(t *testing.T) {
	store := &memoryCredentialStore{token: workspaceTestToken()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"prod"}]`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceListCommand([]string{}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("workspaces list exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "prod") {
		t.Fatalf("table output missing workspace name:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("table output printed a workspace UUID:\n%s", stdout.String())
	}
}

func TestWorkspacesListEmpty(t *testing.T) {
	store := &memoryCredentialStore{token: workspaceTestToken()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
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

// workspaceListServer answers the access-aware workspaces list (HAI-185) with
// the given workspaces and returns a logged-in store for the server host.
func workspaceListServer(t *testing.T, workspaces []workspaceInfo) (*httptest.Server, *memoryCredentialStore) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/workspaces" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(workspaces)
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	return server, &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
}

func TestWorkspacesUseSavesCurrentAndCurrentShowsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, store := workspaceListServer(t, []workspaceInfo{
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod", IsDefault: true},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "staging"},
	})

	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"use", "staging"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("workspaces use exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "staging") {
		t.Fatalf("use output missing workspace name: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runWorkspaceCommand([]string{"current"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("workspaces current exit = %d, stderr = %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "staging" {
		t.Fatalf("current output = %q, want %q", got, "staging")
	}
}

func TestWorkspacesListMarksCurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, store := workspaceListServer(t, []workspaceInfo{
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod", IsDefault: true},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "staging"},
	})
	if err := saveCurrentWorkspace(server.URL, "staging"); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"list"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("workspaces list exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "* staging") {
		t.Fatalf("list did not mark the current workspace staging:\n%s", out)
	}
	if strings.Contains(out, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("list printed a workspace UUID:\n%s", out)
	}
}

func TestWorkspacesCurrentWithoutSavedWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"current"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, &memoryCredentialStore{}); code != 0 {
		t.Fatalf("workspaces current exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No current workspace") {
		t.Fatalf("current output = %q, want a no-current-workspace message", stdout.String())
	}
}

func TestWorkspacesUseRejectsUnknownName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, store := workspaceListServer(t, []workspaceInfo{
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod"},
	})
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"use", "nope"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 1 {
		t.Fatalf("workspaces use unknown exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("use unknown stderr = %q, want a not-found message", stderr.String())
	}
	if name, _ := loadCurrentWorkspace(server.URL); name != "" {
		t.Fatalf("failed use saved current workspace %q", name)
	}
}

func TestBotCredentialResolvesWorkspaceByName(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	workspaceName := "my-workspace"
	workspaceHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/organizations/org-1/workspaces":
			workspaceHits++
			_, _ = w.Write([]byte(`[{"id":"` + workspaceUUID + `","name":"` + workspaceName + `"},{"id":"ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj","name":"other"}]`))
		case "/api/v1/organizations/org-1/runtime-installations/" + installationID + ":issueCredential":
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
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
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
		case "/api/v1/organizations/org-1/workspaces":
			_, _ = w.Write([]byte(`[{"id":"` + workspaceUUID + `","name":"` + workspaceName + `"}]`))
		case "/api/v1/organizations/org-1/runtime-installations":
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
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
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
		if r.URL.Path == "/api/v1/organizations/org-1/runtime-installations" {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(createRuntimeInstallationResponse{ID: "installation-123", Platform: "cli", DisplayName: "test", Status: "pending", BindingStatus: "unverified"})
		}
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
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
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"prod"},{"id":"ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj","name":"staging"}]`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
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
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"dup-name"},{"id":"ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj","name":"dup-name"}]`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "dup-name"}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "multiple workspaces match") {
		t.Fatalf("stderr should mention multiple matches: %s", stderr.String())
	}
}

// botCredentialServer serves the access-aware workspace list and the
// :issueCredential endpoint, asserting the workspace_id in the request body.
func botCredentialServer(t *testing.T, installationID, wantWorkspaceID string, workspaces []workspaceInfo) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/organizations/org-1/workspaces":
			_ = json.NewEncoder(w).Encode(workspaces)
		case "/api/v1/organizations/org-1/runtime-installations/" + installationID + ":issueCredential":
			var body runtimeCredentialRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.WorkspaceID != wantWorkspaceID {
				t.Fatalf("workspace_id = %q, want %q", body.WorkspaceID, wantWorkspaceID)
			}
			json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: "kh_live_test_token"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	t.Setenv("KEI_WORKSPACE", "")
	t.Setenv("KEI_WORKSPACE_ID", "")
	t.Setenv("HOME", t.TempDir())
	return server
}

func TestBotCredentialWorkspaceFlagUUIDRejected(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, workspaceUUID, []workspaceInfo{
		{ID: workspaceUUID, Name: "prod"},
	})
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", workspaceUUID}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("credential command exit = %d, want 1, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "UUID") || !strings.Contains(stderr.String(), "name") {
		t.Fatalf("stderr should hint to use the workspace name: %q", stderr.String())
	}
}

func TestWorkspacesUseRejectsUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, store := workspaceListServer(t, []workspaceInfo{
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "prod"},
	})
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"use", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 1 {
		t.Fatalf("use exit = %d, want 1, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "UUID") || !strings.Contains(stderr.String(), "name") {
		t.Fatalf("stderr should hint to use the workspace name: %q", stderr.String())
	}
}

func TestBotCredentialWorkspaceFromEnvName(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, workspaceUUID, []workspaceInfo{
		{ID: workspaceUUID, Name: "my-workspace"},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "other"},
	})
	t.Setenv("KEI_WORKSPACE", "my-workspace")
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_test_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialWorkspaceIDEnvDeprecationNote(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, workspaceUUID, []workspaceInfo{
		{ID: workspaceUUID, Name: "prod"},
	})
	t.Setenv("KEI_WORKSPACE_ID", workspaceUUID)
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "KEI_WORKSPACE_ID") || !strings.Contains(stderr.String(), "deprecated") {
		t.Fatalf("stderr should carry a one-line KEI_WORKSPACE_ID deprecation note: %q", stderr.String())
	}
}

func TestBotCredentialUsesSavedCurrentWorkspace(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, workspaceUUID, []workspaceInfo{
		{ID: workspaceUUID, Name: "my-workspace"},
	})
	if err := saveCurrentWorkspace(server.URL, "my-workspace"); err != nil {
		t.Fatal(err)
	}
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_test_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialEnvNameBeatsSavedCurrent(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	envUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, envUUID, []workspaceInfo{
		{ID: envUUID, Name: "env-ws"},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "saved-ws"},
	})
	if err := saveCurrentWorkspace(server.URL, "saved-ws"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEI_WORKSPACE", "env-ws")
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
}

func TestBotCredentialPicksDefaultWorkspaceWhenNoneSpecified(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	defaultID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, defaultID, []workspaceInfo{
		{ID: defaultID, Name: "prod", IsDefault: true},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "staging"},
	})
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_test_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialPicksFirstWorkspaceWhenNoDefaultMarked(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	firstID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, firstID, []workspaceInfo{
		{ID: firstID, Name: "alpha"},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "beta"},
	})
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "kh_live_test_token\n" {
		t.Fatalf("credential output = %q", stdout.String())
	}
}

func TestBotCredentialFlagBeatsEnvName(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	flagUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	server := botCredentialServer(t, installationID, flagUUID, []workspaceInfo{
		{ID: flagUUID, Name: "flag-ws"},
		{ID: "ffffffff-gggg-hhhh-iiii-jjjjjjjjjjjj", Name: "env-ws"},
	})
	t.Setenv("KEI_WORKSPACE", "env-ws")
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID, "--workspace", "flag-ws"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
	}
}

func TestIsJSONResponse(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/problem+json", true},
		{"application/json; charset=utf-8", true},
		{"text/html", false},
		{"text/plain", false},
		{"", false},
	}
	for _, tc := range tests {
		resp := &http.Response{Header: http.Header{}}
		if tc.ct != "" {
			resp.Header.Set("Content-Type", tc.ct)
		}
		got := isJSONResponse(resp)
		if got != tc.want {
			t.Errorf("isJSONResponse(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

func TestIsHTMLBody(t *testing.T) {
	tests := []struct {
		body []byte
		want bool
	}{
		{[]byte("<html>"), true},
		{[]byte("  \t\n<html>"), true},
		{[]byte("<!DOCTYPE html>"), true},
		{[]byte(`{"key": "value"}`), false},
		{nil, false},
		{[]byte{}, false},
		{[]byte(""), false},
	}
	for _, tc := range tests {
		got := isHTMLBody(tc.body)
		if got != tc.want {
			t.Errorf("isHTMLBody(%q) = %v, want %v", string(tc.body), got, tc.want)
		}
	}
}

func TestCheckHTMLFallthrough(t *testing.T) {
	t.Run("json response returns nil", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
		}
		if err := checkHTMLFallthrough(resp, []byte(`{"ok":true}`)); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("html body without json content type returns error", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 502,
			Header:     http.Header{"Content-Type": {"text/html"}},
		}
		err := checkHTMLFallthrough(resp, []byte("<html>bad gateway</html>"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "returned HTML") {
			t.Fatalf("error should mention HTML: %v", err)
		}
		if !strings.Contains(err.Error(), "502") {
			t.Fatalf("error should mention status: %v", err)
		}
	})

	t.Run("includes request URL in error when available", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "https://example.com/api/v1/test", nil)
		resp := &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Request:    req,
		}
		err := checkHTMLFallthrough(resp, []byte("<html>not found</html>"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "example.com") {
			t.Fatalf("error should include request URL: %v", err)
		}
	})

	t.Run("html body with nil request returns error without URL", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{"Content-Type": {"text/html"}},
		}
		err := checkHTMLFallthrough(resp, []byte("<html>error</html>"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "<unknown>") {
			t.Fatalf("error should mention <unknown> URL: %v", err)
		}
	})

	t.Run("non-html error response returns nil", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{"Content-Type": {"application/json"}},
		}
		if err := checkHTMLFallthrough(resp, []byte(`{"error":"internal"}`)); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("html content type but empty body returns nil", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/html"}},
		}
		if err := checkHTMLFallthrough(resp, nil); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("problem+json content type returns nil even with HTML body", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 422,
			Header:     http.Header{"Content-Type": {"application/problem+json"}},
		}
		if err := checkHTMLFallthrough(resp, []byte("<html>should not trigger</html>")); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}

func TestLoginPrintsOrgNameAndWorkspaceName(t *testing.T) {
	exchanges := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "AUTHORIZATION_PENDING", Message: "not yet approved"}})
			return
		}
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: testCLIToken})
	})
	mux.HandleFunc("/api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s on orgs endpoint", r.Method)
		}
		json.NewEncoder(w).Encode([]map[string]string{{"id": "org-1", "name": "Acme Corp"}})
	})
	mux.HandleFunc("/api/v1/organizations/org-1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s on workspaces endpoint", r.Method)
		}
		json.NewEncoder(w).Encode([]workspaceInfo{
			{ID: "ws-1", Name: "Main", IsDefault: true},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	out := output.String()
	if !strings.Contains(out, "Acme Corp") {
		t.Fatalf("expected org name in output, got: %q", out)
	}
	if !strings.Contains(out, "Main") {
		t.Fatalf("expected workspace name in output, got: %q", out)
	}
	if !strings.Contains(out, "Logged in to Kei for organization Acme Corp.") {
		t.Fatalf("expected login message with org name, got: %q", out)
	}
	if !strings.Contains(out, "Workspace: Main") {
		t.Fatalf("expected workspace line, got: %q", out)
	}
}

func TestLoginPrintsOrgNameOnlyWhenNoWorkspaces(t *testing.T) {
	exchanges := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "AUTHORIZATION_PENDING", Message: "not yet approved"}})
			return
		}
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: testCLIToken})
	})
	mux.HandleFunc("/api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s on orgs endpoint", r.Method)
		}
		json.NewEncoder(w).Encode([]map[string]string{{"id": "org-1", "name": "Acme Corp"}})
	})
	mux.HandleFunc("/api/v1/organizations/org-1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]workspaceInfo{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	out := output.String()
	if !strings.Contains(out, "Acme Corp") {
		t.Fatalf("expected org name in output, got: %q", out)
	}
	if strings.Contains(out, "Workspace:") {
		t.Fatalf("expected no workspace line when no workspaces exist, got: %q", out)
	}
}

func TestLoginFallbackWhenOrgAPIFails(t *testing.T) {
	exchanges := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deviceAuthorizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
			DeviceCode:              "device-secret",
			UserCode:                "ABCDE-23456",
			VerificationURIComplete: "http://example.com/activate?user_code=ABCDE-23456",
			ExpiresIn:               60,
			Interval:                1,
		})
	})
	mux.HandleFunc("/api/v1/deviceAuthorizations:exchange", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(aipError{Detail: aipErrorDetail{Code: 400, Reason: "AUTHORIZATION_PENDING", Message: "not yet approved"}})
			return
		}
		json.NewEncoder(w).Encode(deviceAuthorizationExchangeResponse{AccessToken: testCLIToken})
	})
	// Org list returns empty — org not found, fallback to org ID.
	mux.HandleFunc("/api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{})
	})
	// No workspace handler, since we never get past org lookup.
	server := httptest.NewServer(mux)
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	out := output.String()
	if !strings.Contains(out, "org-1") {
		t.Fatalf("expected org ID in fallback output, got: %q", out)
	}
	if strings.Contains(out, "Acme Corp") {
		t.Fatalf("expected no org name in fallback output, got: %q", out)
	}
}

var testCLIToken = workspaceTestToken()

func workspaceTestToken() string {
	// JWT with {"org_id":"org-1"} – base64-encoded header.payload, no signature needed
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pld := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"org-1"}`))
	return hdr + "." + pld + "."
}

func TestFeedbackSubmitsDescriptionAndEvidenceAfterConfirmation(t *testing.T) {
	path := t.TempDir() + "/session.jsonl"
	if err := os.WriteFile(path, []byte(`{"message":"the session transcript"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// --session files are redacted into a temp copy; keep it off the real cache.
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: t.TempDir(), Cwd: t.TempDir(), CacheDir: t.TempDir()}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/bug-reports" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if got := r.FormValue("description"); got != "The session stopped unexpectedly." {
			t.Fatalf("description = %q", got)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 || files[0].Filename != "session.jsonl" {
			t.Fatalf("evidence files = %#v", files)
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || string(content) != `{"message":"the session transcript"}` {
			t.Fatalf("evidence content = %q, err = %v", content, err)
		}
		if got := r.FormValue("page"); got != "/cli" {
			t.Fatalf("page = %q", got)
		}
		if got := r.FormValue("source"); got != "cli" {
			t.Fatalf("source = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-123", ReceivedAt: "2026-10-07T00:00:00Z"})
	}))
	defer server.Close()
	apiBaseURL := "https://api.example.test"
	t.Setenv("KEI_WEB_URL", apiBaseURL)
	store := &memoryCredentialStore{server: apiBaseURL, token: "cli-session-token"}
	var targetURL string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		targetURL = request.URL.String()
		forwarded := request.Clone(request.Context())
		forwardedURL := *request.URL
		forwardedURL.Scheme = "http"
		forwardedURL.Host = server.Listener.Addr().String()
		forwarded.URL = &forwardedURL
		forwarded.Host = forwardedURL.Host
		return server.Client().Do(forwarded)
	})}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "The session stopped unexpectedly.", "--session", path, "--yes"}, &stdout, &stderr, strings.NewReader(""), client, store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if targetURL != "https://app.example.test/api/bug-reports" {
		t.Fatalf("feedback URL = %q", targetURL)
	}
	if !strings.Contains(stdout.String(), "session.jsonl") || !strings.Contains(stdout.String(), "Feedback submitted: feedback-123") {
		t.Fatalf("feedback output = %q", stdout.String())
	}
}

func TestFeedbackDeclineDoesNotSubmit(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report"}, &stdout, &stderr, strings.NewReader("n\n"), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if requests != 0 || !strings.Contains(stdout.String(), "Feedback not submitted.") {
		t.Fatalf("requests = %d, output = %q", requests, stdout.String())
	}
}
func TestFeedbackRejectsOversizedEvidenceBeforeConfirmation(t *testing.T) {
	path := t.TempDir() + "/large.log"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFeedbackRequestBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--file", path}, &stdout, &stderr, strings.NewReader("y\n"), server.Client(), store, "1.2.3")
	if code != 2 || requests != 0 || !strings.Contains(stderr.String(), "4 MiB") {
		t.Fatalf("exit = %d, requests = %d, stderr = %q", code, requests, stderr.String())
	}
}

func TestAIPErrorUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		wantOK     bool
		wantCode   int
		wantReason string
		wantMsg    string
	}{
		{
			name:       "wrapped body with details reason",
			json:       `{"error":{"code":400,"message":"not yet approved","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"AUTHORIZATION_PENDING"}]}}`,
			wantOK:     true,
			wantCode:   400,
			wantReason: "AUTHORIZATION_PENDING",
			wantMsg:    "not yet approved",
		},
		{
			name: "flat body without message is rejected",
			json: `{"error":{"code":400,"reason":"SLOW_DOWN"}}`,
		},
		{
			name:       "wrapped body with both top-level reason and details (identity transitional)",
			json:       `{"error":{"code":400,"reason":"TOP_LEVEL","message":"transitional","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ERROR_INFO_REASON"}]}}`,
			wantOK:     true,
			wantCode:   400,
			wantReason: "ERROR_INFO_REASON",
			wantMsg:    "transitional",
		},
		{
			name:       "wrapped body falls back to details[0] when no ErrorInfo match",
			json:       `{"error":{"code":403,"message":"denied","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","reason":"RATE_LIMITED"}]}}`,
			wantOK:     true,
			wantCode:   403,
			wantReason: "RATE_LIMITED",
			wantMsg:    "denied",
		},
		{
			name:   "unrecognized body",
			json:   `{"foo":"bar"}`,
			wantOK: false,
		},
		{
			name:   "plain text",
			json:   `not json`,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ae aipError
			err := json.Unmarshal([]byte(tc.json), &ae)
			if !tc.wantOK {
				if err == nil {
					t.Fatal("expected unmarshal error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if ae.Detail.Code != tc.wantCode {
				t.Errorf("Code = %d, want %d", ae.Detail.Code, tc.wantCode)
			}
			if ae.Detail.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", ae.Detail.Reason, tc.wantReason)
			}
			if ae.Detail.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", ae.Detail.Message, tc.wantMsg)
			}
		})
	}
}

func TestExchangeDeviceAuthorizationUnrecognizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"unrecognized":true}`))
	}))
	defer server.Close()

	_, err := exchangeDeviceAuthorization(context.Background(), server.Client(), server.URL, "device-code")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sErr *httpStatusError
	if !errors.As(err, &sErr) {
		t.Fatalf("error type = %T, want *httpStatusError", err)
	}
	if sErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", sErr.StatusCode)
	}
}
