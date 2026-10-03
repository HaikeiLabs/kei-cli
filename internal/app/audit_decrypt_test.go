package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func runAuditDecryptTest(t *testing.T, handler http.HandlerFunc, args ...string) (int, string, string) {
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
	code := runAuditDecryptCommand(args, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String()
}

func encryptWithIdentity(t *testing.T, identity *age.X25519Identity, plaintext string) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func encryptWithRecipients(t *testing.T, plaintext string, recipients ...age.Recipient) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestAuditDecryptRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	originalPlaintext := `{"tool":"get_weather","args":{"city":"London"}}`
	argsCiphertext := encryptWithIdentity(t, identity, originalPlaintext)

	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rec-42") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(auditRecord{
			ID:              "rec-42",
			Decision:        "ALLOW",
			ArgsCiphertext:  argsCiphertext,
			RecipientKeyIDs: []string{"test-key-id"},
		})
	}, "--record", "rec-42", "--identity", identityPath, "--stdout")

	if code != 0 {
		t.Fatalf("decrypt exit = %d, stderr = %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != strings.TrimSpace(originalPlaintext) {
		t.Fatalf("decrypted content mismatch:\n  got:  %q\n  want: %q", stdout, originalPlaintext)
	}
}

func TestAuditDecryptRequiresOutOrStdout(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:             "rec-noout",
			ArgsCiphertext: encryptWithIdentity(t, identity, "test"),
		})
	}, "--record", "rec-noout", "--identity", identityPath)

	if code != 2 {
		t.Fatalf("expected exit 2, got %d; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "--out") || !strings.Contains(stderr, "--stdout") {
		t.Fatalf("expected error mentioning --out and --stdout, got: %s", stderr)
	}
}

func TestAuditDecryptStdoutWithOutFile(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	originalPlaintext := `{"both":"work"}`
	argsCiphertext := encryptWithIdentity(t, identity, originalPlaintext)

	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "decrypted.txt")
	code, stdout, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:             "rec-both",
			ArgsCiphertext: argsCiphertext,
		})
	}, "--record", "rec-both", "--identity", identityPath, "--out", outPath, "--stdout")

	if code != 0 {
		t.Fatalf("decrypt exit = %d, stderr = %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != originalPlaintext {
		t.Fatalf("stdout mismatch:\n  got:  %q\n  want: %q", stdout, originalPlaintext)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != originalPlaintext {
		t.Fatalf("file content mismatch:\n  got:  %q\n  want: %q", string(data), originalPlaintext)
	}
}

func TestAuditDecryptWithOutFile(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	originalPlaintext := `{"result":"ok"}`
	argsCiphertext := encryptWithIdentity(t, identity, originalPlaintext)

	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "decrypted.txt")
	code, stdout, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:             "rec-out",
			ArgsCiphertext: argsCiphertext,
		})
	}, "--record", "rec-out", "--identity", identityPath, "--out", outPath)

	if code != 0 {
		t.Fatalf("decrypt exit = %d, stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout when --out is used, got: %s", stdout)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != originalPlaintext {
		t.Fatalf("decrypted content mismatch:\n  got:  %q\n  want: %q", string(data), originalPlaintext)
	}
}

func TestAuditDecryptEmptyCiphertext(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:             "rec-empty",
			ArgsCiphertext: "",
		})
	}, "--record", "rec-empty", "--identity", identityPath, "--stdout")

	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "no encrypted content") {
		t.Fatalf("expected 'no encrypted content' error, got: %s", stderr)
	}
}

func TestAuditDecryptWrongIdentity(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	argsCiphertext := encryptWithIdentity(t, identity, "secret payload")

	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(wrongIdentity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:             "rec-wrong",
			ArgsCiphertext: argsCiphertext,
		})
	}, "--record", "rec-wrong", "--identity", identityPath, "--stdout")

	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "decryption failed") {
		t.Fatalf("expected 'decryption failed' error, got: %s", stderr)
	}
}

func TestAuditDecryptRecordNotFound(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}, "--record", "rec-nonexistent", "--identity", identityPath, "--stdout")

	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", stderr)
	}
}

func TestAuditDecryptMissingRecordFlag(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditDecryptCommand([]string{}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "--record is required") {
		t.Fatalf("expected --record required error, got: %s", stderr.String())
	}
}

func TestAuditDecryptPositionalArgsRejected(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditDecryptCommand([]string{"extra-arg"}, &stdout, &stderr, http.DefaultClient, store)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestAuditDecryptLoginRequired(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	code := runAuditDecryptCommand([]string{
		"--record", "rec-nologin",
		"--identity", identityPath,
	}, &stdout, &stderr, http.DefaultClient, store)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("expected login error, got: %s", stderr.String())
	}
}

func TestAuditDecryptMultiRecipient(t *testing.T) {
	identity1, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity2, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	original := `{"multi":"recipient"}`
	argsCiphertext := encryptWithRecipients(t, original, identity1.Recipient(), identity2.Recipient())

	identityPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity1.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAuditDecryptTest(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(auditRecord{
			ID:              "rec-multi",
			ArgsCiphertext:  argsCiphertext,
			RecipientKeyIDs: []string{"key-1", "key-2"},
		})
	}, "--record", "rec-multi", "--identity", identityPath, "--stdout")

	if code != 0 {
		t.Fatalf("decrypt exit = %d, stderr = %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != original {
		t.Fatalf("decrypted content mismatch:\n  got:  %q\n  want: %q", stdout, original)
	}
}
