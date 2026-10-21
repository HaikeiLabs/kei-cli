package app

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// tokenStore implements credentialStore with a fallback chain:
// OS keychain first, then a 0600 file under ~/.config/kei/tokens/.
//
// When the OS keychain is unavailable (e.g. headless CI, SSH session without
// keychain forwarding), Save writes the token to a file and prints the file
// path to stderr — never the token itself.
type tokenStore struct {
	stderr io.Writer

	// Overridable keyring functions (defaulting to go-keyring in production).
	keyringSet    func(service, account, token string) error
	keyringGet    func(service, account string) (string, error)
	keyringDelete func(service, account string) error
}

// defaultKeyringSet is the production implementation that delegates to
// go-keyring. Used when the field is nil.
func defaultKeyringSet(service, account, token string) error {
	return keyring.Set(service, account, token)
}

func defaultKeyringGet(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func defaultKeyringDelete(service, account string) error {
	return keyring.Delete(service, account)
}

// tokenDir returns the directory used for fallback token files:
// ~/.config/kei/tokens/
func tokenDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kei", "tokens"), nil
}

// tokenFilePath returns the path to the fallback token file for the given
// server URL, using the hostname as the account identifier.
func tokenFilePath(serverURL string) (string, error) {
	dir, err := tokenDir()
	if err != nil {
		return "", err
	}
	account := tokenFileAccount(serverURL)
	return filepath.Join(dir, account), nil
}

// tokenFileAccount derives a filesystem-safe account name from a server URL.
// It uses the hostname, replacing any characters that are problematic on
// common filesystems with underscores.
func tokenFileAccount(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Host == "" {
		// Fall back to a safe encoding of the full URL.
		safe := strings.NewReplacer(
			"/", "_", ":", "_", "?", "_", "#", "_",
			" ", "_", "\\", "_",
		).Replace(serverURL)
		return safe
	}
	host := parsed.Host
	// Replace problematic filesystem characters with underscores.
	host = strings.NewReplacer(
		":", "_", "/", "_", "\\", "_",
	).Replace(host)
	return host
}

// saveTokenFile writes the token to a 0600 file under ~/.config/kei/tokens/.
func saveTokenFile(serverURL, token string) (string, error) {
	path, err := tokenFilePath(serverURL)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	// os.WriteFile only sets the mode when creating a new file; for an
	// existing file (e.g. left from a previous run with wrong permissions)
	// we must chmod explicitly so the token is always 0600.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("protect token file: %w", err)
	}
	return path, nil
}

// loadTokenFile reads the token from the fallback file.
func loadTokenFile(serverURL string) (string, error) {
	path, err := tokenFilePath(serverURL)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", keyring.ErrNotFound
		}
		return "", fmt.Errorf("read token file: %w", err)
	}
	return string(data), nil
}

// deleteTokenFile removes the fallback token file. It returns true when the
// file existed and was removed.
func deleteTokenFile(serverURL string) (bool, error) {
	path, err := tokenFilePath(serverURL)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove token file: %w", err)
	}
	return true, nil
}

// Save attempts to store the token in the OS keychain. If the keychain is
// unavailable, it falls back to a 0600 file under ~/.config/kei/tokens/ and
// prints the path to stderr. The token is never written to stderr.
func (s *tokenStore) Save(serverURL, token string) error {
	set := s.keyringSet
	if set == nil {
		set = defaultKeyringSet
	}

	err := set(keychainService, keychainAccount(serverURL), token)
	if err == nil {
		return nil
	}

	// Keychain failed — fall back to a file.
	path, fileErr := saveTokenFile(serverURL, token)
	if fileErr != nil {
		return fmt.Errorf("save CLI token in OS keychain: %v, and file fallback failed: %w", err, fileErr)
	}

	if s.stderr != nil {
		fmt.Fprintf(s.stderr, "CLI token saved to %s (OS keychain unavailable: %v)\n", path, err)
	}
	return nil
}

// Load retrieves the token from the OS keychain first. If not found in the
// current or legacy service, it falls back to the token file on disk.
func (s *tokenStore) Load(serverURL string) (string, error) {
	get := s.keyringGet
	if get == nil {
		get = defaultKeyringGet
	}

	account := keychainAccount(serverURL)

	// Try the current keychain service.
	token, err := get(keychainService, account)
	if err == nil {
		return token, nil
	}

	// Try the legacy keychain service.
	token, err = get(legacyKeychainService, account)
	if err == nil {
		return token, nil
	}

	// Fall back to the token file on disk.
	return loadTokenFile(serverURL)
}

// Delete removes the token from both the OS keychain (current and legacy
// services) and the fallback token file. It returns true if any entry was
// removed. Keychain errors (including unavailability) are treated as
// non-fatal so that a token stored in the fallback file can still be cleaned
// up.
func (s *tokenStore) Delete(serverURL string) (bool, error) {
	del := s.keyringDelete
	if del == nil {
		del = defaultKeyringDelete
	}

	account := keychainAccount(serverURL)
	removed := false

	// Delete from current keychain service — non-fatal on error.
	if err := del(keychainService, account); err == nil {
		removed = true
	}

	// Delete from legacy keychain service — non-fatal on error.
	if err := del(legacyKeychainService, account); err == nil {
		removed = true
	}

	// Delete the fallback file.
	fileRemoved, err := deleteTokenFile(serverURL)
	if err != nil {
		return removed, err
	}
	if fileRemoved {
		removed = true
	}

	return removed, nil
}
