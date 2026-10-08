package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TranscriptDeps holds the injectable dependencies used by the transcript
// locators. Tests substitute HomeDir, Cwd, CacheDir, RunCmd and ReadFile with
// fixtures so no real home directory, harness binary, or config file is ever
// touched.
type TranscriptDeps struct {
	HomeDir  string
	Cwd      string
	CacheDir string
	// RunCmd executes a harness command and returns its stdout.
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
	// ReadFile reads a transcript file. The export path is the only place a
	// transcript is read; it never reads the Kei config or the keychain.
	// Tests inject a recording opener to prove the config path is never opened.
	ReadFile func(path string) ([]byte, error)
}

// TranscriptLocator locates a harness session transcript on disk.
type TranscriptLocator interface {
	// Kind returns the harness kind: claude, codex, or opencode.
	Kind() string
	// Locate returns the path to the transcript file for sessionID. When
	// sessionID is empty it selects the most recent session for the current
	// working directory. The returned path may be a temporary file (opencode).
	Locate(ctx context.Context, sessionID string) (string, error)
}

// newTranscriptLocator builds the locator for the given harness kind.
func newTranscriptLocator(kind string, deps *TranscriptDeps) (TranscriptLocator, error) {
	switch kind {
	case "claude":
		return claudeTranscriptLocator{deps: deps}, nil
	case "codex":
		return codexTranscriptLocator{deps: deps}, nil
	case "opencode":
		return opencodeTranscriptLocator{deps: deps}, nil
	}
	return nil, fmt.Errorf("unknown harness %q (expected claude, codex, or opencode)", kind)
}

// claudeTranscriptLocator finds Claude Code transcripts under
// ~/.claude/projects/<encoded cwd>/<session>.jsonl.
type claudeTranscriptLocator struct {
	deps *TranscriptDeps
}

func (claudeTranscriptLocator) Kind() string { return "claude" }

// encodeClaudeProjectDir mirrors Claude Code's project-directory encoding:
// every path separator becomes a dash.
func encodeClaudeProjectDir(dir string) string {
	return strings.ReplaceAll(filepath.ToSlash(dir), "/", "-")
}

func (l claudeTranscriptLocator) Locate(ctx context.Context, sessionID string) (string, error) {
	projectDir := filepath.Join(l.deps.HomeDir, ".claude", "projects", encodeClaudeProjectDir(l.deps.Cwd))
	if sessionID != "" {
		path := filepath.Join(projectDir, sessionID+".jsonl")
		if !fileExists(path) {
			return "", fmt.Errorf("no Claude Code session %q for %s", sessionID, l.deps.Cwd)
		}
		return path, nil
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no Claude Code sessions found for %s", l.deps.Cwd)
		}
		return "", fmt.Errorf("read Claude Code sessions for %s: %w", l.deps.Cwd, err)
	}
	var (
		latest    string
		latestMod time.Time
		found     bool
	)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(latestMod) {
			latest = filepath.Join(projectDir, entry.Name())
			latestMod = info.ModTime()
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("no Claude Code sessions found for %s", l.deps.Cwd)
	}
	return latest, nil
}

// codexTranscriptLocator finds Codex transcripts under ~/.codex/sessions at any
// depth (rollout-*.jsonl) whose session meta cwd matches the current directory.
type codexTranscriptLocator struct {
	deps *TranscriptDeps
}

func (codexTranscriptLocator) Kind() string { return "codex" }

func (l codexTranscriptLocator) Locate(ctx context.Context, sessionID string) (string, error) {
	sessionsDir := filepath.Join(l.deps.HomeDir, ".codex", "sessions")
	var candidates []string
	walkErr := filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			candidates = append(candidates, path)
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return "", fmt.Errorf("list Codex sessions: %w", walkErr)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no Codex sessions found for %s", l.deps.Cwd)
	}
	var (
		best    string
		bestMod time.Time
		found   bool
	)
	for _, path := range candidates {
		cwd, id, mod, ok := codexSessionMeta(path)
		if !ok {
			continue
		}
		if sessionID != "" {
			if id != sessionID && !strings.Contains(filepath.Base(path), sessionID) {
				continue
			}
		} else if cwd != l.deps.Cwd {
			continue
		}
		if !found || mod.After(bestMod) {
			best = path
			bestMod = mod
			found = true
		}
	}
	if !found {
		if sessionID != "" {
			return "", fmt.Errorf("no Codex session %q for %s", sessionID, l.deps.Cwd)
		}
		return "", fmt.Errorf("no Codex session for %s", l.deps.Cwd)
	}
	return best, nil
}

// codexSessionMeta reads the first line of a rollout file and returns its cwd,
// session id, and modification time. The meta may sit at the top level or be
// nested under a "payload" object.
func codexSessionMeta(path string) (cwd, sessionID string, mod time.Time, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", time.Time{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", "", time.Time{}, false
	}
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", "", time.Time{}, false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", time.Time{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return "", "", time.Time{}, false
	}
	source := raw
	if payload, hasPayload := raw["payload"]; hasPayload {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(payload, &nested); err == nil {
			source = nested
		}
	}
	cwd = codexStringField(source, "cwd")
	sessionID = firstNonEmpty(codexStringField(source, "id"), codexStringField(source, "session_id"))
	return cwd, sessionID, info.ModTime(), true
}

func codexStringField(source map[string]json.RawMessage, key string) string {
	raw, ok := source[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// opencodeTranscriptLocator exports an OpenCode session by running the
// opencode binary. The export is written to a temporary file in the user cache.
type opencodeTranscriptLocator struct {
	deps *TranscriptDeps
}

func (opencodeTranscriptLocator) Kind() string { return "opencode" }

type opencodeSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Cwd       string `json:"cwd"`
	Time      int64  `json:"time"`
	CreatedAt string `json:"created_at"`
}

func (l opencodeTranscriptLocator) Locate(ctx context.Context, sessionID string) (string, error) {
	if l.deps.RunCmd == nil {
		return "", fmt.Errorf("opencode export requires the opencode binary")
	}
	if sessionID == "" {
		sessionID = l.latestSessionForCwd(ctx)
		if sessionID == "" {
			return "", fmt.Errorf("no OpenCode session found for %s", l.deps.Cwd)
		}
	}
	if err := os.MkdirAll(l.deps.CacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create export directory: %w", err)
	}
	out, err := l.deps.RunCmd(ctx, "opencode", "export", sessionID)
	if err != nil {
		return "", fmt.Errorf("opencode export %s: %w", sessionID, err)
	}
	path := filepath.Join(l.deps.CacheDir, "opencode-"+sessionID+".jsonl")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", fmt.Errorf("write opencode export: %w", err)
	}
	return path, nil
}

func (l opencodeTranscriptLocator) latestSessionForCwd(ctx context.Context) string {
	out, err := l.deps.RunCmd(ctx, "opencode", "session", "list", "--format", "json")
	if err != nil {
		return ""
	}
	var sessions []opencodeSession
	if err := json.Unmarshal(out, &sessions); err != nil {
		return ""
	}
	var best *opencodeSession
	for i := range sessions {
		s := &sessions[i]
		dir := s.Directory
		if dir == "" {
			dir = s.Cwd
		}
		if dir != l.deps.Cwd {
			continue
		}
		if best == nil || opencodeSessionTime(s) > opencodeSessionTime(best) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

func opencodeSessionTime(s *opencodeSession) int64 {
	if s.Time != 0 {
		return s.Time
	}
	if s.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// redactSessionFile reads the transcript at path, redacts it, and writes the
// redacted content to a temporary file in the user cache. counter disambiguates
// files that share a basename. It returns the path to the redacted file, the
// redaction counts, whether a Kei runtime token leak was detected, and a
// cleanup function that removes the redacted file. The original file is never
// modified. The transcript is read through deps.ReadFile, the only read path in
// the export; it never reads the Kei config or the keychain.
func redactSessionFile(path string, counter int, deps *TranscriptDeps) (string, map[string]int, bool, func(), error) {
	readFile := deps.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	content, err := readFile(path)
	if err != nil {
		return "", nil, false, nil, fmt.Errorf("read transcript: %w", err)
	}
	result := RedactTranscript(content)
	if err := os.MkdirAll(deps.CacheDir, 0o700); err != nil {
		return "", nil, false, nil, fmt.Errorf("create export directory: %w", err)
	}
	outPath := filepath.Join(deps.CacheDir, fmt.Sprintf("redacted-%d-%s", counter, filepath.Base(path)))
	if err := os.WriteFile(outPath, result.Content, 0o600); err != nil {
		return "", nil, false, nil, fmt.Errorf("write redacted transcript: %w", err)
	}
	cleanup := func() { _ = os.Remove(outPath) }
	return outPath, result.Counts, result.RuntimeTokenLeak, cleanup, nil
}

// exportTranscript locates the harness transcript, redacts it, and writes the
// redacted content to a temporary file in the user cache. It returns the path
// to the redacted file, the redaction counts, the located transcript's base
// name (for the upload filename), whether a Kei runtime token leak was
// detected, and a cleanup function that removes any temporary files the export
// created.
func exportTranscript(ctx context.Context, kind, sessionID string, deps *TranscriptDeps) (string, map[string]int, string, bool, func(), error) {
	locator, err := newTranscriptLocator(kind, deps)
	if err != nil {
		return "", nil, "", false, nil, err
	}
	path, err := locator.Locate(ctx, sessionID)
	if err != nil {
		return "", nil, "", false, nil, err
	}
	outPath, counts, leak, cleanup, err := redactSessionFile(path, 0, deps)
	if err != nil {
		return "", nil, "", false, nil, err
	}
	fullCleanup := func() {
		cleanup()
		if path != outPath && strings.HasPrefix(path, deps.CacheDir+string(filepath.Separator)) {
			_ = os.Remove(path)
		}
	}
	return outPath, counts, filepath.Base(path), leak, fullCleanup, nil
}

// RedactionResult is the outcome of redacting a transcript. Counts holds the
// number of redactions per routine pattern. RuntimeTokenLeak reports whether a
// Kei runtime token (KEI_RUNTIME_TOKEN or kh_live_*) was found; such a value is
// a leak, not a routine redaction, so it is reported separately and never
// folded into Counts.
type RedactionResult struct {
	Content          []byte
	Counts           map[string]int
	RuntimeTokenLeak bool
}

// RedactTranscript redacts known secret patterns from content and returns the
// redacted content, a count of redactions per routine pattern, and whether a
// Kei runtime token leak was detected.
func RedactTranscript(content []byte) RedactionResult {
	counts := map[string]int{}
	leak := false
	for _, rule := range redactionRules {
		var n int
		content, n = rule.Apply(content)
		if n > 0 {
			if rule.Leak {
				leak = true
			} else {
				counts[rule.Name] = n
			}
		}
	}
	return RedactionResult{Content: content, Counts: counts, RuntimeTokenLeak: leak}
}

// redactionRule redacts one class of secret from transcript content. When Leak
// is set, a match marks the result as a runtime-token leak instead of being
// counted as a routine redaction.
type redactionRule struct {
	Name  string
	Leak  bool
	Apply func(content []byte) (newContent []byte, count int)
}

// redactPattern builds a rule that replaces every match of pattern with
// replacement, counting each match.
func redactPattern(pattern, replacement string) func(content []byte) (newContent []byte, count int) {
	re := regexp.MustCompile(pattern)
	return func(content []byte) ([]byte, int) {
		s := string(content)
		matches := re.FindAllString(s, -1)
		if len(matches) == 0 {
			return content, 0
		}
		return []byte(re.ReplaceAllString(s, replacement)), len(matches)
	}
}

var (
	privateKeyBlockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.+?-----END [A-Z ]*PRIVATE KEY-----`)
	privateKeyLineRe  = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
)

func redactPrivateKeyBlocks(content []byte) ([]byte, int) {
	s := string(content)
	count := len(privateKeyBlockRe.FindAllString(s, -1))
	s = privateKeyBlockRe.ReplaceAllString(s, "[REDACTED:private_key]")
	count += len(privateKeyLineRe.FindAllString(s, -1))
	s = privateKeyLineRe.ReplaceAllString(s, "[REDACTED:private_key]")
	if count == 0 {
		return content, 0
	}
	return []byte(s), count
}

// redactionRules are applied in order; more specific patterns run first so a
// value is counted against the most specific rule that matches it.
var redactionRules = []redactionRule{
	{Name: "private_key", Apply: redactPrivateKeyBlocks},
	{Name: "jwt", Apply: redactPattern(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`, `[REDACTED:jwt]`)},
	// A Kei runtime token in a transcript is a leak, not a routine redaction:
	// it is still redacted (defense in depth) but reported separately.
	{Name: "kei_runtime_token", Leak: true, Apply: redactPattern(`(?i)(KEI_RUNTIME_TOKEN["']?\s*[:=]\s*["']?)([A-Za-z0-9._\-/]+)`, `$1[REDACTED:kei_runtime_token]`)},
	{Name: "aws_secret", Apply: redactPattern(`(?i)((?:aws_)?secret_?access_?key["']?\s*[:=]\s*["']?)([A-Za-z0-9/+=]{40})(["']?)`, `$1[REDACTED:aws_secret]$3`)},
	{Name: "aws_access_key", Apply: redactPattern(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`, `[REDACTED:aws_access_key]`)},
	{Name: "anthropic_key", Apply: redactPattern(`\bsk-ant-[A-Za-z0-9_-]+`, `[REDACTED:anthropic_key]`)},
	{Name: "openai_key", Apply: redactPattern(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`, `[REDACTED:openai_key]`)},
	{Name: "github_token", Apply: redactPattern(`\b(?:ghp_|gho_|github_pat_)[A-Za-z0-9_]+`, `[REDACTED:github_token]`)},
	{Name: "slack_token", Apply: redactPattern(`\bxox[bp]-[A-Za-z0-9\-]+`, `[REDACTED:slack_token]`)},
	{Name: "generic_secret", Apply: redactPattern(`(?i)\b(password|passwd|pwd|api_?key|secret|token|access_?token|auth_?token|client_?secret)\b(["']?\s*[:=]\s*["']?)([A-Za-z0-9/+=._\-]{8,})(["']?)`, `$1$2[REDACTED:generic_secret]$4`)},
	// kh_live_* is a Haikei live credential; finding one in a transcript is a
	// runtime-token leak, reported separately from routine redactions.
	{Name: "kh_live", Leak: true, Apply: redactPattern(`kh_live_[A-Za-z0-9]+`, `[REDACTED:kh_live]`)},
	{Name: "bearer", Apply: redactPattern(`Bearer\s+[A-Za-z0-9._\-]+`, `Bearer [REDACTED:token]`)},
}

// formatRedactionCounts renders a redaction count map as "name=count, ..." in
// a stable order.
func formatRedactionCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return strings.Join(parts, ", ")
}

// feedbackTranscriptDeps builds the transcript locator dependencies for the
// real environment. It is a variable so tests can substitute fixtures without
// touching the real home directory or running a real harness binary.
var feedbackTranscriptDeps = func() *TranscriptDeps {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	cache, _ := os.UserCacheDir()
	return &TranscriptDeps{
		HomeDir:  home,
		Cwd:      cwd,
		CacheDir: filepath.Join(cache, "kei", "feedback"),
		RunCmd:   execFeedbackCommand,
		ReadFile: os.ReadFile,
	}
}

func execFeedbackCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
