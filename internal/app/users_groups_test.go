package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const (
	testUserID   = "aaaaaaaa-1111-2222-3333-444444444444"
	testGroupID  = "bbbbbbbb-5555-6666-7777-888888888888"
	testMemberID = "cccccccc-9999-0000-1111-222222222222"
)

func testUserJSON(id string) string {
	return `{"name":"users/` + id + `","email":"user@example.com","display_name":"Test User","create_time":"2026-01-01T00:00:00Z","update_time":"2026-01-01T00:00:00Z"}`
}

func testGroupsJSON() string {
	return `[{"id":"` + testGroupID + `","name":"engineers","created_at":"2026-01-01T00:00:00Z","users":[{"sub":"` + testMemberID + `","email":"member@example.com","name":"Member One"}]}]`
}

// newUsersGroupsServer stands up a single fake Kei host (KEI_WEB_URL) used for
// both loading the token and sending requests, with a token stored under it.
func newUsersGroupsServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *memoryCredentialStore) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(respond))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	t.Setenv("KEI_WORKSPACE_ID", "")
	return server, &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
}

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken("org-1") {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestUsersList(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/users" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("page_size"); got != "50" {
			t.Fatalf("page_size = %q", got)
		}
		_, _ = w.Write([]byte(`{"users":[` + testUserJSON(testUserID) + `]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersList(nil, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testUserID) || !strings.Contains(stdout.String(), "user@example.com") {
		t.Fatalf("list output = %s", stdout.String())
	}
}

func TestUsersListJSON(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"users":[` + testUserJSON(testUserID) + `]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersList([]string{"--json"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name": "users/`+testUserID+`"`) {
		t.Fatalf("JSON output = %s", stdout.String())
	}
}

func TestUsersListEmpty(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersList(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No users found.") {
		t.Fatalf("empty list output = %s", stdout.String())
	}
}

func TestUsersGet(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/users/"+testUserID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(testUserJSON(testUserID)))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersGet([]string{testUserID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "user@example.com") {
		t.Fatalf("get output = %s", stdout.String())
	}
}

func TestUsersCreate(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/users" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Email != "new@example.com" || body.DisplayName != "New User" {
			t.Fatalf("create body = %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(testUserJSON(testUserID)))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersCreate([]string{"--email", "new@example.com", "--name", "New User", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("create exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testUserID) {
		t.Fatalf("create output = %s", stdout.String())
	}
}

func TestUsersCreateRequiresYes(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	// stdin is not a terminal, so without --yes the write is skipped.
	if code := runUsersCreate([]string{"--email", "new@example.com"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("create exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if hit {
		t.Fatal("request was made despite missing --yes")
	}
}

func TestUsersUpdate(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/users/"+testUserID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body updateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Email == nil || *body.Email != "changed@example.com" || body.UpdateMask != "email" {
			t.Fatalf("update body = %#v", body)
		}
		if body.DisplayName != nil {
			t.Fatalf("display_name should be omitted: %#v", body)
		}
		_, _ = w.Write([]byte(testUserJSON(testUserID)))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersUpdate([]string{testUserID, "--email", "changed@example.com", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Updated user") {
		t.Fatalf("update output = %s", stdout.String())
	}
}

func TestUsersUpdateBothFields(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body updateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.UpdateMask != "email,display_name" || body.Email == nil || body.DisplayName == nil {
			t.Fatalf("update body = %#v", body)
		}
		_, _ = w.Write([]byte(testUserJSON(testUserID)))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersUpdate([]string{testUserID, "--email", "a@b.com", "--name", "N", "--yes"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 0 {
		t.Fatalf("update exit = %d, stderr=%s", code, stderr.String())
	}
}

func TestUsersResolve(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/users:resolve" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body resolveUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Email != "find@example.com" {
			t.Fatalf("resolve body = %#v", body)
		}
		_, _ = w.Write([]byte(testUserJSON(testUserID)))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersResolve([]string{"--email", "find@example.com"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("resolve exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testUserID) {
		t.Fatalf("resolve output = %s", stdout.String())
	}
}

func TestUsersIdentitiesList(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/users/"+testUserID+"/identities" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"identities":[{"name":"users/` + testUserID + `/identities/1","provider":"github","subject":"gh-123","create_time":"2026-01-01T00:00:00Z"}]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersIdentities([]string{"list", testUserID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("identities list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "github") || !strings.Contains(stdout.String(), "gh-123") {
		t.Fatalf("identities output = %s", stdout.String())
	}
}

func TestUsersRemoteIdentitiesList(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/users/"+testUserID+"/remoteIdentities" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"remoteIdentities":[{"name":"users/` + testUserID + `/remoteIdentities/1","source":"slack","external_id":"S123","status":"linked","create_time":"2026-01-01T00:00:00Z","update_time":"2026-01-01T00:00:00Z"}]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runUsersRemoteIdentities([]string{"list", testUserID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("remote-identities list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "slack") || !strings.Contains(stdout.String(), "S123") {
		t.Fatalf("remote-identities output = %s", stdout.String())
	}
}

func TestUsersErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		reason string
	}{
		{http.StatusUnauthorized, "UNAUTHENTICATED"},
		{http.StatusForbidden, "PERMISSION_DENIED"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusConflict, "ALREADY_EXISTS"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"code":%d,"reason":%q,"message":"boom"}}`, tc.status, tc.reason)))
			}))
			t.Cleanup(server.Close)
			t.Setenv("KEI_WEB_URL", server.URL)
			store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
			var stdout, stderr bytes.Buffer
			code := runUsersGet([]string{testUserID}, &stdout, &stderr, server.Client(), store)
			if code != 1 {
				t.Fatalf("exit = %d, want 1, stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), strconv.Itoa(tc.status)) {
				t.Fatalf("stderr should contain %d: %s", tc.status, stderr.String())
			}
		})
	}
}

func TestUsersRequiresLogin(t *testing.T) {
	store := &memoryCredentialStore{}
	var stdout, stderr bytes.Buffer
	if code := runUsersList(nil, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestGroupsMembersList(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/groups" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("org_id"); got != "org-1" {
			t.Fatalf("org_id = %q", got)
		}
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersList([]string{testGroupID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testMemberID) || !strings.Contains(stdout.String(), "member@example.com") {
		t.Fatalf("list output = %s", stdout.String())
	}
}

func TestGroupsMembersListJSON(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersList([]string{testGroupID, "--json"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"sub": "`+testMemberID+`"`) {
		t.Fatalf("JSON output = %s", stdout.String())
	}
}

func TestGroupsMembersListNotFound(t *testing.T) {
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersList([]string{"dddddddd-0000-1111-2222-333333333333"}, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("list exit = %d, want 1, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestGroupsMembersListWorkspaceID(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("workspace_id"); got != testWorkspaceID {
			t.Fatalf("workspace_id = %q", got)
		}
		_, _ = w.Write([]byte(testGroupsJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersList([]string{testGroupID, "--workspace", testWorkspaceID}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, stderr.String())
	}
}

func TestGroupsMembersAdd(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/groups/"+testGroupID+"/users" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("org_id"); got != "org-1" {
			t.Fatalf("org_id = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["user_id"] != testMemberID {
			t.Fatalf("add body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"status":"added"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersAdd([]string{testGroupID, testMemberID, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("add exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Added user") {
		t.Fatalf("add output = %s", stdout.String())
	}
}

func TestGroupsMembersAddRequiresYes(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersAdd([]string{testGroupID, testMemberID}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("add exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if hit {
		t.Fatal("request was made despite missing --yes")
	}
}

func TestGroupsMembersRemove(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/groups/"+testGroupID+"/users" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("org_id"); got != "org-1" {
			t.Fatalf("org_id = %q", got)
		}
		if got := r.URL.Query().Get("user_sub"); got != testMemberID {
			t.Fatalf("user_sub = %q", got)
		}
		_, _ = w.Write([]byte(`{"status":"removed"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersRemove([]string{testGroupID, testMemberID, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("remove exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed user") {
		t.Fatalf("remove output = %s", stdout.String())
	}
}

func TestGroupsMembersRemoveRequiresYes(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	var stdout, stderr bytes.Buffer
	if code := runGroupsMembersRemove([]string{testGroupID, testMemberID}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("remove exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if hit {
		t.Fatal("request was made despite missing --yes")
	}
}

func TestGroupsMembersErrorMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"reason":"NOT_FOUND","message":"group not found"}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: testToken("org-1")}
	var stdout, stderr bytes.Buffer
	code := runGroupsMembersRemove([]string{testGroupID, testMemberID, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store)
	if code != 1 {
		t.Fatalf("remove exit = %d, want 1, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "404") {
		t.Fatalf("stderr should contain 404: %s", stderr.String())
	}
}

func TestGroupsMembersRequiresGroupUUID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	store := &memoryCredentialStore{}
	if code := runGroupsMembersAdd([]string{"not-a-uuid", testMemberID, "--yes"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("add exit = %d, want 2, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "UUID") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}
