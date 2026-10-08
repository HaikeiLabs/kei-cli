package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeCode renders into ~/.claude/settings.json: permissions.allow/deny
// entries plus the PreToolUse/PostToolUse audit hook.
type claudeCode struct{}

var claudeSyntax = nativeSyntax{
	kind: "claude_code",
	shellEntry: func(prefix string) string {
		if prefix == "*" {
			return "Bash(*)"
		}
		return "Bash(" + prefix + ":*)"
	},
	skills: true,
}

func (claudeCode) Kind() string        { return claudeSyntax.kind }
func (claudeCode) ImportName() string  { return "claude" }
func (claudeCode) displayName() string { return "Claude Code" }

func (claudeCode) Detect(env Env) bool {
	if _, err := env.home(); err != nil {
		return false
	}
	return dirExists(filepath.Join(env.Home, ".claude"))
}

func claudeSettingsPath(env Env) string {
	return filepath.Join(env.Home, ".claude", "settings.json")
}

func (claudeCode) Targets(env Env) ([]Target, error) {
	if _, err := env.home(); err != nil {
		return nil, err
	}
	return fileTargets(claudeSettingsPath(env)), nil
}

func (claudeCode) Render(b Bundle) (Rendered, error) { return render(claudeSyntax, "", b) }

func (c claudeCode) Apply(_ context.Context, r Rendered, opts ApplyOpts) (Result, error) {
	result, err := applyFiles(c, r, opts)
	if err != nil {
		return Result{}, err
	}
	if _, err := opts.Env.home(); err == nil {
		config, _ := os.ReadFile(claudeSettingsPath(opts.Env))
		if claudeNonPrompting(config) {
			result.Warnings = append(result.Warnings, nonPromptingWarning(c.Kind()))
		}
	}
	return result, nil
}

// claudeNonPrompting reports whether settings let Claude Code run unmatched
// commands without asking.
func claudeNonPrompting(config []byte) bool {
	var root map[string]any
	_ = json.Unmarshal(config, &root)
	if permissions, ok := root["permissions"].(map[string]any); ok {
		mode, _ := permissions["defaultMode"].(string)
		return mode == "bypassPermissions" || mode == "dontAsk"
	}
	return false
}

func (claudeCode) HookSpec(env Env) *HookSpec {
	if _, err := env.home(); err != nil {
		return nil
	}
	// HP-C11: desktop harnesses are sessions of the runtime installation keyed
	// by kind, so the hook no longer carries a --harness uuid.
	command := "kei-proxy hook claude"
	hook := map[string]any{"type": "command", "command": command, "timeout": 5}
	spec := mustJSON(map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{hook}}}, "PostToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{hook}}}})
	return &HookSpec{Files: map[string][]byte{claudeSettingsPath(env): spec}}
}

func (claudeCode) renderFile(path string, cfg []byte, r Rendered) ([]byte, []string, []string, error) {
	if !strings.HasSuffix(path, filepath.Join(".claude", "settings.json")) {
		return cfg, nil, nil, nil
	}
	out, err := mergeClaudePermissions(cfg, r.Allows, r.Denies)
	if err != nil {
		return nil, nil, nil, err
	}
	return out, filterAbsent(claudePresent(cfg, "allow"), r.Allows), filterAbsent(claudePresent(cfg, "deny"), r.Denies), nil
}

func (claudeCode) missingEntries(path string, cfg []byte, allows, denies []string) ([]string, []string) {
	if !strings.HasSuffix(path, filepath.Join(".claude", "settings.json")) {
		return nil, nil
	}
	return filterAbsent(claudePresent(cfg, "allow"), allows), filterAbsent(claudePresent(cfg, "deny"), denies)
}

// claudePresent returns the entries already in permissions[key].
func claudePresent(cfg []byte, key string) map[string]bool {
	var root map[string]any
	_ = json.Unmarshal(cfg, &root)
	present := map[string]bool{}
	if p, ok := root["permissions"].(map[string]any); ok {
		if xs, ok := p[key].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					present[s] = true
				}
			}
		}
	}
	return present
}

func mergeClaudePermissions(cfg []byte, allows, denies []string) ([]byte, error) {
	root, keep, err := permissionRoot(cfg, allows, denies)
	if err != nil || keep != nil {
		return keep, err
	}
	permissions, _ := root.get("permissions")
	permObj, ok := permissions.(*orderedObject)
	if !ok {
		permObj = &orderedObject{values: map[string]any{}}
	}
	for _, key := range []string{"allow", "deny"} {
		var entries []string
		if key == "allow" {
			entries = allows
		} else {
			entries = denies
		}
		existing, _ := permObj.get(key)
		existingArr, _ := existing.([]any)
		seen := map[string]bool{}
		for _, value := range existingArr {
			if item, ok := value.(string); ok {
				seen[item] = true
			}
		}
		for _, item := range entries {
			if !seen[item] {
				existingArr = append(existingArr, item)
				seen[item] = true
			}
		}
		if existingArr != nil {
			permObj.set(key, existingArr)
		}
	}
	root.set("permissions", permObj)
	return marshalOrdered(root, "  ")
}

func (claudeCode) removeManaged(config []byte, allows, denies []string) ([]byte, error) {
	if len(allows) == 0 && len(denies) == 0 {
		return config, nil
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	permissions, ok := root["permissions"].(map[string]any)
	if !ok {
		return config, nil
	}
	for key, entries := range map[string][]string{"allow": allows, "deny": denies} {
		values, ok := permissions[key].([]any)
		if !ok {
			continue
		}
		remove := map[string]int{}
		for _, s := range entries {
			remove[s]++
		}
		kept := make([]any, 0, len(values))
		for _, entry := range values {
			if s, ok := entry.(string); ok && remove[s] > 0 {
				remove[s]--
				continue
			}
			kept = append(kept, entry)
		}
		permissions[key] = kept
	}
	root["permissions"] = permissions
	return json.MarshalIndent(root, "", "  ")
}

func (c claudeCode) mergeHook(cfg, spec []byte) ([]byte, error) {
	return mergeHookJSON(c.Kind(), cfg, spec, false)
}

func (claudeCode) ownsFile(string) bool { return false }

func (claudeCode) ImportRules(env Env, path string) ([]Rule, error) {
	if _, err := env.home(); err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "" {
		path = claudeSettingsPath(env)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s not found", path)
	}
	var settings struct {
		AutoMode struct {
			SoftDeny []string `json:"soft_deny"`
		} `json:"autoMode"`
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var rules []Rule
	add := func(entry, effect string) {
		if rule, ok := claudeEntryToRule(entry, effect); ok {
			rules = append(rules, rule)
		} else {
			rules = append(rules, Rule{Native: entry, SkipReason: "wildcard is not a trailing :*"})
		}
	}
	for _, entry := range settings.Permissions.Allow {
		add(entry, "permit")
	}
	for _, entry := range settings.Permissions.Deny {
		add(entry, "deny")
	}
	for _, entry := range settings.AutoMode.SoftDeny {
		if entry == "$defaults" {
			continue
		}
		add(entry, "deny")
	}
	return rules, nil
}

func claudeEntryToRule(entry, effect string) (Rule, bool) {
	bashMatch := extractBashPattern(entry)
	if bashMatch != "" {
		// Only trailing :* wildcards map to an argv prefix.
		// Mid-pattern wildcards (e.g. helm:*values-prod.yaml*) cannot be
		// expressed as an argv prefix and are skipped.
		if !strings.HasSuffix(bashMatch, ":*") {
			return Rule{}, false
		}
		cmd := strings.TrimSuffix(bashMatch, ":*")
		return Rule{Name: "claude-" + truncateName(cmd, 50), Dst: "shell:" + cmd, Effect: effect}, true
	}
	return Rule{Name: "claude-" + strings.ToLower(entry), Dst: "skill:" + strings.ToLower(entry), Effect: effect}, true
}

func extractBashPattern(entry string) string {
	if !strings.HasPrefix(entry, "Bash(") {
		return ""
	}
	rest := entry[5:]
	end := strings.Index(rest, ")")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
