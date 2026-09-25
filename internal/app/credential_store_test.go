package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialStoreGet(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/credential-store" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"cs-1","org_id":"org-1","secret_backend":"aws_secrets_manager","backend_config":{"region":"us-east-1"},"secret_name_prefix":"kei/","status":"active","version":1}`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet([]string{}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aws_secrets_manager") {
		t.Fatalf("get output missing backend: %s", stdout.String())
	}
}

func TestCredentialStoreGetNotFound(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet([]string{}, &stdout, &stderr, server.Client(), store); code != 1 {
		t.Fatalf("get exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "No credential store") {
		t.Fatalf("get error = %q", stderr.String())
	}
}

func TestCredentialStorePut(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/credential-store" || r.Method != http.MethodPut {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var req putCredentialStoreRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.SecretBackend != "aws_secrets_manager" {
			t.Fatalf("unexpected secret_backend: %q", req.SecretBackend)
		}
		if req.SecretNamePrefix != "kei/" {
			t.Fatalf("unexpected secret_name_prefix: %q", req.SecretNamePrefix)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"cs-1","org_id":"org-1","secret_backend":"aws_secrets_manager","backend_config":{"region":"us-east-1"},"secret_name_prefix":"kei/","status":"active","version":1}`))
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runCredentialStorePut([]string{"--secret-backend", "aws_secrets_manager", "--backend-config", `{"region":"us-east-1"}`, "--secret-name-prefix", "kei/"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("put exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cs-1") {
		t.Fatalf("put output missing ID: %s", stdout.String())
	}
}

func TestCredentialStorePutRequiresBackend(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runCredentialStorePut([]string{}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("put exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--secret-backend") {
		t.Fatalf("put error = %q", stderr.String())
	}
}

func TestCredentialStorePutInvalidJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runCredentialStorePut([]string{"--secret-backend", "test", "--backend-config", "not-json"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("put exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "valid JSON") {
		t.Fatalf("put error = %q", stderr.String())
	}
}

func TestCredentialStoreCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"unknown"}, 2},
		{[]string{"get"}, 1}, // no token → auth fail
		{[]string{"put"}, 2}, // no --secret-backend
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runCredentialStoreCommand(tt.args, &stdout, &stderr, http.DefaultClient, store); code != tt.code {
			t.Errorf("credential-store %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}
