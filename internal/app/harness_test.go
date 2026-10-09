package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/runtime-installations/"+testHarnessInstallationID+"/harnesses" || req.Auth != "Bearer cli-session-token" {
		t.Fatalf("request = %s %s auth=%q", req.Method, req.Path, req.Auth)
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
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != harnessCollectionPath(testHarnessInstallationID) || calls[0].Auth != "Bearer cli-session-token" {
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
	if code != 2 {
		t.Fatalf("add --help exit=%d, want 2 stderr=%s", code, stderr.String())
	}
	out := stderr.String()
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
	if fake.count() != 2 {
		t.Fatalf("requests=%d, want 2", fake.count())
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", true, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "opencode", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "custom", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "claude_code", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"policy_set":` + string(policySet) + `}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "codex", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now)
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
	for _, want := range []string{"not enforceable in Codex: deny web search", "not enforceable in Codex: allow kei-cli skill", "trust it with /hooks"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "claude-only curl") {
		t.Errorf("stdout lists a policy scoped to another harness:\n%s", out)
	}
	if want := "Codex: 1 policies scoped to another harness (e.g. harness:claude_code); widen src to harness:* to share them"; !strings.Contains(out, want) {
		t.Errorf("stdout missing %q:\n%s", want, out)
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
		code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", dryRun, &stdout, &stderr, server.Client(), registry, env, time.Now)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
		case "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":2,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git","effect":"permit","enabled":true},{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","effect":"deny","enabled":true}]}}`))
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
		if code := syncHarnessBundle(t.Context(), config, "", false, &stdout, &stderr, server.Client(), harness.Default, env, time.Now); code != 0 {
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
