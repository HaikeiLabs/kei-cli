package app

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

const (
	testIssuer      = "https://login.example.test/tenant/v2.0"
	customMCPPath   = "/api/v1/connector-templates/custom_mcp"
	customMCPNoURL  = `{"id":"custom_mcp","kind":"mcp","display_name":"Custom MCP server","auth_method":"oauth","identity_method":"oidc"}`
	grafanaTemplate = `{"id":"grafana","kind":"mcp","display_name":"Grafana","default_url":"https://mcp.grafana.example/mcp","auth_method":"oauth","identity_method":"oidc"}`
)

// mcpConsole answers the template lookup with template (by path) and a
// create with the request echoed back plus an id, a slug and the canonical
// mcp_url, the way the catalog does.
func mcpConsole(t *testing.T, templates map[string]string) (*fakeConsole, func(...string) (int, string, string)) {
	t.Helper()
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/api/v1/connector-templates/"):
			body, ok := templates[r.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"connector template not found"}}`))
				return
			}
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodPost && r.Path == "/api/v1/data-connectors":
			w.WriteHeader(http.StatusCreated)
			body := map[string]any{"id": testConnectorID, "status": "pending", "kind": "mcp", "slug": "grafana_prod", "credential_source": "oauth", "account_model": "per_user"}
			for key, value := range r.Body {
				body[key] = value
			}
			if _, ok := body["mcp_url"]; !ok {
				body["mcp_url"] = "https://mcp.grafana.example/mcp"
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			t.Errorf("unexpected console request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	store.token = workspaceTestToken()
	return fake, func(args ...string) (int, string, string) {
		return runConnectors(t, server, store, "", args...)
	}
}

func bodyKeys(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// TestConnectorsCreateMCPFromTemplate (HAI-489 test 4): create --template
// --label looks the template up, then sends only the template, the label and
// the MCP URL, and prints the slug the catalog assigned. No credential field
// is sent: an mcp connector's users connect their own accounts.
func TestConnectorsCreateMCPFromTemplate(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{customMCPPath: customMCPNoURL})
	code, stdout, stderr := run("create", "--workspace", "Main", "--kind", "mcp", "--template", "custom_mcp", "--label", "Grafana (prod)", "--mcp-url", "https://mcp.grafana.example/mcp")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 || fake.requests[0].Method != http.MethodGet || fake.requests[0].Path != customMCPPath {
		t.Fatalf("requests = %#v, want the template lookup then the create", fake.requests)
	}
	create := fake.requests[1]
	if create.Method != http.MethodPost || create.Path != "/api/v1/data-connectors" || create.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("create = %s %s?%s", create.Method, create.Path, create.Query)
	}
	if got := bodyKeys(create.Body); got != "display_name,mcp_url,name,provider" {
		t.Fatalf("create body keys = %s, want display_name,mcp_url,name,provider (%s)", got, create.Raw)
	}
	if create.Body["provider"] != "custom_mcp" || create.Body["display_name"] != "Grafana (prod)" || create.Body["name"] != "Grafana (prod)" || create.Body["mcp_url"] != "https://mcp.grafana.example/mcp" {
		t.Fatalf("create body = %s", create.Raw)
	}
	for _, want := range []string{"grafana_prod", testConnectorID, "Grafana (prod)", "https://mcp.grafana.example/mcp"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("create output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout+stderr, workspaceTestToken()) {
		t.Fatal("create printed the CLI token")
	}
}

// TestConnectorsCreateMCPSlugAndName: --slug and --name are sent as given.
func TestConnectorsCreateMCPSlugAndName(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{"/api/v1/connector-templates/grafana": grafanaTemplate})
	code, _, stderr := run("create", "--workspace", "Main", "--template", "grafana", "--label", "Grafana (prod)", "--slug", "grafana_prod", "--name", "grafana-prod")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	create := fake.requests[1]
	if create.Body["slug"] != "grafana_prod" || create.Body["name"] != "grafana-prod" {
		t.Fatalf("create body = %s, want slug grafana_prod and name grafana-prod", create.Raw)
	}
}

// TestConnectorsCreateMCPUsesTheTemplateURL: a template with a default_url
// needs no --mcp-url, and none is sent, so the catalog uses the template's.
func TestConnectorsCreateMCPUsesTheTemplateURL(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{"/api/v1/connector-templates/grafana": grafanaTemplate})
	code, stdout, stderr := run("create", "--workspace", "Main", "--template", "grafana", "--label", "Grafana (prod)")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if _, sent := fake.requests[1].Body["mcp_url"]; sent {
		t.Fatalf("create sent mcp_url for a template that has one: %s", fake.requests[1].Raw)
	}
	if !strings.Contains(stdout, "https://mcp.grafana.example/mcp") {
		t.Errorf("create output missing the template's MCP URL:\n%s", stdout)
	}
}

// TestConnectorsCreateMCPRequiresAURLTheTemplateLacks: a template without a
// default_url needs --mcp-url; nothing is created without it.
func TestConnectorsCreateMCPRequiresAURLTheTemplateLacks(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{customMCPPath: customMCPNoURL})
	code, _, stderr := run("create", "--workspace", "Main", "--template", "custom_mcp", "--label", "Custom")
	if code != 1 || !strings.Contains(stderr, "--mcp-url") {
		t.Fatalf("exit = %d stderr=%q, want 1 naming --mcp-url", code, stderr)
	}
	if fake.count() != 1 {
		t.Fatalf("requests = %d, want only the template lookup", fake.count())
	}
}

// TestConnectorsCreateTemplateKindIsChecked: --kind must match the
// template's kind, and a template of another kind is not created through the
// MCP path. The CLI holds no template list: the kind comes from the catalog.
func TestConnectorsCreateTemplateKindIsChecked(t *testing.T) {
	linear := `{"id":"linear","kind":"native","display_name":"Linear","auth_method":"oauth","identity_method":"oidc"}`
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"kind mismatch", []string{"--template", "grafana", "--kind", "http_api", "--label", "G"}, `template "grafana" is kind mcp, not http_api`},
		{"native template", []string{"--template", "linear", "--label", "L"}, "--provider linear"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, run := mcpConsole(t, map[string]string{"/api/v1/connector-templates/grafana": grafanaTemplate, "/api/v1/connector-templates/linear": linear})
			code, _, stderr := run(append([]string{"create", "--workspace", "Main"}, tc.args...)...)
			if code != 1 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit = %d stderr=%q, want 1 containing %q", code, stderr, tc.want)
			}
			if fake.count() != 1 {
				t.Fatalf("requests = %d, want only the template lookup", fake.count())
			}
		})
	}
}

// TestConnectorsCreateUnknownTemplate: a template the catalog does not have
// is reported by id, and nothing is created.
func TestConnectorsCreateUnknownTemplate(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{})
	code, _, stderr := run("create", "--workspace", "Main", "--template", "nope", "--label", "X")
	if code != 1 || !strings.Contains(stderr, `unknown connector template "nope"`) || fake.count() != 1 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

// TestConnectorsCreateTemplateFlagRules: --template needs --label, excludes
// --provider and the native-only flags, and --kind needs --template. These
// are checked before any request.
func TestConnectorsCreateTemplateFlagRules(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--template", "grafana"}, "--label"},
		{[]string{"--template", "grafana", "--label", "G", "--provider", "linear"}, "--provider"},
		{[]string{"--template", "grafana", "--label", "G", "--set", "a=b"}, "--set"},
		{[]string{"--template", "grafana", "--label", "G", "--credential-ref", "ref"}, "--credential-ref"},
		{[]string{"--template", "grafana", "--label", "G", "--capabilities", "a"}, "--capabilities"},
		{[]string{"--template", "grafana", "--label", "G", "--account-model", "shared"}, "--account-model"},
		{[]string{"--template", "grafana", "--label", "G", "--base-url", "https://x"}, "--base-url"},
		{[]string{"--provider", "linear", "--name", "x", "--kind", "mcp"}, "--kind"},
		{[]string{"--provider", "linear", "--name", "x", "--mcp-url", "https://x"}, "--mcp-url"},
		{[]string{"--provider", "linear", "--name", "x", "--expected-issuer", testIssuer}, "--expected-issuer"},
		{[]string{"--provider", "linear", "--name", "x", "--label", "L"}, "--label"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fake, run := mcpConsole(t, map[string]string{})
			code, _, stderr := run(append([]string{"create", "--workspace", "Main"}, tc.args...)...)
			if code != 2 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit = %d stderr=%q, want 2 naming %s", code, stderr, tc.want)
			}
			if fake.count() != 0 {
				t.Fatalf("a rejected flag combination called the console %d times", fake.count())
			}
		})
	}
}

// TestConnectorsCreateMCPExpectedIssuer (HAI-563): --expected-issuer is sent
// as written and shown when the catalog stores it.
func TestConnectorsCreateMCPExpectedIssuer(t *testing.T) {
	fake, run := mcpConsole(t, map[string]string{"/api/v1/connector-templates/grafana": grafanaTemplate})
	code, stdout, stderr := run("create", "--workspace", "Main", "--template", "grafana", "--label", "Grafana (prod)", "--expected-issuer", testIssuer)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.requests[1].Body["expected_issuer"] != testIssuer {
		t.Fatalf("create body = %s, want expected_issuer %s", fake.requests[1].Raw, testIssuer)
	}
	if !strings.Contains(stdout, testIssuer) {
		t.Errorf("create output missing the expected issuer:\n%s", stdout)
	}
}

// TestConnectorsCreateMCPExpectedIssuerRejectedByCatalog: the catalog's
// validation error is surfaced with its status.
func TestConnectorsCreateMCPExpectedIssuerRejectedByCatalog(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(grafanaTemplate))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"expected_issuer must be an https URL without query or fragment"}}`))
	})
	store.token = workspaceTestToken()
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--template", "grafana", "--label", "G", "--expected-issuer", "http://insecure.example")
	if code != 1 || !strings.Contains(stderr, "expected_issuer must be an https URL") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// TestConnectorsCreateMCPExpectedIssuerIgnoredByAnOlderCatalog: a catalog
// that predates expected_issuer accepts the create but drops the field. The
// CLI fails closed: it deletes the connector it just created, which would
// otherwise trust any authorization server, and says why.
func TestConnectorsCreateMCPExpectedIssuerIgnoredByAnOlderCatalog(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(grafanaTemplate))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","status":"pending","kind":"mcp","slug":"g","display_name":"G","mcp_url":"https://mcp.grafana.example/mcp"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","status":"revoked"}`))
		}
	})
	store.token = workspaceTestToken()
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--template", "grafana", "--label", "G", "--expected-issuer", testIssuer)
	if code != 1 || !strings.Contains(stderr, "does not support expected_issuer") || !strings.Contains(stderr, "deleted") {
		t.Fatalf("exit = %d stderr=%q, want 1 explaining the catalog dropped expected_issuer and the connector was deleted", code, stderr)
	}
	if fake.count() != 3 || fake.requests[2].Method != http.MethodDelete || fake.requests[2].Path != "/api/v1/data-connectors/"+testConnectorID {
		t.Fatalf("requests = %#v, want the created connector deleted", fake.requests)
	}
}

// TestConnectorsCreateMCPExpectedIssuerRollbackFailure: when the rollback
// itself fails, the CLI names the connector so the admin can delete it.
func TestConnectorsCreateMCPExpectedIssuerRollbackFailure(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(grafanaTemplate))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","status":"pending","kind":"mcp","slug":"g"}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":502,"message":"upstream unavailable"}}`))
		}
	})
	store.token = workspaceTestToken()
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--template", "grafana", "--label", "G", "--expected-issuer", testIssuer)
	if code != 1 || !strings.Contains(stderr, "kei connectors delete "+testConnectorID) {
		t.Fatalf("exit = %d stderr=%q, want the delete command for the connector", code, stderr)
	}
}

// TestConnectorsListShowsKindSlugAndMCPURL: list shows each connector's
// label, slug, kind and MCP URL; native connectors without them show "-".
func TestConnectorsListShowsKindSlugAndMCPURL(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
		{"id":"` + testConnectorID + `","name":"grafana-prod","display_name":"Grafana (prod)","slug":"grafana_prod","kind":"mcp","provider":"custom_mcp","status":"pending","credential_source":"oauth","account_model":"per_user","mcp_url":"https://mcp.grafana.example/mcp"},
		{"id":"55555555-5555-5555-5555-555555555555","name":"events","provider":"tito","status":"active","credential_source":"opaque_ref"}
	]`))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("list output = %q, want a header and two rows", stdout)
	}
	for _, want := range []string{"KIND", "SLUG", "MCP URL"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q missing %s", lines[0], want)
		}
	}
	for _, want := range []string{"Grafana (prod)", "grafana_prod", "mcp", "custom_mcp", "https://mcp.grafana.example/mcp"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("mcp row %q missing %q", lines[1], want)
		}
	}
	if !strings.Contains(lines[2], "events") || !strings.Contains(lines[2], "-") {
		t.Errorf("native row %q, want its name and - placeholders", lines[2])
	}
}

// TestConnectorsGetShowsMCPFields: get shows the label, slug, kind, MCP URL
// and expected issuer of an mcp connector.
func TestConnectorsGetShowsMCPFields(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","name":"grafana-prod","display_name":"Grafana (prod)","slug":"grafana_prod","kind":"mcp","provider":"custom_mcp","status":"pending","credential_source":"oauth","account_model":"per_user","mcp_url":"https://mcp.grafana.example/mcp","expected_issuer":"` + testIssuer + `"}`))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "get", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"Label:", "Grafana (prod)", "Slug:", "grafana_prod", "Kind:", "mcp", "MCP URL:", "https://mcp.grafana.example/mcp", "Issuer:", testIssuer} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}
