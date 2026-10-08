package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fileHarness is a harness whose native store is local files. applyFiles is
// its Apply; the methods below are the only per-harness parts of it.
type fileHarness interface {
	Harness
	displayName() string
	// renderFile merges r into the target file at path and returns the
	// entries it newly manages there. A file it does not manage is returned
	// unchanged.
	renderFile(path string, cfg []byte, r Rendered) (out []byte, allows, denies []string, err error)
	// removeManaged removes previously Kei-written entries from cfg.
	removeManaged(cfg []byte, allows, denies []string) ([]byte, error)
	// mergeHook merges a HookSpec document into the target file.
	mergeHook(cfg, spec []byte) ([]byte, error)
	// ownsFile reports whether path is a file Kei owns outright (removed,
	// not edited, when the harness is removed).
	ownsFile(path string) bool
}

// applyFiles renders r into f's target files: it strips the entries the
// previous sync wrote, merges the new ones and the audit hook, backs up and
// rewrites each changed file, and records what it manages in the ledger.
func applyFiles(f fileHarness, r Rendered, opts ApplyOpts) error {
	targets, err := f.Targets(opts.Env)
	if err != nil {
		return err
	}
	// HP-C11: a desktop harness is a session of the runtime installation keyed
	// by kind, so the local ledger is keyed by kind (not a registered agent id).
	ledgerPath := ledgerPath(opts.Env, f.Kind())
	prior := readLedger(ledgerPath)
	if prior.Bundle > r.BundleVersion {
		return fmt.Errorf("bundle version rollback: local %d, fetched %d", prior.Bundle, r.BundleVersion)
	}
	if prior.Bundle == r.BundleVersion && prior.Digest != "" && prior.Digest != r.BundleDigest {
		return fmt.Errorf("bundle payload changed without a version increase")
	}
	ledger := syncLedger{HarnessID: f.Kind(), Kind: f.Kind(), Bundle: r.BundleVersion, Digest: r.BundleDigest, NotAfter: r.NotAfter, Files: map[string]fileLedger{}}
	inputs := map[string][]byte{}
	for _, target := range targets {
		if target.Remote() {
			return fmt.Errorf("%s: remote target %s needs its own Apply", f.Kind(), target.URL)
		}
		path := target.Path
		cfg, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if previous, ok := prior.Files[path]; ok && len(cfg) > 0 {
			cfg, err = f.removeManaged(cfg, previous.AllowEntries, previous.DenyEntries)
			if err != nil {
				return fmt.Errorf("remove previous managed entries from %s: %w", path, err)
			}
		}
		inputs[path] = cfg
	}
	outputs := map[string][]byte{}
	allows := map[string][]string{}
	denies := map[string][]string{}
	for path, cfg := range inputs {
		out, allowed, denied, err := f.renderFile(path, cfg, r)
		if err != nil {
			return err
		}
		outputs[path] = out
		allows[path] = allowed
		denies[path] = denied
	}
	printRenderHints(opts.Out, f.displayName(), r)
	if hook := f.HookSpec(opts.Env); hook != nil {
		for path, content := range hook.Files {
			base := outputs[path]
			if len(base) == 0 {
				base = inputs[path]
			}
			merged, err := f.mergeHook(base, content)
			if err != nil {
				return err
			}
			outputs[path] = merged
		}
	}
	for path, managed := range outputs {
		cfg := inputs[path]
		digest := sha256.Sum256(managed)
		ledger.Files[path] = fileLedger{Hash: hex.EncodeToString(digest[:]), Content: managed, AllowEntries: allows[path], DenyEntries: denies[path]}
		if bytes.Equal(cfg, managed) {
			continue
		}
		printHarnessDiff(opts.Out, path, cfg, managed)
		if !opts.DryRun {
			if len(cfg) > 0 {
				backup := path + ".kei-backup-" + opts.Now.UTC().Format("20060102T150405.000000000Z")
				if err := os.WriteFile(backup, cfg, 0o600); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, managed, 0o600); err != nil {
				return err
			}
		}
	}
	if !opts.DryRun {
		if err := writeLedger(ledgerPath, ledger); err != nil {
			return err
		}
	}
	return nil
}

// printRenderHints names the policies r could not render natively. Every
// harness's Apply prints them before its changes.
func printRenderHints(out io.Writer, displayName string, r Rendered) {
	for _, name := range r.NotEnforceable {
		fmt.Fprintf(out, "not enforceable in %s: %s\n", displayName, name)
	}
	if r.ScopedElsewhere > 0 {
		fmt.Fprintf(out, "%s: %d policies scoped to another harness (e.g. %s); widen src to harness:* to share them\n", displayName, r.ScopedElsewhere, r.ScopedElsewhereExample)
	}
}

func printHarnessDiff(out io.Writer, path string, before, after []byte) {
	beforeLines := splitLines(string(before))
	afterLines := splitLines(string(after))
	added, removed := 0, 0
	for _, o := range diffOps(beforeLines, afterLines) {
		switch o.typ {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	fmt.Fprintf(out, "%s: +%-3d -%-3d (Kei-managed entries updated)\n", path, added, removed)
	if diff := unifiedDiff(beforeLines, afterLines, 3); diff != "" {
		fmt.Fprintf(out, "--- %s\n+++ %s (Kei render)\n%s", path, path, diff)
	}
}

// splitLines splits a file body into lines, dropping the trailing newline and
// returning nil for an empty body.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// nonPromptingWarning is the warning for a harness configured not to ask
// for unmatched commands.
func nonPromptingWarning(kind string) string {
	return fmt.Sprintf("Warning: %s is configured not to ask for unmatched commands; see ADR-029 OQ1.", kind)
}

// mergeHookJSON merges a JSON hook spec into a JSON settings file. When
// nested is set the spec carries its hooks under a top-level "hooks" key
// (Codex); otherwise the hook events are the top level of the spec (Claude).
func mergeHookJSON(kind string, cfg, spec []byte, nested bool) ([]byte, error) {
	var current *orderedObject
	if len(cfg) > 0 {
		parsed, err := parseOrdered(cfg)
		if err != nil {
			return nil, fmt.Errorf("decode %s config: %w", kind, err)
		}
		obj, ok := parsed.(*orderedObject)
		if !ok {
			return nil, fmt.Errorf("%s config root is not a JSON object", kind)
		}
		current = obj
	} else {
		current = &orderedObject{values: map[string]any{}}
	}
	parsedSpec, err := parseOrdered(spec)
	if err != nil {
		return nil, err
	}
	incoming, ok := parsedSpec.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("hook spec root is not a JSON object")
	}
	if nested {
		if hooks, ok := incoming.get("hooks"); ok {
			if nestedObj, ok := hooks.(*orderedObject); ok {
				incoming = nestedObj
			}
		}
	}
	hooks, _ := current.get("hooks")
	var target *orderedObject
	if hooksObj, ok := hooks.(*orderedObject); ok {
		target = hooksObj
	} else {
		target = &orderedObject{values: map[string]any{}}
	}
	current.set("hooks", target)
	for _, key := range incoming.keys {
		value, _ := incoming.get(key)
		added, ok := value.([]any)
		if !ok {
			target.set(key, value)
			continue
		}
		old, _ := target.get(key)
		oldArr, _ := old.([]any)
		// Drop any existing Kei-managed hook entries (command starting
		// "kei-proxy hook ", including stale "--harness <uuid>" forms) so a
		// re-sync replaces them instead of appending a duplicate.
		kept := make([]any, 0, len(oldArr))
		for _, entry := range oldArr {
			if containsKeiHookCommand(entry) {
				continue
			}
			kept = append(kept, entry)
		}
		target.set(key, append(kept, added...))
	}
	return marshalOrdered(current, "  ")
}

// containsKeiHookCommand reports whether a hook entry (or anything nested
// inside it) carries a command that starts with "kei-proxy hook ". Such
// entries are Kei-managed and are replaced on every sync.
func containsKeiHookCommand(value any) bool {
	switch node := value.(type) {
	case *orderedObject:
		for _, key := range node.keys {
			if key == "command" {
				if s, ok := node.values[key].(string); ok && strings.HasPrefix(s, "kei-proxy hook ") {
					return true
				}
			}
			if containsKeiHookCommand(node.values[key]) {
				return true
			}
		}
		return false
	case []any:
		for _, elem := range node {
			if containsKeiHookCommand(elem) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// permissionRoot parses a JSON settings file for a permission merge. With no
// entries to merge it returns the bytes to keep instead of a root.
func permissionRoot(cfg []byte, allows, denies []string) (*orderedObject, []byte, error) {
	if len(allows) == 0 && len(denies) == 0 {
		if len(cfg) > 0 {
			// No Kei-managed entries but a config exists: leave it untouched.
			// Unmatched commands fall back to the harness's native permission
			// mode and the user's own defaults stand.
			return nil, cfg, nil
		}
		// No existing config and no policies: create a minimal empty config so
		// the user knows Kei is managing it.
		return nil, []byte("{}\n"), nil
	}
	if len(cfg) == 0 {
		return &orderedObject{values: map[string]any{}}, nil, nil
	}
	parsed, err := parseOrdered(cfg)
	if err != nil {
		return nil, nil, err
	}
	obj, ok := parsed.(*orderedObject)
	if !ok {
		return nil, nil, fmt.Errorf("settings root is not a JSON object")
	}
	return obj, nil, nil
}

// filterAbsent returns the entries not already in present.
func filterAbsent(present map[string]bool, entries []string) []string {
	var out []string
	for _, entry := range entries {
		if !present[entry] {
			out = append(out, entry)
		}
	}
	return out
}
