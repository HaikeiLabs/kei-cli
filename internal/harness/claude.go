package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// claudeCode renders into ~/.claude/settings.json: permissions.allow/deny
// entries plus the report-only audit hooks on the Claude Code events. On a
// Claude Code >= 2.1.119 it renders all seven (PreToolUse, PermissionRequest,
// PermissionDenied, PostToolUse, PostToolUseFailure, Stop, SessionEnd;
// HAI-469); on an older or unknown install it renders only PreToolUse and
// PostToolUse, because an unrecognized hook event name made older versions
// ignore the whole settings.json.
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
		result.Warnings = append(result.Warnings, claudeVersionWarnings(opts.Env)...)
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

// claudeHookEvent is one Claude Code hook event Kei renders: its name and the
// matcher ("" for session-level events, which take none).
type claudeHookEvent struct {
	name    string
	matcher string
}

// claudeHookEventsLegacy is the pre-HAI-469 set: the two events older Claude
// Code versions recognize.
var claudeHookEventsLegacy = []claudeHookEvent{
	{name: "PreToolUse", matcher: "*"},
	{name: "PostToolUse", matcher: "*"},
}

// claudeHookEventsFull is the HAI-469 set, rendered only on a Claude Code that
// recognizes every event (>= claudeFullEventsMin).
var claudeHookEventsFull = []claudeHookEvent{
	{name: "PreToolUse", matcher: "*"},
	{name: "PermissionRequest", matcher: "*"},
	{name: "PermissionDenied", matcher: "*"},
	{name: "PostToolUse", matcher: "*"},
	{name: "PostToolUseFailure", matcher: "*"},
	{name: "Stop", matcher: ""},
	{name: "SessionEnd", matcher: ""},
}

// claudeFullEventsMin is the minimum Claude Code version that recognizes all
// seven hook events (HAI-469). Before 2.1.101 an unrecognized hook event name
// made the whole settings.json ignored, and PostToolUseFailure is not
// documented before 2.1.119, so an older or unknown install renders only the
// legacy two.
var claudeFullEventsMin = [3]int{2, 1, 119}

// atLeastClaudeFullEvents reports whether major.minor.patch is at least
// claudeFullEventsMin.
func atLeastClaudeFullEvents(major, minor, patch int) bool {
	got := [3]int{major, minor, patch}
	for i := range claudeFullEventsMin {
		if got[i] != claudeFullEventsMin[i] {
			return got[i] > claudeFullEventsMin[i]
		}
	}
	return true
}

// claudeCodeVersionInfo reports the installed Claude Code version: the trimmed
// version string (for warnings) and a parsed major.minor.patch (for
// comparison). known is false when it could not be determined (no binary, a
// timeout, or unparseable output).
func claudeCodeVersionInfo(env Env) (version string, major, minor, patch int, known bool) {
	out, err := env.runCmd(context.Background(), "claude", "--version")
	if err != nil {
		return "", 0, 0, 0, false
	}
	return parseClaudeVersion(strings.TrimSpace(out))
}

// parseClaudeVersion extracts the leading semver from `claude --version`
// output such as "2.1.296 (Claude Code)".
func parseClaudeVersion(out string) (version string, major, minor, patch int, known bool) {
	start := -1
	for i := 0; i < len(out); i++ {
		if out[i] >= '0' && out[i] <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return "", 0, 0, 0, false
	}
	end := start
	for end < len(out) && (out[end] >= '0' && out[end] <= '9' || out[end] == '.') {
		end++
	}
	parts := strings.Split(out[start:end], ".")
	if len(parts) != 3 {
		return "", 0, 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	patch, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", 0, 0, 0, false
	}
	return out[start:end], major, minor, patch, true
}

// claudeVersionWarnings returns the HAI-469 warning when the installed Claude
// Code is too old (or unknown) to render all seven hook events.
func claudeVersionWarnings(env Env) []string {
	version, major, minor, patch, known := claudeCodeVersionInfo(env)
	if known && atLeastClaudeFullEvents(major, minor, patch) {
		return nil
	}
	label := "unknown"
	if known {
		label = version
	}
	return []string{fmt.Sprintf("Claude Code %s is older than 2.1.119; rendering PreToolUse/PostToolUse only. Upgrade Claude Code to record native decisions (HAI-469).", label)}
}

func (claudeCode) HookSpec(env Env) *HookSpec {
	if _, err := env.home(); err != nil {
		return nil
	}
	// HP-C11: desktop harnesses are sessions of the runtime installation keyed
	// by kind, so the hook no longer carries a --harness uuid.
	command := "kei-proxy hook claude"
	hook := &orderedObject{values: map[string]any{}}
	hook.set("type", "command")
	hook.set("command", command)
	hook.set("timeout", 5)
	// HAI-469: observe Claude Code's native permission decisions. Render all
	// seven events only when the installed Claude Code recognizes them; an
	// unrecognized event name made older versions ignore the whole
	// settings.json (fixed in 2.1.101), so an older or unknown install gets the
	// previous two. The five tool-level events carry a "*" matcher; Stop and
	// SessionEnd are session-level and have none. The hook tells the events
	// apart by hook_event_name, so one command serves them all.
	events := claudeHookEventsLegacy
	if _, major, minor, patch, known := claudeCodeVersionInfo(env); known && atLeastClaudeFullEvents(major, minor, patch) {
		events = claudeHookEventsFull
	}
	root := &orderedObject{values: map[string]any{}}
	for _, e := range events {
		entry := &orderedObject{values: map[string]any{}}
		if e.matcher != "" {
			entry.set("matcher", e.matcher)
		}
		entry.set("hooks", []any{hook})
		root.set(e.name, []any{entry})
	}
	spec, err := marshalOrdered(root, "  ")
	if err != nil {
		return nil
	}
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
