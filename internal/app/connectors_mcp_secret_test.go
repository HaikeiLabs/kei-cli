package app

import (
	"strings"
	"testing"
)

const (
	apiKeyTemplateID = "custom_mcp_api_key"
	apiKeyTemplate   = `{"id":"custom_mcp_api_key","kind":"mcp","display_name":"Custom MCP server (API key)","auth_method":"secret_ref","mcp_transport":"streamable_http","credential_fields":[{"field":"api_key","header":"X-API-Key"}]}`
	twoKeyTemplate   = `{"id":"two_keys","kind":"mcp","display_name":"Two keys","auth_method":"secret_ref","credential_fields":[{"field":"client_id","header":"X-Client-Id"},{"field":"client_secret","header":"X-Client-Secret"}]}`
	secretRefMCPJSON = `{"id":"` + testConnectorID + `","name":"e2e","provider":"custom_mcp_api_key","kind":"mcp","status":"pending","credential_source":"opaque_ref","account_model":"shared","slug":"e2e_test_mcp","mcp_url":"https://mcp.example.com/mcp"}`
)

// TestConnectorsCreateSecretRefMCPSealsEachField (HAI-583): a secret_ref
// template's connector gets each credential field's secret, read without
// echo or from stdin, sealed to the workspace's runtimes before the connector
// is created, then set with :setSecret. Neither the create nor any output
// carries the secret.
func TestConnectorsCreateSecretRefMCPSealsEachField(t *testing.T) {
	const secret = "e2e-mcp-api-key-0123456789abcdef"
	rt := newSecretConsole(t, "")
	rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID,
		"--template", apiKeyTemplateID, "--label", "E2E test MCP", "--mcp-url", "https://mcp.example.com/mcp")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/v1/connector-templates/" + apiKeyTemplateID,
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	create := rt.requests[2]
	for _, key := range []string{"credential_ref", "credential_source", "account_model", "api_key"} {
		if _, present := create.Body[key]; present {
			t.Errorf("create body carries %q: the catalog assigns the reference and the secret is sealed separately", key)
		}
	}
	if set := rt.requests[3]; set.Body["field"] != "api_key" || set.Body["credential_store_installation_id"] != testStoreID {
		t.Fatalf("setSecret body = %v", set.Body)
	}
	if rt.openedFields["api_key"] != secret {
		t.Fatalf("the runtime key opened %q, want the entered secret", rt.openedFields["api_key"])
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
	if !strings.Contains(stdout, `"api_key"`) || !strings.Contains(stdout, "generation 1") {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stdout, "connect their own accounts") {
		t.Fatalf("stdout describes per-user OAuth for a shared-key connector: %q", stdout)
	}
}

// TestConnectorsCreateSecretRefMCPReadsOneLinePerField: with several
// credential fields, each takes the next stdin line and its own setSecret.
func TestConnectorsCreateSecretRefMCPReadsOneLinePerField(t *testing.T) {
	rt := newSecretConsole(t, "")
	rt.templates = map[string]string{"two_keys": twoKeyTemplate}
	code, _, stderr := runConnectors(t, rt.server, rt.store, "client-id-value-0001\nclient-secret-value-0002\n", "create", "--workspace", testWorkspaceID,
		"--template", "two_keys", "--label", "Two", "--mcp-url", "https://mcp.example.com/mcp")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if rt.openedFields["client_id"] != "client-id-value-0001" || rt.openedFields["client_secret"] != "client-secret-value-0002" {
		t.Fatalf("opened = %v", rt.openedFields)
	}
}

// TestConnectorsCreateSecretRefMCPRefusesBeforeCreating: an empty secret or
// no delivery recipient fails before the connector exists.
func TestConnectorsCreateSecretRefMCPRefusesBeforeCreating(t *testing.T) {
	for name, setup := range map[string]struct {
		stdin        string
		noRecipients bool
	}{
		"empty secret":  {stdin: "\n"},
		"no recipients": {stdin: "e2e-mcp-api-key-0123456789abcdef\n", noRecipients: true},
	} {
		rt := newSecretConsole(t, "")
		rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
		rt.noRecipients = setup.noRecipients
		code, _, _ := runConnectors(t, rt.server, rt.store, setup.stdin, "create", "--workspace", testWorkspaceID,
			"--template", apiKeyTemplateID, "--label", "E2E", "--mcp-url", "https://mcp.example.com/mcp")
		if code == 0 {
			t.Errorf("%s: exit 0", name)
		}
		for _, path := range rt.paths() {
			if path == "POST /api/v1/data-connectors" {
				t.Errorf("%s: the connector was created", name)
			}
		}
	}
}

// TestConnectorsReconnectSecretRefMCPReplacesEachField: reconnect on a
// secret_ref MCP connector sets a new generation of each field and does not
// call :reconnect, which is an OAuth flow.
func TestConnectorsReconnectSecretRefMCPReplacesEachField(t *testing.T) {
	const secret = "rotated-e2e-mcp-api-key-0123456789"
	rt := newSecretConsole(t, secretRefMCPJSON)
	rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/v1/data-connectors/" + testConnectorID,
		"GET /api/v1/connector-templates/" + apiKeyTemplateID,
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if rt.openedFields["api_key"] != secret {
		t.Fatalf("opened = %v", rt.openedFields)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}
