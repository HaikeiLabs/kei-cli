package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// HAI-469: the audit hook is rendered on all seven Claude Code events. The
// five tool-level events carry a "*" matcher; Stop and SessionEnd do not.
var claudeHookEvents = []string{
	"PreToolUse", "PermissionRequest", "PermissionDenied", "PostToolUse",
	"PostToolUseFailure", "Stop", "SessionEnd",
}

// claudeEnv is a hermetic Env whose `claude --version` reports versionOutput.
// An empty versionOutput simulates an install whose version cannot be
// determined. Tests never run the real binary.
func claudeEnv(home, versionOutput string) Env {
	env := hermeticEnv(home, nil)
	env.RunCmd = func(ctx context.Context, name string, args ...string) (string, error) {
		if name != "claude" || len(args) != 1 || args[0] != "--version" {
			return "", fmt.Errorf("unexpected command %q %v", name, args)
		}
		if versionOutput == "" {
			return "", fmt.Errorf("exec: claude: executable file not found in $PATH")
		}
		return versionOutput, nil
	}
	return env
}

// claudeSpecEventsPresent returns the hook event names in a raw HookSpec
// document, where the events are top-level keys (the "hooks" wrapper is added
// only when the spec is merged into settings.json).
func claudeSpecEventsPresent(t *testing.T, spec []byte) map[string]bool {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("unmarshal hook spec: %v\nraw:\n%s", err, spec)
	}
	present := map[string]bool{}
	for event := range doc {
		present[event] = true
	}
	return present
}

// claudeHookEventsPresent returns the hook event names present under "hooks" in
// a merged settings document.
func claudeHookEventsPresent(t *testing.T, settings []byte) map[string]bool {
	t.Helper()
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil {
		t.Fatalf("unmarshal settings: %v\nraw:\n%s", err, settings)
	}
	present := map[string]bool{}
	for event := range doc.Hooks {
		present[event] = true
	}
	return present
}

// TestClaudeHookSpecMatchesGolden pins the exact rendered hook document for a
// Claude Code new enough to recognize all seven events.
func TestClaudeHookSpecMatchesGolden(t *testing.T) {
	home := t.TempDir()
	hs := claudeCode{}.HookSpec(claudeEnv(home, "2.1.296 (Claude Code)\n"))
	if hs == nil {
		t.Fatal("HookSpec returned nil")
	}
	got, ok := hs.Files[filepath.Join(home, ".claude", "settings.json")]
	if !ok {
		t.Fatal("settings.json not in HookSpec files")
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "harness", "claude.hooks.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(golden)) {
		t.Fatalf("hook spec mismatch\ngot:\n%s\nwant:\n%s", got, golden)
	}
}

// claudeHookCommands returns every hook command registered for one event in a
// merged settings document (across all matcher groups).
func claudeHookCommands(t *testing.T, merged []byte, event string) []string {
	t.Helper()
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal merged settings: %v\nraw:\n%s", err, merged)
	}
	groups, ok := doc.Hooks[event]
	if !ok {
		t.Fatalf("hooks.%s missing after merge", event)
	}
	var cmds []string
	for _, g := range groups {
		var grp struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(g, &grp); err != nil {
			t.Fatalf("unmarshal %s group: %v", event, err)
		}
		for _, h := range grp.Hooks {
			cmds = append(cmds, h.Command)
		}
	}
	return cmds
}

// HAI-469: re-syncing over a settings file that already carries the user's own
// hooks on Stop and PostToolUse installs all seven Kei entries, leaves the
// user's hooks untouched, and is idempotent.
func TestClaudeHookSpecPreservesUserHooksAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	env := claudeEnv(home, "2.1.296 (Claude Code)\n")
	spec, ok := claudeCode{}.HookSpec(env).Files[filepath.Join(home, ".claude", "settings.json")]
	if !ok {
		t.Fatal("settings.json not in HookSpec files")
	}
	// A user's own hooks on two of the managed events, plus a stale Kei entry
	// from an older sync that must be replaced, not duplicated.
	oldCfg := []byte(`{
  "permissions": { "allow": ["Bash(user:*)"] },
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "user-stop-hook", "timeout": 3 } ] }
    ],
    "PostToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "user-post-hook", "timeout": 3 } ] },
      { "matcher": "*", "hooks": [ { "type": "command", "command": "kei-proxy hook claude --harness 00000000-0000-0000-0000-000000000000", "timeout": 5 } ] }
    ]
  }
}`)
	merged, err := claudeCode{}.mergeHook(oldCfg, spec)
	if err != nil {
		t.Fatalf("mergeHook: %v", err)
	}
	for _, event := range claudeHookEvents {
		cmds := claudeHookCommands(t, merged, event)
		if !contains(cmds, "kei-proxy hook claude") {
			t.Errorf("hooks.%s missing kei-proxy command, got %v", event, cmds)
		}
	}
	if cmds := claudeHookCommands(t, merged, "Stop"); !contains(cmds, "user-stop-hook") {
		t.Errorf("user Stop hook not preserved, got %v", cmds)
	}
	if cmds := claudeHookCommands(t, merged, "PostToolUse"); !contains(cmds, "user-post-hook") {
		t.Errorf("user PostToolUse hook not preserved, got %v", cmds)
	}
	// The stale --harness form is gone and exactly one kei entry remains per event.
	for _, event := range claudeHookEvents {
		cmds := claudeHookCommands(t, merged, event)
		n := 0
		for _, c := range cmds {
			if c == "kei-proxy hook claude" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("hooks.%s: want exactly 1 kei-proxy entry, got %d (%v)", event, n, cmds)
		}
	}
	// Re-syncing is idempotent: the document is byte-stable.
	again, err := claudeCode{}.mergeHook(merged, spec)
	if err != nil {
		t.Fatalf("re-mergeHook: %v", err)
	}
	if !bytes.Equal(merged, again) {
		t.Errorf("re-sync not idempotent:\nfirst:\n%s\nsecond:\n%s", merged, again)
	}
}

// A Bash entry whose wildcard is not a trailing :* has no argv-prefix form;
// it comes back as a skipped rule instead of being dropped silently.
func TestClaudeImportRulesReportsSkippedEntries(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"permissions":{"allow":["Bash(git:*)","Bash(helm:*values-prod.yaml*)"],"deny":["Bash(rm:*)"]},"autoMode":{"soft_deny":["$defaults","WebFetch"]}}`
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	// An empty path reads the env's default settings file.
	rules, err := claudeCode{}.ImportRules(hermeticEnv(home, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{
		{Name: "claude-git", Dst: "shell:git", Effect: "permit"},
		{Native: "Bash(helm:*values-prod.yaml*)", SkipReason: "wildcard is not a trailing :*"},
		{Name: "claude-rm", Dst: "shell:rm", Effect: "deny"},
		{Name: "claude-webfetch", Dst: "skill:webfetch", Effect: "deny"},
	}
	if len(rules) != len(want) {
		t.Fatalf("rules = %+v", rules)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, rules[i], want[i])
		}
	}
}

// HAI-469: an older Claude Code (below 2.1.119) renders only the legacy two
// events, because an unrecognized event name made it ignore the whole file.
func TestClaudeHookSpecOldVersionRendersLegacyEvents(t *testing.T) {
	home := t.TempDir()
	hs := claudeCode{}.HookSpec(claudeEnv(home, "2.1.100 (Claude Code)\n"))
	if hs == nil {
		t.Fatal("HookSpec returned nil")
	}
	raw, ok := hs.Files[filepath.Join(home, ".claude", "settings.json")]
	if !ok {
		t.Fatal("settings.json not in HookSpec files")
	}
	present := claudeSpecEventsPresent(t, raw)
	if !present["PreToolUse"] || !present["PostToolUse"] {
		t.Fatalf("legacy events missing: %v", present)
	}
	if len(present) != 2 {
		t.Fatalf("want exactly 2 events, got %v", present)
	}
}

// HAI-469: when the Claude Code version cannot be determined, sync falls back
// to the legacy two events rather than risk the whole settings.json.
func TestClaudeHookSpecUnknownVersionRendersLegacyEvents(t *testing.T) {
	home := t.TempDir()
	hs := claudeCode{}.HookSpec(claudeEnv(home, ""))
	if hs == nil {
		t.Fatal("HookSpec returned nil")
	}
	raw, ok := hs.Files[filepath.Join(home, ".claude", "settings.json")]
	if !ok {
		t.Fatal("settings.json not in HookSpec files")
	}
	present := claudeSpecEventsPresent(t, raw)
	if !present["PreToolUse"] || !present["PostToolUse"] {
		t.Fatalf("legacy events missing: %v", present)
	}
	if len(present) != 2 {
		t.Fatalf("want exactly 2 events, got %v", present)
	}
}

// HAI-469: a sync on an older or unknown Claude Code warns and renders only the
// legacy two events; a recent one renders all seven and warns nothing.
func TestClaudeSyncWarnsOnOldOrUnknownVersion(t *testing.T) {
	cases := []struct {
		name          string
		versionOutput string
		wantWarning   bool
	}{
		{"recent", "2.1.296 (Claude Code)\n", false},
		{"old", "2.1.100 (Claude Code)\n", true},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := claudeEnv(home, tc.versionOutput)
			b := Bundle{BundleVersion: 1, PayloadDigest: "sha256:v1", NotAfter: time.Now().Add(time.Hour), PolicySet: json.RawMessage(`{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git","action":"permit","enabled":true}]}`)}
			_, result := syncWith(t, claudeCode{}, env, b)
			found := false
			for _, w := range result.Warnings {
				if strings.Contains(w, "is older than 2.1.119; rendering PreToolUse/PostToolUse only") {
					found = true
				}
			}
			if found != tc.wantWarning {
				t.Fatalf("warning present = %v, want %v; warnings = %v", found, tc.wantWarning, result.Warnings)
			}
			if !tc.wantWarning {
				return
			}
			data, err := os.ReadFile(claudeSettingsPath(env))
			if err != nil {
				t.Fatal(err)
			}
			present := claudeHookEventsPresent(t, data)
			if !present["PreToolUse"] || !present["PostToolUse"] {
				t.Fatalf("legacy events missing: %v", present)
			}
			for _, e := range []string{"PermissionRequest", "PermissionDenied", "PostToolUseFailure", "Stop", "SessionEnd"} {
				if present[e] {
					t.Errorf("unexpected event %q rendered on %s install", e, tc.name)
				}
			}
		})
	}
}
