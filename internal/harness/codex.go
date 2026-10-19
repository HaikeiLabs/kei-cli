package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// codex renders into the Kei-owned ~/.codex/rules/kei.rules (argv prefix
// rules; the user's default.rules is never touched) and ~/.codex/hooks.json.
type codex struct{}

var codexSyntax = nativeSyntax{
	kind:       "codex",
	shellEntry: rawShellEntry,
	argv:       codexArgv,
	argvEntry:  codexPattern,
}

func (codex) Kind() string        { return codexSyntax.kind }
func (codex) ImportName() string  { return "codex" }
func (codex) displayName() string { return "Codex" }

func (codex) Detect(env Env) bool {
	if _, err := env.home(); err != nil {
		return false
	}
	return dirExists(filepath.Join(env.Home, ".codex"))
}

func (codex) Targets(env Env) ([]Target, error) {
	if _, err := env.home(); err != nil {
		return nil, err
	}
	return fileTargets(filepath.Join(env.Home, ".codex", "rules", "kei.rules"), filepath.Join(env.Home, ".codex", "hooks.json")), nil
}

func (codex) Render(b Bundle) (Rendered, error) { return render(codexSyntax, "", b) }

func (c codex) Apply(_ context.Context, r Rendered, opts ApplyOpts) (Result, error) {
	result, err := applyFiles(c, r, opts)
	if err != nil {
		return Result{}, err
	}
	if _, err := opts.Env.home(); err == nil {
		config, _ := os.ReadFile(filepath.Join(opts.Env.Home, ".codex", "config.toml"))
		if codexNonPrompting(config) {
			result.Warnings = append(result.Warnings, nonPromptingWarning(c.Kind()))
		}
		if !opts.DryRun {
			hooksPath := filepath.Join(opts.Env.Home, ".codex", "hooks.json")
			hooks, _ := os.ReadFile(hooksPath)
			if untrusted := codexUntrustedKeiHooks(hooksPath, hooks, config); len(untrusted) > 0 {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Codex skips untrusted hooks, and the Kei hooks for %s are not trusted yet.", strings.Join(untrusted, ", ")))
			}
		}
	}
	result.Notes = append(result.Notes, "Codex runs new hooks only after you trust them: open Codex and run /hooks to approve the Kei hooks.")
	return result, nil
}

// codexNonPrompting reports whether config.toml lets Codex run unmatched
// commands without asking.
func codexNonPrompting(config []byte) bool {
	nonPrompting := false
	for _, line := range strings.Split(string(config), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "approval_policy" && strings.Trim(strings.TrimSpace(value), "\"'") == "never" {
			nonPrompting = true
		}
	}
	return nonPrompting
}

func (codex) SkillDirs(env Env) []string {
	if _, err := env.home(); err != nil {
		return nil
	}
	return []string{filepath.Join(env.Home, ".codex", "skills")}
}

func (codex) HookSpec(env Env) *HookSpec {
	if _, err := env.home(); err != nil {
		return nil
	}
	command := "kei-proxy hook codex"
	events := map[string]any{}
	for _, event := range codexHookEvents {
		// Codex clamps SessionEnd hooks to 3s and prints a warning when the
		// declared timeout exceeds it, so declare 3 for that event only.
		timeout := 5
		if event.name == "SessionEnd" {
			timeout = 3
		}
		hook := map[string]any{"type": "command", "command": command, "timeout": timeout}
		group := map[string]any{"hooks": []any{hook}}
		events[event.name] = []any{group}
	}
	spec := mustJSON(map[string]any{"hooks": events})
	return &HookSpec{Files: map[string][]byte{filepath.Join(env.Home, ".codex", "hooks.json"): spec}}
}

// codexHookEvents are the Codex hook events Kei reports, each with the label
// Codex uses for it in a [hooks.state] key: the tool calls, and the session
// start, turn end and session end. PermissionRequest is not rendered: it has
// no tool_use_id, so kei-proxy would record it as a second pre event.
var codexHookEvents = []struct{ name, stateLabel string }{
	{"PreToolUse", "pre_tool_use"},
	{"PostToolUse", "post_tool_use"},
	{"SessionStart", "session_start"},
	{"Stop", "stop"},
	{"SessionEnd", "session_end"},
}

// codexUntrustedKeiHooks returns the events whose Kei hook in hooksJSON has
// no trusted hash in config.toml. Codex keys hook trust by
// "<hooks.json path>:<event label>:<group index>:<handler index>" and skips
// a hook with no trusted hash. A recorded hash is taken as trusted: Codex's
// hash is not recomputed here, so a hook changed since it was trusted is not
// reported.
func codexUntrustedKeiHooks(hooksPath string, hooksJSON, config []byte) []string {
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(hooksJSON, &doc) != nil {
		return nil
	}
	trusted := codexTrustedHookKeys(config)
	var untrusted []string
	for _, event := range codexHookEvents {
		for gi, group := range doc.Hooks[event.name] {
			for hi, handler := range group.Hooks {
				key := fmt.Sprintf("%s:%s:%d:%d", hooksPath, event.stateLabel, gi, hi)
				if strings.HasPrefix(handler.Command, "kei-proxy hook ") && !trusted[key] && !slices.Contains(untrusted, event.name) {
					untrusted = append(untrusted, event.name)
				}
			}
		}
	}
	return untrusted
}

// codexTrustedHookKeys returns the [hooks.state] keys in config.toml that
// carry a trusted_hash, written as [hooks.state."<key>"] tables (Codex's
// form) or as dotted or inline entries under [hooks.state].
func codexTrustedHookKeys(config []byte) map[string]bool {
	trusted := map[string]bool{}
	table, inState := "", false
	for _, line := range strings.Split(string(config), "\n") {
		if header, ok := tomlTableHeader(line); ok {
			table, inState = "", header == "hooks.state"
			if rest, ok := strings.CutPrefix(header, "hooks.state."); ok {
				if key, after, ok := tomlKey(rest); ok && after == "" {
					table = key
				}
			}
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		switch {
		case table != "" && name == "trusted_hash":
			trusted[table] = true
		case inState:
			key, after, ok := tomlKey(name)
			if ok && (strings.TrimSpace(after) == ".trusted_hash" || (after == "" && strings.Contains(value, "trusted_hash"))) {
				trusted[key] = true
			}
		}
	}
	return trusted
}

// tomlKey reads the first key segment of s (double-quoted, single-quoted or
// bare) and returns it with the rest of s after it.
func tomlKey(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, `"`):
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				key, err := strconv.Unquote(s[:i+1])
				return key, strings.TrimSpace(s[i+1:]), err == nil
			}
		}
		return "", "", false
	case strings.HasPrefix(s, "'"):
		end := strings.Index(s[1:], "'")
		if end < 0 {
			return "", "", false
		}
		return s[1 : end+1], strings.TrimSpace(s[end+2:]), true
	default:
		key, rest, found := strings.Cut(s, ".")
		if found {
			rest = "." + rest
		}
		return strings.TrimSpace(key), strings.TrimSpace(rest), key != ""
	}
}

func (codex) renderFile(path string, cfg []byte, r Rendered) ([]byte, []string, []string, error) {
	if !strings.HasSuffix(path, filepath.Join(".codex", "rules", "kei.rules")) {
		return cfg, nil, nil, nil
	}
	// kei.rules is Kei-owned and always rendered whole, even with no rules,
	// so a policy removal clears the previous managed rules.
	var rules strings.Builder
	rules.WriteString("# managed by kei harness sync; edits are overwritten\n")
	for _, entry := range r.Allows {
		fmt.Fprintf(&rules, "prefix_rule(pattern=%s, decision=\"allow\", justification=\"kei policy\")\n", entry)
	}
	for _, entry := range r.Denies {
		fmt.Fprintf(&rules, "prefix_rule(pattern=%s, decision=\"forbidden\", justification=\"kei policy\")\n", entry)
	}
	return []byte(rules.String()), append([]string(nil), r.Allows...), append([]string(nil), r.Denies...), nil
}

func (codex) missingEntries(path string, cfg []byte, allows, denies []string) ([]string, []string) {
	if !strings.HasSuffix(path, filepath.Join(".codex", "rules", "kei.rules")) {
		return nil, nil
	}
	lines := strings.Split(string(cfg), "\n")
	inForce := func(entry, decision string) bool {
		for _, line := range lines {
			if strings.Contains(line, "pattern="+entry+",") && strings.Contains(line, `decision="`+decision+`"`) {
				return true
			}
		}
		return false
	}
	var missingAllows, missingDenies []string
	for _, e := range allows {
		if !inForce(e, "allow") {
			missingAllows = append(missingAllows, e)
		}
	}
	for _, e := range denies {
		if !inForce(e, "forbidden") {
			missingDenies = append(missingDenies, e)
		}
	}
	return missingAllows, missingDenies
}

func (codex) removeManaged(config []byte, allows, denies []string) ([]byte, error) {
	if len(allows) == 0 && len(denies) == 0 {
		return config, nil
	}
	if len(denies) != 0 {
		return config, nil
	}
	lines := strings.Split(string(config), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		remove := false
		for _, entry := range allows {
			if strings.Contains(line, "pattern="+entry) && strings.Contains(line, `decision="allow"`) {
				remove = true
				break
			}
		}
		if remove {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), nil
}

func (c codex) mergeHook(cfg, spec []byte) ([]byte, error) {
	return mergeHookJSON(c.Kind(), cfg, spec, true)
}

func (codex) ownsFile(path string) bool {
	return strings.HasSuffix(path, filepath.Join(".codex", "rules", "kei.rules"))
}

// codexArgv returns the argv prefix a Codex prefix_rule can express for dst.
// Only a concrete shell:<prefix> qualifies: Codex rules have no wildcard, so
// shell:*, *, skill:, tool:, path: and mcp: policies are not expressible.
func codexArgv(dst string) ([]string, bool) {
	if !strings.HasPrefix(dst, "shell:") {
		return nil, false
	}
	argv, err := splitArgv(strings.TrimPrefix(dst, "shell:"))
	if err != nil || len(argv) == 0 || argv[0] == "*" {
		return nil, false
	}
	return argv, true
}

// codexPattern renders argv as a Starlark list for prefix_rule(pattern=...).
func codexPattern(argv []string) string {
	quoted := make([]string, len(argv))
	for i, token := range argv {
		quoted[i] = strconv.Quote(token)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// splitArgv splits a command prefix into argv tokens the way a POSIX shell
// does for words: whitespace separates tokens, single quotes are literal,
// double quotes allow backslash escapes, and a backslash escapes the next
// character outside quotes. An unterminated quote is an error.
func splitArgv(s string) ([]string, error) {
	var argv []string
	var token strings.Builder
	inToken := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inToken {
				argv = append(argv, token.String())
				token.Reset()
				inToken = false
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			token.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inToken = true
		case c == '"':
			closed := false
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				token.WriteByte(s[i])
			}
			if !closed {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
			inToken = true
		case c == '\\' && i+1 < len(s):
			i++
			token.WriteByte(s[i])
			inToken = true
		default:
			token.WriteByte(c)
			inToken = true
		}
	}
	if inToken {
		argv = append(argv, token.String())
	}
	return argv, nil
}

var codexTokenRe = regexp.MustCompile(`"([^"]*)"`)

// ImportRules reads prefix_rule entries from a .rules file, or from every
// .rules file in a directory (default ~/.codex/rules).
func (codex) ImportRules(env Env, path string) ([]Rule, error) {
	if _, err := env.home(); err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "" {
		path = filepath.Join(env.Home, ".codex", "rules")
	}
	var files []string
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s not found", path)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".rules") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
	} else {
		files = []string{path}
	}
	if len(files) == 0 {
		return nil, nil
	}
	var rules []Rule
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s not found", file)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rule, ok := parseCodexLine(line)
			if !ok {
				continue
			}
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func parseCodexLine(line string) (Rule, bool) {
	// Extract pattern=[...]
	pIdx := strings.Index(line, "pattern=[")
	if pIdx < 0 {
		return Rule{}, false
	}
	pStart := pIdx + len("pattern=[")
	pEnd := strings.Index(line[pStart:], "]")
	if pEnd < 0 {
		return Rule{}, false
	}
	pEnd += pStart

	// Extract decision="..."
	dIdx := strings.Index(line, `decision="`)
	if dIdx < 0 {
		return Rule{}, false
	}
	dStart := dIdx + len(`decision="`)
	dEnd := strings.Index(line[dStart:], `"`)
	if dEnd < 0 {
		return Rule{}, false
	}
	decision := line[dStart : dStart+dEnd]

	var effect string
	switch decision {
	case "allow":
		effect = "permit"
	case "forbidden":
		effect = "deny"
	default:
		return Rule{}, false
	}

	// Parse the pattern list: ["tok1", "tok2", ...]
	inner := line[pStart:pEnd]
	var tokens []string
	for _, m := range codexTokenRe.FindAllStringSubmatch(inner, -1) {
		tokens = append(tokens, m[1])
	}
	if len(tokens) == 0 {
		return Rule{}, false
	}

	return Rule{Name: "codex-" + truncateName(strings.Join(tokens, " "), 50), Dst: "shell:" + strings.Join(tokens, " "), Effect: effect}, true
}

// mcpConfigPath is Codex's config.toml, whose [mcp_servers.<name>] tables are
// its MCP servers.
func (codex) mcpConfigPath(env Env) string {
	return filepath.Join(env.Home, ".codex", "config.toml")
}

// codexMCPMarker marks a server table Kei renders.
const codexMCPMarker = "# managed by kei harness sync"

// mergeMCPServers edits config.toml as lines so every byte outside the
// removed and added tables is kept: it drops each [mcp_servers.<name>] table
// (and its subtables) named in remove, then appends a streamable HTTP server
// table for each server the user's config does not already name.
func (codex) mergeMCPServers(cfg []byte, remove []string, servers []mcpServer) ([]byte, []string, []string, error) {
	if len(remove) == 0 && len(servers) == 0 {
		return cfg, nil, nil, nil
	}
	removing := map[string]bool{}
	for _, name := range remove {
		removing[name] = true
	}
	var kept strings.Builder
	present := map[string]bool{}
	dropping := false
	for _, line := range strings.SplitAfter(string(cfg), "\n") {
		if table, ok := tomlTableHeader(line); ok {
			name, isServer := codexMCPServerName(table)
			dropping = isServer && removing[name]
			if isServer && !dropping {
				present[name] = true
			}
		}
		if !dropping {
			kept.WriteString(line)
		}
	}
	out := strings.TrimRight(kept.String(), "\n")
	if out != "" {
		out += "\n"
	}
	var added, skipped []string
	for _, server := range servers {
		if present[server.Name] {
			skipped = append(skipped, server.Name)
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += fmt.Sprintf("[mcp_servers.%s]\n%s\nurl = %s\nbearer_token_env_var = %s\n", server.Name, codexMCPMarker, strconv.Quote(server.URL), strconv.Quote(server.AuthEnvVar))
		added = append(added, server.Name)
	}
	return []byte(out), added, skipped, nil
}

// tomlTableHeader returns the key of a [table] or [[array]] header line.
func tomlTableHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)
	open, close := "[", "]"
	if strings.HasPrefix(line, "[[") {
		open, close = "[[", "]]"
	} else if !strings.HasPrefix(line, "[") {
		return "", false
	}
	end := strings.Index(line, close)
	if end < len(open) {
		return "", false
	}
	return strings.TrimSpace(line[len(open):end]), true
}

// codexMCPServerName returns the server a table key under mcp_servers names
// (mcp_servers.<name> or one of its subtables), bare or double-quoted.
func codexMCPServerName(table string) (string, bool) {
	rest, ok := strings.CutPrefix(table, "mcp_servers.")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, `"`) {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return "", false
		}
		name, err := strconv.Unquote(rest[:end+2])
		return name, err == nil
	}
	name, _, _ := strings.Cut(rest, ".")
	return strings.TrimSpace(name), name != ""
}
