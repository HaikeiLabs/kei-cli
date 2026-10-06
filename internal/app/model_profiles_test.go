package app

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testProfileOrgID = "99999999-9999-9999-9999-999999999999"
	testProfileID    = "11111111-1111-1111-1111-111111111111"
	testAgentID      = "33333333-3333-3333-3333-333333333333"
	testAPIKey       = "sk-test-not-a-real-key-0123456789"
)

const (
	orgProfilesPath = "/api/v1/organizations/" + testProfileOrgID + "/model-profiles"
	wsProfilesPath  = "/api/v1/organizations/" + testProfileOrgID + "/workspaces/" + testWorkspaceID + "/model-profiles"
	agentAssignPath = "/api/v1/organizations/" + testProfileOrgID + "/workspaces/" + testWorkspaceID + "/agents/" + testAgentID + "/model-profiles"
	recipientsPath  = "/api/v1/organizations/" + testProfileOrgID + "/credential-store/recipients"
	credStorePath   = "/api/v1/organizations/" + testProfileOrgID + "/credential-store"
)

const testProfileJSON = `{"profile_id":"` + testProfileID + `","org_id":"` + testProfileOrgID + `","display_name":"primary","endpoint":"https://api.example.com/v1","default_model":"gpt-x","auth_type":"none","status":"active","version":1}`

// newProfileConsole is a fake console whose CLI token carries the org claim.
func newProfileConsole(t *testing.T, respond func(w http.ResponseWriter, r consoleRequest)) (*fakeConsole, *memoryCredentialStore) {
	t.Helper()
	fake, _, store := newFakeConsole(t, respond)
	store.token = testToken(testProfileOrgID)
	return fake, store
}

func (f *fakeConsole) all() []consoleRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]consoleRequest(nil), f.requests...)
}

func (f *fakeConsole) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func assertRequest(t *testing.T, got consoleRequest, method, path string) {
	t.Helper()
	if got.Method != method || got.Path != path {
		t.Fatalf("request = %s %s, want %s %s", got.Method, got.Path, method, path)
	}
	if got.Auth != "Bearer "+testToken(testProfileOrgID) {
		t.Fatalf("Authorization = %q", got.Auth)
	}
}

func TestModelProfilesListOrg(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[` + testProfileJSON + `]`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesList(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodGet, orgProfilesPath)
	if !strings.Contains(stdout.String(), "primary") {
		t.Fatalf("list output = %s", stdout.String())
	}
}

func TestModelProfilesListWorkspaceByName(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/organizations/"+testProfileOrgID+"/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesList([]string{"--workspace", "Main"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[1], http.MethodGet, wsProfilesPath)
}

func TestModelProfilesGetByIDAndName(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == wsProfilesPath {
			_, _ = w.Write([]byte(`[` + testProfileJSON + `,{"profile_id":"44444444-4444-4444-4444-444444444444","display_name":"primary","status":"revoked"}]`))
			return
		}
		_, _ = w.Write([]byte(testProfileJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesGet([]string{testProfileID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodGet, orgProfilesPath+"/"+testProfileID)

	fake.reset()
	if code := runModelProfilesGet([]string{"primary", "--workspace", testWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get by name exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[0], http.MethodGet, wsProfilesPath)
	assertRequest(t, requests[1], http.MethodGet, wsProfilesPath+"/"+testProfileID)
}

func TestModelProfilesCreateNoAuth(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == recipientsPath {
			_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","recipients":[]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testProfileJSON))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--display-name", "primary", "--endpoint", "https://api.example.com/v1", "--auth-type", "none", "--default-model", "gpt-x"}
	if code := runModelProfilesCreate(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[0], http.MethodGet, recipientsPath)
	if requests[0].Query != "" {
		t.Fatalf("org-level recipients query = %q, want none", requests[0].Query)
	}
	assertRequest(t, requests[1], http.MethodPost, orgProfilesPath)
	body := requests[1].Body
	if body["credential_store_installation_id"] != testStoreID || body["auth_type"] != "none" || body["default_model"] != "gpt-x" || body["display_name"] != "primary" {
		t.Fatalf("create body = %#v", body)
	}
	if _, ok := body["recipients"]; ok {
		t.Fatalf("auth_type none must not send recipients: %#v", body)
	}
}

// TestModelProfilesCreateAPIKeyFromStdinIsSealed proves the key is read from
// stdin, sent only as KMP1 envelopes that the runtime key opens, and never
// printed.
func TestModelProfilesCreateAPIKeyFromStdinIsSealed(t *testing.T) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch r.Path {
		case "/api/v1/organizations/" + testProfileOrgID + "/workspaces":
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
		case recipientsPath:
			_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","recipients":[{"runtime_installation_id":"` + testRuntimeID + `","key_id":"k1","public_key":"` + base64.RawStdEncoding.EncodeToString(private.PublicKey().Bytes()) + `"}]}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(testProfileJSON))
		}
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--workspace", "Main", "--agent", testAgentID, "--display-name", "primary", "--endpoint", "https://api.example.com/v1", "--auth-type", "api_key", "--default-model", "gpt-x"}
	if code := runModelProfilesCreate(args, &stdout, &stderr, strings.NewReader(testAPIKey+"\n"), http.DefaultClient, store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[1], http.MethodGet, recipientsPath)
	if requests[1].Query != "agent_id="+testAgentID+"&workspace_id="+testWorkspaceID {
		t.Fatalf("recipients query = %q", requests[1].Query)
	}
	create := requests[2]
	assertRequest(t, create, http.MethodPost, wsProfilesPath)
	for _, r := range requests {
		if strings.Contains(r.Raw, testAPIKey) || strings.Contains(r.Query, testAPIKey) {
			t.Fatalf("plaintext API key sent to %s %s", r.Method, r.Path)
		}
	}
	if strings.Contains(stdout.String(), testAPIKey) || strings.Contains(stderr.String(), testAPIKey) {
		t.Fatal("API key was printed")
	}
	if create.Body["agent_id"] != testAgentID || create.Body["credential_store_installation_id"] != testStoreID {
		t.Fatalf("create body = %#v", create.Body)
	}
	recipients, _ := create.Body["recipients"].([]any)
	if len(recipients) != 1 {
		t.Fatalf("recipients = %#v", create.Body["recipients"])
	}
	recipient := recipients[0].(map[string]any)
	opened, err := openKMP1ForTest(recipient["sealed_payload"].(string), recipient["runtime_installation_id"].(string), recipient["key_id"].(string), private)
	if err != nil || opened != testAPIKey {
		t.Fatalf("runtime key opened %q, %v", opened, err)
	}
}

func TestModelProfilesAPIKeyIsNeverAFlag(t *testing.T) {
	for _, command := range []string{"create", "update"} {
		var stdout, stderr bytes.Buffer
		args := []string{command, "--api-key", testAPIKey}
		if code := runModelProfilesCommand(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Fatalf("%s --api-key exit = %d, want 2", command, code)
		}
		if strings.Contains(stdout.String(), testAPIKey) || strings.Contains(stderr.String(), testAPIKey) {
			t.Fatalf("%s echoed the key: %s", command, stderr.String())
		}
	}
}

func TestModelProfilesCreateAPIKeyRequiresStdin(t *testing.T) {
	var calls atomic.Int32
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) { calls.Add(1) })
	var stdout, stderr bytes.Buffer
	args := []string{"--display-name", "p", "--endpoint", "https://api.example.com/v1", "--auth-type", "api_key", "--default-model", "m"}
	if code := runModelProfilesCreate(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("create exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no API key on stdin") || calls.Load() != 0 {
		t.Fatalf("stderr=%q calls=%d", stderr.String(), calls.Load())
	}
}

func TestModelProfilesCreateNoRecipientsFailsBeforeCreate(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","recipients":[]}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--display-name", "p", "--endpoint", "https://api.example.com/v1", "--auth-type", "api_key", "--default-model", "m"}
	if code := runModelProfilesCreate(args, &stdout, &stderr, strings.NewReader(testAPIKey), http.DefaultClient, store); code != 1 {
		t.Fatalf("create exit = %d, want 1", code)
	}
	assertRequest(t, fake.only(t), http.MethodGet, recipientsPath)
	if !strings.Contains(stderr.String(), "no runtime in this scope") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestModelProfilesCreateValidation(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--display-name", "x"}, "--default-model"},
		{[]string{"--display-name", "x", "--endpoint", "https://e", "--auth-type", "bearer", "--default-model", "m"}, "none or api_key"},
		{[]string{"--display-name", "x", "--endpoint", "https://e", "--auth-type", "none", "--default-model", "m", "--agent", testAgentID}, "require --workspace"},
		{[]string{"--workspace", "Main", "--display-name", "x", "--endpoint", "https://e", "--auth-type", "api_key", "--default-model", "m"}, "--agent or --workspace-default"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		if code := runModelProfilesCreate(tc.args, &stdout, &stderr, strings.NewReader(testAPIKey), http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Fatalf("create %v exit = %d, want 2", tc.args, code)
		}
		if !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("create %v stderr = %q, want %q", tc.args, stderr.String(), tc.want)
		}
	}
}

func TestModelProfilesUpdateSendsOnlyChangedFieldsWithPUT(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testProfileJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesUpdate([]string{testProfileID, "--display-name", "renamed"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPut, orgProfilesPath+"/"+testProfileID)
	if len(got.Body) != 1 || got.Body["display_name"] != "renamed" {
		t.Fatalf("update body = %#v", got.Body)
	}
}

func TestModelProfilesUpdateRotateKeySealsToAssignedAgentRuntimes(t *testing.T) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Path == recipientsPath:
			_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","recipients":[{"runtime_installation_id":"` + testRuntimeID + `","key_id":"k1","public_key":"` + base64.RawStdEncoding.EncodeToString(private.PublicKey().Bytes()) + `"}]}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"profile_id":"` + testProfileID + `","assigned_agent_id":"` + testAgentID + `","auth_type":"api_key"}`))
		default:
			_, _ = w.Write([]byte(testProfileJSON))
		}
	})
	var stdout, stderr bytes.Buffer
	args := []string{testProfileID, "--workspace", testWorkspaceID, "--rotate-key"}
	if code := runModelProfilesUpdate(args, &stdout, &stderr, strings.NewReader(testAPIKey), http.DefaultClient, store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[0], http.MethodGet, wsProfilesPath+"/"+testProfileID)
	assertRequest(t, requests[1], http.MethodGet, recipientsPath)
	if requests[1].Query != "agent_id="+testAgentID+"&workspace_id="+testWorkspaceID {
		t.Fatalf("recipients query = %q", requests[1].Query)
	}
	assertRequest(t, requests[2], http.MethodPut, wsProfilesPath+"/"+testProfileID)
	if strings.Contains(requests[2].Raw, testAPIKey) {
		t.Fatal("plaintext API key sent")
	}
	recipient := requests[2].Body["recipients"].([]any)[0].(map[string]any)
	if opened, err := openKMP1ForTest(recipient["sealed_payload"].(string), testRuntimeID, "k1", private); err != nil || opened != testAPIKey {
		t.Fatalf("runtime key opened %q, %v", opened, err)
	}
}

func TestModelProfilesUpdateRequiresAChange(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesUpdate([]string{testProfileID}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, &memoryCredentialStore{}); code != 2 {
		t.Fatalf("update exit = %d, want 2", code)
	}
}

func TestModelProfilesDelete(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesDelete([]string{testProfileID}, &stdout, &stderr, http.DefaultClient, store); code != 2 || fake.count() != 0 {
		t.Fatalf("delete without --yes exit = %d, requests = %d", code, fake.count())
	}
	if code := runModelProfilesDelete([]string{testProfileID, "--yes", "--workspace", testWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("delete exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodDelete, wsProfilesPath+"/"+testProfileID)
	if !strings.Contains(stdout.String(), "deleted") {
		t.Fatalf("delete output = %q", stdout.String())
	}
}

func TestModelProfilesSetDefaultOrgUsesCustomMethod(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"default_model_profile_id":"` + testProfileID + `"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesSetDefault([]string{testProfileID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("set-default exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPost, orgProfilesPath+":setDefault")
	if len(got.Body) != 1 || got.Body["profile_id"] != testProfileID {
		t.Fatalf("setDefault body = %#v", got.Body)
	}
}

func TestModelProfilesSetDefaultWorkspace(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testProfileJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesSetDefault([]string{testProfileID, "--workspace", testWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("set-default exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPut, wsProfilesPath+"/"+testProfileID)
	if len(got.Body) != 1 || got.Body["is_workspace_default"] != true {
		t.Fatalf("workspace default body = %#v", got.Body)
	}
}

func TestModelProfilesAssignUnassignAssignment(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"workspace_id":"` + testWorkspaceID + `","agent_id":"` + testAgentID + `","model_profile_id":"` + testProfileID + `"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesAssign([]string{testProfileID, "--workspace", testWorkspaceID, "--agent", testAgentID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("assign exit = %d, stderr=%s", code, stderr.String())
	}
	if code := runModelProfilesAssignment([]string{"--workspace", testWorkspaceID, "--agent", testAgentID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("assignment exit = %d, stderr=%s", code, stderr.String())
	}
	if code := runModelProfilesUnassign([]string{"--workspace", testWorkspaceID, "--agent", testAgentID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("unassign exit = %d, stderr=%s", code, stderr.String())
	}
	requests := fake.all()
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	assertRequest(t, requests[0], http.MethodPut, agentAssignPath)
	if len(requests[0].Body) != 1 || requests[0].Body["profile_id"] != testProfileID {
		t.Fatalf("assign body = %#v", requests[0].Body)
	}
	assertRequest(t, requests[1], http.MethodGet, agentAssignPath)
	assertRequest(t, requests[2], http.MethodDelete, agentAssignPath)
	if !strings.Contains(stdout.String(), "assignment removed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestModelProfilesWorkspaceAndAgentRequired(t *testing.T) {
	t.Setenv("KEI_WORKSPACE_ID", "")
	cases := map[string][]string{
		"assign no agent":       {"assign", testProfileID, "--workspace", testWorkspaceID},
		"assign no workspace":   {"assign", testProfileID, "--agent", testAgentID},
		"assign bad agent":      {"assign", testProfileID, "--workspace", testWorkspaceID, "--agent", "bot"},
		"unassign no workspace": {"unassign", "--agent", testAgentID},
		"assignment no agent":   {"assignment", "--workspace", testWorkspaceID},
		"readiness no ws":       {"readiness", testProfileID},
	}
	for name, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := runModelProfilesCommand(args, &stdout, &stderr, nil, http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Errorf("%s exit = %d, want 2 (stderr=%s)", name, code, stderr.String())
		}
	}
}

func TestModelProfilesReadiness(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"profile_id":"` + testProfileID + `","workspace_id":"` + testWorkspaceID + `","state":"ready","runtimes":[]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesReadiness([]string{testProfileID, "--workspace", testWorkspaceID}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("readiness exit = %d, stderr=%s", code, stderr.String())
	}
	assertRequest(t, fake.only(t), http.MethodGet, wsProfilesPath+"/"+testProfileID+"/readiness")
	if !strings.Contains(stdout.String(), `"state":"ready"`) {
		t.Fatalf("readiness output = %s", stdout.String())
	}
}

func TestModelProfilesTestFailsWithoutARequest(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	var stdout, stderr bytes.Buffer
	code := runModelProfilesCommand([]string{"test", "--endpoint", "https://api.example.com/v1", "--model", "m"}, &stdout, &stderr, nil, http.DefaultClient, store)
	if code == 0 {
		t.Fatal("test exit = 0, want non-zero")
	}
	if fake.count() != 0 {
		t.Fatalf("test made %d requests, want none", fake.count())
	}
	if !strings.Contains(stderr.String(), "not available yet") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestModelProfilesWriteForbiddenForNonAdmin surfaces the console's admin
// check on writes as a failure with the server's reason.
func TestModelProfilesWriteForbiddenForNonAdmin(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"reason":"permission_denied","message":"organization admin required"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesSetDefault([]string{testProfileID}, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("set-default exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "organization admin required (HTTP 403)") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestModelProfilesHTMLFallthroughIsAnError covers a console that does not
// yet proxy these /api/v1 routes: the SPA answers with HTML.
func TestModelProfilesHTMLFallthroughIsAnError(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body>app</body></html>`))
	})
	var stdout, stderr bytes.Buffer
	if code := runModelProfilesList(nil, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("list exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "returned HTML") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestModelProfilesCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"unknown"}, 2},
		{[]string{"list"}, 1},           // no token
		{[]string{"get"}, 2},            // no profile
		{[]string{"delete", "x"}, 2},    // no --yes
		{[]string{"readiness", "x"}, 2}, // no workspace
		{[]string{"test"}, 1},           // not available
	}
	t.Setenv("KEI_WORKSPACE_ID", "")
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runModelProfilesCommand(tt.args, &stdout, &stderr, nil, http.DefaultClient, &memoryCredentialStore{}); code != tt.code {
			t.Errorf("model-profiles %v exit = %d, want %d", tt.args, code, tt.code)
		}
	}
}
