package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	policybundle "github.com/HaikeiLabs/kei-connector-contracts/bundle"
)

// HAI-481: the renderer must not rewrite lines it does not own. A settings
// file whose non-managed values carry HTML-escapable (&, <) or non-ASCII
// characters comes back with those lines byte-for-byte identical, and the
// dry-run diff shows only the Kei-managed permissions/hooks entries.
func TestClaudeRenderPreservesUnmanagedBytes(t *testing.T) {
	home := t.TempDir()
	// A recent Claude Code so the full seven-event hook set is rendered
	// deterministically (HAI-469) without running the real binary.
	env := claudeEnv(home, "2.1.296 (Claude Code)\n")
	path := claudeSettingsPath(env)

	input, err := os.ReadFile(filepath.Join("testdata", "harness", "claude.settings.escape.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "harness", "claude.settings.escape.golden.json"))
	if err != nil {
		t.Fatal(err)
	}

	policySet := json.RawMessage(`{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git","action":"permit","enabled":true},{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","action":"deny","enabled":true}]}`)
	bundle := Bundle{Payload: policybundle.Payload{PolicySet: policySet, Harnesses: []policybundle.Harness{{AgentID: "77777777-7777-7777-7777-777777777777", Kind: "claude_code"}}}}
	h := claudeCode{}
	r, err := h.Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	// The full sync path: merge permissions, then merge the audit hook, as
	// applyFiles does before writing the file.
	out, _, _, err := h.renderFile(path, input, r)
	if err != nil {
		t.Fatal(err)
	}
	hook := h.HookSpec(env)
	merged, err := h.mergeHook(out, hook.Files[path])
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytes.TrimSpace(merged), bytes.TrimSpace(golden)) {
		t.Fatalf("rendered config mismatch\ngot:\n%s\nwant:\n%s", merged, golden)
	}

	// The non-managed lines — the ones carrying &, < and a non-ASCII value —
	// must be byte-for-byte identical to the input.
	unmanaged := []string{
		`  "model": "claude-sonnet-4-5",`,
		`  "env": {`,
		`    "GREETING": "a \u0026 b \u003c c",`,
		`    "NAME": "caf\u00e9"`,
	}
	inputLines := splitLines(string(input))
	mergedLines := splitLines(string(merged))
	for _, line := range unmanaged {
		if !containsLine(inputLines, line) {
			t.Fatalf("fixture missing unmanaged line %q", line)
		}
		if !containsLine(mergedLines, line) {
			t.Errorf("unmanaged line not byte-identical after render: %q\ngot:\n%s", line, merged)
		}
	}

	// The dry-run diff must contain only managed entries: no unmanaged line is
	// added or removed.
	for _, o := range diffOps(inputLines, mergedLines) {
		if o.typ == ' ' {
			continue
		}
		for _, line := range unmanaged {
			if o.line == line {
				t.Errorf("dry-run diff touches unmanaged line %q (%c)", line, o.typ)
			}
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
