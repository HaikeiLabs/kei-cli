package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFeedbackRequestBytes = 4 << 20

type feedbackFiles []string

func (f *feedbackFiles) String() string { return strings.Join(*f, ",") }

func (f *feedbackFiles) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("file path must not be empty")
	}
	*f = append(*f, value)
	return nil
}

type feedbackSubmissionResponse struct {
	ReportID   string `json:"report_id"`
	ReceivedAt string `json:"received_at"`
}

func runFeedbackCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore, version string) int {
	flags := flag.NewFlagSet("feedback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	description := flags.String("description", "", "describe what happened and what you expected")
	yes := flags.Bool("yes", false, "submit without asking for confirmation")
	var files feedbackFiles
	flags.Var(&files, "file", "attach an evidence file (repeatable)")
	flags.Var(&files, "screenshot", "attach a screenshot (repeatable)")
	var sessions feedbackFiles
	flags.Var(&sessions, "session", "attach a session transcript file (repeatable), or a single session ID when --export is set")
	export := flags.String("export", "", "locate and attach a redacted harness transcript: claude, codex, or opencode")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "feedback accepts no positional arguments: %q\n", flags.Arg(0))
		return 2
	}
	descriptionText := strings.TrimSpace(*description)
	if descriptionText == "" || utf8.RuneCountInString(descriptionText) > 4000 {
		fmt.Fprintln(stderr, "feedback requires --description with 1 to 4000 characters")
		return 2
	}

	// Build the evidence list. --file and --screenshot are attached as-is.
	// Session transcripts are never sent unredacted: with --export the harness
	// transcript is located and redacted; without it each --session file path
	// is redacted into a temporary copy before upload.
	allFiles := append([]string(nil), files...)
	deps := feedbackTranscriptDeps()
	redactionCountsByPath := map[string]map[string]int{}
	displayNameByPath := map[string]string{}
	// runtimeTokenLeak is true when any redacted transcript contained a Kei
	// runtime token; leakLabels holds a human label per leaked transcript so a
	// separate warning can be printed. The token value itself is never kept.
	runtimeTokenLeak := false
	var leakLabels []string
	var cleanups []func()
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	addRedacted := func(sourcePath string, counter int) (string, error) {
		outPath, counts, leak, cleanup, err := redactSessionFile(sourcePath, counter, deps)
		if err != nil {
			return "", err
		}
		cleanups = append(cleanups, cleanup)
		redactionCountsByPath[outPath] = counts
		displayNameByPath[outPath] = filepath.Base(sourcePath)
		if leak {
			runtimeTokenLeak = true
			leakLabels = append(leakLabels, filepath.Base(sourcePath))
		}
		return outPath, nil
	}

	if *export != "" {
		if *export != "claude" && *export != "codex" && *export != "opencode" {
			fmt.Fprintln(stderr, "feedback: --export must be claude, codex, or opencode")
			return 2
		}
		if len(sessions) > 1 {
			fmt.Fprintln(stderr, "feedback: --export accepts at most one --session (a session ID)")
			return 2
		}
		sessionID := ""
		if len(sessions) == 1 {
			sessionID = sessions[0]
		}
		path, counts, sourceName, leak, cleanup, err := exportTranscript(context.Background(), *export, sessionID, deps)
		if err != nil {
			fmt.Fprintf(stderr, "feedback: %v\n", err)
			return 1
		}
		cleanups = append(cleanups, cleanup)
		redactionCountsByPath[path] = counts
		displayNameByPath[path] = sourceName
		if leak {
			runtimeTokenLeak = true
			leakLabels = append(leakLabels, *export)
		}
		allFiles = append(allFiles, path)
	} else {
		for index, path := range sessions {
			outPath, err := addRedacted(path, index)
			if err != nil {
				fmt.Fprintf(stderr, "feedback: %v\n", err)
				return 1
			}
			allFiles = append(allFiles, outPath)
		}
	}

	if len(allFiles) > 8 {
		fmt.Fprintln(stderr, "feedback accepts at most 8 evidence files")
		return 2
	}

	totalSize := int64(0)
	fileSizes := make([]int64, 0, len(allFiles))
	for _, path := range allFiles {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(stderr, "feedback: inspect %q: %v\n", path, err)
			return 2
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintf(stderr, "feedback: evidence file %q is not a regular file\n", path)
			return 2
		}
		fileSizes = append(fileSizes, info.Size())
		totalSize += info.Size()
		if totalSize > maxFeedbackRequestBytes {
			fmt.Fprintln(stderr, "feedback evidence exceeds the 4 MiB request limit")
			return 2
		}
	}

	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stdout, "Feedback to submit:\n  Description: %q\n", descriptionText)
	if len(allFiles) == 0 {
		fmt.Fprintln(stdout, "  Evidence: none")
	} else {
		fmt.Fprintf(stdout, "  Evidence (%d file(s), %s):\n", len(allFiles), formatFeedbackSize(totalSize))
		for index, path := range allFiles {
			name := displayNameByPath[path]
			if name == "" {
				name = filepath.Base(path)
			}
			fmt.Fprintf(stdout, "    %q (%s)\n", name, formatFeedbackSize(fileSizes[index]))
			if counts, redacted := redactionCountsByPath[path]; redacted {
				fmt.Fprintf(stdout, "      Redactions: %s\n", formatRedactionCounts(counts))
			}
		}
	}
	// A runtime token in a transcript is a leak, not a routine redaction. Warn
	// on stderr (separate from the per-pattern counts) and never print the value.
	for _, label := range leakLabels {
		fmt.Fprintf(stderr, "WARNING: Kei runtime token found in the %s transcript. This is a leak: rotate the credential (kei bot credential --rotate) and report where it came from.\n", label)
	}
	if !*yes {
		fmt.Fprint(stdout, "Send? [y/N] ")
		answer, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(stderr, "feedback: read confirmation: %v\n", err)
			return 1
		}
		if answer = strings.TrimSpace(answer); !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			fmt.Fprintln(stdout, "Feedback not submitted.")
			return 0
		}
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"description":      descriptionText,
		"page":             "/cli",
		"client_timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"user_agent":       fmt.Sprintf("kei-cli/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH),
		"source":           "cli",
		// Boolean only; the token value is never sent.
		"runtime_token_leak_detected": strconv.FormatBool(runtimeTokenLeak),
	} {
		if err := multipartWriter.WriteField(key, value); err != nil {
			fmt.Fprintf(stderr, "feedback: encode request: %v\n", err)
			return 1
		}
	}
	for _, path := range allFiles {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(stderr, "feedback: open %q: %v\n", path, err)
			return 1
		}
		filename := filepath.Base(path)
		if name, ok := displayNameByPath[path]; ok {
			filename = name
		}
		part, err := multipartWriter.CreateFormFile("evidence", filename)
		if err != nil {
			_ = file.Close()
			fmt.Fprintf(stderr, "feedback: encode evidence %q: %v\n", path, err)
			return 1
		}
		remaining := int64(maxFeedbackRequestBytes - body.Len())
		_, err = io.Copy(part, io.LimitReader(file, remaining+1))
		_ = file.Close()
		if err != nil {
			fmt.Fprintf(stderr, "feedback: encode evidence %q: %v\n", path, err)
			return 1
		}
		if body.Len() > maxFeedbackRequestBytes {
			fmt.Fprintln(stderr, "feedback evidence exceeds the 4 MiB request limit")
			return 2
		}
	}
	if err := multipartWriter.Close(); err != nil {
		fmt.Fprintf(stderr, "feedback: finish request: %v\n", err)
		return 1
	}
	if body.Len() > maxFeedbackRequestBytes {
		fmt.Fprintln(stderr, "feedback evidence exceeds the 4 MiB request limit")
		return 2
	}

	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/bug-reports", &body)
	if err != nil {
		fmt.Fprintf(stderr, "feedback: build request: %v\n", err)
		return 1
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintf(stderr, "feedback: submit report: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		fmt.Fprintf(stderr, "feedback: submission returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
		return 1
	}
	var submitted feedbackSubmissionResponse
	if err := json.NewDecoder(response.Body).Decode(&submitted); err != nil {
		fmt.Fprintf(stderr, "feedback: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Feedback submitted: %s\n", submitted.ReportID)
	return 0
}

func formatFeedbackSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f KiB", float64(size)/1024)
}
