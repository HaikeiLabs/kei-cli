package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestAgentMoveApplyPrintsCredentialReloadStepsForRescopedInstallations(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agents/"+testMoveAgentID+":move" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"agent":{"id":"` + testMoveAgentID + `"},"installations":[{"id":"` + testMoveInstallID + `","display_name":"moved-runtime","action":"move_all"},{"id":"cccccccc-dddd-eeee-ffff-000000000000","display_name":"detached-runtime","action":"detach"}],"blockers":[]}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{testMoveAgentID, "--to-workspace", "Main", "--yes"}
	if code := runAgentMove(args, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		"Next steps:",
		testMoveInstallID + " (moved-runtime)",
		"restart its runtime service",
		"kei-proxy policy sync --force",
		"The running kei-proxy keeps its old workspace until it reloads.",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "detached-runtime") {
		t.Errorf("detached installation received credential reload instructions:\n%s", stderr.String())
	}
}

func TestAgentMoveNextStepsSkipDetach(t *testing.T) {
	payload := []byte(`{"installations":[{"id":"` + testMoveInstallID + `","action":"move_all"},{"id":"cccccccc-dddd-eeee-ffff-000000000000","action":"detach"}]}`)
	var output bytes.Buffer
	printAgentMoveNextSteps(&output, payload)
	if !strings.Contains(output.String(), testMoveInstallID) || strings.Contains(output.String(), "cccccccc-dddd-eeee-ffff-000000000000") {
		t.Fatalf("instructions did not select only re-scoped credentials: %s", output.String())
	}
}
