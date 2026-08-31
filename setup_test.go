package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWritesProtectedConfigWithoutPrintingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kei.yaml")
	var stdout, stderr strings.Builder
	if code := runSetupCommand([]string{
		"--config", path,
		"--control-plane-url", "https://control.example.test",
		"--runtime-token", "runtime-secret",
		"--harness-url", "http://127.0.0.1:8088",
		"--proxy-path", "/tmp/kei-proxy",
		"--skip-verify",
	}, &stdout, &stderr, strings.NewReader(""), &http.Client{}); code != 0 {
		t.Fatalf("setup exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "runtime-secret") || strings.Contains(stderr.String(), "runtime-secret") {
		t.Fatalf("setup output exposed runtime token: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
	config, err := loadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeToken != "runtime-secret" || config.ControlPlaneURL != "https://control.example.test" {
		t.Fatalf("unexpected saved config: %#v", config)
	}
}

func TestVerifyRuntimeToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/runtime/whoami" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Fatalf("unexpected verification request: %s %s", r.Method, r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"id":"installation-1","org_id":"org-1","platform":"teams"}`)),
			Header:     make(http.Header),
		}, nil
	})}

	identity, err := verifyRuntimeToken(t.Context(), client, "https://control.example.test", "runtime-secret")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "installation-1" || identity.OrgID != "org-1" || identity.Platform != "teams" {
		t.Fatalf("unexpected runtime identity: %#v", identity)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestLoadRuntimeConfigRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kei.yaml")
	if err := os.WriteFile(path, []byte("unknown: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntimeConfig(path); err == nil {
		t.Fatal("expected unknown config key to be rejected")
	}
}
