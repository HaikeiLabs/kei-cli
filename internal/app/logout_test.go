package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogoutRemovesStoredCredential(t *testing.T) {
	store := &memoryCredentialStore{server: "https://app.haikeilabs.com", token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	if code := runLogoutCommand([]string{"--api-url", "https://app.haikeilabs.com/"}, &stdout, &stderr, store); code != 0 {
		t.Fatalf("logout exit = %d, stderr=%s", code, stderr.String())
	}
	if store.token != "" || store.server != "" {
		t.Fatalf("credential survived logout: %#v", store)
	}
	if !strings.Contains(stdout.String(), "Logged out of Kei for app.haikeilabs.com") {
		t.Fatalf("logout output = %q", stdout.String())
	}
}

func TestLogoutIsIdempotentWhenNotLoggedIn(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	if code := runLogoutCommand([]string{"--api-url", "https://app.haikeilabs.com"}, &stdout, &stderr, store); code != 0 {
		t.Fatalf("logout exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Not logged in to app.haikeilabs.com") {
		t.Fatalf("logout output = %q", stdout.String())
	}
}

func TestLogoutRejectsInvalidURLAndPositionalArgs(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	if code := runLogoutCommand([]string{"--api-url", "not-a-url"}, &stdout, &stderr, store); code != 2 {
		t.Fatalf("invalid URL exit = %d, want 2", code)
	}
	if code := runLogoutCommand([]string{"extra"}, &stdout, &stderr, store); code != 2 {
		t.Fatalf("positional argument exit = %d, want 2", code)
	}
}

func TestPrintVersion(t *testing.T) {
	var out bytes.Buffer
	printVersion(&out, "test-version")
	if got := out.String(); got != "kei test-version\n" {
		t.Fatalf("version output = %q", got)
	}
}
