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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) { sleeps++ }, func(url string) error {
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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error {
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

func TestLogin404FallsBackToLegacy(t *testing.T) {
	var legacyCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/deviceAuthorizations":
			w.WriteHeader(http.StatusNotFound)
		case "/api/cli/device/authorize":
			legacyCalled = true
			json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
				DeviceCode: "legacy-device", UserCode: "LEGACY-12345",
				ExpiresAt: time.Now().Add(time.Minute), IntervalSeconds: 1,
			})
		case "/api/cli/device/token":
			json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "approved", AccessToken: "legacy-token", OrgID: "org-legacy"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !legacyCalled {
		t.Fatal("expected legacy flow to be called on 404")
	}
	if store.token != "legacy-token" {
		t.Fatalf("token = %q, want %q", store.token, "legacy-token")
	}
	if !strings.Contains(output.String(), "Note: AIP device authorization not available") {
		t.Fatalf("output missing fallback notice: %q", output.String())
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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(d time.Duration) { sleepDurations = append(sleepDurations, d) }, func(string) error { return nil })
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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
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
	err := login(context.Background(), server.URL, "kei", "v0.0.1", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
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
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
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
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
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
	store := &memoryCredentialStore{token: testCLIToken}
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
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
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
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	if code := runBotCredentialCommand([]string{"--installation", installationID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("credential command exit = %d, stderr = %s", code, stderr.String())
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

func TestLegacyWebHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{defaultKeiWebURL, "https://app.haikeilabs.com"},
		{"https://api.haikeilabs.com", "https://app.haikeilabs.com"},
		{"https://app.haikeilabs.com", "https://app.haikeilabs.com"},
		{"https://api.custom.io", "https://app.custom.io"},
		{"https://app.custom.io", "https://app.custom.io"},
		{"https://console.custom.io", "https://console.custom.io"},
		{"http://127.0.0.1:8443", "http://127.0.0.1:8443"},
	}
	for _, tc := range tests {
		got, err := legacyWebHost(tc.input)
		if err != nil {
			t.Errorf("legacyWebHost(%q) = _, %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("legacyWebHost(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLoginLegacyFallbackUsesWebHostForActivationURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device/authorize":
			json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
				DeviceCode: "legacy-dev", UserCode: "LGCY-99999",
				ExpiresAt: time.Now().Add(time.Minute), IntervalSeconds: 1,
			})
		case "/api/cli/device/token":
			json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "approved", AccessToken: "t", OrgID: "o"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	// Call legacyLogin directly with a webURL that differs from the API server URL.
	err := legacyLogin(context.Background(), "https://app.haikeilabs.com", server.URL, "kei", &output, server.Client(), store, func(time.Duration) {}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "https://app.haikeilabs.com/cli/activate") {
		t.Fatalf("activation URL should use web host, got:\n%s", output.String())
	}
	if strings.Contains(output.String(), server.URL+"/cli/activate") {
		t.Fatalf("activation URL should NOT use the API server URL, got:\n%s", output.String())
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
	if !strings.Contains(stdout.String(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("table output missing workspace id:\n%s", stdout.String())
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

func TestBotCredentialWorkspaceByUUIDMakesNoDiscoveryCall(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	workspaceUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	workspaceAPICalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/organizations/org-1/workspaces" {
			workspaceAPICalled = true
		}
		if r.URL.Path == "/api/v1/organizations/org-1/runtime-installations/"+installationID+":issueCredential" {
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
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
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
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "The session stopped unexpectedly.", "--session", path, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
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
