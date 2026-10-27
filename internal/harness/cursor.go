package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cursor renders Kei policy into the Cursor CLI's native permission store:
// permissions.allow/deny in ~/.cursor/cli-config.json (HAI-882). Cursor
// matches whole tokens — Shell(base:*), Read(glob), Write(glob),
// WebFetch(domain|*) and Mcp(server:tool) — so a policy set holding
// destinations those tokens cannot express must fail the sync closed: a
// config that silently ignored them would under-enforce what Kei meant to
// gate. The report-only audit hook in ~/.cursor/hooks.json records the
// native decisions kei-proxy cannot see otherwise.

type cursor struct{}

// cursorSyntax is the Cursor native syntax: only single-token shell argv
// prefixes compile (Cursor matches one base command token), skill: and
// path: have no native form, and every applicable policy must compile —
// the rest is collected as not enforceable for the fail-closed check.
var cursorSyntax = nativeSyntax{
	kind:       "cursor",
	shellEntry: cursorShellEntry,
	toolEntry:  cursorToolEntry,
	mcpEntry:   cursorMCPEntry,
	enforceAll: true,
}

func (cursor) Kind() string { return "cursor" }

func (cursor) displayName() string { return "Cursor" }

// Detect reports Cursor installed when ~/.cursor exists: the CLI keeps
// cli-config.json, hooks.json and mcp.json there.
func (cursor) Detect(env Env) bool {
	home, err := env.home()
	if err != nil {
		return false
	}
	return dirExists(filepath.Join(home, ".cursor"))
}

func (cursor) Targets(env Env) ([]Target, error) {
	home, err := env.home()
	if err != nil {
		return nil, err
	}
	return fileTargets(
		filepath.Join(home, ".cursor", "cli-config.json"),
		filepath.Join(home, ".cursor", "hooks.json"),
	), nil
}

func (cursor) Render(b Bundle) (Rendered, error) { return render(cursorSyntax, "", b) }

// Apply refuses to write when part of the bundle has no native Cursor form
// (HAI-882): nothing is written and the error names each destination. A
// config that silently dropped policies would run what Kei meant to gate.
func (c cursor) Apply(_ context.Context, r Rendered, opts ApplyOpts) (Result, error) {
	if len(r.NotEnforceable) > 0 {
		return Result{}, fmt.Errorf("cursor cannot natively gate %d policy destination(s); nothing written: %s",
			len(r.NotEnforceable), strings.Join(r.NotEnforceable, "; "))
	}
	result, err := applyFiles(c, r, opts)
	if err != nil {
		return Result{}, err
	}
	if _, err := opts.Env.home(); err == nil {
		config, _ := os.ReadFile(cursorConfigPath(opts.Env))
		if cursorNonPrompting(config) {
			result.Warnings = append(result.Warnings, nonPromptingWarning(c.Kind()))
		}
	}
	return result, nil
}

// cursorNonPrompting reports whether the config lets Cursor run commands
// without asking, bypassing the permission allowlist Kei rendered.
func cursorNonPrompting(config []byte) bool {
	var root map[string]any
	_ = json.Unmarshal(config, &root)
	return root["approvalMode"] == "unrestricted"
}

func (cursor) SkillDirs(Env) []string { return nil }

// ImportRules is not supported: cursor permission tokens do not map back to
// Kei dst patterns unambiguously.
func (cursor) ImportRules(Env, string) ([]Rule, error) {
	return nil, errors.New("cursor: importing native permissions is not supported")
}

// cursorConfigPath is the user-level Cursor CLI config.
func cursorConfigPath(env Env) string {
	home, _ := env.home()
	return filepath.Join(home, ".cursor", "cli-config.json")
}

// cursorHooksPath is the user-level Cursor hooks file.
func cursorHooksPath(env Env) string {
	home, _ := env.home()
	return filepath.Join(home, ".cursor", "hooks.json")
}

// mcpConfigPath is Cursor's user-level MCP config, whose mcpServers apply in
// every project (the runtime discovers the same file).
func (cursor) mcpConfigPath(env Env) string {
	home, _ := env.home()
	return filepath.Join(home, ".cursor", "mcp.json")
}

// mergeMCPServers renders each server as an http (streamable HTTP) server at
// its upstream URL, with auth by environment-variable reference (ADR-039) —
// the same shape Claude Code renders.
func (cursor) mergeMCPServers(cfg []byte, remove []string, servers []mcpServer) ([]byte, []string, []string, error) {
	return mergeMCPServersJSON(cfg, "mcpServers", remove, servers, func(s mcpServer) *orderedObject {
		return orderedEntry("type", "http", "url", s.URL, "headers", orderedEntry("Authorization", "Bearer ${"+s.AuthEnvVar+"}"))
	})
}

func (cursor) renderFile(path string, cfg []byte, r Rendered) ([]byte, []string, []string, error) {
	if !strings.HasSuffix(path, filepath.Join(".cursor", "cli-config.json")) {
		// hooks.json carries only the audit hook (mergeHook).
		return cfg, nil, nil, nil
	}
	out, err := mergeCursorPermissions(cfg, r.Allows, r.Denies)
	if err != nil {
		return nil, nil, nil, err
	}
	return out,
		filterAbsent(claudePresent(cfg, "allow"), r.Allows),
		filterAbsent(claudePresent(cfg, "deny"), r.Denies), nil
}

func (cursor) missingEntries(path string, cfg []byte, allows, denies []string) ([]string, []string) {
	if !strings.HasSuffix(path, filepath.Join(".cursor", "cli-config.json")) {
		return nil, nil
	}
	return filterAbsent(claudePresent(cfg, "allow"), allows),
		filterAbsent(claudePresent(cfg, "deny"), denies)
}

// removeManaged strips the previous sync's exact entries from the
// permissions arrays — the same JSON shape Claude Code renders.
func (cursor) removeManaged(config []byte, allows, denies []string) ([]byte, error) {
	return (claudeCode{}).removeManaged(config, allows, denies)
}

// mergeHook merges the audit hook spec into hooks.json, whose events live
// under a top-level "hooks" key, and keeps the documented "version" field.
func (cursor) mergeHook(cfg, spec []byte) ([]byte, error) {
	merged, err := mergeHookJSON("cursor", cfg, spec, true)
	if err != nil {
		return nil, err
	}
	obj, err := parseTopLevel(merged, nil)
	if err != nil {
		return nil, fmt.Errorf("decode cursor hooks.json: %w", err)
	}
	if _, ok := obj.get("version"); !ok {
		obj.set("version", 1)
	}
	return marshalOrdered(obj, "  ")
}

func (cursor) ownsFile(string) bool { return false }

// cursorHookEvents are the events that carry a native decision for the audit
// hook: the blocking pre-events (shell, MCP, file reads and the generic tool
// event) plus postToolUse for what ran.
var cursorHookEvents = []string{
	"preToolUse",
	"postToolUse",
	"beforeShellExecution",
	"beforeMCPExecution",
	"beforeReadFile",
}

func (cursor) HookSpec(env Env) *HookSpec {
	if _, err := env.home(); err != nil {
		return nil
	}
	// HP-C11: desktop harnesses are sessions of the runtime installation keyed
	// by kind, so the hook carries no --harness uuid.
	root := &orderedObject{values: map[string]any{}}
	hooks := &orderedObject{values: map[string]any{}}
	for _, event := range cursorHookEvents {
		entry := &orderedObject{values: map[string]any{}}
		entry.set("type", "command")
		entry.set("command", "kei-proxy hook cursor")
		entry.set("timeout", 5)
		hooks.set(event, []any{entry})
	}
	root.set("version", 1)
	root.set("hooks", hooks)
	spec, err := marshalOrdered(root, "  ")
	if err != nil {
		return nil
	}
	return &HookSpec{Files: map[string][]byte{cursorHooksPath(env): spec}}
}

// cursorShellEntry maps a shell argv prefix to the native Shell token. Cursor
// permissions match one base command token, so only single-token argv
// prefixes compile; multi-token rules stay with the kei PDP (and are
// collected as not enforceable).
func cursorShellEntry(prefix string) string {
	tokens := strings.Fields(prefix)
	if len(tokens) != 1 {
		return ""
	}
	return "Shell(" + tokens[0] + ":*)"
}

// cursorToolEntry maps tool:cursor.<name> destinations to the native
// Read/Write/WebFetch tokens.
func cursorToolEntry(name string) string {
	switch strings.TrimPrefix(name, "cursor.") {
	case "read":
		return "Read(**)"
	case "write", "edit":
		return "Write(**)"
	case "webfetch":
		return "WebFetch(*)"
	}
	return ""
}

// cursorMCPEntry maps an mcp:<server>.<tool> destination to the native
// Mcp(server:tool) token; a server without a tool has no native form.
func cursorMCPEntry(server, tool string) string {
	if server == "" || server == "*" || tool == "" || tool == "*" {
		return ""
	}
	return "Mcp(" + server + ":" + tool + ")"
}

// mergeCursorPermissions merges the rendered entries into
// permissions.allow/deny, preserving the user's other keys and entries. Both
// arrays are always written once Kei manages the file: Cursor requires them.
func mergeCursorPermissions(cfg []byte, allows, denies []string) ([]byte, error) {
	root, keep, err := permissionRoot(cfg, allows, denies, "permissions")
	if err != nil || keep != nil {
		return keep, err
	}
	permissions, _ := root.get("permissions")
	permObj, ok := permissions.(*orderedObject)
	if !ok {
		permObj = &orderedObject{values: map[string]any{}}
	}
	for _, key := range []string{"allow", "deny"} {
		entries := allows
		if key == "deny" {
			entries = denies
		}
		existing, _ := permObj.get(key)
		existingArr, _ := existing.([]any)
		seen := map[string]bool{}
		merged := make([]any, 0, len(existingArr)+len(entries))
		for _, value := range existingArr {
			merged = append(merged, value)
			if item, ok := value.(string); ok {
				seen[item] = true
			}
		}
		for _, item := range entries {
			if !seen[item] {
				merged = append(merged, item)
				seen[item] = true
			}
		}
		permObj.set(key, merged)
	}
	root.set("permissions", permObj)
	return marshalOrdered(root, "  ")
}
