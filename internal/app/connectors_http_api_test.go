package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
)

func testHTTPAPIRegistration() *contract.HTTPAPI {
	return &contract.HTTPAPI{
		RegisteredBaseURL: "https://crm.example.com", RegisteredHostname: "crm.example.com",
		AllowedMethods: []contract.HTTPMethod{contract.HTTPMethodGet}, AllowedPathPatterns: []string{"/*"},
		QueryAllowlist: []string{}, RequestMaxBytes: httpAPIRequestMaxBytes, ResponseMaxBytes: httpAPIResponseMaxBytes, TimeoutMS: httpAPITimeoutMS,
		CredentialInjection: contract.CredentialInjection{Location: contract.CredentialInjectionHeader, Name: "X-KEI-API-Key"},
		Redaction:           contract.RedactionRules{Headers: []string{"X-KEI-API-Key"}, QueryParameters: []string{}, ResponseFields: []string{}},
	}
}

func capabilityNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	caps, _ := body["capabilities"].([]any)
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	return names
}

func stringsOf(value any) []string {
	items, _ := value.([]any)
	out := make([]string, len(items))
	for i, item := range items {
		out[i], _ = item.(string)
	}
	return out
}

func TestConnectorsCreateHTTPAPISendsRegistrationAndMethodCapabilities(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main",
		"--provider", "http_api", "--name", "crm", "--base-url", "https://crm.example.com/api",
		"--methods", "get,HEAD,GET", "--paths", "/investors,/investors/*", "--query", "limit,cursor",
		"--auth-header", "X-KEI-API-Key", "--credential-ref", "kei/prod/crm/api-key")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != "POST" || req.Path != "/api/v1/data-connectors" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Body["provider"] != "http_api" || req.Body["name"] != "crm" || req.Body["credential_source"] != "opaque_ref" || req.Body["credential_ref"] != "kei/prod/crm/api-key" {
		t.Fatalf("body = %v", req.Body)
	}
	if _, present := req.Body["config"]; present {
		t.Fatalf("http_api has no config; the registration is the http_api block: %v", req.Body)
	}
	h, _ := req.Body["http_api"].(map[string]any)
	if h["registered_base_url"] != "https://crm.example.com/api" || h["registered_hostname"] != "crm.example.com" {
		t.Fatalf("http_api = %v", h)
	}
	if got := stringsOf(h["allowed_methods"]); strings.Join(got, ",") != "GET,HEAD" {
		t.Fatalf("allowed_methods = %v, want GET,HEAD normalized and deduplicated", got)
	}
	if got := stringsOf(h["allowed_path_patterns"]); strings.Join(got, ",") != "/investors,/investors/*" {
		t.Fatalf("allowed_path_patterns = %v", got)
	}
	if got := stringsOf(h["query_allowlist"]); strings.Join(got, ",") != "limit,cursor" {
		t.Fatalf("query_allowlist = %v", got)
	}
	injection, _ := h["credential_injection"].(map[string]any)
	if injection["location"] != "header" || injection["name"] != "X-KEI-API-Key" || injection["scheme"] != nil {
		t.Fatalf("credential_injection = %v, want the raw key in X-KEI-API-Key", injection)
	}
	redaction, _ := h["redaction"].(map[string]any)
	if !slices.Contains(stringsOf(redaction["headers"]), "X-KEI-API-Key") {
		t.Fatalf("redaction = %v, the credential header must be redacted", redaction)
	}
	if h["allow_redirects"] != false || h["allow_cross_host_redirects"] != false {
		t.Fatalf("redirects = %v", h)
	}
	if got := capabilityNames(t, req.Body); strings.Join(got, ",") != "http.get,http.head" {
		t.Fatalf("capabilities = %v", got)
	}
	policy, _ := req.Body["policy"].(map[string]any)
	if got := stringsOf(policy["allowed_actions"]); strings.Join(got, ",") != "read" {
		t.Fatalf("policy actions = %v", got)
	}
	if !strings.Contains(stdout, "Created connector crm (http_api") || !strings.Contains(stdout, testConnectorID) {
		t.Fatalf("stdout = %q", stdout)
	}
}

// POST and PATCH are writes: each allowed method declares the contract's
// capability for it, with the contract's action. They need a connector
// contract that defines http.post and http.patch (HAI-384 A2).
func TestConnectorsCreateHTTPAPIWriteMethods(t *testing.T) {
	defined := contract.CapabilitiesFor(contract.ProviderHTTPAPI)
	actions := map[string]contract.Action{}
	for _, c := range defined {
		actions[c.Name] = c.Action
	}
	if _, ok := actions["http.post"]; !ok {
		fake, server, store := newFakeConsole(t, failOnRequest(t))
		code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main",
			"--provider", "http_api", "--name", "crm", "--base-url", "https://crm.example.com",
			"--methods", "GET,POST,PATCH", "--credential-ref", "kei/prod/crm/api-key")
		if code == 0 || fake.count() != 0 || !strings.Contains(stderr, "POST") {
			t.Fatalf("exit = %d stderr=%q requests=%d: a contract without http.post must reject POST locally", code, stderr, fake.count())
		}
		t.Skip("kei-connector-contracts in go.mod predates http.post/http.patch; write-method body covered once bumped")
	}
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main",
		"--provider", "http_api", "--name", "crm", "--base-url", "https://crm.example.com",
		"--methods", "GET,HEAD,POST,PATCH", "--auth-header", "X-KEI-API-Key", "--credential-ref", "kei/prod/crm/api-key")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	body := fake.only(t).Body
	h, _ := body["http_api"].(map[string]any)
	if got := stringsOf(h["allowed_methods"]); strings.Join(got, ",") != "GET,HEAD,POST,PATCH" {
		t.Fatalf("allowed_methods = %v", got)
	}
	if got := capabilityNames(t, body); strings.Join(got, ",") != "http.get,http.head,http.post,http.patch" {
		t.Fatalf("capabilities = %v", got)
	}
	caps, _ := body["capabilities"].([]any)
	for _, c := range caps {
		capability := c.(map[string]any)
		if capability["action"] != string(actions[capability["name"].(string)]) {
			t.Errorf("capability %v, want the contract's action %q", capability, actions[capability["name"].(string)])
		}
	}
	policy, _ := body["policy"].(map[string]any)
	want := []string{"read", string(actions["http.post"]), string(actions["http.patch"])}
	if got := stringsOf(policy["allowed_actions"]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("policy actions = %v, want %v", got, want)
	}
}

func TestConnectorsCreateHTTPAPIAuthSchemeDefaults(t *testing.T) {
	cases := map[string]struct {
		args       []string
		name, want string
	}{
		"Authorization defaults to Bearer": {nil, "Authorization", "Bearer"},
		"custom header has no scheme":      {[]string{"--auth-header", "X-API-Key"}, "X-API-Key", ""},
		"explicit scheme":                  {[]string{"--auth-header", "Authorization", "--auth-scheme", "Token"}, "Authorization", "Token"},
		"explicit empty scheme":            {[]string{"--auth-scheme", ""}, "Authorization", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, createdResponse)
			store.token = workspaceTestToken()
			args := append([]string{"create", "--workspace", "Main", "--provider", "http_api", "--name", "crm",
				"--base-url", "https://crm.example.com", "--credential-ref", "kei/prod/crm/api-key"}, tc.args...)
			if code, _, stderr := runConnectors(t, server, store, "", args...); code != 0 {
				t.Fatalf("exit = %d stderr=%s", code, stderr)
			}
			h, _ := fake.only(t).Body["http_api"].(map[string]any)
			injection, _ := h["credential_injection"].(map[string]any)
			scheme, _ := injection["scheme"].(string)
			if injection["name"] != tc.name || scheme != tc.want {
				t.Fatalf("credential_injection = %v, want %s %q", injection, tc.name, tc.want)
			}
		})
	}
}

func TestConnectorsCreateHTTPAPISealsTheSharedKey(t *testing.T) {
	const secret = "crm-shared-api-key-0123456789abcdef"
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main",
		"--provider", "http_api", "--name", "crm", "--base-url", "https://crm.example.com", "--auth-header", "X-KEI-API-Key")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	create := rt.requests[1]
	if create.Body["provider"] != "http_api" || create.Body["http_api"] == nil {
		t.Fatalf("create body = %v", create.Body)
	}
	if _, present := create.Body["credential_ref"]; present {
		t.Fatal("a delivered key's kei:// reference is assigned by the catalog, not the CLI")
	}
	if set := rt.requests[2]; set.Body["field"] != "api_key" {
		t.Fatalf("setSecret body = %v", set.Body)
	}
	if rt.opened != secret {
		t.Fatalf("the runtime key opened %q, want the entered key", rt.opened)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestConnectorsGetShowsHTTPAPIRegistrationAndReferenceOnly(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","name":"crm","provider":"http_api","status":"active","credential_source":"opaque_ref",`+
		`"credential_ref":"kei://connectors/`+testConnectorID+`/api_key","capabilities":[{"name":"http.get"},{"name":"http.post"}],`+
		`"http_api":{"registered_base_url":"https://crm.example.com","registered_hostname":"crm.example.com","allowed_methods":["GET","POST"],`+
		`"allowed_path_patterns":["/investors/*"],"query_allowlist":["limit"],"credential_injection":{"location":"header","name":"X-KEI-API-Key"}}}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "get", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"Name:        crm", "Provider:    http_api", "Base URL:    https://crm.example.com", "Methods:     GET, POST",
		"Paths:       /investors/*", "Query:       limit", "Auth header: X-KEI-API-Key", "Secret ref:  kei://connectors/" + testConnectorID + "/api_key", "http.get, http.post"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}
