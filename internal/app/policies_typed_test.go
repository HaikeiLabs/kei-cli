package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// These tests pin the typed workspace-path policy API (HAI-621 client half):
// every policy CRUD request goes to
// /api/v1/organizations/{org}/workspaces/{ws}/policies[/{id}] and carries a
// typed `principal` and `target` object. The flat /api/v1/policies route and
// the src_pattern/dst_pattern request fields are removed by catalog#468 and
// must never appear in written values.

const (
	typedTestGroupID  = "77777777-7777-7777-7777-777777777777"
	typedTestPolicyID = "88888888-8888-8888-8888-888888888888"
)

func typedPoliciesPath() string {
	return "/api/v1/organizations/org-1/workspaces/" + testWorkspaceID + "/policies"
}

func assertNoLegacyPolicyFields(t *testing.T, body map[string]any, where string) {
	t.Helper()
	for _, key := range []string{"src_pattern", "dst_pattern"} {
		if _, ok := body[key]; ok {
			t.Errorf("%s: request body still carries legacy field %q: %v", where, key, body)
		}
	}
}

// TestPoliciesCreateSendsTypedPrincipalTarget: kei policies create maps
// --src-pattern/--dst-pattern into principal/target objects on the typed
// workspace-path route.
func TestPoliciesCreateSendsTypedPrincipalTarget(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","status":"ok"}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "create",
		"--workspace", "Main", "--json",
		"--name", "allow-git", "--src-pattern", "group:eng",
		"--dst-pattern", "shell:git", "--effect", "permit")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != typedPoliciesPath() {
		t.Fatalf("request = %s %s?%s; want POST %s", req.Method, req.Path, req.Query, typedPoliciesPath())
	}
	assertNoLegacyPolicyFields(t, req.Body, "create")
	principal, ok := req.Body["principal"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing principal object: %v", req.Body)
	}
	if principal["kind"] != "group" || principal["value"] != "eng" {
		t.Errorf("create principal = %v; want {kind:group value:eng}", principal)
	}
	target, ok := req.Body["target"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing target object: %v", req.Body)
	}
	if target["kind"] != "shell" || target["value"] != "git" {
		t.Errorf("create target = %v; want {kind:shell value:git}", target)
	}
	if req.Body["action"] != "permit" {
		t.Errorf("create action = %v; want permit", req.Body["action"])
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Errorf("create output missing policy id:\n%s", stdout)
	}
}

// TestPolicyStandingRuleSendsTypedPrincipalTarget: kei policy create (standing
// rules) sends group and autogroup principals.
func TestPolicyStandingRuleSendsTypedPrincipalTarget(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","status":"ok"}`))
	})
	var stdout, stderr bytes.Buffer
	code := runPolicyCommand([]string{"create", "--workspace", "Main",
		"--group", "autogroup:members", "--allow", "shell:go"},
		&stdout, &stderr, strings.NewReader(""), server.Client(), store)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testPolicyID) {
		t.Errorf("standing rule create output missing policy id:\n%s", stdout.String())
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != typedPoliciesPath() {
		t.Fatalf("request = %s %s?%s; want POST %s", req.Method, req.Path, req.Query, typedPoliciesPath())
	}
	assertNoLegacyPolicyFields(t, req.Body, "standing rule create")
	principal, ok := req.Body["principal"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing principal object: %v", req.Body)
	}
	if principal["kind"] != "autogroup" || principal["value"] != "members" {
		t.Errorf("principal = %v; want {kind:autogroup value:members}", principal)
	}
	target, ok := req.Body["target"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing target object: %v", req.Body)
	}
	if target["kind"] != "shell" || target["value"] != "go" {
		t.Errorf("target = %v; want {kind:shell value:go}", target)
	}
}

// TestPoliciesUpdateSendsTypedFields: kei policies update maps the
// --src-pattern/--dst-pattern flags to principal/target and an update_mask
// naming the typed fields.
func TestPoliciesUpdateSendsTypedFields(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"allow-git","principal":{"kind":"group","value":"sec"},"target":{"kind":"shell","value":"rm"},"action":"deny","enabled":true}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "update", testPolicyID,
		"--workspace", "Main",
		"--src-pattern", "group:sec", "--dst-pattern", "shell:rm", "--effect", "deny")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Updated policy") {
		t.Errorf("update output missing confirmation:\n%s", stdout)
	}
	req := fake.only(t)
	if req.Method != http.MethodPatch || req.Path != typedPoliciesPath()+"/"+testPolicyID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	assertNoLegacyPolicyFields(t, req.Body, "update")
	principal, ok := req.Body["principal"].(map[string]any)
	if !ok || principal["kind"] != "group" || principal["value"] != "sec" {
		t.Errorf("update principal = %v; want {kind:group value:sec}", req.Body["principal"])
	}
	target, ok := req.Body["target"].(map[string]any)
	if !ok || target["kind"] != "shell" || target["value"] != "rm" {
		t.Errorf("update target = %v; want {kind:shell value:rm}", req.Body["target"])
	}
	if req.Body["action"] != "deny" {
		t.Errorf("update action = %v; want deny", req.Body["action"])
	}
	mask := req.Query
	if !strings.Contains(mask, "update_mask=") ||
		strings.Contains(mask, "src_pattern") || strings.Contains(mask, "dst_pattern") {
		t.Errorf("update query = %q; want update_mask with typed field names", mask)
	}
}

// TestPoliciesListUsesTypedWorkspacePath: kei policies list reads the typed
// workspace-path route and renders principal/target objects as the same
// "kind:value" strings users saw before.
func TestPoliciesListUsesTypedWorkspacePath(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
			{"id":"` + testPolicyID + `","name":"allow-git","principal":{"kind":"group","value":"eng"},"target":{"kind":"shell","value":"git"},"action":"permit","enabled":true},
			{"id":"55555555-5555-5555-5555-555555555555","name":"deny-rm","principal":{"kind":"group","value":"eng"},"target":{"kind":"shell","value":"rm"},"action":"deny","enabled":true}
		]`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != typedPoliciesPath() {
		t.Fatalf("request = %s %s?%s; want GET %s", req.Method, req.Path, req.Query, typedPoliciesPath())
	}
	if strings.Contains(req.Query, "workspace_id=") {
		t.Errorf("typed workspace-path route still sends workspace_id in the query: %q", req.Query)
	}
	for _, want := range []string{"allow-git", "group:eng", "shell:git", "permit", "deny-rm", "shell:rm", "deny"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

// TestGroupsDeleteFindsReferencesByPrincipal: kei groups delete looks up
// referencing policies on the typed workspace-path route and matches them by
// the typed principal, not by src_pattern string matching.
func TestGroupsDeleteFindsReferencesByPrincipal(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`[{"id":"` + typedTestGroupID + `","name":"Engineering"}]`))
		case r.Method == http.MethodGet && r.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`[
				{"id":"` + testPolicyID + `","name":"allow-git","principal":{"kind":"group","value":"Engineering"},"target":{"kind":"shell","value":"git"},"action":"permit","enabled":true}
			]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
		}
	})
	var stdout, stderr bytes.Buffer
	code := runGroupsCommand([]string{"delete", "Engineering", "--workspace", "Main", "--yes"},
		&stdout, &stderr, strings.NewReader(""), server.Client(), store)
	if code == 0 {
		t.Fatalf("groups delete should refuse while a policy references the group; stderr=%s stdout=%s", stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "1 policy(s) reference group") {
		t.Errorf("stderr missing reference warning:\n%s", stderr.String())
	}
	var listReq *consoleRequest
	for i := range fake.requests {
		if fake.requests[i].Method == http.MethodGet && fake.requests[i].Path == typedPoliciesPath() {
			listReq = &fake.requests[i]
		}
	}
	if listReq == nil {
		t.Fatalf("policies lookup never hit the typed workspace-path route; requests = %#v", fake.requests)
	}
	if strings.Contains(listReq.Query, "workspace_id=") {
		t.Errorf("policies lookup query still carries workspace_id: %q", listReq.Query)
	}
}
