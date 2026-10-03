package app

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func runBotListTest(t *testing.T, handler http.HandlerFunc, args ...string) (int, string, string, int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "" {
			t.Errorf("authorization header not correct")
		}
		handler(w, r)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"org-1"}`))
	store := &memoryCredentialStore{server: server.URL, token: "header." + payload + ".signature"}
	var stdout, stderr bytes.Buffer
	code := runBotListCommand(args, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String(), calls
}

func TestBotListTableAndJSON(t *testing.T) {
	past := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	body := `{"runtime_installations":[{"id":"inst-1","workspace_ids":["ws-a","ws-b"],"platform":"cli","display_name":"Build bot","status":"active","last_heartbeat_at":"` + past + `","created_at":"2026-01-01T00:00:00Z"}],"next_page_token":""}`
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations/org-1/runtime-installations" || r.URL.Query().Get("page_size") != "50" {
			t.Errorf("unexpected request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(body))
	}
	code, stdout, stderr, _ := runBotListTest(t, handler)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, expected := range []string{"ID", "NAME", "PLATFORM", "STATUS", "WORKSPACES", "LAST HEARTBEAT", "inst-1", "Build bot", "ws-a,ws-b", "3h ago"} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("table missing %q: %s", expected, stdout)
		}
	}
	code, stdout, stderr, _ = runBotListTest(t, handler, "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"runtime_installations"`) && !strings.Contains(stdout, `"id": "inst-1"`) {
		// JSON emits just the item array, not the response envelope.
		if !strings.Contains(stdout, `"id": "inst-1"`) {
			t.Fatalf("JSON output unexpected code=%d stderr=%s stdout=%s", code, stderr, stdout)
		}
	}
}

func TestBotListAllPages(t *testing.T) {
	code, stdout, stderr, calls := runBotListTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_token") == "cursor-2" {
			_, _ = w.Write([]byte(`{"runtime_installations":[{"id":"inst-2","platform":"discord","display_name":"Second","status":"active","workspace_ids":[]}],"next_page_token":""}`))
		} else {
			_, _ = w.Write([]byte(`{"runtime_installations":[{"id":"inst-1","platform":"cli","display_name":"First","status":"active","workspace_ids":[]}],"next_page_token":"cursor-2"}`))
		}
	}, "--all")
	if code != 0 || calls != 2 || !strings.Contains(stdout, "inst-2") || strings.Contains(stderr, "More results") {
		t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, calls, stdout, stderr)
	}
}

func TestBotListEmptyAndErrors(t *testing.T) {
	code, stdout, _, _ := runBotListTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"runtime_installations":[],"next_page_token":""}`))
	})
	if code != 0 || !strings.Contains(stdout, "No runtime installations found") {
		t.Fatalf("empty result: code=%d output=%s", code, stdout)
	}
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusUnauthorized, "not logged in; run kei login first"}, {http.StatusForbidden, "you must be an organization admin for your logged-in organization"}} {
		code, _, stderr, _ := runBotListTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) })
		if code != 1 || !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "secret-token") {
			t.Errorf("status=%d code=%d stderr=%s", tc.status, code, stderr)
		}
	}
}

func TestOrganizationIDFromCLIToken(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"org-1"}`))
	orgID, err := organizationIDFromCLIToken("header." + payload + ".signature")
	if err != nil || orgID != "org-1" {
		t.Fatalf("orgID=%q err=%v", orgID, err)
	}
	if _, err := organizationIDFromCLIToken("not-a-token"); err == nil {
		t.Fatal("expected malformed token error")
	}
}
