package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
	policyapi "github.com/HaikeiLabs/kei-connector-contracts/policy"
)

// cursorPolicy pins the closed-policy set the runtime's cursor adapter maps
// (kei-connector-runtime#272): single-token shell prefixes, harness tool
// policies and MCP server.tool destinations.
const cursorPolicy = `{"id":"allow-git","src_pattern":"harness:cursor","dst_pattern":"shell:git","effect":"permit","enabled":true},` +
	`{"id":"deny-rm","src_pattern":"harness:cursor","dst_pattern":"shell:rm","effect":"deny","enabled":true},` +
	`{"id":"allow-linear","src_pattern":"harness:cursor","dst_pattern":"mcp:linear.create_issue","effect":"permit","enabled":true},` +
	`{"id":"allow-read","src_pattern":"harness:cursor","dst_pattern":"tool:cursor.read","effect":"permit","enabled":true},` +
	`{"id":"deny-write","src_pattern":"harness:cursor","dst_pattern":"tool:cursor.write","effect":"deny","enabled":true},` +
	`{"id":"allow-webfetch","src_pattern":"harness:cursor","dst_pattern":"tool:cursor.webfetch","effect":"permit","enabled":true}`

func TestCursorKindDetectTargets(t *testing.T) {
	var h cursor
	if h.Kind() != "cursor" {
		t.Fatalf("Kind() = %q, want cursor", h.Kind())
	}
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	if h.Detect(env) {
		t.Fatal("cursor detected without ~/.cursor")
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !h.Detect(env) {
		t.Fatal("cursor not detected with ~/.cursor")
	}
	targets, err := h.Targets(env)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(home, ".cursor", "cli-config.json"),
		filepath.Join(home, ".cursor", "hooks.json"),
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i, target := range targets {
		if target.Remote() {
			t.Fatalf("target %d is remote: %+v", i, target)
		}
		if target.Path != want[i] {
			t.Fatalf("target %d = %s, want %s", i, target.Path, want[i])
		}
	}
	if h.SkillDirs(env) != nil {
		t.Fatalf("SkillDirs = %v, want none: cursor skills are not gateable", h.SkillDirs(env))
	}
}

// TestCursorRenderMapsClosedPolicyToPermissionTokens pins the native-first
// mapping into cli-config.json permissions, matching the runtime adapter:
// single-token shell argv prefixes become Shell(base:*), harness tool
// policies become Read/Write/WebFetch, and MCP server.tool policies become
// Mcp(server:tool).
func TestCursorRenderMapsClosedPolicyToPermissionTokens(t *testing.T) {
	b := driftBundle(3, cursorPolicy)
	r, err := cursor{}.Render(b)
	if err != nil {
		t.Fatal(err)
	}
	wantAllow := []string{"Mcp(linear:create_issue)", "Read(**)", "Shell(git:*)", "WebFetch(*)"}
	wantDeny := []string{"Shell(rm:*)", "Write(**)"}
	if !reflect.DeepEqual(r.Allows, wantAllow) {
		t.Fatalf("allows = %v, want %v", r.Allows, wantAllow)
	}
	if !reflect.DeepEqual(r.Denies, wantDeny) {
		t.Fatalf("denies = %v, want %v", r.Denies, wantDeny)
	}
	if len(r.NotEnforceable) != 0 {
		t.Fatalf("not enforceable = %v, want none", r.NotEnforceable)
	}
}

// TestCursorApplyMergesPermissionsAndInstallsHook syncs the closed policy set
// into an existing user config: user keys and entries survive, Kei entries
// merge, both permission arrays exist, the audit hook lands in hooks.json and
// the sync ledger records what Kei manages.
func TestCursorApplyMergesPermissionsAndInstallsHook(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	cliConfig := filepath.Join(home, ".cursor", "cli-config.json")
	copyFixture(t, "cursor.cli-config.input.json", cliConfig)

	b := driftBundle(3, cursorPolicy)
	r, err := cursor{}.Render(b)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped {
		t.Fatal("cursor sync must not skip")
	}
	if !strings.Contains(out.String(), "Kei-managed entries updated") {
		t.Fatalf("sync should report changes:\n%s", out.String())
	}

	data, err := os.ReadFile(cliConfig)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Model       string `json:"model"`
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if doc.Model != "composer-1" {
		t.Fatalf("user keys not preserved: %s", data)
	}
	for _, want := range []string{"Read(docs/**)", "Mcp(linear:create_issue)", "Read(**)", "Shell(git:*)", "WebFetch(*)"} {
		if !slices.Contains(doc.Permissions.Allow, want) {
			t.Fatalf("allow missing %q: %s", want, data)
		}
	}
	for _, want := range []string{"Shell(rm:*)", "Write(**)"} {
		if !slices.Contains(doc.Permissions.Deny, want) {
			t.Fatalf("deny missing %q: %s", want, data)
		}
	}
	// The user's own Shell(rm:*) deny must not be duplicated by the Kei one.
	if got, want := len(doc.Permissions.Deny), 2; got != want {
		t.Fatalf("deny = %v, want %d distinct entries", doc.Permissions.Deny, want)
	}

	hook, err := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatalf("audit hook not installed: %v", err)
	}
	if !strings.Contains(string(hook), "kei-proxy hook cursor") {
		t.Fatalf("hooks.json missing the Kei hook: %s", hook)
	}
	if strings.Contains(string(hook), "--harness ") {
		t.Fatalf("hook command must not carry a --harness uuid: %s", hook)
	}
	var hookDoc struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hook, &hookDoc); err != nil {
		t.Fatalf("decode hooks.json: %v", err)
	}
	if hookDoc.Version != 1 {
		t.Fatalf("hooks.json version = %d, want 1: %s", hookDoc.Version, hook)
	}
	for _, event := range []string{"preToolUse", "postToolUse", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile"} {
		entries, ok := hookDoc.Hooks[event]
		if !ok || len(entries) == 0 {
			t.Fatalf("hooks.json missing event %s: %s", event, hook)
		}
	}

	if _, err := os.Stat(filepath.Join(home, ".config", "kei", "harness-sync", "cursor.json")); err != nil {
		t.Fatalf("sync ledger missing: %v", err)
	}
}

// TestCursorApplyFailsClosedOnUngateablePolicy: a sync that cannot natively
// gate part of the bundle must fail without writing anything (the
// fail-closed decision for cursor, HAI-882) — the error names each ungateable
// destination.
func TestCursorApplyFailsClosedOnUngateablePolicy(t *testing.T) {
	cases := []struct {
		name string
		dst  string
	}{
		{"skill", "skill:my-skill"},
		{"multi-token shell prefix", "shell:git status"},
		{"mcp server without tool", "mcp:linear"},
		{"unknown cursor tool", "tool:cursor.search"},
		{"path", "path:/etc/**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, nil)
			cliConfig := filepath.Join(home, ".cursor", "cli-config.json")
			if err := os.MkdirAll(filepath.Dir(cliConfig), 0o700); err != nil {
				t.Fatal(err)
			}
			user := []byte("{\"model\":\"composer-1\",\"permissions\":{\"allow\":[\"Read(docs/**)\"],\"deny\":[]}}\n")
			if err := os.WriteFile(cliConfig, user, 0o600); err != nil {
				t.Fatal(err)
			}
			policies := `{"id":"p-ungateable","src_pattern":"harness:cursor","dst_pattern":"` + tc.dst + `","effect":"permit","enabled":true},` +
				`{"id":"p-gateable","src_pattern":"harness:cursor","dst_pattern":"shell:git","effect":"permit","enabled":true}`
			r, err := cursor{}.Render(driftBundle(3, policies))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, applyErr := cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()})
			if applyErr == nil {
				t.Fatalf("cursor sync must fail on ungateable %s", tc.dst)
			}
			if !strings.Contains(applyErr.Error(), tc.dst) {
				t.Fatalf("error must name the ungateable destination: %v", applyErr)
			}
			got, err := os.ReadFile(cliConfig)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, user) {
				t.Fatalf("cli-config.json was modified on a refused sync:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(home, ".cursor", "hooks.json")); !os.IsNotExist(err) {
				t.Fatalf("hooks.json written on a refused sync: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "kei", "harness-sync", "cursor.json")); !os.IsNotExist(err) {
				t.Fatalf("sync ledger written on a refused sync: %v", err)
			}
		})
	}
}

// TestCursorRenderCollectsUngateableDsts: the render itself reports the
// policies it cannot express natively, so a connect can refuse before Apply.
func TestCursorRenderCollectsUngateableDsts(t *testing.T) {
	policies := `{"id":"allow-skill","src_pattern":"harness:cursor","dst_pattern":"skill:my-skill","effect":"permit","enabled":true}`
	r, err := cursor{}.Render(driftBundle(3, policies))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Allows) != 0 {
		t.Fatalf("allows = %v, want none", r.Allows)
	}
	if len(r.NotEnforceable) != 1 || !strings.Contains(r.NotEnforceable[0], "skill:my-skill") {
		t.Fatalf("not enforceable = %v, want the skill dst", r.NotEnforceable)
	}
}

// TestCursorMCPServers: Kei-managed MCP connectors land in
// ~/.cursor/mcp.json, the file the runtime discovers.
func TestCursorMCPServers(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	if got := (cursor{}).mcpConfigPath(env); got != filepath.Join(home, ".cursor", "mcp.json") {
		t.Fatalf("mcpConfigPath = %q", got)
	}
	r := Rendered{
		Kind:          "cursor",
		MCPConnectors: []mcpServer{{Name: "linear", URL: "https://mcp.example/linear", AuthEnvVar: "KEI_MCP_LINEAR_TOKEN"}},
	}
	var out bytes.Buffer
	if _, err := (cursor{}).Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://mcp.example/linear", "KEI_MCP_LINEAR_TOKEN", "Bearer ${KEI_MCP_LINEAR_TOKEN}"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("mcp.json missing %q: %s", want, data)
		}
	}
	// The connector's mcp: policy renders the matching native token.
	policies := `{"id":"allow-linear","src_pattern":"harness:cursor","dst_pattern":"mcp:linear.create_issue","effect":"permit","enabled":true}`
	rendered, err := cursor{}.Render(driftBundle(3, policies))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rendered.Allows, []string{"Mcp(linear:create_issue)"}) {
		t.Fatalf("allows = %v", rendered.Allows)
	}
}

// TestCursorNoHook: --no-hook still syncs permissions but skips the audit
// hook, and hooks.json is only created when a hook is installed.
func TestCursorNoHook(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	cliConfig := filepath.Join(home, ".cursor", "cli-config.json")
	copyFixture(t, "cursor.cli-config.input.json", cliConfig)

	policies := `{"id":"allow-git","src_pattern":"harness:cursor","dst_pattern":"shell:git","effect":"permit","enabled":true}`
	r, err := cursor{}.Render(driftBundle(3, policies))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, NoHook: true, Out: &out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Kei-managed entries updated") {
		t.Fatalf("sync should report changes:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("hooks.json installed with NoHook: %v", err)
	}
	data, err := os.ReadFile(cliConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Shell(git:*)") {
		t.Fatalf("permissions not synced with NoHook: %s", data)
	}
}

// TestCursorDryRunWritesNothing pins that --dry-run leaves every file alone
// for cursor too.
func TestCursorDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	cliConfig := filepath.Join(home, ".cursor", "cli-config.json")
	copyFixture(t, "cursor.cli-config.input.json", cliConfig)

	pre := snapshotFiles(t, home)
	r, err := cursor{}.Render(driftBundle(3, cursorPolicy))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, DryRun: true, Out: &out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	post := snapshotFiles(t, home)
	for path, before := range pre {
		if !bytes.Equal(post[path], before) {
			t.Errorf("dry-run modified %s", path)
		}
	}
	if len(post) != len(pre) {
		t.Errorf("dry-run created files: %d before, %d after", len(pre), len(post))
	}
}

// TestCursorResyncIdempotent: a second sync rewrites nothing and creates no
// backups.
func TestCursorResyncIdempotent(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	cliConfig := filepath.Join(home, ".cursor", "cli-config.json")
	copyFixture(t, "cursor.cli-config.input.json", cliConfig)

	r, err := cursor{}.Render(driftBundle(3, cursorPolicy))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	synced := snapshotFiles(t, home)
	backups := len(backupFiles(t, home))

	var out2 bytes.Buffer
	_, err = cursor{}.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out2, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2.String(), "Kei-managed entries updated") {
		t.Fatalf("re-sync should not report changes:\n%s", out2.String())
	}
	if got := backupFiles(t, home); len(got) != backups {
		t.Fatalf("re-sync wrote backups: %d -> %d", backups, len(got))
	}
	after := snapshotFiles(t, home)
	for path, before := range synced {
		if !bytes.Equal(after[path], before) {
			t.Errorf("re-sync rewrote %s", path)
		}
	}
}

// TestContractsRendererAcceptsCursorKind pins that the shared contracts
// renderer gates kind cursor in (harnessmatch.Kinds, kei-connector-contracts
// v0.13.0): cursor rules render as entries for the real kind, so
// nativePermissionEntries passes s.kind through verbatim instead of masking
// it as custom.
func TestContractsRendererAcceptsCursorKind(t *testing.T) {
	if !harnessmatch.ValidKind("cursor") {
		t.Fatal("harnessmatch.Kinds does not list cursor; kei-connector-contracts needs the v0.13.0 bump")
	}
	rules := []policyapi.Rule{{
		ID:        "allow-git",
		Principal: "*",
		Target:    "shell:git",
		Effect:    policyapi.Effect("permit"),
		Enabled:   true,
	}}
	res := policyapi.Render(rules, "", "", nil, "cursor")
	if len(res.Entries) != 1 {
		t.Fatalf("Render(cursor) entries = %d, want 1 (diagnostics: %v)", len(res.Entries), res.Diagnostics)
	}
}
