package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// HAI-1070 (owner option d): a credential is released only to a permit whose
// selector names the connector. A tool grant can name one with --connector;
// without one the tool call is still allowed but no credential is released.

const (
	testMySQLConnectorID = "d9c92bd4-4694-420b-baed-2617787c0b0a"
	testMSSQLConnectorID = "1460eed0-230a-428c-a21f-bd29656fcb2f"
)

func connectorsJSON(connectors ...[2]string) string {
	items := make([]map[string]string, 0, len(connectors))
	for _, c := range connectors {
		items = append(items, map[string]string{"id": c[0], "name": c[1], "provider": c[1]})
	}
	out, _ := json.Marshal(items)
	return string(out)
}

// toolGrantConsole answers the workspace connector list with connectorList and
// echoes a created policy for the POST.
func toolGrantConsole(connectorList string) func(http.ResponseWriter, consoleRequest) {
	return func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors" {
			_, _ = w.Write([]byte(connectorList))
			return
		}
		if r.Method == http.MethodPost && r.Path == typedPoliciesPath() {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"tool:mysql_query / group:db-readers","principal":{"kind":"group","value":"db-readers"},"target":{"kind":"tool","value":"mysql_query"},"action":"permit","enabled":true}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}
}

func policyPosts(t *testing.T, fake *fakeConsole) []consoleRequest {
	t.Helper()
	var posts []consoleRequest
	for _, r := range fake.all() {
		if r.Method == http.MethodPost && r.Path == typedPoliciesPath() {
			posts = append(posts, r)
		}
	}
	return posts
}

func onlyPolicyPost(t *testing.T, fake *fakeConsole) consoleRequest {
	t.Helper()
	posts := policyPosts(t, fake)
	if len(posts) != 1 {
		t.Fatalf("policy POSTs = %d, want 1: %#v", len(posts), fake.all())
	}
	return posts[0]
}

func assertToolConnectorSelector(t *testing.T, body map[string]any, tool, connectorID string) {
	t.Helper()
	selector, ok := body["selector_v2"].(map[string]any)
	if !ok {
		t.Fatalf("selector_v2 missing: %v", body)
	}
	if len(selector) != 2 || selector["tool"] != tool || selector["connector_id"] != connectorID {
		t.Fatalf("selector_v2 = %v, want exactly {tool:%s connector_id:%s}", selector, tool, connectorID)
	}
}

func TestPolicyCreateToolWithConnectorByName(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"}, [2]string{testMSSQLConnectorID, "mssql-seed"})))
	code, stdout, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query", "--connector", "mysql-seed")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := onlyPolicyPost(t, fake)
	assertToolConnectorSelector(t, req.Body, "mysql_query", testMySQLConnectorID)
	target, _ := req.Body["target"].(map[string]any)
	if target["kind"] != "tool" || target["value"] != "mysql_query" {
		t.Errorf("target = %v, want {kind:tool value:mysql_query}", req.Body["target"])
	}
	if strings.Contains(stderr, connectorCredentialWarning) {
		t.Errorf("unexpected credential warning with --connector: %q", stderr)
	}
	if !strings.Contains(stdout, "mysql-seed") {
		t.Errorf("output does not name the connector: %q", stdout)
	}
}

func TestPolicyCreateToolWithConnectorByID(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"}, [2]string{testMSSQLConnectorID, "mssql-seed"})))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query", "--connector", testMSSQLConnectorID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	assertToolConnectorSelector(t, onlyPolicyPost(t, fake).Body, "mysql_query", testMSSQLConnectorID)
}

func TestPolicyCreateToolUnknownConnectorFails(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"})))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query", "--connector", "postgres-prod")
	if code == 0 || !strings.Contains(stderr, `connector "postgres-prod" not found`) {
		t.Fatalf("exit = %d stderr=%q, want a not-found error", code, stderr)
	}
	if len(policyPosts(t, fake)) != 0 {
		t.Fatal("a grant was created for an unknown connector")
	}
}

func TestPolicyCreateToolDefaultsToOnlyConnector(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"})))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	assertToolConnectorSelector(t, onlyPolicyPost(t, fake).Body, "mysql_query", testMySQLConnectorID)
	for _, want := range []string{"mysql-seed", testMySQLConnectorID, "--no-connector"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say which connector was used (%q): %q", want, stderr)
		}
	}
}

func TestPolicyCreateToolSeveralConnectorsWarns(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"}, [2]string{testMSSQLConnectorID, "mssql-seed"})))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s (a tool-only grant is allowed)", code, stderr)
	}
	req := onlyPolicyPost(t, fake)
	if _, ok := req.Body["selector_v2"]; ok {
		t.Fatalf("tool-only grant sent selector_v2: %v", req.Body)
	}
	for _, want := range []string{connectorCredentialWarning, "--connector", "mysql-seed", "mssql-seed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %q", want, stderr)
		}
	}
}

func TestPolicyCreateToolNoConnectorsWarns(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(`[]`))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if _, ok := onlyPolicyPost(t, fake).Body["selector_v2"]; ok {
		t.Fatal("tool-only grant sent selector_v2")
	}
	if !strings.Contains(stderr, connectorCredentialWarning) {
		t.Errorf("stderr missing warning: %q", stderr)
	}
}

func TestPolicyCreateToolNoConnectorFlag(t *testing.T) {
	fake, server, store := newFakeConsole(t, toolGrantConsole(connectorsJSON(
		[2]string{testMySQLConnectorID, "mysql-seed"})))
	code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "db-readers", "--allow", "tool:mysql_query", "--no-connector")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if _, ok := onlyPolicyPost(t, fake).Body["selector_v2"]; ok {
		t.Fatal("--no-connector sent selector_v2")
	}
	if !strings.Contains(stderr, connectorCredentialWarning) {
		t.Errorf("stderr missing warning: %q", stderr)
	}
}

func TestPolicyCreateConnectorFlagMisuse(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string][]string{
		"connector on non-tool target": {"--group", "eng", "--allow", "shell:git", "--connector", "mysql-seed"},
		"connector on deny":            {"--group", "eng", "--deny", "tool:mysql_query", "--connector", "mysql-seed"},
		"no-connector on non-tool":     {"--group", "eng", "--allow", "shell:git", "--no-connector"},
		"both connector flags":         {"--group", "eng", "--allow", "tool:mysql_query", "--connector", "mysql-seed", "--no-connector"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"create", "--workspace", "Main"}, args...)
			code, _, stderr := runPolicy(t, server, store, "", full...)
			if code != 2 || !strings.Contains(stderr, "policy create: --") || strings.Contains(stderr, "not defined") {
				t.Fatalf("exit = %d stderr=%q, want 2 with a connector error", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("an invalid create reached the console")
	}
}

// Non-tool grants and tool denies are byte-identical to before HAI-1070: one
// POST, the same body, the same output, and no connector lookup.
func TestPolicyCreateNonToolGrantsUnchanged(t *testing.T) {
	cases := []struct {
		name, flag, target, effect, wantBody, wantOut string
	}{
		{
			name: "shell allow", flag: "--allow", target: "shell:git", effect: "permit",
			wantBody: `{"name":"shell:git / group:Engineering","principal":{"kind":"group","value":"Engineering"},"target":{"kind":"shell","value":"git"},"action":"permit","enabled":true}`,
			wantOut:  "Created policy created-name (group:Engineering → shell:git, permit).\nPolicy ID: " + testPolicyID + "\n",
		},
		{
			name: "tool deny", flag: "--deny", target: "tool:mysql_query", effect: "deny",
			wantBody: `{"name":"tool:mysql_query / group:Engineering","principal":{"kind":"group","value":"Engineering"},"target":{"kind":"tool","value":"mysql_query"},"action":"deny","enabled":true}`,
			wantOut:  "Created policy created-name (group:Engineering → tool:mysql_query, deny).\nPolicy ID: " + testPolicyID + "\n",
		},
		{
			name: "connector allow", flag: "--allow", target: "connector:" + testMySQLConnectorID, effect: "permit",
			wantBody: `{"name":"connector:` + testMySQLConnectorID + ` / group:Engineering","principal":{"kind":"group","value":"Engineering"},"target":{"kind":"connector","value":"` + testMySQLConnectorID + `"},"action":"permit","enabled":true}`,
			wantOut:  "Created policy created-name (group:Engineering → connector:" + testMySQLConnectorID + ", permit).\nPolicy ID: " + testPolicyID + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"created-name"}`))
			})
			code, stdout, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
				"--group", "Engineering", tc.flag, tc.target)
			if code != 0 {
				t.Fatalf("exit = %d stderr=%s", code, stderr)
			}
			req := fake.only(t)
			if strings.TrimSpace(req.Raw) != tc.wantBody {
				t.Errorf("body =\n%s\nwant\n%s", req.Raw, tc.wantBody)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestPolicyCreateHelpDocumentsConnector(t *testing.T) {
	_, server, store := newFakeConsole(t, failOnRequest(t))
	_, stdout, stderr := runPolicy(t, server, store, "", "create", "--help")
	help := stdout + stderr
	for _, want := range []string{"--connector", "--no-connector", "kei policy create --workspace Main --group db-readers --allow tool:mysql_query --connector mysql-seed"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}
