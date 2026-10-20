package docs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runDocsTest starts an httptest server and runs runDocsCommand with
// --base-url pointing at the server.
func runDocsWithOutput(t *testing.T, handler http.HandlerFunc, args ...string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	fullArgs := append([]string{"--base-url", server.URL}, args...)
	var stdout, stderr bytes.Buffer
	code := Run(fullArgs, &stdout, &stderr, DefaultBaseURL)
	return code, stdout.String(), stderr.String()
}

func TestDocsFetchByFullSlug(t *testing.T) {
	indexJSON := `[
		{"slug": "kei/get-started/sign-in", "title": "Sign in to Kei", "path": "/kei/get-started/sign-in", "product": "kei"},
		{"slug": "kei/get-started/quickstart", "title": "Quickstart", "path": "/kei/get-started/quickstart", "product": "kei"}
	]`
	markdown := "# Sign In\n\nWelcome to Kei.\n"
	requests := struct {
		index bool
		page  bool
	}{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			if got := r.Header.Get("X-Kei-Client"); got != "" {
				t.Errorf("index request carried X-Kei-Client: %q", got)
			}
			requests.index = true
			_, _ = w.Write([]byte(indexJSON))
		case "/kei/get-started/sign-in.md":
			if got := r.Header.Get("X-Kei-Client"); got != "" {
				t.Errorf("page request carried X-Kei-Client: %q", got)
			}
			requests.page = true
			_, _ = w.Write([]byte(markdown))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}
	code, stdout, stderr := runDocsWithOutput(t, handler, "kei/get-started/sign-in")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !requests.index {
		t.Error("index.json was not fetched")
	}
	if !requests.page {
		t.Error("page markdown was not fetched")
	}
	if stdout != markdown {
		t.Errorf("stdout = %q, want %q", stdout, markdown)
	}
}

func TestDocsFetchByUniqueLastSegment(t *testing.T) {
	indexJSON := `[
		{"slug": "kei/get-started/sign-in", "title": "Sign in to Kei", "path": "/kei/get-started/sign-in", "product": "kei"},
		{"slug": "kei/get-started/quickstart", "title": "Quickstart", "path": "/kei/get-started/quickstart", "product": "kei"}
	]`
	markdown := "# Sign In\n"
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write([]byte(indexJSON))
		case "/kei/get-started/sign-in.md":
			_, _ = w.Write([]byte(markdown))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}
	code, stdout, stderr := runDocsWithOutput(t, handler, "sign-in")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if stdout != markdown {
		t.Errorf("stdout = %q, want %q", stdout, markdown)
	}
}

func TestDocsFetchAmbiguousLastSegment(t *testing.T) {
	indexJSON := `[
		{"slug": "kei/get-started/sign-in", "title": "Sign in to Kei", "path": "/kei/get-started/sign-in", "product": "kei"},
		{"slug": "kei/guides/sign-in", "title": "Sign In Guide", "path": "/kei/guides/sign-in", "product": "kei"}
	]`
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(indexJSON))
	}
	code, stdout, stderr := runDocsWithOutput(t, handler, "sign-in")
	if code != 2 {
		t.Fatalf("expected exit code 2 for ambiguous slug, got %d", code)
	}
	if !strings.Contains(stderr, "ambiguous") {
		t.Errorf("stderr should mention ambiguous: %s", stderr)
	}
	if !strings.Contains(stderr, "kei/get-started/sign-in") || !strings.Contains(stderr, "kei/guides/sign-in") {
		t.Errorf("stderr should list matching slugs: %s", stderr)
	}
	if stdout != "" {
		t.Errorf("expected no stdout, got: %s", stdout)
	}
}

func TestDocsFetchUnknownSlug(t *testing.T) {
	indexJSON := `[
		{"slug": "kei/get-started/sign-in", "title": "Sign in to Kei", "path": "/kei/get-started/sign-in", "product": "kei"}
	]`
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(indexJSON))
	}
	code, stdout, stderr := runDocsWithOutput(t, handler, "nonexistent")
	if code != 1 {
		t.Fatalf("expected exit code 1 for unknown slug, got %d", code)
	}
	if !strings.Contains(stderr, "not found") || !strings.Contains(stderr, "run kei docs") {
		t.Errorf("stderr should mention not found and hint: %s", stderr)
	}
	if stdout != "" {
		t.Errorf("expected no stdout, got: %s", stdout)
	}
}

func TestDocsList(t *testing.T) {
	indexJSON := `[
		{"slug": "kei/get-started/sign-in", "title": "Sign in to Kei", "path": "/kei/get-started/sign-in", "product": "kei"},
		{"slug": "kei/get-started/quickstart", "title": "Quickstart", "path": "/kei/get-started/quickstart", "product": "kei"}
	]`
	hit := false
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		hit = true
		_, _ = w.Write([]byte(indexJSON))
	}
	code, stdout, stderr := runDocsWithOutput(t, handler)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if !hit {
		t.Fatal("no request was made to the test server")
	}
	for _, want := range []string{"kei/get-started/sign-in", "Sign in to Kei", "kei/get-started/quickstart", "Quickstart"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}
