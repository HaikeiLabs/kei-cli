package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const updateInstallationID = "00000000-0000-4000-8000-000000000042"

func runBotUpdateTest(t *testing.T, handler http.HandlerFunc, args ...string) (int, string, string, int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer header.") {
			t.Errorf("Authorization = %q, want CLI bearer", r.Header.Get("Authorization"))
		}
		handler(w, r)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"org-1"}`))
	store := &memoryCredentialStore{server: server.URL, token: "header." + payload + ".signature"}
	var stdout, stderr bytes.Buffer
	code := runBotUpdateCommand(args, &stdout, &stderr, server.Client(), store)
	return code, stdout.String(), stderr.String(), calls
}

func TestBotUpdateRenamesInstallation(t *testing.T) {
	code, stdout, stderr, calls := runBotUpdateTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/organizations/org-1/runtime-installations/"+updateInstallationID {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.URL.Query().Get("update_mask"); got != "display_name" {
			t.Errorf("update_mask = %q, want display_name", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["display_name"] != "Renamed runtime" {
			t.Errorf("display_name = %q, want Renamed runtime", body["display_name"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"display_name":"Renamed runtime"}`))
	}, "--installation", updateInstallationID, "--name", " Renamed runtime ")
	if code != 0 || stderr != "" || calls != 1 {
		t.Fatalf("code=%d stderr=%q calls=%d", code, stderr, calls)
	}
	if !strings.Contains(stdout, `Renamed runtime`) {
		t.Fatalf("stdout = %q, want rename confirmation", stdout)
	}
}

func TestBotUpdateReportsConflictAndNotFound(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusConflict, "already uses that name"}, {http.StatusNotFound, "runtime installation not found"}} {
		code, _, stderr, _ := runBotUpdateTest(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}, "--installation", updateInstallationID, "--name", "Renamed")
		if code != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("status=%d code=%d stderr=%q, want error %q", tc.status, code, stderr, tc.want)
		}
	}
}

func TestBotUpdateValidatesArgumentsBeforeRequest(t *testing.T) {
	for _, args := range [][]string{
		{"--installation", "not-a-uuid", "--name", "Renamed"},
		{"--installation", updateInstallationID, "--name", "  "},
	} {
		code, _, stderr, calls := runBotUpdateTest(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request %s", r.URL)
		}, args...)
		if code != 2 || stderr == "" || calls != 0 {
			t.Errorf("args=%v code=%d stderr=%q calls=%d, want validation failure without request", args, code, stderr, calls)
		}
	}
}
