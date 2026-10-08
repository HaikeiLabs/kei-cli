package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPolicyID = "44444444-4444-4444-4444-444444444444"

func runPolicies(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runPoliciesCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
}

// --- List ---

func TestPoliciesList(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[
			{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
			{"id":"55555555-5555-5555-5555-555555555555","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"deny-rm","src_pattern":"harness:claude","dst_pattern":"shell:rm","effect":"deny","priority":200,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/policies" || !strings.Contains(req.Query, "workspace_id="+testWorkspaceID) {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Auth != "Bearer cli-session-token" {
		t.Fatalf("Authorization = %q", req.Auth)
	}
	if req.Headers["X-Kei-API-Shape"] != "aip" {
		t.Fatalf("X-Kei-API-Shape header = %q, want aip", req.Headers["X-Kei-API-Shape"])
	}
	for _, want := range []string{testPolicyID, "allow-git", "harness:claude", "shell:git", "permit", "deny-rm", "deny"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestPoliciesListJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID, "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("JSON output missing policy id:\n%s", stdout)
	}
	_ = fake
}

func TestPoliciesListPagination(t *testing.T) {
	calls := 0
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"policies":[
				{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"page1","src_pattern":"harness:a","dst_pattern":"shell:x","effect":"permit","priority":1,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
			],"next_page_token":"cursor2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"policies":[
				{"id":"55555555-5555-5555-5555-555555555555","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"page2","src_pattern":"harness:b","dst_pattern":"shell:y","effect":"deny","priority":2,"enabled":true,"approval_required":false,"created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
			],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID, "--page-size", "1")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if calls != 2 {
		t.Fatalf("expected 2 requests, got %d", calls)
	}
	if !strings.Contains(fake.requests[0].Query, "workspace_id="+testWorkspaceID) || !strings.Contains(fake.requests[0].Query, "page_size=1") {
		t.Fatalf("page 1 query = %q", fake.requests[0].Query)
	}
	if !strings.Contains(fake.requests[1].Query, "page_token=cursor2") {
		t.Fatalf("page 2 query = %q, missing page_token", fake.requests[1].Query)
	}
	if !strings.Contains(stdout, "page1") || !strings.Contains(stdout, "page2") {
		t.Fatalf("stdout missing both pages:\n%s", stdout)
	}
}

func TestPoliciesListEmpty(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "No policies found") {
		t.Fatalf("empty list output = %q", stdout)
	}
	_ = fake
}

func TestPoliciesListBareArray(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
			{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
			{"id":"55555555-5555-5555-5555-555555555555","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"deny-rm","src_pattern":"harness:claude","dst_pattern":"shell:rm","effect":"deny","priority":200,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		]`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/policies" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	for _, want := range []string{testPolicyID, "allow-git", "deny-rm"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

// --- Get ---

func TestPoliciesGet(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","description":"Allow git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "get", testPolicyID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/policies/"+testPolicyID || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Headers["X-Kei-API-Shape"] != "aip" {
		t.Fatalf("X-Kei-API-Shape header = %q, want aip", req.Headers["X-Kei-API-Shape"])
	}
	for _, want := range []string{testPolicyID, "allow-git", "harness:claude", "shell:git", "permit", "100"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

func TestPoliciesGetJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "get", testPolicyID, "--workspace", testWorkspaceID, "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("JSON output missing policy id:\n%s", stdout)
	}
	_ = fake
}

func TestPoliciesGetByName(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/policies" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}],"next_page_token":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "get", "allow-git", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("expected 2 requests (list + get), got %d", fake.count())
	}
	if !strings.Contains(fake.requests[1].Path, testPolicyID) {
		t.Fatalf("second request should use resolved UUID, path=%s", fake.requests[1].Path)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("output missing policy id:\n%s", stdout)
	}
}

func TestPoliciesGetNameNotFound(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
	})
	code, _, stderr := runPolicies(t, server, store, "", "get", "nonexistent", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "no policy found") || fake.count() != 1 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPoliciesGetNameAmbiguous(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[
			{"id":"` + testPolicyID + `","name":"duplicate","effect":"permit","approval_required":false},
			{"id":"55555555-5555-5555-5555-555555555555","name":"duplicate","effect":"deny","approval_required":false}
		],"next_page_token":""}`))
	})
	code, _, stderr := runPolicies(t, server, store, "", "get", "duplicate", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "multiple policies") || fake.count() != 1 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

// --- Create ---

func TestPoliciesCreate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","src_pattern":"harness:claude","dst_pattern":"shell:git","effect":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "create", "--workspace", testWorkspaceID,
		"--name", "allow-git", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "permit", "--priority", "100")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/policies" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Headers["X-Kei-API-Shape"] != "aip" {
		t.Fatalf("X-Kei-API-Shape header = %q, want aip", req.Headers["X-Kei-API-Shape"])
	}
	if req.Body["name"] != "allow-git" || req.Body["src_pattern"] != "harness:claude" || req.Body["dst_pattern"] != "shell:git" || req.Body["effect"] != "permit" || req.Body["action"] != "permit" {
		t.Fatalf("body = %v", req.Body)
	}
	if req.Body["priority"] != float64(100) {
		t.Fatalf("priority = %v, want 100", req.Body["priority"])
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("create output missing policy id: %q", stdout)
	}
}

func TestPoliciesCreateRequiresFields(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string][]string{
		"missing name":        {"--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "permit"},
		"missing src-pattern": {"--name", "x", "--dst-pattern", "shell:git", "--effect", "permit"},
		"missing dst-pattern": {"--name", "x", "--src-pattern", "harness:claude", "--effect", "permit"},
		"missing effect":      {"--name", "x", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git"},
		"invalid effect":      {"--name", "x", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "maybe"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"create", "--workspace", testWorkspaceID}, args...)
			code, _, stderr := runPolicies(t, server, store, "", full...)
			if code != 2 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want 2 with an error", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("an invalid create reached the console")
	}
}

// --- Update ---

func TestPoliciesUpdate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"renamed","effect":"deny","priority":100,"enabled":true,"approval_required":false}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "update", testPolicyID, "--workspace", testWorkspaceID, "--name", "renamed", "--effect", "deny")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPatch || req.Path != "/api/v1/policies/"+testPolicyID {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(req.Query, "workspace_id="+testWorkspaceID) {
		t.Fatalf("query = %q, want workspace_id", req.Query)
	}
	q, _ := url.ParseQuery(req.Query)
	if q.Get("update_mask") != "name,effect" {
		t.Fatalf("query = %q, want update_mask=name,effect", req.Query)
	}
	if req.Headers["X-Kei-API-Shape"] != "aip" {
		t.Fatalf("X-Kei-API-Shape header = %q, want aip", req.Headers["X-Kei-API-Shape"])
	}
	if req.Body["name"] != "renamed" || req.Body["effect"] != "deny" || req.Body["action"] != "deny" {
		t.Fatalf("body = %v", req.Body)
	}
	if _, present := req.Body["src_pattern"]; present {
		t.Fatal("body contains src_pattern, but it was not set")
	}
	if _, present := req.Body["dst_pattern"]; present {
		t.Fatal("body contains dst_pattern, but it was not set")
	}
	if !strings.Contains(stdout, "renamed") {
		t.Fatalf("update output missing new name: %q", stdout)
	}
}

func TestPoliciesUpdateSendsOnlySetFlags(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"x","effect":"permit","priority":50,"enabled":true,"approval_required":false}`))
	})
	_, _, stderr := runPolicies(t, server, store, "", "update", testPolicyID, "--workspace", testWorkspaceID, "--priority", "50")
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	req := fake.only(t)
	if req.Body["priority"] != float64(50) {
		t.Fatalf("priority = %v, want 50", req.Body["priority"])
	}
	for _, field := range []string{"name", "src_pattern", "dst_pattern", "effect", "action", "enabled", "description"} {
		if _, present := req.Body[field]; present {
			t.Fatalf("body contains %q, but only --priority was set", field)
		}
	}
}

func TestPoliciesUpdateRequiresAtLeastOneField(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, store, "", "update", testPolicyID, "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "at least one") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPoliciesUpdateByName(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/policies" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}],"next_page_token":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"renamed","effect":"deny","approval_required":false}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "update", "allow-git", "--workspace", testWorkspaceID, "--name", "renamed", "--effect", "deny")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("expected 2 requests (list + patch), got %d", fake.count())
	}
	if !strings.Contains(stdout, "renamed") {
		t.Fatalf("update output missing new name: %q", stdout)
	}
}

// --- Delete ---

func TestPoliciesDeleteRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, store, "", "delete", testPolicyID, "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPoliciesDelete(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNoContent)
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "delete", testPolicyID, "--workspace", testWorkspaceID, "--yes")
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
	if req.Headers["X-Kei-API-Shape"] != "aip" {
		t.Fatalf("X-Kei-API-Shape header = %q, want aip", req.Headers["X-Kei-API-Shape"])
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("delete output = %q", stdout)
	}
}

func TestPoliciesDeleteByName(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/policies" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-git","effect":"permit","approval_required":false}],"next_page_token":""}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "delete", "allow-git", "--workspace", testWorkspaceID, "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("expected 2 requests (list + delete), got %d", fake.count())
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("delete output = %q", stdout)
	}
}

// --- Workspace ---

func TestPoliciesRequireWorkspace(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	for _, args := range [][]string{
		{"list"},
		{"get", testPolicyID},
		{"create", "--name", "x", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "permit"},
		{"update", testPolicyID, "--name", "x"},
		{"delete", testPolicyID, "--yes"},
	} {
		code, _, stderr := runPolicies(t, server, store, "", args...)
		if code != 2 || !strings.Contains(stderr, "--workspace") {
			t.Errorf("%v: exit = %d stderr=%q, want 2 naming --workspace", args, code, stderr)
		}
	}
	if fake.count() != 0 {
		t.Fatal("a command without a workspace called the console")
	}
}

func TestPoliciesResolveWorkspaceByName(t *testing.T) {
	fake, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/organizations/org-1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
	})
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 || !strings.Contains(fake.requests[1].Query, "workspace_id="+testWorkspaceID) {
		t.Fatalf("requests = %#v", fake.requests)
	}
	if !strings.Contains(stdout, "No policies") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// --- Auth ---

func TestPoliciesNotLoggedIn(t *testing.T) {
	_, server, _ := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, &memoryCredentialStore{}, "", "list", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "kei login") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// --- Dispatch ---

func TestPoliciesCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 2}, // no workspace
		{[]string{"get", "x"}, 2},
		{[]string{"delete", "x"}, 2},
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runPoliciesCommand(tt.args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != tt.code {
			t.Errorf("policies %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}

func TestPoliciesUsageListsSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei policies list|get|create|update|delete|import") {
		t.Fatalf("usage missing policies:\n%s", stdout.String())
	}
}

// --- Import ---

const testdataDir = "testdata"

func TestPoliciesImportCodexRealFixture(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", filepath.Join(testdataDir, "codex-default.rules"), "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Importing 74 policies") {
		t.Fatalf("want 74 policies, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "harness:codex") {
		t.Fatalf("missing src pattern:\n%s", stdout)
	}
	if !strings.Contains(stdout, "shell:git worktree add") {
		t.Fatalf("missing expected dst pattern:\n%s", stdout)
	}
	if !strings.Contains(stdout, "permit") {
		t.Fatalf("missing permit effect:\n%s", stdout)
	}
	if strings.Contains(stdout, "deny") {
		t.Fatalf("unexpected deny in all-allow fixture:\n%s", stdout)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

func TestPoliciesImportOpenCodeRealFixture(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "opencode",
		"--file", filepath.Join(testdataDir, "opencode-permission.json"), "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Importing 28 policies") {
		t.Fatalf("want 28 policies (3 skill + 25 external_directory allows), got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "skill:draft-plan") {
		t.Fatalf("missing skill pattern:\n%s", stdout)
	}
	if !strings.Contains(stdout, "path:/home/user/code/haikei") {
		t.Fatalf("missing path pattern:\n%s", stdout)
	}
	if !strings.Contains(stdout, "permit") {
		t.Fatalf("missing permit effect:\n%s", stdout)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

func TestPoliciesImportClaudeRealFixture(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "claude",
		"--file", filepath.Join(testdataDir, "claude-automode.json"), "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Importing 2 policies") {
		t.Fatalf("want 2 policies (2 trailing :* Bash entries; kubectl and helm skipped), got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "shell:terraform destroy") {
		t.Fatalf("missing terraform deny:\n%s", stdout)
	}
	if !strings.Contains(stdout, "shell:git init") {
		t.Fatalf("missing git init deny:\n%s", stdout)
	}
	if !strings.Contains(stdout, "harness:claude_code") {
		t.Fatalf("missing claude_code src pattern:\n%s", stdout)
	}
	if strings.Contains(stdout, "kubectl") {
		t.Fatalf("kubectl mid-wildcard entry should be skipped:\n%s", stdout)
	}
	if strings.Contains(stdout, "helm:") {
		t.Fatalf("helm mid-wildcard entry should be skipped:\n%s", stdout)
	}
	if !strings.Contains(stderr, "skipping") || !strings.Contains(stderr, "kubectl:delete*") {
		t.Fatalf("stderr should warn about skipped kubectl entry: %q", stderr)
	}
	if !strings.Contains(stderr, "helm:*values-prod.yaml*") {
		t.Fatalf("stderr should warn about skipped helm entry: %q", stderr)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

func TestPoliciesImportOutFlag(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "policies.json")
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", filepath.Join(testdataDir, "codex-default.rules"), "--workspace", testWorkspaceID, "--out", outFile)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Wrote 74 policies to") {
		t.Fatalf("stdout = %q", stdout)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	var policies []map[string]string
	if err := json.Unmarshal(data, &policies); err != nil {
		t.Fatalf("decode output JSON: %v", err)
	}
	if len(policies) != 74 {
		t.Fatalf("output file has %d policies, want 74", len(policies))
	}
	if policies[0]["src_pattern"] != "harness:codex" {
		t.Fatalf("first policy src_pattern = %q", policies[0]["src_pattern"])
	}
	if policies[0]["effect"] != "permit" {
		t.Fatalf("first policy effect = %q", policies[0]["effect"])
	}
	if fake.count() != 0 {
		t.Fatal("--out made API calls")
	}
}

func TestPoliciesImportSrcOverride(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", filepath.Join(testdataDir, "codex-default.rules"), "--workspace", testWorkspaceID,
		"--src", "harness:custom")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "harness:custom") {
		t.Fatalf("missing custom src pattern:\n%s", stdout)
	}
	if strings.Contains(stdout, "harness:codex") {
		t.Fatalf("should not contain default src pattern:\n%s", stdout)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

func TestPoliciesImportApplyCreatesPolicies(t *testing.T) {
	created := 0
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/cli/workspaces" {
			_, _ = w.Write([]byte(`{"workspaces":[{"id":"` + testWorkspaceID + `","name":"Main"}]}`))
			return
		}
		if r.Method == http.MethodPost && r.Path == "/api/v1/policies" {
			created++
			name, _ := r.Body["name"].(string)
			eff, _ := r.Body["effect"].(string)
			act, _ := r.Body["action"].(string)
			if act != eff {
				t.Errorf("action = %q, want %q", act, eff)
			}
			src, _ := r.Body["src_pattern"].(string)
			dst, _ := r.Body["dst_pattern"].(string)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"` + name + `","effect":"` + eff + `","src_pattern":"` + src + `","dst_pattern":"` + dst + `","approval_required":false}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	code, stdout, stderr := runPolicies(t, server, store, "y\n", "import", "--from", "claude",
		"--file", filepath.Join(testdataDir, "claude-automode.json"), "--workspace", testWorkspaceID, "--apply")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if created != 2 {
		t.Fatalf("created %d policies, want 2", created)
	}
	if !strings.Contains(stdout, "Created 2 policies") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestPoliciesImportApplyDeclined(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/cli/workspaces" {
			_, _ = w.Write([]byte(`{"workspaces":[{"id":"` + testWorkspaceID + `","name":"Main"}]}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	code, stdout, stderr := runPolicies(t, server, store, "n\n", "import", "--from", "claude",
		"--file", filepath.Join(testdataDir, "claude-automode.json"), "--workspace", testWorkspaceID, "--apply")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "aborted") {
		t.Fatalf("stdout = %q, want aborted", stdout)
	}
	if fake.count() != 0 {
		t.Fatalf("requests = %d, want 0 (no API calls on decline)", fake.count())
	}
}

func TestPoliciesImportRequiresFrom(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, store, "", "import", "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "--from") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPoliciesImportUnknownHarness(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, store, "", "import", "--from", "unknown", "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "claude") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestPoliciesImportMissingFile(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", "/nonexistent/path.rules", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "not found") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

// --- API error ---

// --- Effect / Action field mapping ---

func TestPolicyUnmarshalAcceptsEffect(t *testing.T) {
	var p policy
	if err := json.Unmarshal([]byte(`{"effect":"deny"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Effect != "deny" {
		t.Fatalf("Effect = %q, want deny", p.Effect)
	}
}

func TestPolicyUnmarshalAcceptsAction(t *testing.T) {
	var p policy
	if err := json.Unmarshal([]byte(`{"action":"permit"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Effect != "permit" {
		t.Fatalf("Effect = %q, want permit (from action)", p.Effect)
	}
}

func TestPolicyUnmarshalEffectTakesPriority(t *testing.T) {
	var p policy
	if err := json.Unmarshal([]byte(`{"effect":"deny","action":"permit"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Effect != "deny" {
		t.Fatalf("Effect = %q, want deny (effect takes priority)", p.Effect)
	}
}

func TestPolicyMarshalUsesEffect(t *testing.T) {
	p := policy{Effect: "deny"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"effect":"deny"`) {
		t.Fatalf("JSON output missing effect field: %s", data)
	}
	if strings.Contains(string(data), `"action"`) {
		t.Fatalf("JSON output should not contain action field: %s", data)
	}
}

func TestPoliciesListLegacyActionShape(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"policies":[
			{"id":"` + testPolicyID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"allow-git","src_pattern":"harness:claude","dst_pattern":"shell:git","action":"permit","priority":100,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"},
			{"id":"55555555-5555-5555-5555-555555555555","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","name":"deny-rm","src_pattern":"harness:claude","dst_pattern":"shell:rm","action":"deny","priority":200,"enabled":true,"approval_required":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
		],"next_page_token":""}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"permit", "deny"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing effect %q (from action field):\n%s", want, stdout)
		}
	}
	_ = fake
}

func TestPoliciesListLegacyBareArrayWithAction(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
			{"id":"` + testPolicyID + `","name":"allow-git","action":"permit"},
			{"id":"55555555-5555-5555-5555-555555555555","name":"deny-rm","action":"deny"}
		]`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"permit", "deny"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing effect %q (from action in bare array):\n%s", want, stdout)
		}
	}
	_ = fake
}

func TestPoliciesCreateSendsActionField(t *testing.T) {
	// The kei-policy-catalog legacy route reads "action" when the console
	// does not forward X-Kei-API-Shape. This test verifies the CLI sends
	// both "action" and "effect" so both code paths work.
	var gotBody map[string]any
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Body["action"] == nil || r.Body["action"] != r.Body["effect"] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"action must be 'permit' or 'deny'"}`))
			return
		}
		gotBody = r.Body
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"test","effect":"permit","approval_required":false}`))
	})
	code, stdout, stderr := runPolicies(t, server, store, "", "create", "--workspace", testWorkspaceID,
		"--name", "test", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "permit")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if gotBody == nil || gotBody["action"] != "permit" || gotBody["effect"] != "permit" {
		t.Fatalf("body missing action or effect fields: %v", gotBody)
	}
	if !strings.Contains(stdout, testPolicyID) {
		t.Fatalf("create output missing policy id: %q", stdout)
	}
}

func TestPoliciesUpdateSendsActionField(t *testing.T) {
	var gotBody map[string]any
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Body["action"] == nil || r.Body["action"] != r.Body["effect"] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"action must be 'permit' or 'deny'"}`))
			return
		}
		gotBody = r.Body
		_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"test","effect":"deny","approval_required":false}`))
	})
	code, _, stderr := runPolicies(t, server, store, "", "update", testPolicyID, "--workspace", testWorkspaceID, "--effect", "deny")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if gotBody == nil || gotBody["action"] != "deny" || gotBody["effect"] != "deny" {
		t.Fatalf("body missing action or effect fields: %v", gotBody)
	}
}

func TestPoliciesSurfacesAPIError(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"effect must be permit or deny"}`))
	})
	code, _, stderr := runPolicies(t, server, store, "", "create", "--workspace", testWorkspaceID,
		"--name", "x", "--src-pattern", "harness:claude", "--dst-pattern", "shell:git", "--effect", "permit")
	if code != 1 || !strings.Contains(stderr, "effect must be permit or deny") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestPoliciesImportPrintsDefaultScope(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", filepath.Join(testdataDir, "codex-default.rules"), "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if want := "Scope: src harness:codex (only that harness; pass --src harness:* to share these rules with every harness)"; !strings.Contains(stdout, want) {
		t.Fatalf("stdout missing %q:\n%s", want, stdout)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

func TestPoliciesImportSrcHarnessStarSharesRules(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runPolicies(t, server, store, "", "import", "--from", "codex",
		"--file", filepath.Join(testdataDir, "codex-default.rules"), "--workspace", testWorkspaceID,
		"--src", "harness:*")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Scope: src harness:* (shared with every harness)") {
		t.Fatalf("missing shared scope line:\n%s", stdout)
	}
	if strings.Contains(stdout, "harness:codex") {
		t.Fatalf("default src still used:\n%s", stdout)
	}
	if fake.count() != 0 {
		t.Fatal("dry run made API calls")
	}
}

// importApplyCapturingSrc runs an interactive --apply import of the Claude
// fixture with the given stdin and returns stdout and each created src.
func importApplyCapturingSrc(t *testing.T, stdin string, interactive bool, extra ...string) (string, []string) {
	t.Helper()
	previous := stdinIsInteractive
	stdinIsInteractive = func(io.Reader) bool { return interactive }
	t.Cleanup(func() { stdinIsInteractive = previous })
	var srcs []string
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/cli/workspaces" {
			_, _ = w.Write([]byte(`{"workspaces":[{"id":"` + testWorkspaceID + `","name":"Main"}]}`))
			return
		}
		if r.Method == http.MethodPost && r.Path == "/api/v1/policies" {
			src, _ := r.Body["src_pattern"].(string)
			srcs = append(srcs, src)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `","name":"p","effect":"permit","src_pattern":"` + src + `","dst_pattern":"shell:git","approval_required":false}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	args := append([]string{"import", "--from", "claude", "--file", filepath.Join(testdataDir, "claude-automode.json"), "--workspace", testWorkspaceID, "--apply"}, extra...)
	code, stdout, stderr := runPolicies(t, server, store, stdin, args...)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	return stdout, srcs
}

func TestPoliciesImportInteractiveApplyToAllHarnesses(t *testing.T) {
	stdout, srcs := importApplyCapturingSrc(t, "y\ny\n", true)
	if !strings.Contains(stdout, "Apply to all harnesses? [y/N]") {
		t.Fatalf("missing scope prompt:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Scope: src harness:* (shared with every harness)") {
		t.Fatalf("missing shared scope line:\n%s", stdout)
	}
	if len(srcs) != 2 || srcs[0] != "harness:*" || srcs[1] != "harness:*" {
		t.Fatalf("created srcs = %q, want harness:*", srcs)
	}
}

func TestPoliciesImportInteractiveKeepsDefaultScope(t *testing.T) {
	stdout, srcs := importApplyCapturingSrc(t, "n\ny\n", true)
	if !strings.Contains(stdout, "Apply to all harnesses? [y/N]") {
		t.Fatalf("missing scope prompt:\n%s", stdout)
	}
	if len(srcs) != 2 || srcs[0] != "harness:claude_code" || srcs[1] != "harness:claude_code" {
		t.Fatalf("created srcs = %q, want harness:claude_code", srcs)
	}
}

func TestPoliciesImportScopePromptSkipped(t *testing.T) {
	// Non-interactive stdin keeps the default without asking.
	stdout, srcs := importApplyCapturingSrc(t, "y\n", false)
	if strings.Contains(stdout, "Apply to all harnesses") {
		t.Fatalf("prompted on non-interactive stdin:\n%s", stdout)
	}
	if len(srcs) != 2 || srcs[0] != "harness:claude_code" {
		t.Fatalf("created srcs = %q, want harness:claude_code", srcs)
	}
	// An explicit --src is the answer; no prompt.
	stdout, srcs = importApplyCapturingSrc(t, "y\n", true, "--src", "harness:*")
	if strings.Contains(stdout, "Apply to all harnesses") {
		t.Fatalf("prompted despite explicit --src:\n%s", stdout)
	}
	if len(srcs) != 2 || srcs[0] != "harness:*" {
		t.Fatalf("created srcs = %q, want harness:*", srcs)
	}
}
