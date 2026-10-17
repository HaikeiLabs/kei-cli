package app

import (
	"net/http"
	"strings"
	"testing"
)

// approveConsole serves the manifests and answers POST …:approve with
// approveStatus and approveBody.
func approveConsole(t *testing.T, approveStatus int, approveBody string, manifests ...string) (*fakeConsole, func(...string) (int, string, string)) {
	t.Helper()
	members := map[string]string{}
	for _, m := range manifests {
		for _, id := range []string{approvedManifestID, candidateManifestID} {
			if strings.Contains(m, `"id":"`+id+`"`) {
				members[manifestsPath+"/"+id] = m
			}
		}
	}
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":approve"):
			w.WriteHeader(approveStatus)
			_, _ = w.Write([]byte(approveBody))
		case r.Method == http.MethodGet && r.Path == manifestsPath:
			_, _ = w.Write([]byte("[" + strings.Join(manifests, ",") + "]"))
		case r.Method == http.MethodGet && members[r.Path] != "":
			_, _ = w.Write([]byte(members[r.Path]))
		default:
			t.Errorf("unexpected console request %s %s", r.Method, r.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	store.token = workspaceTestToken()
	return fake, func(args ...string) (int, string, string) {
		return runConnectors(t, server, store, "", args...)
	}
}

func approveRequests(fake *fakeConsole) []consoleRequest {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var out []consoleRequest
	for _, r := range fake.requests {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

// TestConnectorsManifestsApprove (ADR-036 §5.4 step 3): approve shows the
// candidate and its changes, then calls the :approve custom method on that
// manifest with no body, and reports the approval.
func TestConnectorsManifestsApprove(t *testing.T) {
	approvedNow := strings.Replace(strings.Replace(candidateManifest, `"status":"candidate"`, `"status":"approved","approved_by":"44444444-4444-4444-4444-444444444444","approved_at":"2026-10-14T10:00:00Z"`, 1), "\n", "", -1)
	fake, run := approveConsole(t, http.StatusOK, approvedNow, approvedManifest, candidateManifest)
	code, stdout, stderr := run("manifests", "approve", testConnectorID, candidateManifestID, "--workspace", "Main", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	posts := approveRequests(fake)
	if len(posts) != 1 {
		t.Fatalf("approve requests = %d, want 1", len(posts))
	}
	post := posts[0]
	if post.Path != manifestsPath+"/"+candidateManifestID+":approve" || post.Query != "workspace_id="+testWorkspaceID || post.Raw != "" {
		t.Fatalf("approve = POST %s?%s body %q", post.Path, post.Query, post.Raw)
	}
	for _, want := range []string{"+ query", "- render", "~ search (description)", "Approved manifest " + candidateManifestID, "2 tools"} {
		if !strings.Contains(stdout+stderr, want) {
			t.Errorf("approve output missing %q:\nstdout=%s\nstderr=%s", want, stdout, stderr)
		}
	}
}

// TestConnectorsToolsApproveAlias: kei connectors tools approve is the same
// command.
func TestConnectorsToolsApproveAlias(t *testing.T) {
	fake, run := approveConsole(t, http.StatusOK, candidateManifest, candidateManifest)
	if code, _, stderr := run("tools", "approve", testConnectorID, candidateManifestID, "--workspace", "Main", "--yes"); code != 0 || len(approveRequests(fake)) != 1 {
		t.Fatalf("exit = %d stderr=%s posts=%d", code, stderr, len(approveRequests(fake)))
	}
}

// TestConnectorsManifestsApproveNeedsConfirmation: without --yes and
// without a terminal to ask on, nothing is approved.
func TestConnectorsManifestsApproveNeedsConfirmation(t *testing.T) {
	fake, run := approveConsole(t, http.StatusOK, candidateManifest, candidateManifest)
	code, stdout, stderr := run("manifests", "approve", testConnectorID, candidateManifestID, "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("exit = %d stderr=%q, want 2 naming --yes", code, stderr)
	}
	if len(approveRequests(fake)) != 0 {
		t.Fatal("approve was sent without confirmation")
	}
	if !strings.Contains(stdout, "query") {
		t.Errorf("the manifest was not shown before asking:\n%s", stdout)
	}
}

// TestConnectorsManifestsApproveRequiresAManifestID: approve never picks a
// manifest for the admin; it is checked before any request.
func TestConnectorsManifestsApproveRequiresAManifestID(t *testing.T) {
	fake, run := approveConsole(t, http.StatusOK, "")
	for _, args := range [][]string{
		{"manifests", "approve", testConnectorID},
		{"manifests", "approve", testConnectorID, "not-a-uuid"},
	} {
		if code, _, stderr := run(append(args, "--workspace", "Main", "--yes")...); code != 2 || stderr == "" {
			t.Errorf("%v: exit = %d stderr=%q, want 2", args, code, stderr)
		}
	}
	if fake.count() != 0 {
		t.Fatalf("requests = %d, want none", fake.count())
	}
}

// TestConnectorsManifestsApproveAlreadyApproved sends nothing.
func TestConnectorsManifestsApproveAlreadyApproved(t *testing.T) {
	fake, run := approveConsole(t, http.StatusOK, "", approvedManifest)
	code, stdout, stderr := run("manifests", "approve", testConnectorID, approvedManifestID, "--workspace", "Main", "--yes")
	if code != 0 || !strings.Contains(stdout, "already approved") || len(approveRequests(fake)) != 0 {
		t.Fatalf("exit = %d stdout=%q stderr=%s posts=%d", code, stdout, stderr, len(approveRequests(fake)))
	}
}

// TestConnectorsManifestsApproveSurfacesCatalogErrors: a superseded manifest
// (409) and a non-admin caller (403) are reported with the server's message.
func TestConnectorsManifestsApproveSurfacesCatalogErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusConflict, `{"error":{"code":409,"status":"ALREADY_EXISTS","message":"manifest is superseded or already approved"}}`, "manifest is superseded or already approved"},
		{http.StatusForbidden, `organization administrator role is required`, "organization administrator role is required"},
	} {
		_, run := approveConsole(t, tc.status, tc.body, candidateManifest)
		code, _, stderr := run("manifests", "approve", testConnectorID, candidateManifestID, "--workspace", "Main", "--yes")
		if code != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("HTTP %d: exit = %d stderr=%q, want 1 containing %q", tc.status, code, stderr, tc.want)
		}
	}
}
