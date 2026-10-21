package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errKeychainUnavailable simulates the OS keychain being unavailable (e.g., exit status 152 on macOS,
// no secret service on Linux, CI without keychain access).
var errKeychainUnavailable = errors.New("keychain unavailable (exit status 152)")

func TestTokenStoreFallbackToFileOnKeychainFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "", errKeychainUnavailable
		},
		keyringSet: func(_, _, _ string) error {
			return errKeychainUnavailable
		},
		keyringDelete: func(_, _ string) error {
			return errKeychainUnavailable
		},
	}

	serverURL := "https://api.haikeilabs.com"
	token := "cli-token-abc123"

	// Save should succeed via file fallback.
	if err := store.Save(serverURL, token); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Assert stderr contains the token file path but NOT the token.
	stderrOut := stderrBuf.String()
	if !strings.Contains(stderrOut, ".config/kei/tokens/") {
		t.Fatalf("stderr should mention token file path, got: %s", stderrOut)
	}
	if strings.Contains(stderrOut, token) {
		t.Fatalf("stderr must not contain the token itself, got: %s", stderrOut)
	}

	// Compute the expected token file path.
	home := t.TempDir() // retake since we used t.TempDir() above in Setenv
	home = os.Getenv("HOME")
	tokenDir := filepath.Join(home, ".config", "kei", "tokens")
	entries, err := os.ReadDir(tokenDir)
	if err != nil {
		t.Fatalf("read token dir %s: %v", tokenDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 token file, got %d: %v", len(entries), entries)
	}
	tokenFile := filepath.Join(tokenDir, entries[0].Name())

	// Assert file mode is 0600.
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat token file %s: %v", tokenFile, err)
	}
	if info.Mode() != 0o600 {
		t.Fatalf("token file mode = %o, want 0600", info.Mode())
	}

	// Assert file contains the token.
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read token file %s: %v", tokenFile, err)
	}
	if string(data) != token {
		t.Fatalf("token file content = %q, want %q", string(data), token)
	}

	// Load should succeed, reading from the file.
	loaded, err := store.Load(serverURL)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != token {
		t.Fatalf("Load returned %q, want %q", loaded, token)
	}

	// Delete should remove the file (keychain delete fails, but file delete succeeds).
	removed, err := store.Delete(serverURL)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !removed {
		t.Fatal("Delete returned removed=false, want true")
	}

	// Assert the token file is gone.
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("token file %s should be deleted, stat: %v", tokenFile, err)
	}

	// Load should now fail (nothing in keychain, nothing on disk).
	_, err = store.Load(serverURL)
	if err == nil {
		t.Fatal("Load after delete should fail")
	}
}

func TestTokenStoreKeychainFirstOnLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "keychain-token", nil
		},
		keyringSet: func(_, _, _ string) error {
			return nil
		},
		keyringDelete: func(_, _ string) error {
			return nil
		},
	}

	serverURL := "https://api.haikeilabs.com"

	// Load should return the keychain token without hitting the file.
	loaded, err := store.Load(serverURL)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != "keychain-token" {
		t.Fatalf("Load returned %q, want %q", loaded, "keychain-token")
	}
}

func TestTokenStoreSaveSucceedsWithKeychain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "", errors.New("not found")
		},
		keyringSet: func(_, _, _ string) error {
			return nil
		},
		keyringDelete: func(_, _ string) error {
			return nil
		},
	}

	serverURL := "https://api.haikeilabs.com"
	token := "cli-token-from-keychain"

	if err := store.Save(serverURL, token); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// No fallback message when keychain works.
	if stderrBuf.Len() > 0 {
		t.Fatalf("stderr should be empty when keychain works, got: %s", stderrBuf.String())
	}

	// No token file should exist.
	home := os.Getenv("HOME")
	tokenDir := filepath.Join(home, ".config", "kei", "tokens")
	if _, err := os.Stat(tokenDir); !os.IsNotExist(err) {
		t.Fatalf("token dir %s should not exist when keychain works", tokenDir)
	}
}

func TestTokenStoreDeleteFromBoth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	keychainDeleted := false
	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "", errKeychainUnavailable
		},
		keyringSet: func(_, _, _ string) error {
			return errKeychainUnavailable
		},
		keyringDelete: func(_, _ string) error {
			keychainDeleted = true
			return nil
		},
	}

	serverURL := "https://api.haikeilabs.com"
	token := "cli-token-to-delete"

	// Save (falls back to file).
	if err := store.Save(serverURL, token); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Delete.
	removed, err := store.Delete(serverURL)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !removed {
		t.Fatal("Delete returned removed=false, want true")
	}
	if !keychainDeleted {
		t.Fatal("keychain delete was not called")
	}

	// Token file should be gone.
	home := os.Getenv("HOME")
	tokenDir := filepath.Join(home, ".config", "kei", "tokens")
	entries, _ := os.ReadDir(tokenDir)
	if len(entries) != 0 {
		t.Fatalf("expected no token files after delete, got %d", len(entries))
	}
}

func TestTokenStoreLoadPrefersKeychain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	callCount := 0
	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			callCount++
			return "keychain-token", nil
		},
		keyringSet: func(_, _, _ string) error {
			return nil
		},
		keyringDelete: func(_, _ string) error {
			return nil
		},
	}

	serverURL := "https://api.haikeilabs.com"

	// Load should use keychain only.
	loaded, err := store.Load(serverURL)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded != "keychain-token" {
		t.Fatalf("Load returned %q, want %q", loaded, "keychain-token")
	}
	if callCount != 1 {
		t.Fatalf("keyring.Get called %d times, want 1", callCount)
	}
}

func TestTokenStoreFallbackFixesExistingFileMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "", errKeychainUnavailable
		},
		keyringSet: func(_, _, _ string) error {
			return errKeychainUnavailable
		},
		keyringDelete: func(_, _ string) error {
			return errKeychainUnavailable
		},
	}

	serverURL := "https://api.haikeilabs.com"
	token := "cli-token-mode-fix"

	// First save creates the file (0600 via WriteFile).
	if err := store.Save(serverURL, token); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Find the token file.
	home := os.Getenv("HOME")
	tokenDir := filepath.Join(home, ".config", "kei", "tokens")
	entries, err := os.ReadDir(tokenDir)
	if err != nil {
		t.Fatalf("read token dir: %v", err)
	}
	tokenFile := filepath.Join(tokenDir, entries[0].Name())

	// Deliberately weaken permissions to 0644, simulating a prior run that
	// left the file with overly permissive mode.
	if err := os.Chmod(tokenFile, 0o644); err != nil {
		t.Fatalf("chmod to 0644: %v", err)
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat before second save: %v", err)
	}
	if info.Mode() != 0o644 {
		t.Fatalf("expected pre-existing 0644 for this test, got %o", info.Mode())
	}

	// Second save — the file already exists at 0644; saveTokenFile must
	// correct it back to 0600 via the explicit os.Chmod.
	if err := store.Save(serverURL, "cli-token-mode-fix-renewed"); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	info, err = os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat after second save: %v", err)
	}
	if info.Mode() != 0o600 {
		t.Fatalf("token file mode after correction = %o, want 0600", info.Mode())
	}

	// Content should reflect the latest token.
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if string(data) != "cli-token-mode-fix-renewed" {
		t.Fatalf("token file content = %q, want %q", string(data), "cli-token-mode-fix-renewed")
	}
}

func TestTokenStoreStderrMessageContainsPathOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var stderrBuf bytes.Buffer
	store := &tokenStore{
		stderr: &stderrBuf,
		keyringGet: func(_, _ string) (string, error) {
			return "", errKeychainUnavailable
		},
		keyringSet: func(_, _, _ string) error {
			return errKeychainUnavailable
		},
		keyringDelete: func(_, _ string) error {
			return errKeychainUnavailable
		},
	}

	serverURL := "https://api.haikeilabs.com"
	token := "super-secret-token-value"

	if err := store.Save(serverURL, token); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	msg := stderrBuf.String()
	// Must contain the path.
	if !strings.Contains(msg, "CLI token saved to") {
		t.Fatalf("stderr should contain 'CLI token saved to', got: %s", msg)
	}
	if !strings.Contains(msg, ".config/kei/tokens/") {
		t.Fatalf("stderr should contain token directory, got: %s", msg)
	}
	// Must NOT contain the token.
	if strings.Contains(msg, token) {
		t.Fatalf("stderr must not contain the token, got: %s", msg)
	}
	// Must contain the fallback reason.
	if !strings.Contains(msg, "OS keychain unavailable") {
		t.Fatalf("stderr should mention 'OS keychain unavailable', got: %s", msg)
	}
}
