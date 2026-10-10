package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// errClaudeDesktopNeedsSelection is returned by exportClaudeDesktopTranscript
// when the export parsed but no --session was given; the caller has already
// listed the available conversations and exits with a usage error.
var errClaudeDesktopNeedsSelection = errors.New("no conversation selected")

// claudeDesktopConversation is one conversation in a claude.ai data export
// (conversations.json is a JSON array of these objects).
type claudeDesktopConversation struct {
	UUID         string                 `json:"uuid"`
	Name         string                 `json:"name"`
	CreatedAt    string                 `json:"created_at"`
	ChatMessages []claudeDesktopMessage `json:"chat_messages"`
}

// claudeDesktopMessage is one message in a claude.ai export. The text is in
// Text, or in Content items of type "text".
type claudeDesktopMessage struct {
	Sender  string              `json:"sender"`
	Text    string              `json:"text"`
	Content []claudeDesktopItem `json:"content"`
}

// claudeDesktopItem is one content item in a claude.ai export message.
type claudeDesktopItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// exportClaudeDesktopTranscript parses a claude.ai data export (the export
// .zip or its bare conversations.json), selects the conversation for
// sessionID (uuid or exact name), renders it to plain text, and redacts it
// through the same path as other transcripts. When sessionID is empty it
// lists up to 20 conversations on stdout and returns
// errClaudeDesktopNeedsSelection. The returned path is a redacted temporary
// file; cleanup removes every temporary file the export created. Unredacted
// export text is never uploaded.
func exportClaudeDesktopTranscript(ctx context.Context, exportPath, sessionID string, deps *TranscriptDeps, stdout io.Writer) (string, map[string]int, string, bool, func(), error) {
	conversations, err := parseClaudeDesktopExport(exportPath, deps)
	if err != nil {
		return "", nil, "", false, nil, err
	}
	if sessionID == "" {
		listClaudeDesktopConversations(conversations, stdout)
		return "", nil, "", false, nil, errClaudeDesktopNeedsSelection
	}
	conv, err := selectClaudeDesktopConversation(conversations, sessionID)
	if err != nil {
		return "", nil, "", false, nil, err
	}
	rendered := renderClaudeDesktopConversation(*conv)
	if err := os.MkdirAll(deps.CacheDir, 0o700); err != nil {
		return "", nil, "", false, nil, fmt.Errorf("create export directory: %w", err)
	}
	renderedPath := filepath.Join(deps.CacheDir, "claude-desktop-"+conv.UUID+".txt")
	if err := os.WriteFile(renderedPath, []byte(rendered), 0o600); err != nil {
		return "", nil, "", false, nil, fmt.Errorf("write rendered conversation: %w", err)
	}
	outPath, counts, leak, cleanup, err := redactSessionFile(renderedPath, 0, deps)
	if err != nil {
		_ = os.Remove(renderedPath)
		return "", nil, "", false, nil, err
	}
	fullCleanup := func() {
		cleanup()
		_ = os.Remove(renderedPath)
	}
	return outPath, counts, filepath.Base(renderedPath), leak, fullCleanup, nil
}

// parseClaudeDesktopExport reads a claude.ai data export: either the export
// .zip (conversations.json inside) or the bare conversations.json. Any other
// shape is rejected; no other format is guessed.
func parseClaudeDesktopExport(exportPath string, deps *TranscriptDeps) ([]claudeDesktopConversation, error) {
	readFile := deps.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(exportPath)
	if err != nil {
		return nil, fmt.Errorf("read export %q: %w", exportPath, err)
	}
	var jsonBytes []byte
	if strings.HasSuffix(strings.ToLower(exportPath), ".zip") {
		jsonBytes, err = claudeDesktopConversationsFromZip(data)
		if err != nil {
			return nil, err
		}
	} else {
		jsonBytes = data
	}
	var conversations []claudeDesktopConversation
	if err := json.Unmarshal(jsonBytes, &conversations); err != nil {
		return nil, errors.New("not a claude.ai export: expected conversations.json")
	}
	return conversations, nil
}

// claudeDesktopConversationsFromZip returns the conversations.json entry of a
// claude.ai export zip, or the not-an-export error when the zip is missing or
// does not contain it.
func claudeDesktopConversationsFromZip(data []byte) ([]byte, error) {
	const wanted = "conversations.json"
	notAnExport := errors.New("not a claude.ai export: expected conversations.json")
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, notAnExport
	}
	for _, entry := range reader.File {
		if strings.HasSuffix(entry.Name, "/") || filepath.Base(entry.Name) != wanted {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, notAnExport
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, notAnExport
		}
		return content, nil
	}
	return nil, notAnExport
}

// selectClaudeDesktopConversation picks the conversation whose uuid matches
// sessionID, or whose name matches it exactly.
func selectClaudeDesktopConversation(conversations []claudeDesktopConversation, sessionID string) (*claudeDesktopConversation, error) {
	for i := range conversations {
		if conversations[i].UUID == sessionID {
			return &conversations[i], nil
		}
	}
	for i := range conversations {
		if conversations[i].Name == sessionID {
			return &conversations[i], nil
		}
	}
	return nil, fmt.Errorf("no Claude Desktop conversation %q in the export", sessionID)
}

// renderClaudeDesktopConversation renders a conversation as plain text, one
// "sender: text" line per message. Message text comes from Text, or from the
// content items of type "text" when Text is empty.
func renderClaudeDesktopConversation(conv claudeDesktopConversation) string {
	var b strings.Builder
	for _, message := range conv.ChatMessages {
		text := message.Text
		if text == "" {
			var parts []string
			for _, item := range message.Content {
				if item.Type == "text" {
					parts = append(parts, item.Text)
				}
			}
			text = strings.Join(parts, "")
		}
		fmt.Fprintf(&b, "%s: %s\n", message.Sender, text)
	}
	return b.String()
}

// listClaudeDesktopConversations prints up to 20 conversations (uuid, name,
// date) newest first so the user can pick one with --session.
func listClaudeDesktopConversations(conversations []claudeDesktopConversation, stdout io.Writer) {
	const limit = 20
	sorted := make([]claudeDesktopConversation, len(conversations))
	copy(sorted, conversations)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, oki := claudeDesktopTime(sorted[i].CreatedAt)
		tj, okj := claudeDesktopTime(sorted[j].CreatedAt)
		if oki && okj {
			return ti.After(tj)
		}
		return oki && !okj
	})
	if len(sorted) > limit {
		fmt.Fprintf(stdout, "Conversations in the claude.ai export (showing %d of %d, newest first):\n", limit, len(sorted))
	} else {
		fmt.Fprintln(stdout, "Conversations in the claude.ai export (newest first):")
	}
	for i, conv := range sorted {
		if i >= limit {
			break
		}
		fmt.Fprintf(stdout, "  %s  %s  %s\n", conv.UUID, conv.Name, claudeDesktopDate(conv.CreatedAt))
	}
}

func claudeDesktopTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	return t, err == nil
}

func claudeDesktopDate(value string) string {
	if t, ok := claudeDesktopTime(value); ok {
		return t.UTC().Format("2006-01-02")
	}
	if value == "" {
		return "unknown date"
	}
	return value
}
