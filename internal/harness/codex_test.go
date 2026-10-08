package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// HAI-425: an owner-like bundle (tool:/skill: denies, Claude-scoped and
// human-sourced entries, shell: entries) renders the shell: policies that
// apply to Codex as prefix rules and names every other Codex policy as not
// enforceable instead of silently writing nothing.
func TestCodexRenderOwnerBundleGolden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet, err := os.ReadFile(filepath.Join("testdata", "harness", "codex-owner-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "harness", "codex-owner.rules.golden"))
	if err != nil {
		t.Fatal(err)
	}
	rulesPath := filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")
	got, err := renderNative("codex", "", Bundle{PolicySet: policySet}, map[string][]byte{rulesPath: nil})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Files[rulesPath], golden) {
		t.Fatalf("rendered kei.rules mismatch\ngot:\n%s\nwant:\n%s", got.Files[rulesPath], golden)
	}
	wantUnenforceable := []string{"allow kei-cli skill", "deny codex shell tool", "deny every shell command", "deny prod-deploy skill", "deny web fetch", "deny web search"}
	if strings.Join(got.Unenforceable, "|") != strings.Join(wantUnenforceable, "|") {
		t.Fatalf("unenforceable = %q, want %q", got.Unenforceable, wantUnenforceable)
	}
}

func TestCodexRenderDenyIsForbidden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet := json.RawMessage(`{"policies":[{"id":"p1","name":"deny curl","src_pattern":"*","dst_pattern":"shell:curl","effect":"deny","enabled":true}]}`)
	rulesPath := filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")
	got, err := renderNative("codex", "", Bundle{PolicySet: policySet}, map[string][]byte{rulesPath: nil})
	if err != nil {
		t.Fatal(err)
	}
	want := "# managed by kei harness sync; edits are overwritten\nprefix_rule(pattern=[\"curl\"], decision=\"forbidden\", justification=\"kei policy\")\n"
	if string(got.Files[rulesPath]) != want {
		t.Fatalf("kei.rules = %q, want %q", got.Files[rulesPath], want)
	}
	if strings.Join(got.DenyEntries[rulesPath], "|") != `["curl"]` || len(got.AllowEntries[rulesPath]) != 0 {
		t.Fatalf("managed allows/denies = %v / %v", got.AllowEntries[rulesPath], got.DenyEntries[rulesPath])
	}
}

func TestSplitArgvQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"git status", []string{"git", "status"}},
		{"  git   status  ", []string{"git", "status"}},
		{`git commit -m "wip fix"`, []string{"git", "commit", "-m", "wip fix"}},
		{`echo 'a "b" c'`, []string{"echo", `a "b" c`}},
		{`echo "say \"hi\""`, []string{"echo", `say "hi"`}},
		{`echo a\ b`, []string{"echo", "a b"}},
		{`echo pre"fix"'ed'`, []string{"echo", "prefixed"}},
		{`echo ""`, []string{"echo", ""}},
	}
	for _, tc := range cases {
		got, err := splitArgv(tc.in)
		if err != nil {
			t.Errorf("splitArgv(%q) error: %v", tc.in, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") || len(got) != len(tc.want) {
			t.Errorf("splitArgv(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{`echo "open`, `echo 'open`} {
		if _, err := splitArgv(bad); err == nil {
			t.Errorf("splitArgv(%q) accepted an unterminated quote", bad)
		}
	}
	if _, ok := codexArgv(`shell:echo "open`); ok {
		t.Error("codexArgv accepted an unterminated quote")
	}
}

// An empty bundle still renders the Kei-owned kei.rules, holding only the
// managed header, so a policy removal clears previously managed rules.
func TestCodexRenderEmptyBundleWritesEmptyManagedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rulesPath := filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")
	prior := []byte("# managed by kei harness sync; edits are overwritten\nprefix_rule(pattern=[\"rm\"], decision=\"forbidden\", justification=\"kei policy\")\n")
	got, err := renderNative("codex", "", Bundle{PolicySet: json.RawMessage(`{"policies":[]}`)}, map[string][]byte{rulesPath: prior})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# managed by kei harness sync; edits are overwritten\n"; string(got.Files[rulesPath]) != want {
		t.Fatalf("kei.rules = %q, want %q", got.Files[rulesPath], want)
	}
	if len(got.Unenforceable) != 0 {
		t.Fatalf("unenforceable = %q", got.Unenforceable)
	}
}

// Policies whose src names a different harness are counted per harness so
// sync can say why they were not rendered.
func TestRenderCountsPoliciesScopedToAnotherHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet, err := os.ReadFile(filepath.Join("testdata", "harness", "codex-owner-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), ".claude", "settings.json")
	got, err := renderNative("claude_code", "", Bundle{PolicySet: policySet}, map[string][]byte{settings: nil})
	if err != nil {
		t.Fatal(err)
	}
	if want := (otherHarnessScope{Count: 3, Example: "harness:codex"}); got.OtherHarness != want {
		t.Fatalf("claude_code other-harness scope = %+v, want %+v", got.OtherHarness, want)
	}
	rules := filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")
	got, err = renderNative("codex", "", Bundle{PolicySet: policySet}, map[string][]byte{rules: nil})
	if err != nil {
		t.Fatal(err)
	}
	if want := (otherHarnessScope{Count: 1, Example: "harness:claude_code"}); got.OtherHarness != want {
		t.Fatalf("codex other-harness scope = %+v, want %+v", got.OtherHarness, want)
	}
}

// A rules directory is read file by file; only allow and forbidden decisions
// become rules.
func TestCodexImportRulesReadsRulesDirectory(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"default.rules": "# comment\nprefix_rule(pattern=[\"git\", \"status\"], decision=\"allow\")\nprefix_rule(pattern=[\"curl\"], decision=\"prompt\")\n",
		"extra.rules":   "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\")\n",
		"notes.txt":     "prefix_rule(pattern=[\"ls\"], decision=\"allow\")\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rules, err := codex{}.ImportRules(hermeticEnv(home, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{{Name: "codex-git-status", Dst: "shell:git status", Effect: "permit"}, {Name: "codex-rm", Dst: "shell:rm", Effect: "deny"}}
	if len(rules) != len(want) || rules[0] != want[0] || rules[1] != want[1] {
		t.Fatalf("rules = %+v, want %+v", rules, want)
	}
}
