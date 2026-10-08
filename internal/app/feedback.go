package app

import (
	"bufio"
	"bytes"
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
	flags.Var(&files, "session", "attach a session transcript (repeatable)")
	flags.Var(&files, "screenshot", "attach a screenshot (repeatable)")
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
	if len(files) > 8 {
		fmt.Fprintln(stderr, "feedback accepts at most 8 evidence files")
		return 2
	}

	totalSize := int64(0)
	fileSizes := make([]int64, 0, len(files))
	for _, path := range files {
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
	if len(files) == 0 {
		fmt.Fprintln(stdout, "  Evidence: none")
	} else {
		fmt.Fprintf(stdout, "  Evidence (%d file(s), %s):\n", len(files), formatFeedbackSize(totalSize))
		for index, path := range files {
			fmt.Fprintf(stdout, "    %q (%s)\n", filepath.Base(path), formatFeedbackSize(fileSizes[index]))
		}
	}
	if !*yes {
		fmt.Fprint(stdout, "Submit this report? [y/N] ")
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
	} {
		if err := multipartWriter.WriteField(key, value); err != nil {
			fmt.Fprintf(stderr, "feedback: encode request: %v\n", err)
			return 1
		}
	}
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(stderr, "feedback: open %q: %v\n", path, err)
			return 1
		}
		part, err := multipartWriter.CreateFormFile("evidence", filepath.Base(path))
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
