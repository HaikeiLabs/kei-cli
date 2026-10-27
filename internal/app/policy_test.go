package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func runPolicy(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runPolicyCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
}

// --- List (delegates to runPoliciesList) ---

func TestPolicyList(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[
			{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
			{"id":"55555555-5555-5555-5555-555555555555","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"deny-rm","src_pattern":"harness:claude","dst_pattern":"shell:rm","effect":"deny","priority":200,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/policies" || !strings.Contains(req.Query, "workspace_id="+testWorkspaceID) {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Auth != "Bearer "+workspaceTestToken() {
		t.Fatalf("Authorization = %q", req.Auth)
	}
	for _, want := range []string{testPolicyID, "allow-git", "harness:claude", "shell:git", "permit", "deny-rm", "deny"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestPolicyListJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "list", "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("JSON output missing policy id:\n%s", stdout)
	}
	_ = fake
}

// --- Get (delegates to runPoliciesGet) ---

func TestPolicyGet(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","description":"Allow git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "get", testPolicyID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/policies/"+testPolicyID || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	for _, want := range []string{testPolicyID, "allow-git", "harness:claude", "shell:git", "permit"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Priority") {
		t.Errorf("get output still prints a priority (ADR-038 D-1):\n%s", stdout)
	}
}

// --- Create ---

func TestPolicyCreate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"shell:git / group:Engineering","src_pattern":"group:Engineering","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "Engineering", "--allow", "shell:git")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/policies" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Body["src_pattern"] != "group:Engineering" {
		t.Errorf("src_pattern = %q, want group:Engineering", req.Body["src_pattern"])
	}
	if req.Body["dst_pattern"] != "shell:git" {
		t.Errorf("dst_pattern = %q, want shell:git", req.Body["dst_pattern"])
	}
	if req.Body["effect"] != "permit" {
		t.Errorf("effect = %q, want permit", req.Body["effect"])
	}
	if _, present := req.Body["action"]; present {
		t.Errorf("body contains action, want effect alone: %#v", req.Body)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("create output missing policy id: %q", stdout)
	}
}

func TestPolicyCreateDeny(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"shell:rm / group:Engineering","src_pattern":"group:Engineering","dst_pattern":"shell:rm","effect":"deny","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
		"--group", "Engineering", "--deny", "shell:rm")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Body["effect"] != "deny" {
		t.Errorf("effect = %q, want deny", req.Body["effect"])
	}
	if req.Body["dst_pattern"] != "shell:rm" {
		t.Errorf("dst_pattern = %q, want shell:rm", req.Body["dst_pattern"])
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("create output missing policy id: %q", stdout)
	}
}

func TestPolicyCreateRequiresGroupAndEffect(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string][]string{
		"no group":        {"--allow", "shell:git"},
		"no effect":       {"--group", "Engineering"},
		"both allow+deny": {"--group", "Engineering", "--allow", "shell:git", "--deny", "shell:rm"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"create", "--workspace", "Main"}, args...)
			code, _, stderr := runPolicy(t, server, store, "", full...)
			if code != 2 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want 2 with an error", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("an invalid create reached the console")
	}
}

func TestPolicyCreateRejectsUserEmailAgent(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string]string{
		"user: source":  "user:alice",
		"email: source": "email:alice@example.com",
		"agent: source": "agent:abc-123",
	}
	for name, groupVal := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := runPolicy(t, server, store, "", "create", "--workspace", "Main",
				"--group", groupVal, "--allow", "shell:git")
			if code != 2 || !strings.Contains(stderr, "kei access grant") {
				t.Fatalf("exit = %d stderr=%q, want 2 with message about kei access grant", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("a rejected create reached the console")
	}
}

// --- Delete (delegates to runPoliciesDelete) ---

func TestPolicyDeleteRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicy(t, server, store, "", "delete", testPolicyID, "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPolicyDelete(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNoContent)
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "delete", testPolicyID, "--workspace", "Main", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodDelete || req.Path != "/api/v1/policies/"+testPolicyID {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(req.Query, "workspace_id="+testWorkspaceID) {
		t.Fatalf("query = %q, want workspace_id", req.Query)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("delete output = %q", stdout)
	}
}

// --- Test ---

func TestPolicyTest(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"decision":"permit","decided_by":"` + testPolicyID + `"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "test", "--workspace", "Main",
		"--as", "user:alice", "shell:git")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	testReq := fake.only(t)
	if testReq.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", testReq.Method)
	}
	if testReq.Path != "/api/v1/organizations/org-1/workspaces/"+testWorkspaceID+"/policies:test" {
		t.Fatalf("path = %s, want the workspace policies:test endpoint", testReq.Path)
	}
	if testReq.Body["as"] != "user:alice" {
		t.Errorf("body as = %q, want user:alice", testReq.Body["as"])
	}
	if testReq.Body["target"] != "shell:git" {
		t.Errorf("body target = %q, want shell:git", testReq.Body["target"])
	}
	if !strings.Contains(stdout, "permit") {
		t.Errorf("stdout missing decision 'permit':\n%s", stdout)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Errorf("stdout missing decided_by:\n%s", stdout)
	}
}

func TestPolicyTestDeny(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"decision":"deny","decided_by":""}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "test", "--workspace", "Main",
		"--as", "user:alice", "shell:rm")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "deny") {
		t.Errorf("stdout missing decision 'deny':\n%s", stdout)
	}
	if !strings.Contains(stdout, "(none)") {
		t.Errorf("stdout missing the no-matching-rule marker:\n%s", stdout)
	}
	_ = fake
}

func TestPolicyTestWithReasonAndHint(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"decision":"deny","decided_by":"` + testPolicyID + `","reason_code":"restricted_tool","hint":"This tool is restricted to the Engineering group."}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "test", "--workspace", "Main",
		"--as", "user:bob", "shell:git")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "deny") {
		t.Errorf("stdout missing decision 'deny':\n%s", stdout)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Errorf("stdout missing decided_by:\n%s", stdout)
	}
	if !strings.Contains(stdout, "restricted_tool") {
		t.Errorf("stdout missing reason_code:\n%s", stdout)
	}
	if !strings.Contains(stdout, "restricted to the Engineering") {
		t.Errorf("stdout missing hint:\n%s", stdout)
	}
	_ = fake
}

func TestPolicyTestJSONWithReasonAndHint(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"decision":"deny","decided_by":"` + testPolicyID + `","reason_code":"restricted_tool","hint":"This tool is restricted to the Engineering group."}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "test", "--workspace", "Main",
		"--as", "user:bob", "--json", "shell:git")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var result struct {
		Decision   string  `json:"decision"`
		DecidedBy  string  `json:"decided_by"`
		ReasonCode *string `json:"reason_code"`
		Hint       *string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != "deny" || result.DecidedBy != testPolicyID {
		t.Errorf("decision fields: %+v", result)
	}
	if result.ReasonCode == nil || *result.ReasonCode != "restricted_tool" {
		t.Errorf("reason_code: %v", result.ReasonCode)
	}
	if result.Hint == nil || *result.Hint != "This tool is restricted to the Engineering group." {
		t.Errorf("hint: %v", result.Hint)
	}
	_ = fake
}

func TestPolicyTestNoReasonOrHint(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"decision":"permit","decided_by":"` + testPolicyID + `"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "test", "--workspace", "Main",
		"--as", "user:alice", "shell:git")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "Reason code:") {
		t.Errorf("stdout should not contain Reason code when absent:\n%s", stdout)
	}
	if strings.Contains(stdout, "Hint:") {
		t.Errorf("stdout should not contain Hint when absent:\n%s", stdout)
	}
	_ = fake
}

// --- Migration report ---

func TestPolicyMigrationReport(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{
			"standing_principals":[
				{"id":"` + testPolicyID + `","name":"allow-alice","src_pattern":"user:alice","dst_pattern":"shell:git","action":"permit","proposed_group":"migrated-user-alice","members":["user:alice"],"conversion":"autogroup"}
			],
			"wildcard_roles":[
				{"id":"55555555-5555-5555-5555-555555555555","name":"allow-any","src_pattern":"*","dst_pattern":"shell:read","action":"permit","proposed_source":"autogroup:member","needs_owner_decision":false}
			],
			"harness_sources":[
				{"id":"66666666-6666-6666-6666-666666666666","name":"allow-codex","src_pattern":"group:Engineering","dst_pattern":"harness:codex","action":"permit","harness_kind":"codex","condition":"when: {harness: \"codex\"}"}
			],
			"deny_wins_impact":[
				{"id":"77777777-7777-7777-7777-777777777777","name":"conflict-pair","src_pattern":"group:Engineering","dst_pattern":"shell:git","action":"permit","priority":100,"old_decision":"allow","new_decision":"deny","old_outranked":["88888888-8888-8888-8888-888888888888"]}
			],
			"rejected_rows":[
				{"id":"99999999-9999-9999-9999-999999999999","name":"invalid-rule","src_pattern":"user:unknown","dst_pattern":"shell:admin","action":"permit","reject_reason":"non_person_principal"}
			],
			"markdown":"# Policy migration dry run (ADR-038)\n\nRead-only.\n"
		}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "migration-report", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	wantPath := "/api/v1/organizations/org-1/workspaces/" + testWorkspaceID + "/policies:migrationReport"
	if req.Path != wantPath {
		t.Fatalf("path = %s, want %s", req.Path, wantPath)
	}
	if req.Auth != "Bearer "+workspaceTestToken() {
		t.Fatalf("Authorization = %q", req.Auth)
	}
	// Verify the human summary mentions all categories.
	for _, want := range []string{"Standing principals to autogroups:", "1", "Wildcard/role to autogroups:", "1", "Harness scoping to conditions:", "1", "Deny-wins outcome changes:", "1", "Rows that would be rejected:", "1", "Total rows in report:", "5", "dry run"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	_ = fake
}

func TestPolicyMigrationReportJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"standing_principals":[],"wildcard_roles":[],"harness_sources":[],"deny_wins_impact":[],"rejected_rows":[],"markdown":"# empty"}`))
	})
	code, stdout, stderr := runPolicy(t, server, store, "", "migration-report", "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(parsed) == 0 {
		t.Fatal("--json output is empty")
	}
	_ = fake
}

func TestPolicyMigrationReportUnknownPositional(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicy(t, server, store, "", "migration-report", "--workspace", "Main", "extra-arg")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	_ = fake
}

// --- Dispatch ---

func TestPolicyCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 0}, // help to stdout
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 1}, // no token — workspace auto-picked, fails at credential
		{[]string{"get", "x"}, 1},
		{[]string{"delete", "x"}, 2},
		{[]string{"create"}, 2},
		{[]string{"test"}, 2},
		{[]string{"migration-report"}, 1}, // no token — workspace auto-picked, fails at credential
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runPolicyCommand(tt.args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != tt.code {
			t.Errorf("policy %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}

func TestPolicyUsageListedInHelp(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei policy list|get|create|delete|test|migration-report") {
		t.Fatalf("usage missing policy migration-report:\n%s", stdout.String())
	}
}

func TestPolicyWiredIntoMain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main("test", "policy", nil, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (help to stdout); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected help in stdout, got: %s", stdout.String())
	}
}

// --- Deprecation ---

func TestPoliciesAliasShowsDeprecation(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "kei policy") {
		t.Fatalf("deprecation notice missing from stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "No policies") {
		t.Fatalf("stdout = %q", stdout)
	}
	_ = fake
}

// --- Import partial failure ---

func TestPoliciesImportPartialFailure(t *testing.T) {
	attempts := 0
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodPost && r.Path == "/api/v1/policies" {
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"allow-x","effect":"permit","src_pattern":"harness:test","dst_pattern":"shell:x","approval_required":false}`))
				return
			}
			// Second policy fails
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"bad policy"}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	code, stdout, stderr := runPolicies(t, server, store, "y\ny\n", "import", "--from", "claude",
		"--file", filepath.Join(testdataDir, "claude-automode.json"), "--workspace", "Main", "--apply", "--src", "harness:claude_code")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (partial failure); stdout=%q stderr=%q", code, stdout, stderr)
	}
	if attempts != 2 {
		t.Fatalf("attempted %d policies, want 2 (stop at the first failure)", attempts)
	}
	if !strings.Contains(stderr, "failed") {
		t.Fatalf("stderr should mention failure: %q", stderr)
	}
}
