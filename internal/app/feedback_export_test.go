package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustLocator(t *testing.T, kind string, deps *TranscriptDeps) TranscriptLocator {
	t.Helper()
	locator, err := newTranscriptLocator(kind, deps)
	if err != nil {
		t.Fatal(err)
	}
	return locator
}

func TestRedactTranscriptRemovesPlantedSecrets(t *testing.T) {
	content := strings.Join([]string{
		`key is kh_live_abc123def456`,
		`Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
		`Authorization: Bearer mybearertokenvalue123`,
		`aws_access_key_id = AKIAIOSFODNN7EXAMPLE`,
		`aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
		`SecretAccessKey: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
		`GITHUB_TOKEN=ghp_ABCDEFghijklmnopqrstuvwxyz0123456789`,
		`SLACK_TOKEN=xoxb-1234567890-abcdef`,
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7v5x\n-----END RSA PRIVATE KEY-----",
		`KEI_RUNTIME_TOKEN=kh_live_runtime_token_value_123`,
		`anthropic sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789`,
		`openai sk-proj-abcdefghijklmnopqrstuvwxyz0123456789`,
		`openai2 sk-abcdefghijklmnopqrstuvwxyz0123456789ab`,
		`"api_key": "mygenericsecretvalue123"`,
		`API_KEY=envsecretvalue456`,
	}, "\n")
	result := RedactTranscript([]byte(content))
	out := string(result.Content)
	for _, secret := range []string{
		"kh_live_abc123def456",
		"eyJhbGciOiJIUzI1NiJ9",
		"mybearertokenvalue123",
		"AKIAIOSFODNN7EXAMPLE",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"ghp_ABCDEFghijklmnopqrstuvwxyz0123456789",
		"xoxb-1234567890-abcdef",
		"MIIEpAIBAAKCAQEA7v5x",
		"kh_live_runtime_token_value_123",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"sk-proj-abcdefghijklmnopqrstuvwxyz0123456789",
		"sk-abcdefghijklmnopqrstuvwxyz0123456789ab",
		"mygenericsecretvalue123",
		"envsecretvalue456",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted output still contains %q:\n%s", secret, out)
		}
	}
	// kei_runtime_token and kh_live are leaks, not routine redactions: they are
	// still redacted but excluded from the per-pattern counts.
	want := map[string]int{
		"private_key": 1, "jwt": 1, "aws_secret": 2,
		"aws_access_key": 1, "anthropic_key": 1, "openai_key": 2, "github_token": 1,
		"slack_token": 1, "generic_secret": 2, "bearer": 1,
	}
	if len(result.Counts) != len(want) {
		t.Fatalf("counts = %v, want exactly %v", result.Counts, want)
	}
	for name, n := range want {
		if result.Counts[name] != n {
			t.Fatalf("counts[%q] = %d, want %d (counts = %v)", name, result.Counts[name], n, result.Counts)
		}
	}
	if _, ok := result.Counts["kei_runtime_token"]; ok {
		t.Fatalf("counts must not include the kei_runtime_token leak: %v", result.Counts)
	}
	if _, ok := result.Counts["kh_live"]; ok {
		t.Fatalf("counts must not include the kh_live leak: %v", result.Counts)
	}
	if !result.RuntimeTokenLeak {
		t.Fatalf("RuntimeTokenLeak = false, want true (content has a runtime token)")
	}
}

func TestRedactTranscriptNoSecretsLeavesContentUntouched(t *testing.T) {
	content := `{"message":"nothing sensitive here"}`
	result := RedactTranscript([]byte(content))
	if string(result.Content) != content {
		t.Fatalf("redacted = %q, want unchanged", result.Content)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("counts = %v, want empty", result.Counts)
	}
	if result.RuntimeTokenLeak {
		t.Fatalf("RuntimeTokenLeak = true, want false")
	}
}

func TestClaudeLocatorSelectsLatestByCwd(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	dir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd))
	old := filepath.Join(dir, "old.jsonl")
	new := filepath.Join(dir, "new.jsonl")
	writeFixture(t, old, `{"old":true}`)
	writeFixture(t, new, `{"new":true}`)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	deps := &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: t.TempDir()}
	path, err := mustLocator(t, "claude", deps).Locate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "new.jsonl" {
		t.Fatalf("located %q, want new.jsonl", path)
	}
}

func TestClaudeLocatorBySession(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	dir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd))
	writeFixture(t, filepath.Join(dir, "sess-a.jsonl"), `{"a":true}`)
	writeFixture(t, filepath.Join(dir, "sess-b.jsonl"), `{"b":true}`)
	deps := &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: t.TempDir()}
	path, err := mustLocator(t, "claude", deps).Locate(context.Background(), "sess-b")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "sess-b.jsonl" {
		t.Fatalf("located %q, want sess-b.jsonl", path)
	}
}

func TestClaudeLocatorMissing(t *testing.T) {
	home := t.TempDir()
	deps := &TranscriptDeps{HomeDir: home, Cwd: "/home/empty/project", CacheDir: t.TempDir()}
	_, err := mustLocator(t, "claude", deps).Locate(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no Claude Code sessions") {
		t.Fatalf("err = %v, want a clear missing-sessions error", err)
	}
}

func TestCodexLocatorSelectsByCwd(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "10", "08")
	match := filepath.Join(sessions, "rollout-1.jsonl")
	other := filepath.Join(sessions, "rollout-2.jsonl")
	writeFixture(t, match, `{"type":"session_meta","payload":{"id":"s1","cwd":"`+cwd+`"}}`)
	writeFixture(t, other, `{"type":"session_meta","payload":{"id":"s2","cwd":"/elsewhere"}}`)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}
	deps := &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: t.TempDir()}
	path, err := mustLocator(t, "codex", deps).Locate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "rollout-1.jsonl" {
		t.Fatalf("located %q, want rollout-1.jsonl", path)
	}
}

func TestCodexLocatorMissing(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "10", "08")
	writeFixture(t, filepath.Join(sessions, "rollout-1.jsonl"), `{"type":"session_meta","payload":{"id":"s1","cwd":"/elsewhere"}}`)
	deps := &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: t.TempDir()}
	_, err := mustLocator(t, "codex", deps).Locate(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no Codex session") {
		t.Fatalf("err = %v, want a clear missing-session error", err)
	}
}

func TestOpenCodeLocatorBySession(t *testing.T) {
	cache := t.TempDir()
	var calls []string
	runCmd := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "opencode" {
			t.Fatalf("name = %q", name)
		}
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "export" {
			return []byte(`{"message":"opencode session"}`), nil
		}
		return nil, nil
	}
	deps := &TranscriptDeps{HomeDir: t.TempDir(), Cwd: "/home/test/project", CacheDir: cache, RunCmd: runCmd}
	path, err := mustLocator(t, "opencode", deps).Locate(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "export sess-1" {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.HasPrefix(path, cache) {
		t.Fatalf("path %q not in cache dir", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != `{"message":"opencode session"}` {
		t.Fatalf("content = %q", content)
	}
}

func TestOpenCodeLocatorLatestForCwd(t *testing.T) {
	cwd := "/home/test/project"
	runCmd := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "session":
			return []byte(`[{"id":"old","directory":"` + cwd + `","time":100},{"id":"new","directory":"` + cwd + `","time":200},{"id":"other","directory":"/elsewhere","time":300}]`), nil
		case "export":
			if args[1] != "new" {
				t.Fatalf("exported %q, want new", args[1])
			}
			return []byte(`{"message":"new session"}`), nil
		}
		return nil, nil
	}
	deps := &TranscriptDeps{HomeDir: t.TempDir(), Cwd: cwd, CacheDir: t.TempDir(), RunCmd: runCmd}
	path, err := mustLocator(t, "opencode", deps).Locate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "opencode-new") {
		t.Fatalf("path = %q, want opencode-new", path)
	}
}

func TestOpenCodeLocatorMissing(t *testing.T) {
	runCmd := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "session" {
			return []byte(`[{"id":"other","directory":"/elsewhere","time":300}]`), nil
		}
		return nil, nil
	}
	deps := &TranscriptDeps{HomeDir: t.TempDir(), Cwd: "/home/test/project", CacheDir: t.TempDir(), RunCmd: runCmd}
	_, err := mustLocator(t, "opencode", deps).Locate(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no OpenCode session") {
		t.Fatalf("err = %v, want a clear missing-session error", err)
	}
}

func TestExportTranscriptRedactsAndCleansUp(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	session := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd), "sess.jsonl")
	writeFixture(t, session, `{"message":"token kh_live_abc123def456 and AKIAIOSFODNN7EXAMPLE"}`)
	cache := t.TempDir()
	deps := &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: cache}
	path, counts, sourceName, leak, cleanup, err := exportTranscript(context.Background(), "claude", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if sourceName != "sess.jsonl" {
		t.Fatalf("sourceName = %q, want sess.jsonl", sourceName)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(content)
	if strings.Contains(out, "kh_live_abc123def456") || strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("redacted output still contains secrets:\n%s", out)
	}
	// kh_live is a runtime-token leak (reported separately); aws_access_key is a
	// routine redaction (counted).
	if !leak {
		t.Fatalf("leak = false, want true (transcript has a kh_live token)")
	}
	if counts["aws_access_key"] != 1 {
		t.Fatalf("counts = %v, want aws_access_key=1", counts)
	}
	if _, ok := counts["kh_live"]; ok {
		t.Fatalf("counts must not include the kh_live leak: %v", counts)
	}
	original, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), "kh_live_abc123def456") {
		t.Fatalf("original transcript was modified:\n%s", original)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("redacted file not cleaned up: %v", err)
	}
}

func TestFeedbackExportYesSubmitsRedactedTranscript(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	session := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd), "sess.jsonl")
	writeFixture(t, session, `{"message":"secret kh_live_abc123def456"}`)
	cache := t.TempDir()

	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: cache}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bug-reports" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 {
			t.Fatalf("evidence files = %d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = string(content)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-423"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(uploaded, "kh_live_abc123def456") {
		t.Fatalf("uploaded transcript still contains the secret:\n%s", uploaded)
	}
	if !strings.Contains(uploaded, "[REDACTED:kh_live]") {
		t.Fatalf("uploaded transcript missing redaction marker:\n%s", uploaded)
	}
	// kh_live is a runtime-token leak, not a routine redaction: the preview
	// shows no routine counts and a separate leak warning is printed on stderr.
	if !strings.Contains(stdout.String(), "Redactions: none") {
		t.Fatalf("preview missing redaction line:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: Kei runtime token found in the claude transcript") {
		t.Fatalf("stderr missing leak warning:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Feedback submitted: feedback-423") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestFeedbackExportMissingTranscriptErrors(t *testing.T) {
	home := t.TempDir()
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: home, Cwd: "/home/empty/project", CacheDir: t.TempDir()}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s", r.URL.Path)
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 1 {
		t.Fatalf("feedback exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no Claude Code sessions") {
		t.Fatalf("stderr = %q, want a clear missing-sessions error", stderr.String())
	}
}

func TestFeedbackExportDeclineSendsNothingAndHidesContent(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	session := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd), "sess.jsonl")
	writeFixture(t, session, `TOPSECRETTRANSCRIPTCONTENT kh_live_abc123def456`)
	cache := t.TempDir()

	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: cache}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	// Answer "no" (not y/yes) to the Send? prompt.
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude"}, &stdout, &stderr, strings.NewReader("no\n"), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	out := stdout.String()
	if !strings.Contains(out, "Feedback not submitted.") {
		t.Fatalf("output = %q, want a not-submitted message", out)
	}
	if strings.Contains(out, "TOPSECRETTRANSCRIPTCONTENT") {
		t.Fatalf("preview printed transcript content:\n%s", out)
	}
	if strings.Contains(out, "kh_live_abc123def456") {
		t.Fatalf("preview printed a secret:\n%s", out)
	}
}

func TestFeedbackSessionFileIsRedacted(t *testing.T) {
	// A --session file path (no --export) is redacted before upload.
	sessionPath := t.TempDir() + "/session.jsonl"
	writeFixture(t, sessionPath, `{"message":"secret kh_live_abc123def456 and sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"}`)
	cache := t.TempDir()

	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: t.TempDir(), Cwd: t.TempDir(), CacheDir: cache}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 {
			t.Fatalf("evidence files = %d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = string(content)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-424"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--session", sessionPath, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(uploaded, "kh_live_abc123def456") || strings.Contains(uploaded, "sk-ant-api03-") {
		t.Fatalf("uploaded session file still contains secrets:\n%s", uploaded)
	}
	if !strings.Contains(uploaded, "[REDACTED:kh_live]") || !strings.Contains(uploaded, "[REDACTED:anthropic_key]") {
		t.Fatalf("uploaded session file missing redaction markers:\n%s", uploaded)
	}
	// anthropic_key is a routine redaction (counted); kh_live is a leak (warned
	// on stderr, not counted).
	if !strings.Contains(stdout.String(), "Redactions:") || !strings.Contains(stdout.String(), "anthropic_key=1") {
		t.Fatalf("preview missing redaction counts:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: Kei runtime token found in the session.jsonl transcript") {
		t.Fatalf("stderr missing leak warning:\n%s", stderr.String())
	}
	original, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), "kh_live_abc123def456") {
		t.Fatalf("original session file was modified:\n%s", original)
	}
}

func TestFeedbackMultipleSessionFilesAreRedacted(t *testing.T) {
	dir := t.TempDir()
	path1 := dir + "/s1.jsonl"
	path2 := dir + "/s2.jsonl"
	writeFixture(t, path1, `{"message":"secret kh_live_one123456789"}`)
	writeFixture(t, path2, `{"message":"secret kh_live_two123456789"}`)
	cache := t.TempDir()

	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: t.TempDir(), Cwd: t.TempDir(), CacheDir: cache}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 2 {
			t.Fatalf("evidence files = %d, want 2", len(files))
		}
		for _, f := range files {
			file, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			uploaded = append(uploaded, string(content))
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-425"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--session", path1, "--session", path2, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if len(uploaded) != 2 {
		t.Fatalf("uploaded = %d, want 2", len(uploaded))
	}
	for i, content := range uploaded {
		if strings.Contains(content, "kh_live_one123456789") || strings.Contains(content, "kh_live_two123456789") {
			t.Fatalf("uploaded[%d] still contains a secret:\n%s", i, content)
		}
		if !strings.Contains(content, "[REDACTED:kh_live]") {
			t.Fatalf("uploaded[%d] missing redaction marker:\n%s", i, content)
		}
	}
}

func TestFeedbackExportRejectsMultipleSessions(t *testing.T) {
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: t.TempDir(), Cwd: t.TempDir(), CacheDir: t.TempDir()}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude", "--session", "a", "--session", "b", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 2 {
		t.Fatalf("feedback exit = %d, want 2", code)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(stderr.String(), "at most one --session") {
		t.Fatalf("stderr = %q, want a clear error", stderr.String())
	}
}

func TestExportNeverReadsRuntimeConfig(t *testing.T) {
	// A runtime token stored only in a fake config must never reach the export
	// or the upload, and the export must not open the config path.
	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "kei.yaml")
	writeFixture(t, configPath, "runtime_token: kh_live_config_only_token_12345\n")
	cwd := "/home/test/project"
	session := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd), "sess.jsonl")
	writeFixture(t, session, `{"message":"no secrets here"}`)
	cache := t.TempDir()

	var opened []string
	readFile := func(path string) ([]byte, error) {
		opened = append(opened, path)
		return os.ReadFile(path)
	}
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: cache, ReadFile: readFile}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 {
			t.Fatalf("evidence files = %d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = string(content)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-426"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	// The config token must never appear in the upload body or any output.
	for _, out := range []string{uploaded, stdout.String(), stderr.String()} {
		if strings.Contains(out, "kh_live_config_only_token_12345") {
			t.Fatalf("config token leaked into output:\n%s", out)
		}
	}
	// The export must not open the config path.
	for _, p := range opened {
		if p == configPath || strings.HasSuffix(p, "kei.yaml") {
			t.Fatalf("export opened the config path %q (opened: %v)", p, opened)
		}
	}
}

func TestRuntimeTokenLeakWarning(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/test/project"
	session := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(cwd), "sess.jsonl")
	writeFixture(t, session, `{"message":"oops kh_live_99999888887777766666"}`)
	cache := t.TempDir()

	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{HomeDir: home, Cwd: cwd, CacheDir: cache}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded string
	var leakField string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		leakField = r.FormValue("runtime_token_leak_detected")
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 {
			t.Fatalf("evidence files = %d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = string(content)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-427"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	// Redacted output: the token value is gone, the redaction marker is present.
	if strings.Contains(uploaded, "kh_live_99999888887777766666") {
		t.Fatalf("uploaded transcript still contains the token:\n%s", uploaded)
	}
	if !strings.Contains(uploaded, "[REDACTED:kh_live]") {
		t.Fatalf("uploaded transcript missing redaction marker:\n%s", uploaded)
	}
	// A separate leak warning is printed on stderr, with the harness name and
	// the rotation hint.
	if !strings.Contains(stderr.String(), "WARNING: Kei runtime token found in the claude transcript") {
		t.Fatalf("stderr missing leak warning:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "kei bot credential --rotate") {
		t.Fatalf("stderr missing rotation hint:\n%s", stderr.String())
	}
	// The leak is reported in the metadata as a boolean, never the value.
	if leakField != "true" {
		t.Fatalf("runtime_token_leak_detected = %q, want true", leakField)
	}
	// The token value appears nowhere.
	for _, out := range []string{uploaded, stdout.String(), stderr.String()} {
		if strings.Contains(out, "kh_live_99999888887777766666") {
			t.Fatalf("token value leaked into output:\n%s", out)
		}
	}
}
