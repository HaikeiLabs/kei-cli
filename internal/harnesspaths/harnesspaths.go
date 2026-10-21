// Package harnesspaths resolves coding-harness config directories, honoring
// environment-variable overrides such as CODEX_HOME and CLAUDE_CONFIG_DIR
// (HAI-825). Both the harness package and the app package use these helpers so
// the resolution logic lives in one place.
package harnesspaths

import "path/filepath"

// CodexDir returns the codex config directory: $CODEX_HOME when set, otherwise
// homeDir/.codex.
func CodexDir(getenv func(string) string, homeDir string) string {
	if getenv != nil {
		if dir := getenv("CODEX_HOME"); dir != "" {
			return dir
		}
	}
	return filepath.Join(homeDir, ".codex")
}

// ClaudeDir returns the claude config directory: $CLAUDE_CONFIG_DIR when set,
// otherwise homeDir/.claude.
func ClaudeDir(getenv func(string) string, homeDir string) string {
	if getenv != nil {
		if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return dir
		}
	}
	return filepath.Join(homeDir, ".claude")
}
