package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	claudeDesktopJSONFixture = "testdata/claude-desktop/conversations.json"
	claudeDesktopZipFixture  = "testdata/claude-desktop/export.zip"
)

func writeZipFixture(t *testing.T, path, name, content string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipWriter := zip.NewWriter(file)
	w, err := zipWriter.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeDesktopParseBareJSON(t *testing.T) {
	deps := &TranscriptDeps{CacheDir: t.TempDir()}
	conversations, err := parseClaudeDesktopExport(claudeDesktopJSONFixture, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 {
		t.Fatalf("conversations = %d, want 2", len(conversations))
	}
	if conversations[0].UUID != "conv-uuid-1111" || conversations[0].Name != "Fix the login bug" {
		t.Fatalf("first conversation = %+v", conversations[0])
	}
	if len(conversations[0].ChatMessages) != 4 {
		t.Fatalf("messages = %d, want 4", len(conversations[0].ChatMessages))
	}
}

func TestClaudeDesktopParseZip(t *testing.T) {
	deps := &TranscriptDeps{CacheDir: t.TempDir()}
	conversations, err := parseClaudeDesktopExport(claudeDesktopZipFixture, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 {
		t.Fatalf("conversations = %d, want 2", len(conversations))
	}
	if conversations[1].UUID != "conv-uuid-2222" || conversations[1].Name != "Weekly summary" {
		t.Fatalf("second conversation = %+v", conversations[1])
	}
}

func TestClaudeDesktopBadShape(t *testing.T) {
	dir := t.TempDir()
	deps := &TranscriptDeps{CacheDir: dir}
	cases := []struct {
		name  string
		path  string
		write func()
	}{
		{"json object", filepath.Join(dir, "object.json"), func() { writeFixture(t, filepath.Join(dir, "object.json"), `{"conversations": []}`) }},
		{"not json", filepath.Join(dir, "plain.txt"), func() { writeFixture(t, filepath.Join(dir, "plain.txt"), "this is not json") }},
		{"zip without conversations.json", filepath.Join(dir, "other.zip"), func() { writeZipFixture(t, filepath.Join(dir, "other.zip"), "other.json", `[]`) }},
		{"corrupt zip", filepath.Join(dir, "corrupt.zip"), func() { writeFixture(t, filepath.Join(dir, "corrupt.zip"), "not a zip at all") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.write()
			_, err := parseClaudeDesktopExport(tc.path, deps)
			if err == nil || !strings.Contains(err.Error(), "not a claude.ai export: expected conversations.json") {
				t.Fatalf("err = %v, want the not-a-claude.ai-export error", err)
			}
		})
	}
}

func TestClaudeDesktopSelectByUUID(t *testing.T) {
	cache := t.TempDir()
	deps := &TranscriptDeps{CacheDir: cache}
	var stdout bytes.Buffer
	path, counts, sourceName, leak, cleanup, err := exportClaudeDesktopTranscript(context.Background(), claudeDesktopJSONFixture, "conv-uuid-1111", deps, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if sourceName != "claude-desktop-conv-uuid-1111.txt" {
		t.Fatalf("sourceName = %q", sourceName)
	}
	if !leak {
		t.Fatalf("leak = false, want true (fixture has a kh_live token)")
	}
	if _, ok := counts["kh_live"]; ok {
		t.Fatalf("counts must not include the kh_live leak: %v", counts)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(content)
	for _, want := range []string{
		"human: Why does login fail? My token is [REDACTED:kh_live]",
		"assistant: Let me check the auth flow.",
		"human: It still fails.",
		"assistant: Fixed. The redirect URI was wrong.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kh_live_fixturetoken123456") {
		t.Fatalf("rendered transcript still contains the token:\n%s", out)
	}
	if strings.Contains(out, "ignored.png") {
		t.Fatalf("non-text content items must be dropped:\n%s", out)
	}
	// The rendered temp file sits next to the redacted one until cleanup.
	rendered := filepath.Join(cache, "claude-desktop-conv-uuid-1111.txt")
	if _, err := os.Stat(rendered); err != nil {
		t.Fatalf("rendered temp file missing: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("redacted file not cleaned up: %v", err)
	}
	if _, err := os.Stat(rendered); !os.IsNotExist(err) {
		t.Fatalf("rendered file not cleaned up: %v", err)
	}
}

func TestClaudeDesktopSelectByName(t *testing.T) {
	deps := &TranscriptDeps{CacheDir: t.TempDir()}
	var stdout bytes.Buffer
	path, _, sourceName, _, cleanup, err := exportClaudeDesktopTranscript(context.Background(), claudeDesktopJSONFixture, "Weekly summary", deps, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if sourceName != "claude-desktop-conv-uuid-2222.txt" {
		t.Fatalf("sourceName = %q, want claude-desktop-conv-uuid-2222.txt", sourceName)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "human: Summarize my week.") || !strings.Contains(string(content), "assistant: You shipped three features.") {
		t.Fatalf("rendered transcript = %q", content)
	}
}

func TestClaudeDesktopUnknownSession(t *testing.T) {
	deps := &TranscriptDeps{CacheDir: t.TempDir()}
	var stdout bytes.Buffer
	_, _, _, _, _, err := exportClaudeDesktopTranscript(context.Background(), claudeDesktopJSONFixture, "nope", deps, &stdout)
	if err == nil || !strings.Contains(err.Error(), "no Claude Desktop conversation") {
		t.Fatalf("err = %v, want a clear missing-conversation error", err)
	}
}

func TestClaudeDesktopMissingSelectionListsConversations(t *testing.T) {
	deps := &TranscriptDeps{CacheDir: t.TempDir()}
	var stdout bytes.Buffer
	_, _, _, _, _, err := exportClaudeDesktopTranscript(context.Background(), claudeDesktopJSONFixture, "", deps, &stdout)
	if !errors.Is(err, errClaudeDesktopNeedsSelection) {
		t.Fatalf("err = %v, want errClaudeDesktopNeedsSelection", err)
	}
	out := stdout.String()
	for _, want := range []string{"conv-uuid-2222", "Weekly summary", "2026-10-02", "conv-uuid-1111", "Fix the login bug", "2026-10-01"} {
		if !strings.Contains(out, want) {
			t.Fatalf("listing missing %q:\n%s", want, out)
		}
	}
	// Newest first.
	if strings.Index(out, "conv-uuid-2222") > strings.Index(out, "conv-uuid-1111") {
		t.Fatalf("listing not newest first:\n%s", out)
	}
}

func TestFeedbackClaudeDesktopMissingSelectionExits2(t *testing.T) {
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{CacheDir: t.TempDir()}
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
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude-desktop", "--file", claudeDesktopJSONFixture, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 2 {
		t.Fatalf("feedback exit = %d, want 2 (stderr = %s)", code, stderr.String())
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	out := stdout.String()
	if !strings.Contains(out, "conv-uuid-1111") || !strings.Contains(out, "Fix the login bug") {
		t.Fatalf("listing missing conversations:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "--session") {
		t.Fatalf("stderr missing the pick-one prompt:\n%s", stderr.String())
	}
}

func TestFeedbackClaudeDesktopRequiresFile(t *testing.T) {
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{CacheDir: t.TempDir()}
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
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude-desktop", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 2 {
		t.Fatalf("feedback exit = %d, want 2", code)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(stderr.String(), "requires exactly one --file") {
		t.Fatalf("stderr = %q, want a clear missing-file error", stderr.String())
	}
}

func TestFeedbackClaudeDesktopUploadsRedactedConversation(t *testing.T) {
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{CacheDir: t.TempDir()}
	}
	defer func() { feedbackTranscriptDeps = oldDeps }()

	var uploaded, uploadedName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bug-reports" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		files := r.MultipartForm.File["evidence"]
		if len(files) != 1 {
			t.Fatalf("evidence files = %d, want 1", len(files))
		}
		uploadedName = files[0].Filename
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
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-428"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude-desktop", "--file", claudeDesktopZipFixture, "--session", "conv-uuid-1111", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if uploadedName != "claude-desktop-conv-uuid-1111.txt" {
		t.Fatalf("uploaded filename = %q", uploadedName)
	}
	if strings.Contains(uploaded, "kh_live_fixturetoken123456") {
		t.Fatalf("uploaded transcript still contains the token:\n%s", uploaded)
	}
	if !strings.Contains(uploaded, "[REDACTED:kh_live]") {
		t.Fatalf("uploaded transcript missing redaction marker:\n%s", uploaded)
	}
	for _, want := range []string{
		"human: Why does login fail?",
		"assistant: Let me check the auth flow.",
		"human: It still fails.",
	} {
		if !strings.Contains(uploaded, want) {
			t.Fatalf("uploaded transcript missing %q:\n%s", want, uploaded)
		}
	}
	if !strings.Contains(stdout.String(), "Feedback submitted: feedback-428") {
		t.Fatalf("output = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: Kei runtime token found in the claude-desktop transcript") {
		t.Fatalf("stderr missing leak warning:\n%s", stderr.String())
	}
	// The raw export is never attached as evidence: exactly one file uploads.
	if !strings.Contains(stdout.String(), "Evidence (1 file(s)") {
		t.Fatalf("preview = %q, want exactly one evidence file", stdout.String())
	}
}

func TestFeedbackClaudeDesktopBadShapeErrors(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "not-an-export.json")
	writeFixture(t, badPath, `{"conversations": []}`)
	oldDeps := feedbackTranscriptDeps
	feedbackTranscriptDeps = func() *TranscriptDeps {
		return &TranscriptDeps{CacheDir: t.TempDir()}
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
	code := runFeedbackCommand([]string{"--description", "A report", "--export", "claude-desktop", "--file", badPath, "--session", "conv-uuid-1111", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 1 {
		t.Fatalf("feedback exit = %d, want 1", code)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(stderr.String(), "not a claude.ai export: expected conversations.json") {
		t.Fatalf("stderr = %q, want the not-a-claude.ai-export error", stderr.String())
	}
}
