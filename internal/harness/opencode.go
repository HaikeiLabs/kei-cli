package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openCode renders into the resolved opencode.json (permission.bash, skill
// and external_directory) and installs the kei-audit.js plugin.
type openCode struct{}

var openCodeSyntax = nativeSyntax{
	kind:       "opencode",
	shellEntry: rawShellEntry,
	skills:     true,
	paths:      true,
}

func (openCode) Kind() string        { return openCodeSyntax.kind }
func (openCode) ImportName() string  { return "opencode" }
func (openCode) displayName() string { return "OpenCode" }

// Detect counts OpenCode as installed when its global config dir exists or a
// config file is resolvable (OPENCODE_CONFIG / XDG_CONFIG_HOME / global / project).
func (openCode) Detect(env Env) bool {
	if _, err := env.home(); err != nil {
		return false
	}
	if dirExists(filepath.Join(env.Home, ".config", "opencode")) || dirExists(filepath.Join(env.XDGConfigHome(), "opencode")) {
		return true
	}
	_, exists := opencodeConfigPath(env)
	return exists
}

// opencodeConfigDir returns the OpenCode config directory (for plugins etc.)
// using the same resolution OpenCode does:
//  1. OPENCODE_CONFIG env var → parent of that file
//  2. $XDG_CONFIG_HOME/opencode
//  3. ~/.config/opencode
func opencodeConfigDir(env Env) string {
	if path := env.getenv("OPENCODE_CONFIG"); path != "" {
		return filepath.Dir(path)
	}
	if xdg := env.XDGConfigHome(); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	return filepath.Join(env.Home, ".config", "opencode")
}

// opencodeConfigName returns config file names to try, in preference order.
func opencodeConfigName() []string {
	return []string{"opencode.json", "opencode.jsonc"}
}

// opencodeConfigPath resolves the OpenCode config file the way OpenCode does:
//  1. OPENCODE_CONFIG env var (explicit file path)
//  2. $XDG_CONFIG_HOME/opencode/opencode.json(c)
//  3. ~/.config/opencode/opencode.json(c)
//  4. Project opencode.json(c) — walk up from cwd to the nearest git root
//
// It returns the path and whether that file exists. Only an existing config is
// ever edited; when none exists the caller should hint rather than create one.
func opencodeConfigPath(env Env) (string, bool) {
	if path := env.getenv("OPENCODE_CONFIG"); path != "" {
		return path, fileExists(path)
	}
	for _, name := range opencodeConfigName() {
		if xdg := env.XDGConfigHome(); xdg != "" {
			if p := filepath.Join(xdg, "opencode", name); fileExists(p) {
				return p, true
			}
		}
	}
	home := env.Home
	for _, name := range opencodeConfigName() {
		if p := filepath.Join(home, ".config", "opencode", name); fileExists(p) {
			return p, true
		}
	}
	if cwd, err := env.getwd(); err == nil {
		dir := cwd
		for {
			for _, name := range opencodeConfigName() {
				if p := filepath.Join(dir, name); fileExists(p) {
					return p, true
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Return the default global path even when it doesn't exist, so the caller
	// has something to hint about.
	if xdg := env.XDGConfigHome(); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.json"), false
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), false
}

func opencodePluginPath(env Env) string {
	return filepath.Join(opencodeConfigDir(env), "plugins", "kei-audit.js")
}

func (openCode) Targets(env Env) ([]Target, error) {
	if _, err := env.home(); err != nil {
		return nil, err
	}
	cfgPath, _ := opencodeConfigPath(env)
	return fileTargets(cfgPath, opencodePluginPath(env)), nil
}

func (openCode) Render(b Bundle) (Rendered, error) { return render(openCodeSyntax, "", b) }

func (o openCode) Apply(_ context.Context, r Rendered, opts ApplyOpts) (Result, error) {
	// Capture whether the config existed before sync, since the sync creates
	// it when missing.
	cfgPath, hadConfig := opencodeConfigPath(opts.Env)
	result, err := applyFiles(o, r, opts)
	if err != nil {
		return Result{}, err
	}
	if _, err := opts.Env.home(); err == nil {
		path, _ := opencodeConfigPath(opts.Env)
		config, _ := os.ReadFile(path)
		if opencodeNonPrompting(config) {
			result.Warnings = append(result.Warnings, nonPromptingWarning(o.Kind()))
		}
	}
	if opts.DryRun {
		result.Notes = append(result.Notes,
			fmt.Sprintf("OpenCode: resolved config path: %s", cfgPath),
			fmt.Sprintf("OpenCode: resolved plugin path: %s", opencodePluginPath(opts.Env)))
	} else if !hadConfig {
		result.Notes = append(result.Notes, fmt.Sprintf("OpenCode: created %s with Kei-managed permission block.", cfgPath))
	}
	return result, nil
}

// opencodeNonPrompting reports whether opencode.json lets OpenCode run
// unmatched commands without asking.
func opencodeNonPrompting(config []byte) bool {
	var root map[string]any
	_ = json.Unmarshal(config, &root)
	if permission, ok := root["permission"].(map[string]any); ok {
		mode, _ := permission["*"].(string)
		return mode == "allow"
	}
	return false
}

const opencodePlugin = "// managed by kei harness sync\nconst report = async (phase, event) => { try { const child = Bun.spawn(['kei-proxy','hook','opencode'], { stdin: 'pipe', stdout: 'ignore', stderr: 'ignore' }); child.stdin.write(JSON.stringify({phase, ...event})); child.stdin.end(); } catch {} };\nexport const KeiAudit = async () => ({ 'tool.execute.before': async (event) => { void report('pre', event); }, 'tool.execute.after': async (event) => { void report('post', event); }, 'permission.ask': async (event) => { void report('ask', event); }, 'permission.replied': async (event) => { void report('permission_reply', event); } });\n"

func (openCode) HookSpec(env Env) *HookSpec {
	if _, err := env.home(); err != nil {
		return nil
	}
	return &HookSpec{Files: map[string][]byte{opencodePluginPath(env): []byte(opencodePlugin)}}
}

func (openCode) renderFile(path string, cfg []byte, r Rendered) ([]byte, []string, []string, error) {
	if !strings.HasSuffix(path, filepath.Join("opencode", "opencode.json")) {
		return cfg, nil, nil, nil
	}
	out, err := mergeOpenCodePermissions(cfg, r.Allows, r.Denies)
	if err != nil {
		return nil, nil, nil, err
	}
	present := opencodePresent(cfg)
	return out, filterAbsent(present, r.Allows), filterAbsent(present, r.Denies), nil
}

// opencodePresent returns the patterns already in permission.bash.
func opencodePresent(cfg []byte) map[string]bool {
	var root map[string]any
	_ = json.Unmarshal(cfg, &root)
	present := map[string]bool{}
	if p, ok := root["permission"].(map[string]any); ok {
		if b, ok := p["bash"].(map[string]any); ok {
			for k := range b {
				present[k] = true
			}
		}
	}
	return present
}

func (openCode) missingEntries(path string, cfg []byte, allows, denies []string) ([]string, []string) {
	if !strings.HasSuffix(path, filepath.Join("opencode", "opencode.json")) {
		return nil, nil
	}
	var root struct {
		Permission struct {
			Bash     map[string]any `json:"bash"`
			Skill    map[string]any `json:"skill"`
			External map[string]any `json:"external_directory"`
		} `json:"permission"`
	}
	_ = json.Unmarshal(cfg, &root)
	inForce := func(entry, decision string) bool {
		switch {
		case strings.HasPrefix(entry, "Skill(") && strings.HasSuffix(entry, ")"):
			return root.Permission.Skill[strings.TrimSuffix(strings.TrimPrefix(entry, "Skill("), ")")] == decision
		case strings.HasPrefix(entry, "path:"):
			return root.Permission.External[strings.TrimPrefix(entry, "path:")] == decision
		case entry == "*":
			// Never written; see mergeOpenCodePermissions.
			return true
		default:
			// The ledger tracks the bare prefix; its "prefix *" twin is
			// written alongside it but may be the user's own key.
			return root.Permission.Bash[entry] == decision
		}
	}
	var missingAllows, missingDenies []string
	for _, e := range allows {
		if !inForce(e, "allow") {
			missingAllows = append(missingAllows, e)
		}
	}
	for _, e := range denies {
		if !inForce(e, "deny") {
			missingDenies = append(missingDenies, e)
		}
	}
	return missingAllows, missingDenies
}

func mergeOpenCodePermissions(cfg []byte, allows, denies []string) ([]byte, error) {
	root, keep, err := permissionRoot(cfg, allows, denies)
	if err != nil || keep != nil {
		return keep, err
	}
	permission, _ := root.get("permission")
	permObj, ok := permission.(*orderedObject)
	if !ok {
		permObj = &orderedObject{values: map[string]any{}}
	}
	bash, _ := permObj.get("bash")
	bashObj, ok := bash.(*orderedObject)
	if !ok {
		bashObj = &orderedObject{values: map[string]any{}}
	}
	skill, _ := permObj.get("skill")
	skillObj, ok := skill.(*orderedObject)
	if !ok {
		skillObj = &orderedObject{values: map[string]any{}}
	}
	external, _ := permObj.get("external_directory")
	externalObj, ok := external.(*orderedObject)
	if !ok {
		externalObj = &orderedObject{values: map[string]any{}}
	}
	apply := func(entry, decision string) {
		switch {
		case strings.HasPrefix(entry, "Skill(") && strings.HasSuffix(entry, ")"):
			name := strings.TrimSuffix(strings.TrimPrefix(entry, "Skill("), ")")
			if _, exists := skillObj.get(name); !exists {
				skillObj.set(name, decision)
			}
		case strings.HasPrefix(entry, "path:"):
			pattern := strings.TrimPrefix(entry, "path:")
			if _, exists := externalObj.get(pattern); !exists {
				externalObj.set(pattern, decision)
			}
		default:
			if entry == "*" {
				// Never write a "*" catch-all key: unmatched commands fall
				// back to OpenCode's native permission mode, and any
				// user-defined "*" key is left untouched.
				return
			}
			for _, pattern := range []string{entry, entry + " *"} {
				if _, exists := bashObj.get(pattern); !exists {
					bashObj.set(pattern, decision)
				}
			}
		}
	}
	for _, entry := range allows {
		apply(entry, "allow")
	}
	for _, entry := range denies {
		apply(entry, "deny")
	}
	permObj.set("bash", bashObj)
	if len(skillObj.keys) > 0 {
		permObj.set("skill", skillObj)
	}
	if len(externalObj.keys) > 0 {
		permObj.set("external_directory", externalObj)
	}
	root.set("permission", permObj)
	return marshalOrdered(root, "  ")
}

func (openCode) removeManaged(config []byte, allows, denies []string) ([]byte, error) {
	if len(allows) == 0 && len(denies) == 0 {
		return config, nil
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	permission, ok := root["permission"].(map[string]any)
	if !ok {
		return config, nil
	}
	bash, ok := permission["bash"].(map[string]any)
	if !ok {
		return config, nil
	}
	for _, entry := range allows {
		if bash[entry] == "allow" {
			delete(bash, entry)
		}
	}
	for _, entry := range denies {
		if bash[entry] == "deny" {
			delete(bash, entry)
		}
	}
	permission["bash"] = bash
	root["permission"] = permission
	return json.MarshalIndent(root, "", "  ")
}

// mergeHook writes the audit plugin whole; it is a Kei-owned file.
func (openCode) mergeHook(_, spec []byte) ([]byte, error) { return spec, nil }

func (openCode) ownsFile(path string) bool {
	return strings.HasSuffix(path, filepath.Join("opencode", "plugins", "kei-audit.js"))
}

func (openCode) ImportRules(env Env, path string) ([]Rule, error) {
	if _, err := env.home(); err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "" {
		path = filepath.Join(opencodeConfigDir(env), "opencode.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s not found", path)
	}
	var config struct {
		Permission map[string]json.RawMessage `json:"permission"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var rules []Rule
	for tool, raw := range config.Permission {
		var strVal string
		if err := json.Unmarshal(raw, &strVal); err == nil {
			if rule := opencodeRule(tool, tool, strVal); rule.Effect != "" {
				rules = append(rules, rule)
			}
			continue
		}
		var mapVal map[string]string
		if err := json.Unmarshal(raw, &mapVal); err != nil {
			continue
		}
		for pattern, val := range mapVal {
			if rule := opencodeRule(tool, pattern, val); rule.Effect != "" {
				rules = append(rules, rule)
			}
		}
	}
	return rules, nil
}

func opencodeRule(tool, pattern, value string) Rule {
	var effect string
	switch value {
	case "allow":
		effect = "permit"
	case "deny":
		effect = "deny"
	default:
		// "ask" and unknown values are skipped
		return Rule{}
	}
	var dst string
	switch tool {
	case "bash":
		dst = "shell:" + strings.TrimSuffix(pattern, "*")
	case "skill":
		dst = "skill:" + pattern
	case "external_directory":
		dst = "path:" + pattern
	default:
		dst = "tool:" + tool
	}
	return Rule{Name: "opencode-" + truncateName(tool+"-"+pattern, 50), Dst: dst, Effect: effect}
}
