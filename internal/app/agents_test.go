package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	testMoveAgentID   = "11111111-2222-3333-4444-555555555555"
	testMoveInstallID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
)

func TestAgentMoveDryRunResolvesNameAndSendsPlanRequest(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			if r.URL.Query().Get("org_id") != "org-1" {
				t.Fatalf("agent list query = %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`[{"id":"` + testMoveAgentID + `","name":"Default"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/"+testMoveAgentID+":move":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["destination_workspace"] != testWorkspaceID || body["validate_only"] != true {
				t.Fatalf("move request = %#v", body)
			}
			_, _ = w.Write([]byte(`{"agent":{"id":"` + testMoveAgentID + `"},"groups":[],"users_added":[],"installations":[{"id":"` + testMoveInstallID + `","display_name":"runtime","action":"move_all"}],"connector_bindings":[],"model_profile_assignment":null,"access_grants":[],"blockers":[]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runAgentMove([]string{"Default", "--to-workspace", "Main", "--dry-run", "--json"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"blockers":[]`) {
		t.Fatalf("dry-run output = %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "Next steps") {
		t.Fatalf("dry-run printed apply instructions: %s", stderr.String())
	}
}

func TestAgentMoveApplyPreviewsThenWrites(t *testing.T) {
	calls := 0
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agents/"+testMoveAgentID+":move" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		calls++
		if body["destination_workspace"] != testWorkspaceID {
			t.Fatalf("destination = %#v", body["destination_workspace"])
		}
		if calls == 1 && body["validate_only"] != true {
			t.Fatalf("preview request = %#v", body)
		}
		if calls == 2 && body["validate_only"] != false {
			t.Fatalf("apply request = %#v", body)
		}
		if calls == 2 {
			groups := body["groups"].(map[string]any)
			installations := body["installations"].(map[string]any)
			if groups["Engineering"] != "copy" || installations[testMoveInstallID] != "detach" {
				t.Fatalf("options = groups:%#v installations:%#v", groups, installations)
			}
		}
		_, _ = w.Write([]byte(`{"agent":{"id":"` + testMoveAgentID + `"},"installations":[{"id":"` + testMoveInstallID + `","display_name":"runtime","action":"move_all"}],"blockers":[]}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{testMoveAgentID, "--to-workspace", "Main", "--group", "Engineering=copy", "--installation", testMoveInstallID + "=detach", "--yes", "--json"}
	if code := runAgentMove(args, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if calls != 2 {
		t.Fatalf("POST count = %d, want preview + apply", calls)
	}
	wantJSON := `{"agent":{"id":"` + testMoveAgentID + `"},"installations":[{"id":"` + testMoveInstallID + `","display_name":"runtime","action":"move_all"}],"blockers":[]}` + "\n"
	if stdout.String() != wantJSON {
		t.Fatalf("JSON output changed: got %s want %s", stdout.String(), wantJSON)
	}
	if strings.Contains(stderr.String(), "Next steps") {
		t.Fatalf("JSON mode printed human instructions: %s", stderr.String())
	}
}

func TestAgentMoveBlockersRefuseBeforeWrite(t *testing.T) {
	calls := 0
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		calls++
		_, _ = w.Write([]byte(`{"blockers":[{"reason":"shared_installation","installation_id":"` + testMoveInstallID + `"}]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runAgentMove([]string{testMoveAgentID, "--to-workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 1 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if calls != 1 || !strings.Contains(stderr.String(), "--installation ID=move_all") {
		t.Fatalf("calls=%d stderr=%s", calls, stderr.String())
	}
}

func TestParseAgentMoveOptionsRejectsInvalidAndDuplicateEntries(t *testing.T) {
	for _, tc := range []struct {
		groups, installations []string
	}{
		{groups: []string{"Engineering=unknown"}},
		{groups: []string{"Engineering=copy", "Engineering=move"}},
		{installations: []string{"not-a-uuid=detach"}},
		{installations: []string{testMoveInstallID + "=unknown"}},
	} {
		if _, err := parseAgentMoveOptions(tc.groups, tc.installations); err == nil {
			t.Errorf("parseAgentMoveOptions(%v, %v) succeeded", tc.groups, tc.installations)
		}
	}
}

func TestAgentMoveMissingDestinationProfileExplainsBlocker(t *testing.T) {
	const profileID = "12345678-1234-1234-1234-123456789012"
	var output bytes.Buffer
	printAgentMoveBlockers(&output, []byte(`{"blockers":[{"reason":"model_profile_destination_missing","resource":"`+profileID+`","message":"missing destination profile"}]}`))
	for _, want := range []string{"model_profile_destination_missing", profileID, "equivalent model profile in the destination workspace"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("blocker output %q missing %q", output.String(), want)
		}
	}
}

func TestAgentMoveRequiresConfirmationBeforeApply(t *testing.T) {
	calls := 0
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			calls++
		}
		_, _ = w.Write([]byte(`{"blockers":[]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runAgentMove([]string{testMoveAgentID, "--to-workspace", "Main"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if calls != 1 {
		t.Fatalf("POST count=%d, want preview only", calls)
	}
}
