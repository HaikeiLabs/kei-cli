package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

const testHarnessInstallationID = "66666666-6666-6666-6666-666666666666"

func TestHarnessListUsesConsoleV1ProxyAndBearer(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"harnesses":[{"installation_id":"` + testHarnessInstallationID + `","agent_id":"77777777-7777-7777-7777-777777777777","kind":"custom","agent_name":"My Agent"}]}`))
	})
	var stdoutBuf, stderrBuf bytes.Buffer
	code := runHarnessCommand([]string{"list", "--installation", testHarnessInstallationID}, &stdoutBuf, &stderrBuf, server.Client(), store)
	stdout, stderr := stdoutBuf.String(), stderrBuf.String()
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var req *consoleRequest
	for _, r := range fake.all() {
		if r.Path == "/api/v1/runtime-installations/"+testHarnessInstallationID+"/harnesses" {
			r := r
			req = &r
		}
	}
	if req == nil || req.Method != http.MethodGet || req.Auth != "Bearer "+workspaceTestToken() {
		t.Fatalf("harnesses request = %#v", fake.all())
	}
	if !strings.Contains(stdout, "custom") {
		t.Fatalf("list output missing harness: %q", stdout)
	}
}

func TestHarnessAddAndRemoveUseAIPResource(t *testing.T) {
	var calls []consoleRequest
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		calls = append(calls, r)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"installation_id":"` + testHarnessInstallationID + `","agent_id":"77777777-7777-7777-7777-777777777777","kind":"custom","agent_name":"Kei Assistant"}`))
		}
	})
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--installation", testHarnessInstallationID, "--kind", "custom", "--agent", "77777777-7777-7777-7777-777777777777"}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != harnessCollectionPath(testHarnessInstallationID) || calls[0].Auth != "Bearer "+workspaceTestToken() {
		t.Fatalf("add request = %#v", calls)
	}
	if calls[0].Body["kind"] != "custom" || calls[0].Body["agent_id"] != "77777777-7777-7777-7777-777777777777" {
		t.Fatalf("add body = %#v", calls[0].Body)
	}
	stdout.Reset()
	stderr.Reset()
	code = runHarnessCommand([]string{"remove", "77777777-7777-7777-7777-777777777777", "--installation", testHarnessInstallationID}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("remove exit=%d stderr=%s", code, stderr.String())
	}
	_ = fake
	if len(calls) != 2 || calls[1].Method != http.MethodDelete || calls[1].Path != harnessCollectionPath(testHarnessInstallationID)+"/77777777-7777-7777-7777-777777777777" {
		t.Fatalf("remove request = %#v", calls[1])
	}
}

func TestHarnessAddResolvesDefaultAgent(t *testing.T) {
	const inst = testHarnessInstallationID
	const defaultAgent = "77777777-7777-7777-7777-777777777777"
	var calls []consoleRequest
	fake, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		calls = append(calls, r)
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/runtime-installations/"+inst+"/agents") {
			_, _ = w.Write([]byte(`[{"installation_id":"` + inst + `","agent_id":"` + defaultAgent + `","is_default":true}]`))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"installation_id":"` + inst + `","agent_id":"` + defaultAgent + `","kind":"custom","agent_name":"Kei Assistant"}`))
		}
	})
	_ = fake
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--installation", inst, "--kind", "custom"}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	if len(calls) != 2 {
		t.Fatalf("console calls = %d, want 2: %#v", len(calls), calls)
	}
	if calls[0].Method != http.MethodGet || calls[0].Path != "/api/v1/organizations/org-1/runtime-installations/"+inst+"/agents" {
		t.Fatalf("agents request = %#v", calls[0])
	}
	if calls[1].Method != http.MethodPost || calls[1].Path != harnessCollectionPath(inst) {
		t.Fatalf("add request = %#v", calls[1])
	}
	if calls[1].Body["agent_id"] != defaultAgent {
		t.Fatalf("add body agent_id = %v, want %s", calls[1].Body["agent_id"], defaultAgent)
	}
}

func TestHarnessAddResolvesDefaultInstallationAndAgent(t *testing.T) {
	const inst = testHarnessInstallationID
	const defaultAgent = "77777777-7777-7777-7777-777777777777"
	var calls []consoleRequest
	fake, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		calls = append(calls, r)
		if r.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"` + inst + `","org_id":"org-1","platform":"cli"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/runtime-installations/"+inst+"/agents") {
			_, _ = w.Write([]byte(`[{"installation_id":"` + inst + `","agent_id":"` + defaultAgent + `","is_default":true}]`))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"installation_id":"` + inst + `","agent_id":"` + defaultAgent + `","kind":"custom","agent_name":"Kei Assistant"}`))
		}
	})
	_ = fake
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".config", "kei.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveRuntimeConfig(configPath, runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret", HarnessURL: "http://127.0.0.1:8088"}); err != nil {
		t.Fatal(err)
	}
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--kind", "custom"}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	if len(calls) != 3 {
		t.Fatalf("console calls = %d, want 3: %#v", len(calls), calls)
	}
	if calls[0].Path != "/api/v1/runtime/whoami" {
		t.Fatalf("first call = %#v, want whoami", calls[0])
	}
	if calls[2].Method != http.MethodPost || calls[2].Path != harnessCollectionPath(inst) {
		t.Fatalf("add request = %#v", calls[2])
	}
	if calls[2].Body["agent_id"] != defaultAgent {
		t.Fatalf("add body agent_id = %v, want %s", calls[2].Body["agent_id"], defaultAgent)
	}
}

func TestHarnessAddNoDefaultAgentRequiresFlag(t *testing.T) {
	const inst = testHarnessInstallationID
	fake, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/runtime-installations/"+inst+"/agents") {
			_, _ = w.Write([]byte(`[{"installation_id":"` + inst + `","agent_id":"77777777-7777-7777-7777-777777777777","is_default":false}]`))
			return
		}
	})
	_ = fake
	store := &memoryCredentialStore{server: server.URL, token: testCLIToken}
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--installation", inst, "--kind", "custom"}, &stdout, &stderr, server.Client(), store)
	if code != 1 {
		t.Fatalf("add exit=%d, want 1 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no default agent") {
		t.Fatalf("stderr missing no-default hint: %q", stderr.String())
	}
}

func TestHarnessAddAlreadyExistsPrintsHint(t *testing.T) {
	const inst = testHarnessInstallationID
	const agent = "77777777-7777-7777-7777-777777777777"
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"reason":"already_exists","message":"agent_harness_exists"}`))
		}
	})
	_ = fake
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--installation", inst, "--kind", "custom", "--agent", agent}, &stdout, &stderr, server.Client(), store)
	if code != 1 {
		t.Fatalf("add exit=%d, want 1 stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "don't need kei harness add; run kei harness sync") {
		t.Fatalf("stderr missing harness-sync hint: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "custom (SDK) harness") {
		t.Fatalf("stderr missing custom-harness hint: %q", stderr.String())
	}
}

// 'kei harness add --help' says it is for custom/SDK harnesses and points
// desktop users to 'kei harness sync'.
func TestHarnessAddUsagePointsDesktopToSync(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"add", "--help"}, &stdout, &stderr, nil, nil)
	if code != 0 {
		t.Fatalf("add --help exit=%d, want 0 stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "custom (SDK) harness") {
		t.Fatalf("usage missing custom/SDK note: %q", out)
	}
	if !strings.Contains(out, "kei harness sync") {
		t.Fatalf("usage missing pointer to kei harness sync: %q", out)
	}
}

func TestHarnessListFollowsAIPPageTokens(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if strings.Contains(r.Query, "page_token=next") {
			_, _ = w.Write([]byte(`{"harnesses":[{"installation_id":"` + testHarnessInstallationID + `","agent_id":"88888888-8888-8888-8888-888888888888","kind":"opencode","agent_name":"OpenCode"}],"next_page_token":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"harnesses":[{"installation_id":"` + testHarnessInstallationID + `","agent_id":"77777777-7777-7777-7777-777777777777","kind":"codex","agent_name":"Codex"}],"next_page_token":"next"}`))
	})
	var stdout, stderr bytes.Buffer
	code := runHarnessCommand([]string{"list", "--installation", testHarnessInstallationID}, &stdout, &stderr, server.Client(), store)
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr.String())
	}
	pages := 0
	for _, r := range fake.all() {
		if r.Path == "/api/v1/runtime-installations/"+testHarnessInstallationID+"/harnesses" {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("harnesses pages=%d, want 2: %#v", pages, fake.all())
	}
	if !strings.Contains(stdout.String(), "Codex") || !strings.Contains(stdout.String(), "OpenCode") {
		t.Fatalf("list output=%q", stdout.String())
	}
}

func TestFetchHarnessBundleFallsBackToV1(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		requests++
		if requests == 1 {
			if got := r.Header.Get("Accept"); got != "application/vnd.kei.policy-bundle.v2+json, application/vnd.kei.policy-bundle.v1+json" {
				t.Errorf("initial Accept = %q", got)
			}
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.kei.policy-bundle.v1+json" {
			t.Errorf("fallback Accept = %q", got)
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	bundle, _, err := fetchHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, server.Client())
	if err != nil {
		t.Fatalf("fetchHarnessBundle: %v", err)
	}
	if bundle.Schema != "kei.policy-bundle/v1" || requests != 2 {
		t.Fatalf("bundle schema=%q, bundle requests=%d", bundle.Schema, requests)
	}
}

func TestFetchHarnessBundleUsesV2HarnessPolicies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v2","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"harness_policies":[{"policy_id":"p1","name":"Allow Git","src_pattern":"user:123","dst_pattern":"shell:git","action":"permit"}]}}`))
	}))
	defer server.Close()
	bundle, _, err := fetchHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, server.Client())
	if err != nil {
		t.Fatalf("fetchHarnessBundle: %v", err)
	}
	var set struct {
		Policies []struct {
			ID         string `json:"id"`
			SrcPattern string `json:"src_pattern"`
			DstPattern string `json:"dst_pattern"`
			Effect     string `json:"effect"`
			Enabled    bool   `json:"enabled"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(bundle.PolicySet, &set); err != nil {
		t.Fatal(err)
	}
	if bundle.Schema != "kei.policy-bundle/v2" || len(set.Policies) != 1 || set.Policies[0].ID != "p1" || set.Policies[0].DstPattern != "shell:git" || !set.Policies[0].Enabled {
		t.Fatalf("v2 bundle policy set = %#v (schema %s)", set, bundle.Schema)
	}
}

// TestFetchHarnessBundleV2PolicySetJSONIsStable locks the exact bytes of the
// policy set that fetchHarnessBundle builds from v2 harness_policies: key set,
// key order, the always-true enabled flag, the scope object, and no priority.
func TestFetchHarnessBundleV2PolicySetJSONIsStable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v2","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"harness_policies":[{"policy_id":"p1","name":"Allow Git","priority":100,"src_pattern":"user:123","dst_pattern":"shell:git","action":"permit"},{"policy_id":"p2","name":"Deny rm","src_pattern":"*","dst_pattern":"shell:rm","action":"deny","scope":{"agent_id":"agent-1"}}]}}`))
	}))
	defer server.Close()
	bundle, _, err := fetchHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, server.Client())
	if err != nil {
		t.Fatalf("fetchHarnessBundle: %v", err)
	}
	const want = `{"policies":[` +
		`{"dst_pattern":"shell:git","effect":"permit","enabled":true,"id":"p1","name":"Allow Git","scope":{"agent_id":null},"src_pattern":"user:123"},` +
		`{"dst_pattern":"shell:rm","effect":"deny","enabled":true,"id":"p2","name":"Deny rm","scope":{"agent_id":"agent-1"},"src_pattern":"*"}]}`
	if string(bundle.PolicySet) != want {
		t.Fatalf("policy set JSON changed:\n got %s\nwant %s", bundle.PolicySet, want)
	}
}

// TestHarnessSetPolicyMarshalsLegacyKeys pins the named wire struct to the
// keys the former map[string]any carried.
func TestHarnessSetPolicyMarshalsLegacyKeys(t *testing.T) {
	agent := "agent-1"
	got, err := json.Marshal(harnessSetPolicy{ID: "p", Name: "n", SrcPattern: "s", DstPattern: "d", Effect: "deny", Enabled: true, Scope: harnessPolicyScope{AgentID: &agent}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"dst_pattern":"d","effect":"deny","enabled":true,"id":"p","name":"n","scope":{"agent_id":"agent-1"},"src_pattern":"s"}`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestFetchHarnessBundleCarriesV2Subject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v2","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"subject":{"user_id":"usr-fixture-dev","email":"dev@example.com","groups":["release_eng"]},"policy_set":{"harness_policies":[{"policy_id":"p1","name":"Group deploy","src_pattern":"group:release_eng","dst_pattern":"skill:deploy","action":"deny"}]}}`))
	}))
	defer server.Close()
	bundle, _, err := fetchHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, server.Client())
	if err != nil {
		t.Fatalf("fetchHarnessBundle: %v", err)
	}
	if bundle.Subject == nil || bundle.Subject.UserID != "usr-fixture-dev" || bundle.Subject.Email != "dev@example.com" || len(bundle.Subject.Groups) != 1 || bundle.Subject.Groups[0] != "release_eng" {
		t.Fatalf("v2 subject = %#v", bundle.Subject)
	}
}

func TestHarnessSyncV2PriorityPreventsSkippedDenyWithholding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v2","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"subject":{"user_id":"usr-dev","email":"dev@example.com","groups":["dev"]},"policy_set":{"harness_policies":[{"policy_id":"p-permit","name":"Allow git status","priority":100,"src_pattern":"group:dev","dst_pattern":"shell:git status","action":"permit"},{"policy_id":"p-deny","name":"Block git","priority":10,"src_pattern":"group:ops","dst_pattern":"shell:git","action":"deny"}]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// D-1: a matching deny always wins, regardless of priority. The skipped
	// deny ("Block git" for group:ops) overlaps the permit ("Allow git status"
	// for group:dev), so the permit is withheld even though it has higher
	// priority (100 > 10).
	if strings.Contains(string(updated), "Bash(git status:*)") {
		t.Fatalf("v2 bundle with overlapping skipped deny must withhold the permit (D-1: deny wins); config:\n%s", updated)
	}
}

func TestHarnessSyncFetchesBundleWritesManagedConfigWithBackupAndHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	harnessID := "77777777-7777-7777-7777-777777777777"
	configPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"permissions":{"allow":["Bash(user command:*)"]}}`)
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		if r.URL.Path == "/api/v1/runtime/policy-bundles/current" {
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"bundle-1","bundle_version":2,"policy_revision":4,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[{"agent_id":"` + harnessID + `","kind":"claude_code"}],"policy_set":{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:kei command","effect":"permit","enabled":true}]}}`))
			return
		}
		if r.Method == http.MethodPatch {
			if r.URL.Query().Get("update_mask") == "installed_skills" {
				return
			}
			if r.URL.Path != "/api/v1/runtime/harnesses/"+harnessID || r.URL.Query().Get("update_mask") != "last_synced_at,last_synced_bundle_version,last_synced_digest" {
				t.Errorf("sync report request = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode report body: %v", err)
			} else if body["last_synced_bundle_version"] != float64(2) || !strings.HasPrefix(body["last_synced_digest"].(string), "sha256:") {
				t.Errorf("sync report body = %#v", body)
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Bash(user command:*)", "Bash(kei command:*)"} {
		if !strings.Contains(string(updated), want) {
			t.Errorf("config missing %q: %s", want, updated)
		}
	}
	// The Claude Code audit hook is merged into settings.json.
	if !strings.Contains(string(updated), "kei-proxy hook claude") {
		t.Fatalf("hook missing: %s", updated)
	}
	if strings.Contains(string(updated), "--harness") {
		t.Fatalf("hook still carries a --harness uuid: %s", updated)
	}
	backups, err := filepath.Glob(configPath + ".kei-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	// HP-C11: the ledger is keyed by kind, not a registered agent id.
	if _, err := os.Stat(testLedgerPath(home, "claude_code")); err != nil {
		t.Fatalf("ledger missing: %v", err)
	}
}

func TestHarnessSyncDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// HP-C11: sync renders for locally-detected kinds; create ~/.claude so
	// claude_code is detected and the dry-run path is actually exercised.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", true, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	// HP-C11: the ledger is keyed by kind; a dry-run must not write it.
	if _, err := os.Stat(testLedgerPath(home, "claude_code")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote ledger: %v", err)
	}
}

func TestHarnessExpiredBundleRemovesOnlyManagedAllows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := "77777777-7777-7777-7777-777777777777"
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["Bash(user:*)","Bash(kei:*)"],"deny":["Bash(rm:*)"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeTestLedger(t, home, id, map[string]any{"harness_id": id, "kind": "claude_code", "not_after": time.Now().Add(-time.Minute), "files": map[string]any{path: map[string]any{"allow_entries": []string{"Bash(kei:*)"}}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	updated, _ := os.ReadFile(path)
	if strings.Contains(string(updated), "Bash(kei:*)") || !strings.Contains(string(updated), "Bash(user:*)") || !strings.Contains(string(updated), "Bash(rm:*)") {
		t.Fatalf("expired config = %s", updated)
	}
	if !strings.Contains(stdout.String(), "renew") {
		t.Fatalf("expiry output missing renew prompt: %q", stdout.String())
	}
}

// HAI-810: after syncing a desktop harness (claude_code, codex, opencode),
// print a note that it does not appear in 'kei harness list' and suggest
// running kei doctor then restarting the agent session.
func TestHarnessSyncDesktopPrintsHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("OPENCODE_CONFIG", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	server, _ := harnessSyncReportServer(t, testHarnessInstallationID, 0)
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Desktop harnesses are configured locally") {
		t.Fatalf("stdout missing desktop hint: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "kei doctor") {
		t.Fatalf("stdout missing kei doctor hint: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "restart your agent session") {
		t.Fatalf("stdout missing restart hint: %q", stdout.String())
	}
}

// When no OpenCode config exists, sync creates opencode.json in the resolved
// XDG-aware config dir with the Kei-managed permission block.
func TestHarnessSyncOpencodeCreatesConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(t.TempDir())
	// The global opencode dir exists (so the kind is detected) but no config file.
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "opencode", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	cfgBytes, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if string(cfgBytes) != "{}\n" {
		t.Fatalf("config content: got %q, want %q", string(cfgBytes), "{}\n")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "plugins", "kei-audit.js")); err != nil {
		t.Fatalf("audit plugin not installed: %v", err)
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Fatalf("missing creation hint: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "scoped to another harness") {
		t.Fatalf("printed a scope hint with no scoped policies: %q", stdout.String())
	}
}

func TestHarnessSyncCustomSkipsFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var reported bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
		case r.URL.Path == "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"bundle-1","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"policies":[]}}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/runtime/harnesses/"):
			reported = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	// HP-C11: custom is only synced when explicitly selected with --harness.
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "custom", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%q", code, stderr.String())
	}
	if reported {
		t.Fatalf("custom sync must not report (no registered harness to report to)")
	}
	if !strings.Contains(stdout.String(), "custom") || !strings.Contains(stdout.String(), "no native allowlist") {
		t.Fatalf("custom sync note = %q", stdout.String())
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("custom sync wrote files: entries=%v err=%v", entries, err)
	}
}

// TestSyncReplacesStaleHarnessHook verifies HP-C11: a sync replaces an existing
// "kei-proxy hook ... --harness <uuid>" entry (left over from the old
// registered-harness model) with the new kind-only hook, instead of appending.
func TestSyncReplacesStaleHarnessHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	staleID := "05c31c54-0000-0000-0000-000000000000"
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	stale := `{"permissions":{"allow":["Bash(user:*)"]},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"kei-proxy hook claude --harness ` + staleID + `","timeout":5}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	updated, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), staleID) || strings.Contains(string(updated), "--harness") {
		t.Fatalf("stale --harness hook not replaced: %s", updated)
	}
	if !strings.Contains(string(updated), "kei-proxy hook claude") {
		t.Fatalf("new kind-only hook missing: %s", updated)
	}
}

func TestHarnessResponseErrorParsesReasonMessage(t *testing.T) {
	got := harnessResponseError([]byte(`{"reason":"invalid_argument","message":"effect must be permit or deny"}`), 400, nil)
	if got != "invalid_argument: effect must be permit or deny" {
		t.Fatalf("got %q", got)
	}
}

func TestHarnessResponseErrorParsesDetailSubject(t *testing.T) {
	got := harnessResponseError([]byte(`{"detail":"bundle_version_conflict","subject":"workspace/installation"}`), 409, nil)
	if !strings.Contains(got, "bundle_version_conflict") || !strings.Contains(got, "try 'kei harness sync' again") {
		t.Fatalf("got %q", got)
	}
}

func TestHarnessResponseErrorDetailWithoutSubject(t *testing.T) {
	got := harnessResponseError([]byte(`{"detail":"runtime_not_bound"}`), 409, nil)
	if !strings.Contains(got, "run 'kei bot bind'") {
		t.Fatalf("got %q", got)
	}
}

func TestHarnessResponseErrorDetailUnknown(t *testing.T) {
	got := harnessResponseError([]byte(`{"detail":"unknown_code","subject":"some info"}`), 409, nil)
	if got != "unknown_code: some info" {
		t.Fatalf("got %q", got)
	}
}

func TestHarnessResponseErrorDelegatesToResponseError(t *testing.T) {
	got := harnessResponseError([]byte(`not json`), 500, nil)
	if got != "not json" {
		t.Fatalf("got %q", got)
	}
}

func TestBundleFetchHintKnownCodes(t *testing.T) {
	tests := []struct {
		detail string
		want   string
	}{
		{"bundle_version_conflict", "try 'kei harness sync' again"},
		{"runtime_not_bound", "run 'kei bot bind'"},
		{"token_revoked", "rotate the runtime credential"},
		{"installation_not_found", "check that the runtime installation ID"},
		{"unknown_code", ""},
	}
	for _, tc := range tests {
		got := bundleFetchHint(tc.detail)
		if !strings.Contains(got, tc.want) && tc.want != "" {
			t.Errorf("bundleFetchHint(%q) = %q, want contains %q", tc.detail, got, tc.want)
		}
		if got == "" && tc.want != "" {
			t.Errorf("bundleFetchHint(%q) = empty, want %q", tc.detail, tc.want)
		}
	}
}

func TestHarnessSyncCodexWritesKeiRulesAndReportsUnenforceable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rulesDir := filepath.Join(home, ".codex", "rules")
	if err := os.MkdirAll(rulesDir, 0700); err != nil {
		t.Fatal(err)
	}
	userRules := []byte("prefix_rule(pattern=[\"ls\"], decision=\"allow\")\n")
	if err := os.WriteFile(filepath.Join(rulesDir, "default.rules"), userRules, 0600); err != nil {
		t.Fatal(err)
	}
	keiRules := filepath.Join(rulesDir, "kei.rules")
	if err := os.WriteFile(keiRules, []byte("# managed by kei harness sync; edits are overwritten\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policySet, err := os.ReadFile(filepath.Join(harnessTestdata, "codex-owner-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"connectors":[{"slug":"linear","kind":"mcp","mcp_url":"https://mcp.example.com"}],"policy_set":` + string(policySet) + `}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "codex", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	golden, err := os.ReadFile(filepath.Join(harnessTestdata, "codex-owner.rules.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(keiRules); err != nil || !bytes.Equal(got, golden) {
		t.Fatalf("kei.rules = %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(rulesDir, "default.rules")); err != nil || !bytes.Equal(got, userRules) {
		t.Fatalf("default.rules changed: %q err=%v", got, err)
	}
	if backups, err := filepath.Glob(keiRules + ".kei-backup-*"); err != nil || len(backups) != 1 {
		t.Fatalf("kei.rules backups=%v err=%v", backups, err)
	}
	if hooks, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json")); err != nil || !strings.Contains(string(hooks), "kei-proxy hook codex") {
		t.Fatalf("hooks.json = %q err=%v", hooks, err)
	}
	out := stdout.String()
	var nextSteps []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Next steps") || strings.HasPrefix(line, "  1.") || strings.HasPrefix(line, "  2.") || strings.HasPrefix(line, "  3.") {
			nextSteps = append(nextSteps, line)
		}
	}
	wantNextSteps := []string{
		"Next steps (Codex):",
		"  1. Share claude-only curl (shell:curl) with this harness: kei policies update p-claude-only --workspace workspace-id --src-pattern harness:*",
		"  2. Open Codex and run /hooks to trust the Kei hooks.",
		"  3. If no manifest exists, ask Codex: \"List tools from the linear MCP server\"; this first tools/list call creates a candidate manifest for admin review.",
	}
	if strings.Join(nextSteps, "\n") != strings.Join(wantNextSteps, "\n") {
		t.Errorf("next steps snapshot = %q, want %q", nextSteps, wantNextSteps)
	}
	for _, want := range []string{"not enforceable in Codex: deny web search", "not enforceable in Codex: allow kei-cli skill", "/hooks"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "claude-only curl (shell:curl)") {
		t.Errorf("stdout does not identify the policy scoped to another harness:\n%s", out)
	}
	if want := "Codex: 1 policies scoped to another harness (e.g. harness:claude_code)"; !strings.Contains(out, want) {
		t.Errorf("stdout missing %q:\n%s", want, out)
	}
	for _, want := range []string{
		"kei policies update p-claude-only --workspace workspace-id --src-pattern harness:*",
		"Open Codex and run /hooks to trust the Kei hooks",
		"List tools from the linear MCP server",
		"creates a candidate manifest",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing next step %q:\n%s", want, out)
		}
	}
}

func TestHarnessSyncGuidanceNamesMissingPoliciesAndNextSteps(t *testing.T) {
	policySet := `{"policies":[{"id":"p-linear","name":"Allow Linear search","src_pattern":"harness:claude_code","dst_pattern":"tool:linear.search","effect":"permit","enabled":true}]}`
	guidance := harnessSyncGuidance("codex", "workspace-id", json.RawMessage(policySet), []harness.BundleConnector{{Slug: "linear", Kind: "mcp", MCPURL: "https://mcp.example.com"}})
	got := strings.Join(guidance, "\n")
	for _, want := range []string{
		"Allow Linear search (tool:linear.search)",
		"harness:*",
		"kei policies update p-linear --workspace workspace-id --src-pattern harness:*",
		"Open Codex and run /hooks",
		"List tools from the linear MCP server",
		"creates a candidate manifest",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q:\n%s", want, got)
		}
	}
}

// harnessTestdata holds the renderer golden files, which live with the
// renderers in internal/harness.
var harnessTestdata = filepath.Join("..", "harness", "testdata", "harness")

// testLedgerPath is where harness sync keeps its ledger for id under home.
func testLedgerPath(home, id string) string {
	return filepath.Join(home, ".config", "kei", "harness-sync", id+".json")
}

// writeTestLedger writes a harness sync ledger as a previous sync would.
func writeTestLedger(t *testing.T, home, id string, ledger map[string]any) {
	t.Helper()
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	path := testLedgerPath(home, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// remoteHarness stands in for a harness whose native store is a remote admin
// API (OpenWebUI, HAI-430): its target is a URL and Apply records what sync
// handed it instead of writing files.
type remoteHarness struct {
	url     string
	applied *[]harness.ApplyOpts
	rules   *[]harness.Rendered
}

func (remoteHarness) Kind() string                           { return "remote_test" }
func (remoteHarness) Detect(harness.Env) bool                { return true }
func (remoteHarness) HookSpec(harness.Env) *harness.HookSpec { return nil }
func (remoteHarness) SkillDirs(harness.Env) []string         { return nil }
func (remoteHarness) ImportRules(harness.Env, string) ([]harness.Rule, error) {
	return nil, harness.ErrNoNativeStore
}
func (h remoteHarness) Targets(harness.Env) ([]harness.Target, error) {
	return []harness.Target{{URL: h.url}}, nil
}
func (h remoteHarness) Render(b harness.Bundle) (harness.Rendered, error) {
	return harness.Rendered{Kind: h.Kind(), Allows: []string{"git"}, BundleVersion: b.BundleVersion, BundleDigest: b.PayloadDigest}, nil
}
func (h remoteHarness) Apply(_ context.Context, r harness.Rendered, opts harness.ApplyOpts) (harness.Result, error) {
	*h.applied = append(*h.applied, opts)
	*h.rules = append(*h.rules, r)
	targets, _ := h.Targets(opts.Env)
	fmt.Fprintf(opts.Out, "would PATCH %s\n", targets[0].URL)
	return harness.Result{Notes: []string{"remote: applied"}, Warnings: []string{"remote: warning"}}, nil
}

// A harness whose target is a remote API plugs into sync through the same
// interface: it is detected, rendered, applied (dry run honoured), its sync is
// reported, and its notes and warnings are printed.
func TestHarnessSyncDrivesRemoteHarness(t *testing.T) {
	const agentID = "77777777-7777-7777-7777-777777777777"
	var reported int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
		case r.URL.Path == "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":3,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[{"agent_id":"` + agentID + `","kind":"remote_test"}],"policy_set":{"policies":[]}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/runtime/harnesses/"+agentID:
			if r.URL.Query().Get("update_mask") == "installed_skills" {
				return
			}
			reported++
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	var applied []harness.ApplyOpts
	var rendered []harness.Rendered
	registry := harness.NewRegistry(remoteHarness{url: "https://openwebui.example/api/v1/configs", applied: &applied, rules: &rendered})
	env := harness.Env{Home: t.TempDir()}
	for _, dryRun := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", dryRun, false, &stdout, &stderr, server.Client(), registry, env, time.Now)
		if code != 0 {
			t.Fatalf("dryRun=%v exit=%d stderr=%s", dryRun, code, stderr.String())
		}
		if want := "would PATCH https://openwebui.example/api/v1/configs\nremote: applied\n"; stdout.String() != want {
			t.Fatalf("dryRun=%v stdout = %q, want %q", dryRun, stdout.String(), want)
		}
		if stderr.String() != "remote: warning\n" {
			t.Fatalf("dryRun=%v stderr = %q", dryRun, stderr.String())
		}
	}
	if len(applied) != 2 || !applied[0].DryRun || applied[1].DryRun || applied[1].Env.Home != env.Home {
		t.Fatalf("apply opts = %+v", applied)
	}
	if rendered[1].BundleVersion != 3 || !strings.HasPrefix(rendered[1].BundleDigest, "sha256:") {
		t.Fatalf("rendered = %+v", rendered[1])
	}
	if reported != 1 {
		t.Fatalf("sync reported %d times, want once (not on dry run)", reported)
	}
}

// HAI-416: a stale writer (an open Claude Code session saving settings it
// loaded before the last sync) drops the Kei-written permissions. --check
// reports it as JSON and exits 1; sync re-applies the entries and says so;
// --check then exits 0. Neither output carries the runtime token.
func TestHarnessSyncCheckDetectsAndSyncHealsStaleWriter(t *testing.T) {
	home := t.TempDir()
	env := harness.Env{Home: home, Getenv: func(string) string { return "" }}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["Bash(user:*)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "runtime-secret-hai416"
	// The bundle is the same on every request: not_after is fixed once, so a
	// request on the far side of a second boundary does not see a changed
	// payload under the same bundle_version.
	notAfter := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
		case "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":2,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + notAfter + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git","effect":"permit","enabled":true},{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","effect":"deny","enabled":true}]}}`))
		case "/api/v1/runtime/harnesses:reportSync":
			if r.Method != http.MethodPost {
				t.Errorf("desktop sync report method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+token {
				t.Errorf("desktop sync report authorization = %q", got)
			}
			var report struct {
				Kind                    string `json:"kind"`
				LastSyncedAt            string `json:"last_synced_at"`
				LastSyncedBundleVersion int64  `json:"last_synced_bundle_version"`
				LastSyncedDigest        string `json:"last_synced_digest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Errorf("decode desktop sync report: %v", err)
			}
			if report.Kind != "claude_code" || report.LastSyncedBundleVersion != 2 || report.LastSyncedAt == "" || !strings.HasPrefix(report.LastSyncedDigest, "sha256:") || len(report.LastSyncedDigest) != 71 {
				t.Errorf("desktop sync report = %+v", report)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	config := runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: token}
	check := func(jsonOutput bool) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := checkHarnessDrift(t.Context(), config, "", jsonOutput, &stdout, &stderr, server.Client(), harness.Default, env, time.Now)
		if strings.Contains(stdout.String()+stderr.String(), token) {
			t.Fatalf("check output leaks the runtime token")
		}
		if code == 2 {
			t.Fatalf("check error: %s", stderr.String())
		}
		return code, stdout.String()
	}
	sync := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := syncHarnessBundle(t.Context(), config, "", false, false, &stdout, &stderr, server.Client(), harness.Default, env, time.Now); code != 0 {
			t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), token) {
			t.Fatalf("sync output leaks the runtime token")
		}
		return stdout.String()
	}

	sync()
	if code, out := check(false); code != 0 || !strings.Contains(out, "claude_code: in sync with bundle 2") {
		t.Fatalf("check after sync exit=%d out=%s", code, out)
	}
	// The stale writer keeps the hook and the user's answers, drops Kei's.
	stale := `{"permissions":{"allow":["Bash(user:*)","Bash(npm test:*)"]},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"kei-proxy hook claude","timeout":5}]}]}}`
	if err := os.WriteFile(settings, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := check(true)
	if code != 1 {
		t.Fatalf("check after stale write exit=%d out=%s", code, out)
	}
	var report harness.DriftReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report %q: %v", out, err)
	}
	if report.Schema != harness.DriftSchema || !report.Drift || report.Bundle.Version != 2 || !strings.HasPrefix(report.Bundle.Digest, "sha256:") || len(report.Harnesses) != 1 {
		t.Fatalf("report = %+v", report)
	}
	got := report.Harnesses[0]
	want := []harness.DriftEntry{{File: settings, Effect: "allow", Entry: "Bash(git:*)"}, {File: settings, Effect: "deny", Entry: "Bash(rm:*)"}}
	if got.Kind != "claude_code" || got.Status != harness.DriftDetected || len(got.Missing) != 2 || got.Missing[0] != want[0] || got.Missing[1] != want[1] {
		t.Fatalf("claude_code drift = %+v", got)
	}

	if out := sync(); !strings.Contains(out, "Claude Code: re-applied 2 entries removed since last sync (likely an open Claude session saved stale settings") {
		t.Fatalf("sync stdout = %s", out)
	}
	healed, _ := os.ReadFile(settings)
	for _, want := range []string{"Bash(git:*)", "Bash(rm:*)", "Bash(user:*)", "Bash(npm test:*)", "kei-proxy hook claude"} {
		if !strings.Contains(string(healed), want) {
			t.Errorf("healed settings missing %q:\n%s", want, healed)
		}
	}
	if code, out := check(true); code != 0 || !strings.Contains(out, `"drift":false`) {
		t.Fatalf("check after heal exit=%d out=%s", code, out)
	}
}

func TestHarnessSyncCheckFlagValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"--json"}, {"--check", "--dry-run"}} {
		var stdout, stderr bytes.Buffer
		if code := runHarnessSync(args, &stdout, &stderr, http.DefaultClient, nil, harness.Default); code != 2 {
			t.Errorf("%v exit=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestHarnessSyncCheckFetchErrorExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := checkHarnessDrift(t.Context(), runtimeConfig{ControlPlaneURL: "http://example.com", RuntimeToken: "t"}, "", true, &stdout, &stderr, http.DefaultClient, harness.Default, harness.Env{Home: t.TempDir()}, time.Now)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "require HTTPS") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// RED: --manifest flag parsing and validation (Step 1 of HAI-683).
func TestHarnessSyncManifestValidation(t *testing.T) {
	manifestDir := t.TempDir()
	writeFile := func(name, content string) string {
		p := filepath.Join(manifestDir, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	validFile := writeFile("valid.json", `{"schema":"kei.tool-manifest/v2","tools":[{"name":"x","route":"x"}]}`)
	badJSON := writeFile("bad.json", `not json`)
	emptyTools := writeFile("empty.json", `{"schema":"kei.tool-manifest/v2","tools":[]}`)
	nonexistent := filepath.Join(manifestDir, "nope.json")

	tests := []struct {
		name string
		args []string
		code int
		want string // substring expected in stderr
	}{
		{
			name: "manifest without --harness",
			args: []string{"--manifest", validFile},
			code: 2,
			want: "--manifest requires --harness custom",
		},
		{
			name: "manifest with non-custom harness",
			args: []string{"--manifest", validFile, "--harness", "claude_code"},
			code: 2,
			want: "--manifest requires --harness custom",
		},
		{
			name: "manifest file not found",
			args: []string{"--manifest", nonexistent, "--harness", "custom"},
			code: 2,
			want: "",
		},
		{
			name: "manifest invalid JSON",
			args: []string{"--manifest", badJSON, "--harness", "custom"},
			code: 2,
			want: "invalid JSON",
		},
		{
			name: "manifest empty tools",
			args: []string{"--manifest", emptyTools, "--harness", "custom"},
			code: 2,
			want: "empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runHarnessSync(tc.args, &stdout, &stderr, nil, nil, harness.Default)
			if code != tc.code {
				t.Fatalf("exit=%d, want %d; stderr=%q", code, tc.code, stderr.String())
			}
			if tc.want != "" && !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q should contain %q", stderr.String(), tc.want)
			}
		})
	}
}

// RED: --dry-run with --manifest lists tools and makes no HTTP request (Step 2 of HAI-683).
// Before implementation, this exits 1 because config loading runs and fails.
// After implementation, it exits 0, lists tool names on stdout, and the httptest
// server is never hit.
func TestHarnessSyncManifestDryRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("dry-run must not make HTTP request, got: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	home := t.TempDir()
	manifestFile := filepath.Join(home, "tools.json")
	if err := os.WriteFile(manifestFile, []byte(`{"schema":"kei.tool-manifest/v2","tools":[{"name":"tool-a","route":"read"},{"name":"tool-b","route":"write"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Point --config at a non-existent file so the test fails fast if
	// the dry-run path doesn't intercept before config loading.
	nowhereConfig := filepath.Join(home, "nonexistent-config.yaml")

	var stdout, stderr bytes.Buffer
	code := runHarnessSync([]string{
		"--manifest", manifestFile,
		"--harness", "custom",
		"--dry-run",
		"--config", nowhereConfig,
	}, &stdout, &stderr, server.Client(), nil, harness.Default)

	out := stdout.String()
	errStr := stderr.String()
	if code != 0 {
		t.Fatalf("dry-run exit=%d; stderr=%q; want exit=0 with tool names on stdout", code, errStr)
	}
	if !strings.Contains(out, "tool-a") || !strings.Contains(out, "tool-b") {
		t.Fatalf("stdout should list tools; got=%q", out)
	}
	if !strings.Contains(out, "Would sync") {
		t.Fatalf("stdout missing summary; got=%q", out)
	}
	if errStr != "" {
		t.Fatalf("stderr should be empty; got=%q", errStr)
	}
}

// RED/Step 3: --manifest with valid config PUTs to /api/v1/runtime/tools and
// exits 0 on success. Before implementation, the code falls through to
// syncHarnessBundle which hits the wrong endpoint.
func TestHarnessSyncManifestSync(t *testing.T) {
	const token = "my-secret-runtime-token"
	manifestContent := `{"schema":"kei.tool-manifest/v2","tools":[{"name":"tool-a","route":"read"},{"name":"tool-b","route":"write"}]}`

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/api/v1/runtime/tools" {
			t.Fatalf("path = %s, want /api/v1/runtime/tools", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q, want Bearer %s", got, token)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	home := t.TempDir()
	configPath := filepath.Join(home, "config")
	if err := saveRuntimeConfig(configPath, runtimeConfig{
		ControlPlaneURL: server.URL,
		RuntimeToken:    token,
		HarnessURL:      "http://127.0.0.1:8088",
	}); err != nil {
		t.Fatal(err)
	}

	manifestFile := filepath.Join(home, "tools.json")
	if err := os.WriteFile(manifestFile, []byte(manifestContent), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runHarnessSync([]string{
		"--manifest", manifestFile,
		"--harness", "custom",
		"--config", configPath,
	}, &stdout, &stderr, server.Client(), nil, harness.Default)

	out := stdout.String()
	errStr := stderr.String()
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errStr)
	}
	if !strings.Contains(out, "Synced") {
		t.Fatalf("stdout missing success msg; got=%q", out)
	}
	if strings.Contains(out, token) || strings.Contains(errStr, token) {
		t.Fatalf("token leaked in output\nstdout=%q\nstderr=%q", out, errStr)
	}

	// Verify the PUT body matches the manifest.
	var sent struct {
		Schema   string           `json:"schema"`
		SyncMode string           `json:"sync_mode"`
		Tools    []toolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Schema != "kei.tool-manifest/v2" {
		t.Errorf("schema = %q, want kei.tool-manifest/v2", sent.Schema)
	}
	if sent.SyncMode != "full" {
		t.Errorf("sync_mode = %q, want full", sent.SyncMode)
	}
	if len(sent.Tools) != 2 || sent.Tools[0].Name != "tool-a" || sent.Tools[1].Name != "tool-b" {
		t.Errorf("tools = %+v", sent.Tools)
	}
}

// RED/Step 3: on 400 the server's response body is surfaced to stderr and the
// token never leaks.
func TestHarnessSyncManifestSync400(t *testing.T) {
	const token = "my-secret-runtime-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`tool validation failed: name is required`))
	}))
	defer server.Close()

	home := t.TempDir()
	configPath := filepath.Join(home, "config")
	if err := saveRuntimeConfig(configPath, runtimeConfig{
		ControlPlaneURL: server.URL,
		RuntimeToken:    token,
		HarnessURL:      "http://127.0.0.1:8088",
	}); err != nil {
		t.Fatal(err)
	}

	manifestFile := filepath.Join(home, "tools.json")
	if err := os.WriteFile(manifestFile, []byte(`{"schema":"kei.tool-manifest/v2","tools":[{"name":"","route":"x"}]}`), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runHarnessSync([]string{
		"--manifest", manifestFile,
		"--harness", "custom",
		"--config", configPath,
	}, &stdout, &stderr, server.Client(), nil, harness.Default)

	out := stdout.String()
	errStr := stderr.String()
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errStr)
	}
	if !strings.Contains(errStr, "tool validation failed") {
		t.Fatalf("stderr=%q should contain server message", errStr)
	}
	if out != "" {
		t.Fatalf("stdout should be empty on error; got=%q", out)
	}
	if strings.Contains(out, token) || strings.Contains(errStr, token) {
		t.Fatalf("token leaked in output\nstdout=%q\nstderr=%q", out, errStr)
	}
}

// reportedHarnessPatch is one PATCH the sync sent to the control plane.
type reportedHarnessPatch struct {
	mask string
	body map[string]any
}

// harnessSyncReportServer serves whoami and a v1 bundle that registers one
// claude_code harness, and records every PATCH the sync sends. A non-zero
// installedSkillsStatus is returned for the installed_skills PATCH.
func harnessSyncReportServer(t *testing.T, harnessID string, installedSkillsStatus int) (*httptest.Server, *[]reportedHarnessPatch) {
	t.Helper()
	patches := &[]reportedHarnessPatch{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		case "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"bundle-1","bundle_version":2,"policy_revision":4,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[{"agent_id":"` + harnessID + `","kind":"claude_code"}],"policy_set":{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:kei command","effect":"permit","enabled":true}]}}`))
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode %s body: %v", r.URL.Path, err)
			}
			mask := r.URL.Query().Get("update_mask")
			*patches = append(*patches, reportedHarnessPatch{mask: mask, body: body})
			if mask == "installed_skills" && installedSkillsStatus != 0 {
				w.WriteHeader(installedSkillsStatus)
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, patches
}

func installedSkillsPatch(t *testing.T, patches []reportedHarnessPatch) reportedHarnessPatch {
	t.Helper()
	for _, patch := range patches {
		if patch.mask == "installed_skills" {
			return patch
		}
	}
	t.Fatalf("sync sent no installed_skills PATCH: %#v", patches)
	return reportedHarnessPatch{}
}

func syncHarnessForSkillsReport(t *testing.T, server *httptest.Server) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
	return code, stdout.String(), stderr.String()
}

// HAI-641: only HaikeiLabs/skills lock entries are reported, matched
// case-insensitively, and the body carries names and versions only.
func TestHarnessSyncReportsInstalledHaikeiSkills(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei":        haikeiLockEntry("0123456789abcdef0123"),
		"kei-cli":       {"source": "haikeilabs/SKILLS", "skillFolderHash": "fedcba9876543210abcd"},
		"skill-creator": {"source": "anthropics/skills", "skillFolderHash": "deadbeefdeadbeef"},
	})

	server, patches := harnessSyncReportServer(t, testHarnessInstallationID, 0)
	code, _, stderr := syncHarnessForSkillsReport(t, server)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr)
	}
	patch := installedSkillsPatch(t, *patches)
	if len(patch.body) != 1 {
		t.Fatalf("installed skills body has keys %v, want only installed_skills", patch.body)
	}
	entries, ok := patch.body["installed_skills"].([]any)
	if !ok {
		t.Fatalf("installed_skills = %#v, want a list", patch.body["installed_skills"])
	}
	if len(entries) != 2 {
		t.Fatalf("reported %d skills, want the 2 Haikei ones: %#v", len(entries), entries)
	}
	got := map[string]string{}
	for _, item := range entries {
		entry, ok := item.(map[string]any)
		if !ok || len(entry) != 2 {
			t.Fatalf("skill entry = %#v, want only name and version", item)
		}
		name, _ := entry["name"].(string)
		version, _ := entry["version"].(string)
		got[name] = version
	}
	want := map[string]string{"haikei": "0123456789ab", "kei-cli": "fedcba987654"}
	if len(got) != len(want) {
		t.Fatalf("reported skills = %#v, want %#v", got, want)
	}
	for name, version := range want {
		if got[name] != version {
			t.Errorf("%s version = %q, want %q", name, got[name], version)
		}
	}
	if _, ok := got["skill-creator"]; ok {
		t.Fatalf("reported a non-Haikei skill: %#v", got)
	}
	// Deterministic order keeps the report diffable.
	if first, _ := entries[0].(map[string]any); first["name"] != "haikei" {
		t.Errorf("entries are not sorted by name: %#v", entries)
	}
}

// HAI-641: no lock file means the sync reports an empty set, not an omission.
func TestHarnessSyncReportsNoInstalledSkillsWithoutLockFile(t *testing.T) {
	skillsHome(t)
	t.Chdir(t.TempDir())

	server, patches := harnessSyncReportServer(t, testHarnessInstallationID, 0)
	code, _, stderr := syncHarnessForSkillsReport(t, server)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr)
	}
	patch := installedSkillsPatch(t, *patches)
	entries, ok := patch.body["installed_skills"].([]any)
	if !ok || len(entries) != 0 {
		t.Fatalf("installed_skills = %#v, want an empty list", patch.body["installed_skills"])
	}
}

// HAI-641: the catalog side may not accept the field yet. A 404 only warns;
// the sync still exits 0.
func TestHarnessSyncWarnsWhenInstalledSkillsReportFails(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei": haikeiLockEntry("0123456789abcdef0123"),
	})

	server, _ := harnessSyncReportServer(t, testHarnessInstallationID, http.StatusNotFound)
	code, _, stderr := syncHarnessForSkillsReport(t, server)
	if code != 0 {
		t.Fatalf("sync exit=%d, want 0 when only the skills report fails; stderr=%s", code, stderr)
	}
	warnings := 0
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if strings.Contains(line, "installed skills") {
			warnings++
			if !strings.Contains(line, "HTTP 404") {
				t.Errorf("warning does not name the failure: %q", line)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("want exactly one warning line, got %d; stderr=%s", warnings, stderr)
	}
}
