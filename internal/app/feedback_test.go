package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFeedbackNonTTYStdinWithoutYesFails covers Claude Code's `!` prefix and
// other piped invocations: when stdin is not a terminal and --yes is absent,
// the command must refuse to read a confirmation from the pipe, print the
// --yes hint to stderr, exit non-zero, and send nothing.
func TestFeedbackNonTTYStdinWithoutYesFails(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code == 0 {
		t.Fatalf("feedback exit = 0, want non-zero; stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "stdin is not a terminal") || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("stderr = %q, want the non-TTY --yes hint", stderr.String())
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0: nothing may be sent without confirmation", requests)
	}
	if strings.Contains(stdout.String(), "Feedback submitted") {
		t.Fatalf("stdout = %q, want no submission", stdout.String())
	}
}

// TestFeedbackNonTTYStdinWithYesSubmits covers the scripted path: with --yes
// the command submits without prompting, even when stdin is not a terminal.
func TestFeedbackNonTTYStdinWithYesSubmits(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(feedbackSubmissionResponse{ReportID: "feedback-456", ReceivedAt: "2026-11-25T00:00:00Z"})
	}))
	defer server.Close()
	t.Setenv("KEI_WEB_URL", server.URL)
	store := &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
	var stdout, stderr bytes.Buffer
	code := runFeedbackCommand([]string{"--description", "A report", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store, "1.2.3")
	if code != 0 {
		t.Fatalf("feedback exit = %d, stderr = %s", code, stderr.String())
	}
	if requests != 1 || !strings.Contains(stdout.String(), "Feedback submitted: feedback-456") {
		t.Fatalf("requests = %d, output = %q", requests, stdout.String())
	}
}
