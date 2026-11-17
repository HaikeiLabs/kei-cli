package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

const (
	noStoreReadinessJSON     = `{"store":null,"sync_runtimes":0,"ready":false,"reasons":["no_store"],"next_step":"Create the credential store."}`
	noSyncRuntimeNextStep    = "Register a credential-sync runtime in this workspace: run kei-proxy credential sync on it."
	noSyncRuntimeReadiness   = `{"store":{"id":"77777777-7777-7777-7777-777777777777","scope":"workspace","secret_backend":"azure-key-vault","status":"active"},"sync_runtimes":0,"ready":false,"reasons":["no_sync_runtime"],"next_step":"` + noSyncRuntimeNextStep + `"}`
	wsReadinessPath          = "/api/v1/organizations/" + testProfileOrgID + "/workspaces/" + testWorkspaceID + "/credential-store/readiness"
	connectorWSReadinessPath = "/api/v1/organizations/org-1/workspaces/" + testWorkspaceID + "/credential-store/readiness"
)

// TestCredentialStoreStatus_PrintsReadiness: status reads the workspace's
// readiness (with the agent scope) and prints the store, the sync-runtime
// count, ready, and the next step; never key material.
func TestCredentialStoreStatus_PrintsReadiness(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
	})
	fake.readiness = `{"store":{"id":"` + testStoreID + `","scope":"workspace","secret_backend":"azure-key-vault","status":"active","public_key":"LEAKED-KEY"},"sync_runtimes":2,"ready":true,"reasons":[],"next_step":"Nothing to do."}`
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreStatus([]string{"--workspace", "Main", "--agent", testAgentID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	if len(fake.readinessRequests) != 1 {
		t.Fatalf("readiness requests = %#v", fake.readinessRequests)
	}
	got := fake.readinessRequests[0]
	if got.Path != wsReadinessPath || got.Query != "agent_id="+testAgentID {
		t.Fatalf("readiness request = %s?%s", got.Path, got.Query)
	}
	out := stdout.String()
	for _, want := range []string{testStoreID, "workspace", "azure-key-vault", "active", "Sync runtimes:  2", "Ready:          yes", "Nothing to do."} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "LEAKED-KEY") {
		t.Fatalf("status printed key material:\n%s", out)
	}

	// --json prints the contract fields only.
	stdout.Reset()
	if code := runCredentialStoreStatus([]string{"--workspace", "Main", "--json"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("status --json exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "LEAKED-KEY") || !strings.Contains(stdout.String(), `"ready":true`) || !strings.Contains(stdout.String(), `"sync_runtimes":2`) {
		t.Fatalf("status --json = %s", stdout.String())
	}
}

// With no store, status prints the exact put command instead of the
// server's generic next step.
func TestCredentialStoreStatus_NoStorePrintsPutCommand(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
	})
	fake.readiness = noStoreReadinessJSON
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreStatus([]string{"--workspace", "Main"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Store:          none", "Ready:          no (no_store)", credentialStorePutCommand} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestCredentialStoreStatus_ForbiddenFails(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {})
	fake.readinessStatus = http.StatusForbidden
	fake.readiness = `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"not a member of this workspace"}}`
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreStatus([]string{"--workspace", "Main"}, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("status exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "HTTP 403") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCredentialStoreStatus_RejectsNonUUIDAgent(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {})
	var stdout, stderr bytes.Buffer
	if code := runCredentialStoreStatus([]string{"--workspace", "Main", "--agent", "bob"}, &stdout, &stderr, http.DefaultClient, store); code != 2 {
		t.Fatalf("status exit = %d, want 2", code)
	}
	if len(fake.readinessRequests) != 0 {
		t.Fatal("a bad --agent must fail before any request")
	}
}

// TestConnectorSecret_PassesWorkspaceID: the connector secret path checks the
// workspace's readiness, then lists recipients scoped to that workspace.
func TestConnectorSecret_PassesWorkspaceID(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if len(rt.fake.readinessRequests) != 1 || rt.fake.readinessRequests[0].Path != connectorWSReadinessPath {
		t.Fatalf("readiness requests = %#v", rt.fake.readinessRequests)
	}
	recipients := rt.requests[0]
	if recipients.Path != "/api/v1/organizations/org-1/credential-store/recipients" || recipients.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("recipients request = %s?%s", recipients.Path, recipients.Query)
	}
}

// TestConnectorSecret_NoStorePrintsPutCommand: with no store the connector
// create fails before anything is created and prints the put command.
func TestConnectorSecret_NoStorePrintsPutCommand(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	rt.fake.readiness = noStoreReadinessJSON
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(stderr, "Nothing was created") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, credentialStorePutCommand) {
		t.Fatalf("stderr missing the put command:\n%s", stderr)
	}
	if got := rt.paths(); len(got) != 0 {
		t.Fatalf("requests = %v, want none after the readiness check", got)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

// The reconnect secret path (database connectors included) runs the same
// preflight before it lists recipients or sets a secret.
func TestConnectorReconnect_NoStorePrintsPutCommand(t *testing.T) {
	rt := newSecretConsole(t, databaseConnectorJSON("postgres", "active"))
	rt.fake.readiness = noStoreReadinessJSON
	code, _, stderr := runConnectors(t, rt.server, rt.store, databaseDSNs["postgres"]+"\n", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, credentialStorePutCommand) {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	want := []string{"GET /api/v1/data-connectors/" + testConnectorID}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

// TestModelProfileCreate_NoSyncRuntimeSameMessageAsConnector: both secret
// paths print one message built from the same readiness answer.
func TestModelProfileCreate_NoSyncRuntimeSameMessageAsConnector(t *testing.T) {
	want := "credential store not ready (no_sync_runtime): " + noSyncRuntimeNextStep

	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
	})
	fake.readiness = noSyncRuntimeReadiness
	var stdout, stderr bytes.Buffer
	args := []string{"--workspace", "Main", "--agent", testAgentID, "--display-name", "primary", "--endpoint", "https://api.example.com/v1", "--auth-type", "api_key", "--default-model", "gpt-x"}
	if code := runModelProfilesCreate(args, &stdout, &stderr, strings.NewReader(testAPIKey+"\n"), http.DefaultClient, store); code != 1 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("model profile stderr = %q, want %q", stderr.String(), want)
	}
	if len(fake.readinessRequests) != 1 || fake.readinessRequests[0].Query != "agent_id="+testAgentID {
		t.Fatalf("readiness requests = %#v", fake.readinessRequests)
	}

	rt := newSecretConsole(t, "")
	rt.fake.readiness = noSyncRuntimeReadiness
	code, _, connectorErr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "tito-live-secret-value-0123456789\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(connectorErr, want) {
		t.Fatalf("connector exit = %d stderr = %q, want %q", code, connectorErr, want)
	}
}

// Model profile update with a new key runs the same preflight.
func TestModelProfileUpdate_NoStorePrintsPutCommand(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testProfileJSON))
	})
	fake.readiness = noStoreReadinessJSON
	var stdout, stderr bytes.Buffer
	args := []string{testProfileID, "--workspace", "Main", "--rotate-key"}
	if code := runModelProfilesUpdate(args, &stdout, &stderr, strings.NewReader(testAPIKey+"\n"), http.DefaultClient, store); code != 1 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), credentialStorePutCommand) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// An older catalog (no readiness route) falls back to the recipients check,
// which reports the same message for the same cause.
func TestConnectorSecret_ReadinessNotFoundFallsBackToRecipients(t *testing.T) {
	rt := newSecretConsole(t, "")
	rt.fake.readinessStatus = http.StatusNotFound
	rt.fake.readiness = `{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`
	rt.noRecipients = true
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "tito-live-secret-value-0123456789\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(stderr, "credential store not ready (no_sync_runtime)") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if got := rt.paths(); len(got) != 1 || !strings.HasSuffix(got[0], "/recipients") {
		t.Fatalf("requests = %v, want only the recipients lookup", got)
	}
}

// Owner decision Q3: a connector that names an external secret (--credential-ref)
// needs a credential store, but not a credential-sync runtime.
func TestConnectorCreate_CredentialRefRequiresStore(t *testing.T) {
	rt := newSecretConsole(t, "")
	rt.fake.readiness = noStoreReadinessJSON
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme", "--credential-ref", "tito-api-token")
	if code != 1 || !strings.Contains(stderr, credentialStorePutCommand) {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if got := rt.paths(); len(got) != 0 {
		t.Fatalf("requests = %v, want none", got)
	}

	rt = newSecretConsole(t, "")
	rt.fake.readiness = noSyncRuntimeReadiness
	code, _, stderr = runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme", "--credential-ref", "tito-api-token")
	if code != 0 {
		t.Fatalf("a credential-ref connector needs no sync runtime: exit = %d stderr=%q", code, stderr)
	}
}

func TestCredentialStoreUsageListsStatus(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei credential-store get|put|update | status [--workspace WORKSPACE] [--agent AGENT_ID] [--json]") {
		t.Fatalf("usage missing credential-store status:\n%s", stdout.String())
	}
}
