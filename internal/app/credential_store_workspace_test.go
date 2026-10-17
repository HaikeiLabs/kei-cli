package app

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	testOverrideWorkspaceID = "abababab-1111-2222-3333-444444444444"
	wsCredStorePath         = "/api/v1/organizations/" + testProfileOrgID + "/workspaces/" + testOverrideWorkspaceID + "/credential-store"
)

// TestCredentialStoreGetWorkspace (ADR-036 §10 HAI-489 test 5): --workspace
// reads the workspace's override resource, not the organization default.
func TestCredentialStoreGetWorkspace(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testCredentialStoreJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet([]string{"--workspace", testOverrideWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodGet, wsCredStorePath)
}

func TestCredentialStoreGetWorkspaceNotFound(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`credential store not found`))
	})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreGet([]string{"--workspace", testOverrideWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("get exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "No credential store override") {
		t.Fatalf("get error = %q", stderr.String())
	}
}

// Workspace credential stores are read-only from this CLI surface. Model
// profiles remain the only path for writing model secret values.
func TestCredentialStoreWorkspaceWritesRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string, io.Writer, io.Writer, *http.Client, credentialStore) int
		args []string
	}{
		{name: "put", run: runCredentialStorePut, args: []string{"--workspace", testOverrideWorkspaceID, "--secret-backend", "aws-secrets-manager"}},
		{name: "update", run: runCredentialStoreUpdate, args: []string{"--workspace", testOverrideWorkspaceID, "--status", "disabled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) { hit = true })
			var stdout, stderr bytes.Buffer
			if code := tc.run(tc.args, &stdout, &stderr, http.DefaultClient, store); code != 2 {
				t.Fatalf("exit = %d, want 2, stderr=%s", code, stderr.String())
			}
			if hit || !strings.Contains(stderr.String(), "flag provided but not defined: -workspace") {
				t.Fatalf("hit=%v stderr=%q", hit, stderr.String())
			}
		})
	}
}
