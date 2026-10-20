package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveScopedWorkspaceAcceptsNameAndID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
	}))
	defer server.Close()

	for _, value := range []string{"Main", testWorkspaceID} {
		got, err := resolveScopedWorkspace(context.Background(), server.Client(), server.URL, workspaceTestToken(), workspaceSelection{value: value})
		if err != nil || got != testWorkspaceID {
			t.Errorf("resolveScopedWorkspace(%q) = %q, %v; want %q", value, got, err, testWorkspaceID)
		}
	}
}

func TestResolveScopedWorkspaceListsNamesAndIDsOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
	}))
	defer server.Close()

	for _, sel := range []workspaceSelection{{pickDefault: true}, {value: "missing"}} {
		_, err := resolveScopedWorkspace(context.Background(), server.Client(), server.URL, workspaceTestToken(), sel)
		if err == nil || !strings.Contains(err.Error(), "Main") || !strings.Contains(err.Error(), testWorkspaceID) {
			t.Errorf("resolveScopedWorkspace(%+v) error = %v; want available workspace name and ID", sel, err)
		}
	}
}

func TestUpgradeHintVersions(t *testing.T) {
	old := upgradeHintForVersions("0.2.2", "0.2.3", context.DeadlineExceeded)
	if !strings.Contains(old, "kei upgrade") {
		t.Fatalf("old-version hint = %q, want kei upgrade", old)
	}
	current := upgradeHintForVersions("0.2.3", "0.2.3", context.DeadlineExceeded)
	if current != "" {
		t.Fatalf("current-version hint = %q, want none", current)
	}
}
