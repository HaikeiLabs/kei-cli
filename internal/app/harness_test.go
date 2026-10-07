package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
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

func TestHarnessRemoveCleansOnlyItsManagedLocalEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := "77777777-7777-7777-7777-777777777777"
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"permissions":{"allow":["Bash(user:*)","Bash(kei:*)"]},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"user-hook"},{"type":"command","command":"kei-proxy hook claude --harness ` + id + `"}]}]}}`)
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeLedger(harnessLedgerPath(id), syncLedger{HarnessID: id, Kind: "claude_code", Files: map[string]fileLedger{path: {AllowEntries: []string{"Bash(kei:*)"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := removeLocalHarness(id); err != nil {
		t.Fatal(err)
	}
	cleaned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleaned), "Bash(kei:*)") || strings.Contains(string(cleaned), "--harness "+id) || !strings.Contains(string(cleaned), "Bash(user:*)") || !strings.Contains(string(cleaned), "user-hook") {
		t.Fatalf("cleanup damaged config: %s", cleaned)
	}
}

type fakeHarnessRenderer struct {
	allow    string
	hookPath string
}

func (f fakeHarnessRenderer) Render(kind, harnessID string, bundle Bundle, files map[string][]byte) (renderedHarness, error) {
	path := ""
	for key := range files {
		if strings.HasSuffix(key, "settings.json") {
			path = key
		}
	}
	var root map[string]any
	if len(files[path]) > 0 {
		if err := json.Unmarshal(files[path], &root); err != nil {
			return renderedHarness{}, err
		}
	} else {
		root = map[string]any{}
	}
	perms := map[string]any{}
	if old, ok := root["permissions"].(map[string]any); ok {
		perms = old
	}
	allow := []any{"Bash(user command:*)"}
	if old, ok := perms["allow"].([]any); ok {
		allow = append(allow, old...)
	}
	allow = append(allow, f.allow)
	perms["allow"] = allow
	root["permissions"] = perms
	out, _ := json.Marshal(root)
	return renderedHarness{Files: map[string][]byte{path: out}, AllowEntries: map[string][]string{path: []string{f.allow}}}, nil
}
func (f fakeHarnessRenderer) HookSpec(kind, harnessID string) (map[string][]byte, error) {
	// HP-C11: the hook command no longer carries a --harness uuid.
	return map[string][]byte{f.hookPath: []byte(`{"PreToolUse":[{"type":"command","command":"kei-proxy hook claude"}]}`)}, nil
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
	hookPath := filepath.Join(home, ".claude", "hooks.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		if r.URL.Path == "/api/v1/runtime/policy-bundles/current" {
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"bundle-1","bundle_version":2,"policy_revision":4,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[{"agent_id":"` + harnessID + `","kind":"claude_code"}],"policy_set":{"policies":[]}}`))
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "claude_code", false, &stdout, &stderr, server.Client(), fakeHarnessRenderer{allow: "Bash(kei command:*)", hookPath: hookPath}, time.Now)
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
	if got, err := os.ReadFile(hookPath); err != nil || !strings.Contains(string(got), "kei-proxy hook claude") {
		t.Fatalf("hook = %q err=%v", got, err)
	}
	if strings.Contains(string(updated), "--harness") {
		t.Fatalf("hook still carries a --harness uuid: %s", updated)
	}
	backups, err := filepath.Glob(configPath + ".kei-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	// HP-C11: the ledger is keyed by kind, not a registered agent id.
	if _, err := os.Stat(harnessLedgerPath("claude_code")); err != nil {
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", true, &stdout, &stderr, server.Client(), fakeHarnessRenderer{allow: "Bash(git:*)", hookPath: filepath.Join(home, "hook.json")}, time.Now)
	if code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	// HP-C11: the ledger is keyed by kind; a dry-run must not write it.
	if _, err := os.Stat(harnessLedgerPath("claude_code")); !os.IsNotExist(err) {
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
	if err := writeLedger(harnessLedgerPath(id), syncLedger{HarnessID: id, Kind: "claude_code", NotAfter: time.Now().Add(-time.Minute), Files: map[string]fileLedger{path: {AllowEntries: []string{"Bash(kei:*)"}}}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/whoami" {
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v1","bundle_id":"b","bundle_version":1,"policy_revision":1,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `","harnesses":[],"policy_set":{"policies":[]}}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "", false, &stdout, &stderr, server.Client(), nativeHarnessRenderer{}, time.Now)
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

func TestHarnessRendererGoldenConfigs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet := json.RawMessage(`{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","action":"permit","enabled":true},{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","action":"deny","enabled":true},{"id":"p3","src_pattern":"user:someone","dst_pattern":"shell:secret","action":"permit","enabled":true},{"id":"p4","src_pattern":"*","dst_pattern":"shell:*","action":"permit","enabled":true},{"id":"p5","src_pattern":"*","dst_pattern":"skill:my-skill","action":"permit","enabled":true}]}`)
	bundle := Bundle{PolicySet: policySet, Harnesses: []bundleHarness{{AgentID: "77777777-7777-7777-7777-777777777777", Kind: "claude_code"}, {AgentID: "88888888-8888-8888-8888-888888888888", Kind: "codex"}, {AgentID: "99999999-9999-9999-9999-999999999999", Kind: "opencode"}}}
	cases := []struct{ kind, id, input, golden, path string }{
		{"claude_code", bundle.Harnesses[0].AgentID, "claude.settings.input.json", "claude.settings.golden.json", filepath.Join(t.TempDir(), ".claude", "settings.json")},
		{"codex", bundle.Harnesses[1].AgentID, "codex.rules.input", "codex.rules.golden", filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")},
		{"opencode", bundle.Harnesses[2].AgentID, "opencode.input.json", "opencode.golden.json", filepath.Join(t.TempDir(), ".config", "opencode", "opencode.json")},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("testdata", "harness", tc.input))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "harness", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			got, err := (nativeHarnessRenderer{}).Render(tc.kind, tc.id, bundle, map[string][]byte{tc.path: input})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(got.Files[tc.path]), bytes.TrimSpace(golden)) {
				t.Fatalf("rendered config mismatch\ngot:\n%s\nwant:\n%s", got.Files[tc.path], golden)
			}
			if len(got.AllowEntries[tc.path]) == 0 || len(got.DenyEntries[tc.path]) == 0 {
				t.Fatalf("managed allows/denies = %v / %v", got.AllowEntries[tc.path], got.DenyEntries[tc.path])
			}
		})
	}
}

func TestNativePermissionEntriesSharedHarnessFixtures(t *testing.T) {
	// FixturePath is the contracts package's exported fixture helper. The
	// contracts currently ships it from its test source, so resolve the same
	// testdata file through the module directory for this consumer test.
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/HaikeiLabs/kei-connector-contracts").Output()
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(strings.TrimSpace(string(module)), "harnessmatch", "testdata", "cases.v1.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Cases []struct {
			ID       string            `json:"id"`
			Call     harnessmatch.Call `json:"call"`
			Policies []struct {
				ID, Src, Dst, Action string
				Enabled              bool `json:"enabled"`
			} `json:"policies"`
			WantOutcome harnessmatch.Outcome `json:"want_outcome"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	for _, tc := range suite.Cases {
		if tc.WantOutcome != harnessmatch.OutcomePermit && tc.WantOutcome != harnessmatch.OutcomeDeny && tc.WantOutcome != harnessmatch.OutcomeUnmatched && tc.WantOutcome != harnessmatch.OutcomeNotRenderable {
			continue
		}
		if tc.ID != "shell-argv-prefix-permit" && tc.ID != "shell-deny-still-works" && tc.ID != "shell-star-permit-rejected" && tc.ID != "custom-kind-evaluate-via-pdp" {
			continue
		}
		policies := make([]bundlePolicy, 0, len(tc.Policies))
		for _, p := range tc.Policies {
			policies = append(policies, bundlePolicy{ID: p.ID, SrcPattern: p.Src, DstPattern: p.Dst, Action: p.Action, Effect: p.Action, Enabled: p.Enabled})
		}
		h := &bundleHarness{AgentID: tc.Call.AgentID, Kind: tc.Call.Kind}
		allows, denies := nativePermissionEntries(tc.Call.Kind, tc.Call.HarnessID, h, policies)
		switch tc.WantOutcome {
		case harnessmatch.OutcomePermit:
			if len(allows) == 0 {
				t.Errorf("%s: permit produced no allow", tc.ID)
			}
		case harnessmatch.OutcomeDeny:
			if len(denies) == 0 {
				t.Errorf("%s: deny produced no deny", tc.ID)
			}
		default:
			if len(allows)+len(denies) != 0 {
				t.Errorf("%s: %s rendered allow=%v deny=%v", tc.ID, tc.WantOutcome, allows, denies)
			}
		}
	}
}

func TestHarnessNonPromptingModesWarnAndDoNotBlock(t *testing.T) {
	cases := []struct {
		kind, config string
		want         string
	}{
		{"codex", "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n", "codex"},
		{"claude_code", `{"permissions":{"defaultMode":"bypassPermissions"}}`, "claude_code"},
		{"claude_code", `{"permissions":{"defaultMode":"dontAsk"}}`, "claude_code"},
		{"opencode", `{"permission":{"*":"allow"}}`, "opencode"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+tc.config, func(t *testing.T) {
			var out bytes.Buffer
			if !warnNonPromptingMode(tc.kind, []byte(tc.config), &out) || !strings.Contains(out.String(), "ADR-029 OQ1") || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("warning = %q", out.String())
			}
		})
	}
	var out bytes.Buffer
	if warnNonPromptingMode("codex", []byte("approval_policy = \"on-request\"\nsandbox_mode = \"workspace-write\""), &out) || out.Len() != 0 {
		t.Fatalf("unexpected warning for prompting config: %q", out.String())
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "runtime-secret"}, "custom", false, &stdout, &stderr, server.Client(), rendererMustNotRun{}, time.Now)
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
	code := syncHarnessBundle(t.Context(), runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: "t"}, "claude_code", false, &stdout, &stderr, server.Client(), nativeHarnessRenderer{}, time.Now)
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

func TestUnifiedDiff(t *testing.T) {
	before := []string{"a", "b", "c", "d"}
	after := []string{"a", "B", "c", "d", "e"}
	diff := unifiedDiff(before, after, 1)
	if !strings.Contains(diff, "@@") {
		t.Fatalf("missing hunk header: %s", diff)
	}
	if !strings.Contains(diff, "-b\n") || !strings.Contains(diff, "+B\n") {
		t.Fatalf("missing replacement: %s", diff)
	}
	if !strings.Contains(diff, "+e\n") {
		t.Fatalf("missing insertion: %s", diff)
	}
	if got := unifiedDiff(before, before, 3); got != "" {
		t.Fatalf("expected empty diff for equal inputs, got: %s", got)
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

type rendererMustNotRun struct{}

func (rendererMustNotRun) Render(string, string, Bundle, map[string][]byte) (renderedHarness, error) {
	return renderedHarness{}, fmt.Errorf("custom harness renderer must not run")
}
func (rendererMustNotRun) HookSpec(string, string) (map[string][]byte, error) {
	return nil, fmt.Errorf("custom harness hook must not run")
}
