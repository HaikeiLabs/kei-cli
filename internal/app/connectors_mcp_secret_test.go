package app

import (
	"strings"
	"testing"
)

const (
	apiKeyTemplateID = "custom_mcp_api_key"
	apiKeyTemplate   = `{"id":"custom_mcp_api_key","kind":"mcp","display_name":"Custom MCP server (API key)","auth_method":"secret_ref","mcp_transport":"streamable_http","credential_fields":[{"field":"api_key","header":"X-API-Key"}]}`
	twoKeyTemplate   = `{"id":"two_keys","kind":"mcp","display_name":"Two keys","auth_method":"secret_ref","credential_fields":[{"field":"client_id","header":"X-Client-Id"},{"field":"client_secret","header":"X-Client-Secret"}]}`
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
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID,
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
// credential fields, each takes the next stdin line and gets its own
// :setSecret.
func TestConnectorsCreateSecretRefMCPReadsOneLinePerField(t *testing.T) {
	rt := newSecretConsole(t, "")
	rt.templates = map[string]string{"two_keys": twoKeyTemplate}
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "client-id-value-0001\nclient-secret-value-0002\n", "create", "--workspace", testWorkspaceID,
		"--template", "two_keys", "--label", "Two", "--mcp-url", "https://mcp.example.com/mcp")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if rt.openedFields["client_id"] != "client-id-value-0001" || rt.openedFields["client_secret"] != "client-secret-value-0002" {
		t.Fatalf("opened = %v", rt.openedFields)
	}
	want := []string{
		"GET /api/v1/connector-templates/two_keys",
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	assertNoPlaintext(t, "client-secret-value-0002", rt.requests, stdout, stderr)
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
		t.Run(name, func(t *testing.T) {
			rt := newSecretConsole(t, "")
			rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
			rt.noRecipients = setup.noRecipients
			code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, setup.stdin, "create", "--workspace", testWorkspaceID,
				"--template", apiKeyTemplateID, "--label", "E2E", "--mcp-url", "https://mcp.example.com/mcp")
			if code == 0 {
				t.Fatalf("exit 0; stdout=%s", stdout)
			}
			for _, path := range rt.paths() {
				if path == "POST /api/v1/data-connectors" {
					t.Errorf("%s: the connector was created", name)
				}
			}
			assertNoPlaintext(t, "e2e-mcp-api-key-0123456789abcdef", rt.requests, stdout, stderr)
		})
	}
}

// TestConnectorsCreateSecretRefMCPRejectsExpectedIssuer: an issuer pin is an
// OAuth setting; a secret_ref create refuses it before reading or creating
// anything.
func TestConnectorsCreateSecretRefMCPRejectsExpectedIssuer(t *testing.T) {
	rt := newSecretConsole(t, "")
	rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "e2e-mcp-api-key-0123456789abcdef\n", "create", "--workspace", testWorkspaceID,
		"--template", apiKeyTemplateID, "--label", "E2E", "--mcp-url", "https://mcp.example.com/mcp", "--expected-issuer", "https://login.example.com/tenant/v2.0")
	if code == 0 || !strings.Contains(stderr, "uses static secrets") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	for _, path := range rt.paths() {
		if path == "POST /api/v1/data-connectors" {
			t.Fatal("the connector was created")
		}
	}
}

// TestConnectorsCreateSecretRefMCPJSONKeepsSecretsOutOfOutput: --json keeps
// the per-field delivery chatter out of the output and still sets every
// secret.
func TestConnectorsCreateSecretRefMCPJSONKeepsSecretsOutOfOutput(t *testing.T) {
	const secret = "e2e-mcp-api-key-0123456789abcdef"
	rt := newSecretConsole(t, "")
	rt.templates = map[string]string{apiKeyTemplateID: apiKeyTemplate}
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID,
		"--template", apiKeyTemplateID, "--label", "E2E", "--mcp-url", "https://mcp.example.com/mcp", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"`+testConnectorID+`"`) {
		t.Fatalf("stdout has no JSON create response: %q", stdout)
	}
	if strings.Contains(stdout, "generation") {
		t.Fatalf("JSON output carries secret delivery chatter: %q", stdout)
	}
	if len(rt.paths()) == 0 || rt.paths()[len(rt.paths())-1] != "POST /api/v1/data-connectors/"+testConnectorID+":setSecret" {
		t.Fatalf("requests = %v, want the setSecret", rt.paths())
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}
