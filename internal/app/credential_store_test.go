package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

const testCredentialStoreJSON = `{"id":"cs-1","org_id":"` + testProfileOrgID + `","secret_backend":"aws-secrets-manager","backend_config":{"region":"us-east-1"},"secret_name_prefix":"kei/","status":"active","version":1}`

func TestCredentialStoreGet(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testCredentialStoreJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodGet, credStorePath)
	if !strings.Contains(stdout.String(), "aws-secrets-manager") {
		t.Fatalf("get output = %s", stdout.String())
	}
}

func TestCredentialStoreGetNotFound(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`credential store not found`))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("get exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "No credential store") {
		t.Fatalf("get error = %q", stderr.String())
	}
}

func TestCredentialStoreGetHTMLIsNotReportedAsMissing(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html></html>`))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("get exit = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), "No credential store") || !strings.Contains(stderr.String(), "returned HTML") {
		t.Fatalf("get error = %q", stderr.String())
	}
}

func TestCredentialStorePut(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testCredentialStoreJSON))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--secret-backend", "aws-secrets-manager", "--backend-config", `{"region":"us-east-1"}`, "--secret-name-prefix", "kei/"}
	if code := runCredentialStorePut(args, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("put exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPut, credStorePath)
	if got.Body["secret_backend"] != "aws-secrets-manager" || got.Body["secret_name_prefix"] != "kei/" {
		t.Fatalf("put body = %#v", got.Body)
	}
	if config, _ := got.Body["backend_config"].(map[string]any); config["region"] != "us-east-1" {
		t.Fatalf("put backend_config = %#v", got.Body["backend_config"])
	}
	if !strings.Contains(stdout.String(), "cs-1") {
		t.Fatalf("put output = %s", stdout.String())
	}
}

func TestCredentialStoreUpdateUsesPATCHWithUpdateMask(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testCredentialStoreJSON))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--secret-name-prefix", "kei-prod/", "--backend-config", `{"region":"us-west-2"}`}
	if code := runCredentialStoreUpdate(args, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, credStorePath)
	mask, _ := got.Body["update_mask"].(map[string]any)
	paths, _ := mask["paths"].([]any)
	if len(paths) != 2 || paths[0] != "backend_config" || paths[1] != "secret_name_prefix" {
		t.Fatalf("update_mask = %#v", got.Body["update_mask"])
	}
	if got.Body["secret_name_prefix"] != "kei-prod/" {
		t.Fatalf("update body = %#v", got.Body)
	}
	if _, ok := got.Body["secret_backend"]; ok {
		t.Fatalf("unmasked secret_backend sent: %#v", got.Body)
	}
	if _, ok := got.Body["status"]; ok {
		t.Fatalf("unmasked status sent: %#v", got.Body)
	}
}

func TestCredentialStoreUpdateRequiresAField(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreUpdate(nil, &stdout, &stderr, http.DefaultClient, &memoryCredentialStore{}); code != 2 {
		t.Fatalf("update exit = %d, want 2", code)
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
	for _, config := range []string{"not-json", `{"region":1}`} {
		var stdout, stderr bytes.Buffer
		store := &memoryCredentialStore{token: "cli-session-token"}
		if code := runCredentialStorePut([]string{"--secret-backend", "test", "--backend-config", config}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
			t.Fatalf("put %s exit = %d, want 2", config, code)
		}
		if !strings.Contains(stderr.String(), "valid JSON") {
			t.Fatalf("put error = %q", stderr.String())
		}
	}
}

func TestCredentialStoreCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"unknown"}, 2},
		{[]string{"get"}, 1},    // no token → auth fail
		{[]string{"put"}, 2},    // no --secret-backend
		{[]string{"update"}, 2}, // no field
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runCredentialStoreCommand(tt.args, &stdout, &stderr, http.DefaultClient, store); code != tt.code {
			t.Errorf("credential-store %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}
