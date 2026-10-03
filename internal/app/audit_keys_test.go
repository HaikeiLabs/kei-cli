package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func testToken(orgID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"org_id":%q}`, orgID)))
	return "header." + payload + ".signature"
}

func runAuditKeysCreateTest(t *testing.T, handler http.HandlerFunc, extraArgs ...string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("authorization header missing")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
	var stdout, stderr bytes.Buffer
	args := append([]string{}, extraArgs...)
	code := runAuditKeysCreateCommand(args, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String()
}

func runAuditKeysListTest(t *testing.T, handler http.HandlerFunc, extraArgs ...string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysListCommand(extraArgs, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String()
}

func runAuditKeysDisableTest(t *testing.T, handler http.HandlerFunc, args []string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysDisableCommand(args, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String()
}

func TestAuditKeysCreateWritesIdentityFileWithMode0600(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "audit-identity.txt")

	code, stdout, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req createAuditKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.PublicKey == "" || !strings.HasPrefix(req.PublicKey, "age1") {
			t.Fatalf("public_key missing or invalid: %q", req.PublicKey)
		}
		if req.DisplayName != "test-key" {
			t.Fatalf("display_name = %q, want %q", req.DisplayName, "test-key")
		}
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:       "key-abc123",
			PublicKey:   req.PublicKey,
			DisplayName: "test-key",
			State:       "active",
			Kind:        "customer",
			CreateTime:  timePtr(time.Now()),
		})
	}, "--identity-out", identityPath, "--name", "test-key")

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}

	info, err := os.Stat(identityPath)
	if err != nil {
		t.Fatalf("identity file not created: %v", err)
	}
	if info.Mode()&0o777 != 0o600 {
		t.Fatalf("identity file mode = %o, want 0600", info.Mode()&0o777)
	}

	data, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "AGE-SECRET-KEY-") {
		t.Fatalf("identity file missing secret key: %s", string(data))
	}

	if strings.Contains(stdout, "AGE-SECRET-KEY-") {
		t.Fatalf("stdout leaked private key: %s", stdout)
	}
	if !strings.Contains(stdout, "key_id: key-abc123") {
		t.Fatalf("stdout missing key_id: %s", stdout)
	}
	if !strings.Contains(stdout, "public_key: age1") {
		t.Fatalf("stdout missing public_key: %s", stdout)
	}
}

func TestAuditKeysCreateRejectsExistingFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "audit-identity.txt")
	if err := os.WriteFile(identityPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach server")
	}, "--identity-out", identityPath)

	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("expected 'already exists' error, got: %s", stderr)
	}
}

func TestAuditKeysCreateForceOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "audit-identity.txt")
	if err := os.WriteFile(identityPath, []byte("old-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req createAuditKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:     "key-xyz",
			PublicKey: req.PublicKey,
			State:     "active",
			Kind:      "customer",
		})
	}, "--identity-out", identityPath, "--force")

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}

	data, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "old-data") {
		t.Fatalf("identity file was not overwritten")
	}
	if !strings.Contains(string(data), "AGE-SECRET-KEY-") {
		t.Fatalf("identity file missing new key: %s", string(data))
	}
}

func TestAuditKeysCreateForceFixesPermissions(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "audit-identity.txt")
	if err := os.WriteFile(identityPath, []byte("loose-perms"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req createAuditKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:     "key-perm-fix",
			PublicKey: req.PublicKey,
			State:     "active",
			Kind:      "customer",
		})
	}, "--identity-out", identityPath, "--force")

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}

	info, err := os.Stat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o777 != 0o600 {
		t.Fatalf("identity file mode = %o, want 0600 (was 0644 before --force)", info.Mode()&0o777)
	}
}

func TestAuditKeysCreateUploadsOnlyPublicKey(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity.txt")

	code, stdout, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["public_key"]; !ok {
			t.Fatal("request missing public_key")
		}
		if body["private_key"] != nil {
			t.Fatal("request contains private_key")
		}
		if body["identity"] != nil {
			t.Fatal("request contains identity")
		}
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:     "key-no-secret",
			PublicKey: body["public_key"].(string),
			State:     "active",
			Kind:      "customer",
		})
	}, "--identity-out", identityPath)

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}
	_ = stdout
}

func TestAuditKeysListTable(t *testing.T) {
	now := time.Now()
	code, stdout, stderr := runAuditKeysListTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		json.NewEncoder(w).Encode(auditKeyListResponse{
			Keys: []auditEncryptionKey{
				{KeyID: "key-1", PublicKey: "age1aaaa", DisplayName: "prod-key", State: "active", Kind: "customer", CreateTime: &now},
				{KeyID: "key-2", PublicKey: "age1bbbb", DisplayName: "backup-key", State: "disabled", Kind: "customer", CreateTime: &now},
			},
		})
	}))

	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "key-1") {
		t.Fatalf("table missing key-1:\n%s", stdout)
	}
	if !strings.Contains(stdout, "age1aaaa") {
		t.Fatalf("table missing public key:\n%s", stdout)
	}
	if !strings.Contains(stdout, "prod-key") {
		t.Fatalf("table missing display name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "active") {
		t.Fatalf("table missing state:\n%s", stdout)
	}
	if !strings.Contains(stdout, "disabled") {
		t.Fatalf("table missing disabled state:\n%s", stdout)
	}
}

func TestAuditKeysListEmpty(t *testing.T) {
	code, stdout, stderr := runAuditKeysListTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditKeyListResponse{Keys: []auditEncryptionKey{}})
	}))

	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "No audit encryption keys found.") {
		t.Fatalf("unexpected output for empty list: %s", stdout)
	}
}

func TestAuditKeysListRequiresLogin(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysListCommand([]string{}, &stdout, &stderr, http.DefaultClient, store)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("expected login error, got: %s", stderr.String())
	}
}

func TestAuditKeysDisableWithConfirmation(t *testing.T) {
	keyID := "key-to-disable"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/"+keyID+":disable") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:       keyID,
			PublicKey:   "age1test",
			State:       "disabled",
			Kind:        "customer",
			DisableTime: timePtr(time.Now()),
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	_, _ = w.WriteString(keyID + "\n")
	_ = w.Close()
	defer func() { os.Stdin = origStdin }()

	var stdout, stderr bytes.Buffer
	code := runAuditKeysDisableCommand([]string{keyID}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("disable exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "disabled") {
		t.Fatalf("output missing disable confirmation: %s", stdout.String())
	}
}

func TestAuditKeysDisableRejectsWrongConfirmation(t *testing.T) {
	keyID := "key-to-disable"
	store := &memoryCredentialStore{}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	_, _ = w.WriteString("wrong-key\n")
	_ = w.Close()
	defer func() { os.Stdin = origStdin }()

	var stdout, stderr bytes.Buffer
	code := runAuditKeysDisableCommand([]string{keyID}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "confirmation does not match") {
		t.Fatalf("expected confirmation error, got: %s", stderr.String())
	}
}

func TestAuditKeysCreateRejectsPositionalArgs(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysCreateCommand([]string{"extra-arg"}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestAuditKeysListRejectsPositionalArgs(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysListCommand([]string{"extra-arg"}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestAuditKeysCreateUsesDefaultIdentityPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	code, _, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req createAuditKeyRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:     "key-default-path",
			PublicKey: req.PublicKey,
			State:     "active",
			Kind:      "customer",
		})
	})

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}

	expectedPath := filepath.Join(dir, ".config", "kei", "audit-identity.txt")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("identity file not at default path %q: %v", expectedPath, err)
	}
}

func TestAuditKeysDisableRejectsNoArgs(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditKeysDisableCommand([]string{}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestLoadAgeIdentity(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	content := "# age identity\n# key_id: test-key\n" + identity.String() + "\n"
	if err := os.WriteFile(identityPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, pubKey, err := loadAgeIdentity(identityPath)
	if err != nil {
		t.Fatalf("loadAgeIdentity: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded identity is nil")
	}
	if pubKey != identity.Recipient().String() {
		t.Fatalf("public key mismatch: %q vs %q", pubKey, identity.Recipient().String())
	}
}

func TestLoadAgeIdentityMissingKey(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte("# just a comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadAgeIdentity(identityPath)
	if err == nil || !strings.Contains(err.Error(), "no age secret key found") {
		t.Fatalf("expected error about missing key, got: %v", err)
	}
}

func TestAuditKeysCreateNoName(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "identity.txt")

	code, _, stderr := runAuditKeysCreateTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req createAuditKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.DisplayName != "" {
			t.Fatalf("display_name should be empty when --name not set, got %q", req.DisplayName)
		}
		json.NewEncoder(w).Encode(auditEncryptionKey{
			KeyID:     "key-no-name",
			PublicKey: req.PublicKey,
			State:     "active",
			Kind:      "customer",
		})
	}, "--identity-out", identityPath)

	if code != 0 {
		t.Fatalf("create exit = %d, stderr = %s", code, stderr)
	}
}
