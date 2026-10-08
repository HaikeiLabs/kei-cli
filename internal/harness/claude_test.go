package harness

import (
	"os"
	"path/filepath"
	"testing"
)

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
