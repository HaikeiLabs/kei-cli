package harness

import (
	"fmt"
	"io"
)

// renderedHarness is one harness's render merged into a set of files, the
// shape the renderer golden tests assert on.
type renderedHarness struct {
	Files         map[string][]byte
	AllowEntries  map[string][]string
	DenyEntries   map[string][]string
	Unenforceable []string
	OtherHarness  otherHarnessScope
}

var syntaxes = map[string]nativeSyntax{"claude_code": claudeSyntax, "codex": codexSyntax, "opencode": openCodeSyntax}

// syntaxFor returns the native syntax of kind; a kind with no native store
// renders shell prefixes as is and has no skill or path form.
func syntaxFor(kind string) nativeSyntax {
	if s, ok := syntaxes[kind]; ok {
		return s
	}
	return nativeSyntax{kind: kind, shellEntry: rawShellEntry}
}

// renderNative renders b for the harness of kind registered as harnessID and
// merges the result into each of files, as applyFiles does before writing.
func renderNative(kind, harnessID string, b Bundle, files map[string][]byte) (renderedHarness, error) {
	h, ok := Default.Get(kind)
	if !ok {
		return renderedHarness{}, fmt.Errorf("unsupported harness kind %q", kind)
	}
	f := h.(fileHarness)
	r, err := render(syntaxFor(kind), harnessID, b)
	if err != nil {
		return renderedHarness{}, err
	}
	result := renderedHarness{Files: map[string][]byte{}, AllowEntries: map[string][]string{}, DenyEntries: map[string][]string{}, Unenforceable: r.NotEnforceable, OtherHarness: otherHarnessScope{Count: r.ScopedElsewhere, Example: r.ScopedElsewhereExample}}
	for path, cfg := range files {
		out, allows, denies, err := f.renderFile(path, cfg, r)
		if err != nil {
			return renderedHarness{}, err
		}
		result.Files[path] = out
		if allows != nil {
			result.AllowEntries[path] = allows
		}
		if denies != nil {
			result.DenyEntries[path] = denies
		}
	}
	return result, nil
}

// warnNonPromptingMode writes the non-prompting warning for kind when config
// lets it run unmatched commands without asking.
func warnNonPromptingMode(kind string, config []byte, out io.Writer) bool {
	var nonPrompting bool
	switch kind {
	case "claude_code":
		nonPrompting = claudeNonPrompting(config)
	case "codex":
		nonPrompting = codexNonPrompting(config)
	case "opencode":
		nonPrompting = opencodeNonPrompting(config)
	}
	if nonPrompting {
		fmt.Fprintln(out, nonPromptingWarning(kind))
	}
	return nonPrompting
}
