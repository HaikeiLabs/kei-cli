package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelProfilesList(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/model-profiles" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`[{"profile_id":"11111111-1111-1111-1111-111111111111","org_id":"org-1","display_name":"test-profile","endpoint":"https://api.example.com/v1","auth_type":"bearer","status":"active","version":1}]`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesList([]string{"--api-url", server.URL}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "test-profile") {
		t.Fatalf("list output missing profile: %s", stdout.String())
	}
}

func TestModelProfilesGet(t *testing.T) {
	profileID := "11111111-1111-1111-1111-111111111111"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/cli/model-profiles/" + profileID
		if r.URL.Path != wantPath || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"profile_id":"` + profileID + `","org_id":"org-1","display_name":"test-profile","endpoint":"https://api.example.com/v1","auth_type":"bearer","status":"active","version":1}`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesGet([]string{"--api-url", server.URL, profileID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), profileID) {
		t.Fatalf("get output missing profile ID: %s", stdout.String())
	}
}

func TestModelProfilesGetRequiresUUID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runModelProfilesGet([]string{"not-a-uuid"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("get exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "UUID") {
		t.Fatalf("get error = %q", stderr.String())
	}
}

func TestModelProfilesCreate(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/model-profiles" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var req createModelProfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DisplayName != "my-profile" || req.Endpoint != "https://api.openai.com/v1" || req.AuthType != "bearer" {
			t.Fatalf("unexpected request body: %#v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"profile_id":"22222222-2222-2222-2222-222222222222","org_id":"org-1","display_name":"my-profile","endpoint":"https://api.openai.com/v1","auth_type":"bearer","status":"active","version":1}`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesCreate([]string{"--api-url", server.URL, "--display-name", "my-profile", "--endpoint", "https://api.openai.com/v1", "--auth-type", "bearer"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "22222222") {
		t.Fatalf("create output missing profile: %s", stdout.String())
	}
}

func TestModelProfilesCreateRequiresFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runModelProfilesCreate([]string{"--display-name", "x"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("create exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--endpoint") {
		t.Fatalf("create error = %q", stderr.String())
	}
}

func TestModelProfilesUpdate(t *testing.T) {
	profileID := "11111111-1111-1111-1111-111111111111"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/cli/model-profiles/" + profileID
		if r.URL.Path != wantPath || r.Method != http.MethodPut {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var req updateModelProfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DisplayName != "new-name" {
			t.Fatalf("unexpected display_name: %q", req.DisplayName)
		}
		_, _ = w.Write([]byte(`{"profile_id":"` + profileID + `","display_name":"new-name","endpoint":"https://api.example.com/v1","auth_type":"bearer","status":"active","version":2}`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesUpdate([]string{"--api-url", server.URL, "--display-name", "new-name", profileID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "new-name") {
		t.Fatalf("update output missing new name: %s", stdout.String())
	}
}

func TestModelProfilesDeleteRequiresConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	profileID := "11111111-1111-1111-1111-111111111111"
	if code := runModelProfilesDelete([]string{profileID}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("delete exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("delete error = %q", stderr.String())
	}
}

func TestModelProfilesDelete(t *testing.T) {
	profileID := "11111111-1111-1111-1111-111111111111"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/cli/model-profiles/" + profileID
		if r.URL.Path != wantPath || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesDelete([]string{"--api-url", server.URL, "--yes", profileID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("delete exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted") {
		t.Fatalf("delete output = %q", stdout.String())
	}
}

func TestModelProfilesTest(t *testing.T) {
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/model-profiles/test" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var req testModelProfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Endpoint != "https://api.openai.com/v1" {
			t.Fatalf("unexpected endpoint: %q", req.Endpoint)
		}
		_, _ = w.Write([]byte(`{"status":"ok","model":"gpt-4","latency":"320ms"}`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesTest([]string{"--api-url", server.URL, "--endpoint", "https://api.openai.com/v1", "--model", "gpt-4"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("test exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status":"ok"`) {
		t.Fatalf("test output = %s", stdout.String())
	}
}

func TestModelProfilesTestRequiresEndpoint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runModelProfilesTest([]string{}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("test exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--endpoint") {
		t.Fatalf("test error = %q", stderr.String())
	}
}

func TestModelProfilesSetDefault(t *testing.T) {
	profileID := "11111111-1111-1111-1111-111111111111"
	store := &memoryCredentialStore{token: "cli-session-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/cli/model-profiles/" + profileID + "/default"
		if r.URL.Path != wantPath || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cli-session-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"profile_id":"` + profileID + `","is_workspace_default":true}`))
	}))
	defer server.Close()
	store.server = server.URL
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesSetDefault([]string{"--api-url", server.URL, profileID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("set-default exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), profileID) {
		t.Fatalf("set-default output missing profile ID: %s", stdout.String())
	}
}

func TestModelProfilesSetDefaultRequiresUUID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runModelProfilesSetDefault([]string{"not-a-uuid"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("set-default exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "UUID") {
		t.Fatalf("set-default error = %q", stderr.String())
	}
}

func TestModelProfilesCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 1},        // no token → auth fail
		{[]string{"get", "x"}, 2},    // bad UUID
		{[]string{"delete", "x"}, 2}, // bad UUID
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runModelProfilesCommand(tt.args, &stdout, &stderr, http.DefaultClient, store); code != tt.code {
			t.Errorf("model-profiles %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}
