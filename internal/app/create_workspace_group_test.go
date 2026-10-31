package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const testCreatedWorkspaceJSON = `{"id":"` + testWorkspaceID + `","org_id":"org-1","name":"e2e","is_default":false}`

func TestWorkspacesCreate(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/organizations/org-1/workspaces" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		// The creator comes from the authenticated caller, never the body.
		if len(body) != 1 || body["name"] != "e2e" {
			t.Fatalf("create body = %#v, want only name", body)
		}
		_, _ = w.Write([]byte(testCreatedWorkspaceJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"create", "--name", "e2e", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Workspace ID: "+testWorkspaceID) {
		t.Fatalf("create output = %s", stdout.String())
	}
}

func TestWorkspacesCreateJSON(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testCreatedWorkspaceJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"create", "--name", "e2e", "--yes", "--json"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	var got workspaceInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.ID != testWorkspaceID || got.Name != "e2e" {
		t.Fatalf("json output = %q (err %v)", stdout.String(), err)
	}
}

func TestWorkspacesCreateConflict(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "a workspace with this name already exists", http.StatusConflict)
	})
	var stdout, stderr bytes.Buffer
	if code := runWorkspaceCommand([]string{"create", "--name", "e2e", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 1 {
		t.Fatalf("create exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestWorkspacesCreateValidation(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	for _, args := range [][]string{
		{"create", "--yes"},                       // no name
		{"create", "--name", "e2e"},               // no --yes on a non-terminal
		{"create", "--name", "e2e", "--yes", "x"}, // stray positional
	} {
		var stdout, stderr bytes.Buffer
		if code := runWorkspaceCommand(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
			t.Fatalf("%v: exit = %d, want 2", args, code)
		}
	}
	if hit {
		t.Fatal("request was made for an invalid invocation")
	}
}

func TestGroupsCreate(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/groups" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("org_id"); got != "org-1" {
			t.Fatalf("org_id = %q", got)
		}
		if got := r.URL.Query().Get("workspace_id"); got != testWorkspaceID {
			t.Fatalf("workspace_id = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["name"] != "e2e-analysts" || body["description"] != "connector e2e" {
			t.Fatalf("create body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"id":"` + testGroupID + `","name":"e2e-analysts","description":"connector e2e"}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"create", "--name", "e2e-analysts", "--description", "connector e2e", "--workspace", "Main", "--yes"}
	if code := runGroupsCommand(args, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Group ID: "+testGroupID) {
		t.Fatalf("create output = %s", stdout.String())
	}
}

func TestGroupsCreateRequiresWorkspace(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	// No --workspace: requireFlags reports it before any network call.
	if code := runGroupsCommand([]string{"create", "--name", "e2e-analysts", "--yes"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("create exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing required flag --workspace") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if hit {
		t.Fatal("request was made without a workspace")
	}
}

func TestGroupsCreateRequiresNameAndYes(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	for _, args := range [][]string{
		{"create", "--workspace", "Main", "--yes"},
		{"create", "--name", "e2e-analysts", "--workspace", "Main"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runGroupsCommand(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
			t.Fatalf("%v: exit = %d, want 2", args, code)
		}
	}
	if hit {
		t.Fatal("request was made for an invalid invocation")
	}
}

func TestGroupsList(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/groups" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("workspace_id"); got != testWorkspaceID {
			t.Fatalf("workspace_id = %q", got)
		}
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"list", "--workspace", "Main"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testGroupID) || !strings.Contains(stdout.String(), "engineers") {
		t.Fatalf("list output = %s", stdout.String())
	}

	stdout.Reset()
	if code := runGroupsCommand([]string{"list", "--workspace", "Main", "--json"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("list --json exit = %d, stderr=%s", code, stderr.String())
	}
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &groups); err != nil || len(groups) != 1 || groups[0].ID != testGroupID || groups[0].Name != "engineers" {
		t.Fatalf("json output = %q (err %v)", stdout.String(), err)
	}
}

func TestGroupsDeleteRequiresYes(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", testGroupID, "--workspace", "Main"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("delete exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("stderr = %s, should mention --yes", stderr.String())
	}
	if hit {
		t.Fatal("request was made despite missing --yes")
	}
}

func TestGroupsDeleteByIDNoPolicies(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(testGroupsJSON()))
		case r.Method == http.MethodGet && r.URL.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/groups/"+testGroupID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", testGroupID, "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("delete exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted") {
		t.Fatalf("delete output = %s", stdout.String())
	}
}

func TestGroupsDeleteByNameNoPolicies(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(testGroupsJSON()))
		case r.Method == http.MethodGet && r.URL.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`{"policies":[],"next_page_token":""}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/groups/"+testGroupID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", "engineers", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("delete exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted") {
		t.Fatalf("delete output = %s", stdout.String())
	}
}

func TestGroupsDeleteRefusesWithPolicyReferences(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(testGroupsJSON()))
		case r.Method == http.MethodGet && r.URL.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`{"policies":[{"id":"` + testPolicyID + `","name":"allow-policy","principal":{"kind":"group","value":"engineers"},"target":{"kind":"shell","value":"git"},"action":"permit","priority":100,"enabled":true,"approval_required":false}],"next_page_token":""}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", "engineers", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 1 {
		t.Fatalf("delete exit = %d, want 1, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "policy(s) reference") {
		t.Fatalf("stderr should mention policy reference: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--force") {
		t.Fatalf("stderr should mention --force: %s", stderr.String())
	}
}

func TestGroupsDeleteForceOverridesPolicyReferences(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(testGroupsJSON()))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/groups/"+testGroupID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", testGroupID, "--workspace", "Main", "--yes", "--force"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("delete exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted") {
		t.Fatalf("delete output = %s", stdout.String())
	}
}

func TestGroupsDeleteByNameNotFound(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"delete", "nonexistent", "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 1 {
		t.Fatalf("delete exit = %d, want 1, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr should mention not found: %s", stderr.String())
	}
}

func TestGroupsListRequiresWorkspace(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	if code := runGroupsCommand([]string{"list"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("list exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "missing required flag --workspace") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if hit {
		t.Fatal("request was made without a workspace")
	}
}
