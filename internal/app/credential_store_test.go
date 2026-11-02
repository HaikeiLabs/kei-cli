package app

import (
	"bytes"
	"encoding/json"
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

func TestCredentialStoreGetNeverPrintsSecretValue(t *testing.T) {
	const sentinel = "credential-store-secret-sentinel"
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"cs-1","secret_backend":"aws-secrets-manager","secret_name_prefix":"kei/prod","version":3,"secret_value":"` + sentinel + `"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), sentinel) || strings.Contains(stderr.String(), sentinel) {
		t.Fatalf("get output exposed secret value: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("get output is not JSON: %v; output=%q", err, stdout.String())
	}
	if _, ok := output["secret_value"]; ok {
		t.Fatalf("get JSON exposed secret value field: %v", output)
	}
	if !strings.Contains(stdout.String(), `"secret_name_prefix":"kei/prod"`) {
		t.Fatalf("get output omitted secret reference: %q", stdout.String())
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

var credentialStorePutArgs = []string{"--secret-backend", "aws-secrets-manager", "--backend-config", `{"region":"us-east-1"}`, "--secret-name-prefix", "my-chat"}

// TestCredentialStorePutUsesPATCHWithAllowMissing checks the HAI-954 wire
// shape: PATCH, all three fields in update_mask, allow_missing=true, and both
// 201 (created) and 200 (updated) succeed.
func TestCredentialStorePutUsesPATCHWithAllowMissing(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(testCredentialStoreJSON))
			})
			var stdout, stderr bytes.Buffer
			if code := runCredentialStorePut(credentialStorePutArgs, &stdout, &stderr, http.DefaultClient, store); code != 0 {
				t.Fatalf("put exit = %d, stderr=%s", code, stderr.String())
			}
			got := fake.only(t)
			assertRequest(t, got, http.MethodPatch, credStorePath)
			if got.Query != "allow_missing=true" {
				t.Fatalf("put query = %q, want allow_missing=true", got.Query)
			}
			if got.Body["allow_missing"] != true {
				t.Fatalf("put body allow_missing = %#v", got.Body["allow_missing"])
			}
			mask, _ := got.Body["update_mask"].(map[string]any)
			paths, _ := mask["paths"].([]any)
			if len(paths) != 3 || paths[0] != "secret_backend" || paths[1] != "backend_config" || paths[2] != "secret_name_prefix" {
				t.Fatalf("update_mask = %#v", got.Body["update_mask"])
			}
			if got.Body["secret_backend"] != "aws-secrets-manager" || got.Body["secret_name_prefix"] != "my-chat" {
				t.Fatalf("put body = %#v", got.Body)
			}
			if config, _ := got.Body["backend_config"].(map[string]any); config["region"] != "us-east-1" {
				t.Fatalf("put backend_config = %#v", got.Body["backend_config"])
			}
			if !strings.Contains(stdout.String(), "cs-1") {
				t.Fatalf("put output = %s", stdout.String())
			}
		})
	}
}

// TestCredentialStorePutOlderCatalog: a catalog without HAI-954 refuses
// PATCH-create; the error must say the catalog needs that update.
func TestCredentialStorePutOlderCatalog(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusMethodNotAllowed, `{"error":"method not allowed"}`},
		{http.StatusNotFound, `{"error":"credential store not found"}`},
		{http.StatusBadRequest, `{"error":"update_mask is required"}`},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			var stdout, stderr bytes.Buffer
			if code := runCredentialStorePut(credentialStorePutArgs, &stdout, &stderr, http.DefaultClient, store); code != 1 {
				t.Fatalf("put exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "HAI-954") || !strings.Contains(stderr.String(), "catalog") {
				t.Fatalf("put error = %q, want a catalog HAI-954 hint", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("put stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestCredentialStorePutOtherErrorHasNoCatalogHint(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"credentials exist"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStorePut(credentialStorePutArgs, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("put exit = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), "HAI-954") {
		t.Fatalf("put error = %q, want no older-catalog hint on 409", stderr.String())
	}
}

func TestCredentialStorePutRequiresSecretNamePrefix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{token: "cli-session-token"}
	if code := runCredentialStorePut([]string{"--secret-backend", "aws-secrets-manager"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("put exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--secret-name-prefix") {
		t.Fatalf("put error = %q", stderr.String())
	}
}

func TestCredentialStoreSecretNamePrefixHelp(t *testing.T) {
	for _, sub := range []string{"put", "update"} {
		var stdout, stderr bytes.Buffer
		_ = runCredentialStoreCommand([]string{sub, "--help"}, &stdout, &stderr, http.DefaultClient, &memoryCredentialStore{})
		help := stdout.String() + stderr.String()
		if !strings.Contains(help, secretNamePrefixHelp) {
			t.Fatalf("credential-store %s help does not explain --secret-name-prefix: %s", sub, help)
		}
	}
}

func TestCredentialStoreUpdateDoesNotSendAllowMissing(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testCredentialStoreJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreUpdate([]string{"--status", "disabled"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	if _, ok := got.Body["allow_missing"]; ok || got.Query != "" {
		t.Fatalf("update must not create a store: query=%q body=%#v", got.Query, got.Body)
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
		if code := runCredentialStorePut([]string{"--secret-backend", "test", "--secret-name-prefix", "my-chat", "--backend-config", config}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
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
		{[]string{}, 0}, // help to stdout
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
