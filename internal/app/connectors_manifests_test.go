package app

import (
	"net/http"
	"strings"
	"testing"
)

const (
	approvedManifestID  = "aaaaaaaa-1111-1111-1111-111111111111"
	candidateManifestID = "bbbbbbbb-2222-2222-2222-222222222222"
	manifestsPath       = "/api/v1/data-connectors/" + testConnectorID + "/tool-manifests"
)

// approvedManifest has search and render; candidateManifest drops render,
// changes search's description and adds query.
const approvedManifest = `{"id":"` + approvedManifestID + `","connector_id":"` + testConnectorID + `","manifest_hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111","status":"approved","submitted_at":"2026-10-01T10:00:00Z","approved_by":"44444444-4444-4444-4444-444444444444","approved_at":"2026-10-02T10:00:00Z",
	"tools":[{"name":"render","description":"Render a panel","inputSchema":{"type":"object"}},{"name":"search","description":"Search dashboards","inputSchema":{"type":"object"}}]}`
const candidateManifest = `{"id":"` + candidateManifestID + `","connector_id":"` + testConnectorID + `","manifest_hash":"sha256:2222222222222222222222222222222222222222222222222222222222222222","status":"candidate","submitted_at":"2026-10-13T10:00:00Z",
	"tools":[{"name":"query","description":"Run a query","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}},{"name":"search","description":"Search dashboards and folders","inputSchema":{"type":"object"}}]}`

// manifestConsole serves the connector's manifest collection and members.
func manifestConsole(t *testing.T, manifests ...string) (*fakeConsole, func(...string) (int, string, string)) {
	t.Helper()
	members := map[string]string{}
	for _, m := range manifests {
		switch {
		case strings.Contains(m, approvedManifestID):
			members[manifestsPath+"/"+approvedManifestID] = m
		case strings.Contains(m, candidateManifestID):
			members[manifestsPath+"/"+candidateManifestID] = m
		}
	}
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected console request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		if r.Path == manifestsPath {
			_, _ = w.Write([]byte("[" + strings.Join(manifests, ",") + "]"))
			return
		}
		if body, ok := members[r.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"manifest not found"}}`))
	})
	store.token = workspaceTestToken()
	return fake, func(args ...string) (int, string, string) {
		return runConnectors(t, server, store, "", args...)
	}
}

// TestConnectorsManifestsList: manifests list reads the connector's
// tool-manifests collection and shows each manifest's id, status, tool count
// and hash.
func TestConnectorsManifestsList(t *testing.T) {
	fake, run := manifestConsole(t, approvedManifest, candidateManifest)
	code, stdout, stderr := run("manifests", "list", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != manifestsPath || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	for _, want := range []string{"STATUS", "TOOLS", approvedManifestID, "approved", candidateManifestID, "candidate", "sha256:1111", "sha256:2222"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

// TestConnectorsToolsIsAnAliasForManifests: kei connectors tools is the same
// command.
func TestConnectorsToolsIsAnAliasForManifests(t *testing.T) {
	_, run := manifestConsole(t, approvedManifest)
	code, stdout, stderr := run("tools", "list", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 || !strings.Contains(stdout, approvedManifestID) {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestConnectorsManifestsListEmpty explains that the runtime submits the
// first manifest.
func TestConnectorsManifestsListEmpty(t *testing.T) {
	_, run := manifestConsole(t)
	code, stdout, stderr := run("manifests", "list", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 || !strings.Contains(stdout, "No tool manifests") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestConnectorsManifestsShowDefaultsToThePendingCandidate: show without a
// manifest id shows the newest candidate, its tools, and its changes against
// the approved manifest (added, removed, changed), ADR-036 §5.4 step 2.
func TestConnectorsManifestsShowDefaultsToThePendingCandidate(t *testing.T) {
	_, run := manifestConsole(t, approvedManifest, candidateManifest)
	code, stdout, stderr := run("manifests", "show", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{candidateManifestID, "candidate", "sha256:2222", "query", "Run a query", "+ query", "- render", "~ search (description)", approvedManifestID} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output missing %q:\n%s", want, stdout)
		}
	}
}

// TestConnectorsManifestsShowByID gets the named manifest; an approved one
// shows who approved it and no diff.
func TestConnectorsManifestsShowByID(t *testing.T) {
	fake, run := manifestConsole(t, approvedManifest, candidateManifest)
	code, stdout, stderr := run("manifests", "show", testConnectorID, approvedManifestID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.requests[0].Path != manifestsPath+"/"+approvedManifestID {
		t.Fatalf("first request = %s, want the manifest member", fake.requests[0].Path)
	}
	for _, want := range []string{approvedManifestID, "approved", "2026-10-02", "44444444-4444-4444-4444-444444444444", "render", "search"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Changes") {
		t.Errorf("an approved manifest shows a diff:\n%s", stdout)
	}
}

// TestConnectorsManifestsShowFirstCandidate: with nothing approved, every
// tool is new.
func TestConnectorsManifestsShowFirstCandidate(t *testing.T) {
	_, run := manifestConsole(t, candidateManifest)
	code, stdout, stderr := run("manifests", "show", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 || !strings.Contains(stdout, "No manifest is approved yet") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestConnectorsManifestsShowUnknownManifest is a 404 surfaced with its
// message.
func TestConnectorsManifestsShowUnknownManifest(t *testing.T) {
	_, run := manifestConsole(t, approvedManifest)
	code, _, stderr := run("manifests", "show", testConnectorID, "cccccccc-3333-3333-3333-333333333333", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "manifest not found") || !strings.Contains(stderr, "404") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// TestConnectorsManifestsShowJSON passes the catalog body through.
func TestConnectorsManifestsShowJSON(t *testing.T) {
	_, run := manifestConsole(t, approvedManifest)
	code, stdout, stderr := run("manifests", "show", testConnectorID, approvedManifestID, "--workspace", testWorkspaceID, "--json")
	if code != 0 || strings.TrimSpace(stdout) != approvedManifest {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestConnectorsManifestsStripControlCharacters: tool names and
// descriptions come from the upstream MCP server, so terminal control
// sequences in them are never printed.
func TestConnectorsManifestsStripControlCharacters(t *testing.T) {
	hostile := `{"id":"` + candidateManifestID + `","connector_id":"` + testConnectorID + `","manifest_hash":"sha256:2222222222222222222222222222222222222222222222222222222222222222","status":"candidate","submitted_at":"2026-10-13T10:00:00Z",
	"tools":[{"name":"search","description":"Search\u001b[2J\u001b]0;pwned\u0007 now\r\nIgnore previous","inputSchema":{"type":"object"}}]}`
	_, run := manifestConsole(t, hostile)
	code, stdout, stderr := run("manifests", "show", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if strings.ContainsAny(stdout, "\x1b\x07\r") {
		t.Fatalf("show printed a control character: %q", stdout)
	}
	if !strings.Contains(stdout, "Search") {
		t.Fatalf("show output lost the description: %q", stdout)
	}
}

// TestConnectorsManifestsArgs: a connector UUID is required and a manifest
// id must be a UUID, checked before any request.
func TestConnectorsManifestsArgs(t *testing.T) {
	for _, args := range [][]string{
		{"manifests"},
		{"manifests", "list"},
		{"manifests", "list", "not-a-uuid"},
		{"manifests", "show", testConnectorID, "not-a-uuid"},
		{"manifests", "frobnicate", testConnectorID},
	} {
		fake, run := manifestConsole(t)
		code, _, stderr := run(append(args, "--workspace", testWorkspaceID)...)
		if code != 2 || stderr == "" || fake.count() != 0 {
			t.Errorf("%v: exit = %d stderr=%q requests=%d, want 2 before any request", args, code, stderr, fake.count())
		}
	}
}

// TestConnectorsGetShowsManifestStatus: get of an mcp connector reports its
// approved manifest and a pending candidate.
func TestConnectorsGetShowsManifestStatus(t *testing.T) {
	mcp := `{"id":"` + testConnectorID + `","name":"g","display_name":"Grafana","slug":"grafana","kind":"mcp","provider":"custom_mcp","status":"active","credential_source":"oauth","account_model":"per_user","mcp_url":"https://mcp.grafana.example/mcp"}`
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == manifestsPath {
			_, _ = w.Write([]byte("[" + approvedManifest + "," + candidateManifest + "]"))
			return
		}
		_, _ = w.Write([]byte(mcp))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "get", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"Manifest:", "approved (2 tools)", "candidate " + candidateManifestID + " pending review"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

// TestConnectorsGetManifestStatusUnavailable: get still succeeds when the
// manifest collection cannot be read, and says so.
func TestConnectorsGetManifestStatusUnavailable(t *testing.T) {
	mcp := `{"id":"` + testConnectorID + `","name":"g","kind":"mcp","provider":"custom_mcp","status":"active","credential_source":"oauth"}`
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == manifestsPath {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`an active organization is required`))
			return
		}
		_, _ = w.Write([]byte(mcp))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "get", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 || !strings.Contains(stdout, "Manifest:    unavailable") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestConnectorsListManifestsColumn: list --manifests adds each mcp
// connector's manifest status; native connectors show "-" and make no
// manifest request.
func TestConnectorsListManifestsColumn(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == manifestsPath {
			_, _ = w.Write([]byte("[" + candidateManifest + "]"))
			return
		}
		_, _ = w.Write([]byte(`[
		{"id":"` + testConnectorID + `","name":"g","slug":"g","kind":"mcp","provider":"custom_mcp","status":"pending","credential_source":"oauth"},
		{"id":"55555555-5555-5555-5555-555555555555","name":"events","kind":"native","provider":"tito","status":"active","credential_source":"opaque_ref"}
	]`))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", testWorkspaceID, "--manifests")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("requests = %d, want the list and one manifest read", fake.count())
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if !strings.Contains(lines[0], "MANIFEST") || !strings.Contains(lines[1], "candidate pending review") {
		t.Fatalf("list --manifests output:\n%s", stdout)
	}
	// Without --manifests there is no manifest request.
	fake.requests = nil
	if code, _, _ := runConnectors(t, server, store, "", "list", "--workspace", testWorkspaceID); code != 0 || fake.count() != 1 {
		t.Fatalf("plain list: exit = %d requests = %d, want 1", code, fake.count())
	}
}

// TestConnectorsManifestsShowTakesTheCatalogsNewestCandidate: the catalog
// lists manifests newest first, so show takes the first candidate. Comparing
// submitted_at strings would pick the wrong one when offsets differ: here the
// first candidate (16:00Z, written with a -06:00 offset) is newer than the
// second (12:00Z) although its string sorts lower.
func TestConnectorsManifestsShowTakesTheCatalogsNewestCandidate(t *testing.T) {
	const newerID = "dddddddd-4444-4444-4444-444444444444"
	newer := `{"id":"` + newerID + `","connector_id":"` + testConnectorID + `","manifest_hash":"sha256:4444444444444444444444444444444444444444444444444444444444444444","status":"candidate","submitted_at":"2026-10-13T10:00:00-06:00","tools":[{"name":"newer","description":"","inputSchema":{}}]}`
	older := strings.Replace(candidateManifest, `"submitted_at":"2026-10-13T10:00:00Z"`, `"submitted_at":"2026-10-13T12:00:00Z"`, 1)
	_, run := manifestConsole(t, newer, older)
	code, stdout, stderr := run("manifests", "show", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, newerID) || strings.Contains(stdout, candidateManifestID) {
		t.Fatalf("show did not take the catalog's first (newest) candidate %s:\n%s", newerID, stdout)
	}
}
