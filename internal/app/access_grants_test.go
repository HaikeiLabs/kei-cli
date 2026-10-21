package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testAccessGrantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func runAccessGrant(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAccessGrantCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
}

// --- Create ---

func TestAccessGrantCreate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testAccessGrantID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","principal_type":"user","principal_id":"alice","target_type":"shell","target_id":"git","reason":"need it","expire_time":"2026-10-21T00:00:00Z","created_at":"2026-10-20T22:00:00Z"}`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "create", "--workspace", "Main",
		"--principal", "user:alice", "--target", "shell:git", "--ttl", "2h", "--reason", "need it")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if !strings.Contains(req.Path, "/access-grants") {
		t.Fatalf("path = %s, want access-grants endpoint", req.Path)
	}
	if req.Body["principal"] != "user:alice" {
		t.Errorf("principal = %q, want user:alice", req.Body["principal"])
	}
	if req.Body["target"] != "shell:git" {
		t.Errorf("target = %q, want shell:git", req.Body["target"])
	}
	if req.Body["reason"] != "need it" {
		t.Errorf("reason = %q, want need it", req.Body["reason"])
	}
	expireStr, ok := req.Body["expire_time"].(string)
	if !ok || expireStr == "" {
		t.Fatalf("expire_time missing or not a string: %v", req.Body["expire_time"])
	}
	if _, err := time.Parse(time.RFC3339, expireStr); err != nil {
		t.Fatalf("expire_time = %q is not RFC3339: %v", expireStr, err)
	}
	if !strings.Contains(stdout, testAccessGrantID) {
		t.Fatalf("create output missing grant id: %q", stdout)
	}
}

func TestAccessGrantCreateWithoutReason(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + testAccessGrantID + `"}`))
	})
	code, _, stderr := runAccessGrant(t, server, store, "", "create", "--workspace", "Main",
		"--principal", "user:alice", "--target", "shell:git", "--ttl", "1h")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if _, present := req.Body["reason"]; present {
		t.Fatal("body contains reason, but --reason was not set")
	}
	_ = fake
}

func TestAccessGrantCreateRequiresFields(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	cases := map[string][]string{
		"missing principal": {"--target", "shell:git", "--ttl", "1h"},
		"missing target":    {"--principal", "user:alice", "--ttl", "1h"},
		"missing ttl":       {"--principal", "user:alice", "--target", "shell:git"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			full := append([]string{"create", "--workspace", "Main"}, args...)
			code, _, stderr := runAccessGrant(t, server, store, "", full...)
			if code != 2 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want 2 with an error", code, stderr)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatal("an invalid create reached the console")
	}
}

func TestAccessGrantCreateServerError(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"expire_time exceeds maximum TTL of 168h0m0s"}`))
	})
	code, _, stderr := runAccessGrant(t, server, store, "", "create", "--workspace", "Main",
		"--principal", "user:alice", "--target", "shell:git", "--ttl", "999h")
	if code != 1 || !strings.Contains(stderr, "maximum TTL") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

// --- List ---

func TestAccessGrantList(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
			{"id":"` + testAccessGrantID + `","principal_type":"user","principal_id":"alice","target_type":"shell","target_id":"git","reason":"need it","expire_time":"2026-10-21T00:00:00Z","created_at":"2026-10-20T22:00:00Z"},
			{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","principal_type":"agent","principal_id":"agent-1","target_type":"connector","target_id":"linear","reason":"bug triage","expire_time":"2026-10-22T00:00:00Z","created_at":"2026-10-20T22:00:00Z"}
		]`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || !strings.Contains(req.Path, "/access-grants") {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	for _, want := range []string{testAccessGrantID, "user:alice", "shell:git", "need it", "agent:agent-1", "connector:linear", "bug triage"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestAccessGrantListJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
			{"id":"` + testAccessGrantID + `","principal_type":"user","principal_id":"alice","target_type":"shell","target_id":"git"}
		]`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "list", "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, testAccessGrantID) {
		t.Fatalf("JSON output missing grant id:\n%s", stdout)
	}
	_ = fake
}

func TestAccessGrantListEmpty(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[]`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "No access grants found") {
		t.Fatalf("empty list output = %q", stdout)
	}
	_ = fake
}

// --- Get ---

func TestAccessGrantGet(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testAccessGrantID + `","org_id":"org-1","workspace_id":"` + testWorkspaceID + `","principal_type":"user","principal_id":"alice","target_type":"shell","target_id":"git","reason":"need it","expire_time":"2026-10-21T00:00:00Z","created_at":"2026-10-20T22:00:00Z"}`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "get", testAccessGrantID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || !strings.Contains(req.Path, "/access-grants/"+testAccessGrantID) {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	for _, want := range []string{testAccessGrantID, "user:alice", "shell:git", "need it"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

func TestAccessGrantGetJSON(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testAccessGrantID + `","principal_type":"user","principal_id":"alice","target_type":"shell","target_id":"git"}`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "get", testAccessGrantID, "--workspace", "Main", "--json")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, testAccessGrantID) {
		t.Fatalf("JSON output missing grant id:\n%s", stdout)
	}
	_ = fake
}

func TestAccessGrantGetNotFound(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"reason":"not_found","message":"access grant not found"}`))
	})
	code, _, stderr := runAccessGrant(t, server, store, "", "get", "nonexistent", "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "not found") || !strings.Contains(stderr, "404") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	_ = fake
}

// --- Revoke ---

func TestAccessGrantRevokeRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runAccessGrant(t, server, store, "", "revoke", testAccessGrantID, "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestAccessGrantRevoke(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testAccessGrantID + `","revoked_at":"2026-10-20T23:00:00Z","revoked_by":"org-1"}`))
	})
	code, stdout, stderr := runAccessGrant(t, server, store, "", "revoke", testAccessGrantID, "--workspace", "Main", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || !strings.Contains(req.Path, "/access-grants/"+testAccessGrantID+":revoke") {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(stdout, "revoked") {
		t.Fatalf("revoke output = %q", stdout)
	}
}

// --- Dispatch ---

func TestAccessGrantCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 0}, // help to stdout
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 1}, // no token — workspace auto-picked, fails at credential
		{[]string{"get", "x"}, 1},
		{[]string{"revoke", "x"}, 2},
		{[]string{"create"}, 2},
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runAccessGrantCommand(tt.args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != tt.code {
			t.Errorf("access grant %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}

func TestAccessGrantUsageListedInHelp(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei access grant create") {
		t.Fatalf("usage missing access grant:\n%s", stdout.String())
	}
}

func TestAccessGrantWiredIntoMain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main("test", "access", []string{"grant"}, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (help to stdout); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected help in stdout, got: %s", stdout.String())
	}
}

// --- Auth ---

func TestAccessGrantNotLoggedIn(t *testing.T) {
	_, server, _ := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runAccessGrant(t, server, &memoryCredentialStore{}, "", "list", "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "kei login") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}
