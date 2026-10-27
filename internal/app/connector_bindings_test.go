package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testConnectorBindingID      = "44444444-4444-4444-4444-444444444444"
	testConnectorBindingAgentID = "55555555-5555-5555-5555-555555555555"
)

// testConnectorBindingJSON is one connector binding as the catalog's AIP
// handler returns it.
func testConnectorBindingJSON(id string) string {
	return `{"id":"` + id + `","org_id":"org-1","workspace_id":"` + testWorkspaceID +
		`","agent_id":"` + testConnectorBindingAgentID + `","connector_id":"` + testConnectorID +
		`","capabilities":["object.read"],"created_by":"user-1","created_at":"2026-10-26T20:00:00Z","updated_at":"2026-10-26T20:00:00Z"}`
}

func runConnectorBinding(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runConnectorBindingsCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
}

// fakeRequests returns every recorded console request under the fake's lock.
func fakeRequests(fake *fakeConsole) []consoleRequest {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]consoleRequest(nil), fake.requests...)
}

// aipErrorEnvelope is the AIP-193 error payload the catalog writes.
func aipErrorEnvelope(status, message string) string {
	return `{"error":{"code":` + status + `,"status":"` + status + `","message":"` + message +
		`","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"` + status + `","domain":"haikeilabs.com"}]}}`
}

// --- Create ---

func TestConnectorBindingCreate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testConnectorBindingJSON(testConnectorBindingID)))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID, "--connector", testConnectorID, "--capabilities", "object.read,object.write")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if !strings.Contains(req.Path, "/connector-bindings") {
		t.Fatalf("path = %s, want connector-bindings endpoint", req.Path)
	}
	if req.Body["agent_id"] != testConnectorBindingAgentID {
		t.Errorf("agent_id = %q, want %q", req.Body["agent_id"], testConnectorBindingAgentID)
	}
	if req.Body["connector_id"] != testConnectorID {
		t.Errorf("connector_id = %q, want %q", req.Body["connector_id"], testConnectorID)
	}
	caps, ok := req.Body["capabilities"].([]any)
	if !ok || len(caps) != 2 || caps[0] != "object.read" || caps[1] != "object.write" {
		t.Errorf("capabilities = %v, want [object.read object.write]", req.Body["capabilities"])
	}
	if _, present := req.Body["created_by"]; present {
		t.Fatal("body contains created_by, but the CLI caller is already authenticated")
	}
	if !strings.Contains(stdout, testConnectorBindingID) {
		t.Fatalf("create output missing binding id: %q", stdout)
	}
}

func TestConnectorBindingCreateDefaultCapabilities(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testConnectorBindingJSON(testConnectorBindingID)))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID, "--connector", testConnectorID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if _, present := req.Body["capabilities"]; present {
		t.Fatal("body contains capabilities, but --capabilities was not set")
	}
}

func TestConnectorBindingCreateRequiresFields(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string][]string{
		"missing agent":     {"--connector", testConnectorID},
		"missing connector": {"--agent", testConnectorBindingAgentID},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"create", "--workspace", "Main"}, args...)
			code, _, stderr := runConnectorBinding(t, server, store, "", full...)
			if code != 2 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want 2 with an error", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("an invalid create reached the console")
	}
}

func TestConnectorBindingCreateRequiresAgentUUID(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", "not-a-uuid", "--connector", testConnectorID)
	if code != 2 || !strings.Contains(stderr, "UUID") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorBindingCreateResolvesConnectorName(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors" {
			_, _ = w.Write([]byte(`[{"id":"` + testConnectorID + `","name":"Linear"}]`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testConnectorBindingJSON(testConnectorBindingID)))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID, "--connector", "Linear")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	reqs := fakeRequests(fake)
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].Method != http.MethodGet || reqs[0].Path != "/api/v1/data-connectors" {
		t.Fatalf("first request = %s %s, want GET /api/v1/data-connectors", reqs[0].Method, reqs[0].Path)
	}
	if reqs[1].Method != http.MethodPost || reqs[1].Body["connector_id"] != testConnectorID {
		t.Fatalf("second request = %s %s connector_id=%v", reqs[1].Method, reqs[1].Path, reqs[1].Body["connector_id"])
	}
	if !strings.Contains(stdout, testConnectorBindingID) {
		t.Fatalf("create output missing binding id: %q", stdout)
	}
}

func TestConnectorBindingCreateConflict(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(aipErrorEnvelope("409", "a binding for this agent and connector already exists in the workspace")))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID, "--connector", testConnectorID)
	if code != 1 || !strings.Contains(stderr, "already exists") || !strings.Contains(stderr, "409") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorBindingCreateServerError(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(aipErrorEnvelope("400", "connector not found in workspace")))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "create", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID, "--connector", testConnectorID)
	if code != 1 || !strings.Contains(stderr, "connector not found") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// --- List ---

func TestConnectorBindingList(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method != http.MethodGet || !strings.Contains(r.Path, "/connector-bindings") {
			t.Fatalf("request = %s %s", r.Method, r.Path)
		}
		if strings.Contains(r.Query, "page_token=") {
			_, _ = w.Write([]byte(`{"items":[` + testConnectorBindingJSON("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb") + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[` + testConnectorBindingJSON(testConnectorBindingID) + `],"nextPageToken":"tok-2"}`))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	reqs := fakeRequests(fake)
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (pagination)", len(reqs))
	}
	if strings.Contains(reqs[0].Query, "page_token=") {
		t.Fatalf("first request query = %q, want no page_token", reqs[0].Query)
	}
	if !strings.Contains(reqs[0].Query, "page_size=100") {
		t.Fatalf("first request query = %q, want page_size=100", reqs[0].Query)
	}
	if !strings.Contains(reqs[1].Query, "page_token=tok-2") || !strings.Contains(reqs[1].Query, "page_size=100") {
		t.Fatalf("second request query = %q, want page_token=tok-2 and page_size=100", reqs[1].Query)
	}
	for _, want := range []string{testConnectorBindingID, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", testConnectorBindingAgentID, testConnectorID, "object.read"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorBindingListPageSizeAndToken(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[` + testConnectorBindingJSON(testConnectorBindingID) + `]}`))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "list", "--workspace", "Main",
		"--page-size", "1", "--page-token", "tok-start")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if !strings.Contains(req.Query, "page_size=1") || !strings.Contains(req.Query, "page_token=tok-start") {
		t.Fatalf("query = %q, want page_size=1 and page_token=tok-start", req.Query)
	}
}

func TestConnectorBindingListAgentFilter(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "list", "--workspace", "Main",
		"--agent", testConnectorBindingAgentID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if !strings.Contains(req.Query, "agent_id="+testConnectorBindingAgentID) {
		t.Fatalf("query = %q, want agent_id=%s", req.Query, testConnectorBindingAgentID)
	}
}

func TestConnectorBindingListJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[` + testConnectorBindingJSON(testConnectorBindingID) + `]}`))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "list", "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("JSON output did not parse as an array: %v\n%s", err, stdout)
	}
	if len(items) != 1 || items[0]["id"] != testConnectorBindingID {
		t.Fatalf("JSON output = %s", stdout)
	}
	_ = fake
}

func TestConnectorBindingListEmpty(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "No connector bindings found") {
		t.Fatalf("empty list output = %q", stdout)
	}
	_ = fake
}

// --- Get ---

func TestConnectorBindingGet(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testConnectorBindingJSON(testConnectorBindingID)))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "get", testConnectorBindingID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || !strings.Contains(req.Path, "/connector-bindings/"+testConnectorBindingID) {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	for _, want := range []string{testConnectorBindingID, testConnectorBindingAgentID, testConnectorID, "object.read"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorBindingGetJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testConnectorBindingJSON(testConnectorBindingID)))
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "get", testConnectorBindingID, "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var binding map[string]any
	if err := json.Unmarshal([]byte(stdout), &binding); err != nil {
		t.Fatalf("JSON output did not parse: %v\n%s", err, stdout)
	}
	if binding["id"] != testConnectorBindingID {
		t.Fatalf("JSON output = %s", stdout)
	}
	_ = fake
}

func TestConnectorBindingGetNotFound(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(aipErrorEnvelope("404", "connector binding not found")))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "get", "nonexistent", "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "not found") || !strings.Contains(stderr, "404") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// --- Delete ---

func TestConnectorBindingDeleteRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectorBinding(t, server, store, "", "delete", testConnectorBindingID, "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorBindingDelete(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNoContent)
	})
	code, stdout, stderr := runConnectorBinding(t, server, store, "", "delete", testConnectorBindingID, "--workspace", "Main", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodDelete || !strings.Contains(req.Path, "/connector-bindings/"+testConnectorBindingID) {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(stdout, testConnectorBindingID) || !strings.Contains(stdout, "deleted") {
		t.Fatalf("delete output = %q", stdout)
	}
}

func TestConnectorBindingDeleteNotFound(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(aipErrorEnvelope("404", "connector binding not found")))
	})
	code, _, stderr := runConnectorBinding(t, server, store, "", "delete", testConnectorBindingID, "--workspace", "Main", "--yes")
	if code != 1 || !strings.Contains(stderr, "not found") || !strings.Contains(stderr, "404") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// --- Dispatch / help ---

func TestConnectorBindingCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 0}, // help to stdout
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 1}, // no token — workspace auto-picked, fails at credential
		{[]string{"get", "x"}, 1},
		{[]string{"delete", "x"}, 2}, // missing --yes
		{[]string{"create"}, 2},      // missing --agent/--connector
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runConnectorBindingsCommand(tt.args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != tt.code {
			t.Errorf("connector-bindings %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}

func TestConnectorBindingUsageListedInHelp(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei connector-bindings list") {
		t.Fatalf("usage missing connector-bindings:\n%s", stdout.String())
	}
}

func TestConnectorBindingWiredIntoMain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main("test", "connector-bindings", []string{}, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (help to stdout); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected help in stdout, got: %s", stdout.String())
	}
}

func TestConnectorBindingListHelpMentionsADR038(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main("test", "connector-bindings", []string{"create", "-h"}, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (help to stdout); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "never a permission gate") {
		t.Fatalf("create help missing ADR-038 wiring-only note:\n%s", stdout.String())
	}
}

// --- Auth ---

func TestConnectorBindingNotLoggedIn(t *testing.T) {
	_, server, _ := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectorBinding(t, server, &memoryCredentialStore{}, "", "list", "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "kei login") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}
