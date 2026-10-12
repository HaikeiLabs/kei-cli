package harness

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateHookGolden = flag.Bool("update-hook-golden", false, "rewrite the Codex and OpenCode hook goldens under testdata/harness")

// assertGolden compares got with the golden file at path, rewriting it under
// -update-hook-golden.
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateHookGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

// TestCodexHookSpecMatchesGolden pins the Codex hook document: one matcher
// group running kei-proxy hook codex on the tool events and on the session
// lifecycle events (HAI-497).
func TestCodexHookSpecMatchesGolden(t *testing.T) {
	home := t.TempDir()
	spec := codex{}.HookSpec(hermeticEnv(home, nil))
	got := spec.Files[filepath.Join(home, ".codex", "hooks.json")]
	assertGolden(t, filepath.Join("testdata", "harness", "codex.hooks.golden.json"), got)
}

// codexHookGroups returns the raw matcher groups of every event in a
// hooks.json document.
func codexHookGroups(t *testing.T, doc []byte) map[string][]json.RawMessage {
	t.Helper()
	var parsed struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("unmarshal hooks.json: %v\n%s", err, doc)
	}
	return parsed.Hooks
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// A sync over a hooks.json written by kei 0.1.23 (bare Kei PreToolUse and
// PostToolUse entries) beside herdr's SessionStart group and a user's own
// PreToolUse group migrates the Kei entries to matcher groups in place, adds
// the session events, keeps the foreign groups unchanged and first, records
// hooks.json in the ledger, and is idempotent on re-sync.
func TestCodexSyncMigratesLegacyHooksGolden(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(filepath.Join("testdata", "harness", "codex.hooks.legacy.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	syncWith(t, codex{}, env, driftBundle(1, driftPolicies))
	got, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, filepath.Join("testdata", "harness", "codex.hooks.legacy.golden.json"), got)

	before, after := codexHookGroups(t, legacy), codexHookGroups(t, got)
	for _, event := range []string{"SessionStart", "PreToolUse"} {
		if compactJSON(t, after[event][0]) != compactJSON(t, before[event][len(before[event])-1]) {
			t.Fatalf("foreign %s group changed:\nbefore %s\nafter  %s", event, before[event], after[event])
		}
	}
	if !strings.Contains(string(got), `"description": "herdr and user hooks"`) {
		t.Fatalf("top-level description not kept:\n%s", got)
	}

	ledger := readLedger(ledgerPath(env, "codex"))
	if _, ok := ledger.Files[hooksPath]; !ok {
		t.Fatalf("hooks.json not in the ledger: %v", ledger.Files)
	}

	syncWith(t, codex{}, env, driftBundle(1, driftPolicies))
	again, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, got) {
		t.Fatalf("re-sync changed hooks.json:\n%s", again)
	}
}

// Sync warns about each Kei hook event that has no trusted hash in
// config.toml, keyed the way Codex keys hook trust, and keeps the /hooks
// instruction.
func TestCodexSyncReportsUntrustedKeiHooks(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "model = \"gpt-5\"\n\n" +
		"[hooks.state.\"" + hooksPath + ":pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:aaa\"\n\n" +
		"[hooks.state.\"" + hooksPath + ":post_tool_use:0:0\"]\nenabled = true\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	_, result := syncWith(t, codex{}, env, driftBundle(1, driftPolicies))
	want := "Codex skips untrusted hooks, and the Kei hooks for PostToolUse, SessionStart, Stop, SessionEnd are not trusted yet."
	if !slices.Contains(result.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", result.Warnings, want)
	}
	if !slices.ContainsFunc(result.Notes, func(n string) bool { return strings.Contains(n, "run /hooks") }) {
		t.Fatalf("notes = %q, want the /hooks instruction", result.Notes)
	}
}

func TestCodexUntrustedKeiHooks(t *testing.T) {
	const path = "/home/u/.codex/hooks.json"
	hooks := []byte(`{"hooks":{
  "SessionStart":[{"hooks":[{"type":"command","command":"herdr hook"}]},{"hooks":[{"type":"command","command":"kei-proxy hook codex"}]}],
  "PreToolUse":[{"hooks":[{"type":"command","command":"kei-proxy hook codex"}]}],
  "Stop":[{"hooks":[{"type":"command","command":"kei-proxy hook codex"}]}]}}`)
	cases := []struct {
		name, config string
		want         []string
	}{
		{"no state", "", []string{"PreToolUse", "SessionStart", "Stop"}},
		{"codex tables", `[hooks.state."` + path + `:pre_tool_use:0:0"]
trusted_hash = "sha256:a"

[hooks.state."` + path + `:session_start:0:0"]
trusted_hash = "sha256:herdr"
`, []string{"SessionStart", "Stop"}},
		{"inline and dotted entries", `[hooks.state]
"` + path + `:session_start:1:0" = { trusted_hash = "sha256:b" }
'` + path + `:stop:0:0'.trusted_hash = "sha256:c"
"` + path + `:pre_tool_use:0:0".enabled = false
`, []string{"PreToolUse"}},
		{"all trusted", `[hooks.state."` + path + `:pre_tool_use:0:0"]
trusted_hash = "sha256:a"
[hooks.state."` + path + `:session_start:1:0"]
trusted_hash = "sha256:b"
[hooks.state."` + path + `:stop:0:0"]
trusted_hash = "sha256:c"
`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexUntrustedKeiHooks(path, hooks, []byte(tc.config)); !slices.Equal(got, tc.want) {
				t.Fatalf("untrusted = %q, want %q", got, tc.want)
			}
		})
	}
}
