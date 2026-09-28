package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type upgradeRoundTripper func(*http.Request) (*http.Response, error)

func (f upgradeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func releaseTestArchive(t *testing.T, binary string) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tr := tar.NewWriter(gz)
	if err := tr.WriteHeader(&tar.Header{Name: "kei", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Write([]byte(binary)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := out.Bytes()
	sum := sha256.Sum256(archive)
	return archive, fmt.Sprintf("%x", sum)
}

func releaseTestClient(handler func(string) (int, []byte)) *http.Client {
	return &http.Client{Transport: upgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
		status, body := handler(req.URL.String())
		return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
}

func TestUpgradeReplacesCurrentBinary(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "kei")
	if err := os.WriteFile(current, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, sum := releaseTestArchive(t, "new-binary")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/kei-cli/latest.txt") {
			return 200, []byte("0.2.0")
		}
		if strings.HasSuffix(url, ".tar.gz") {
			return 200, archive
		}
		return 200, []byte(fmt.Sprintf("%s  kei-cli_0.2.0_%s_%s.tar.gz\n", sum, testReleaseOS(), testReleaseArch()))
	})
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return current, nil }, func(string) string { return "https://release.test" })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("current binary content = %q", data)
	}
	info, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("current binary permissions = %v", info.Mode())
	}
	if !strings.Contains(stdout.String(), "Upgraded kei to 0.2.0 at "+current) {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
}

func testReleaseOS() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	return "Linux"
}

func testReleaseArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x86_64"
}

func TestUpgradePinsRequestedVersion(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "kei")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, sum := releaseTestArchive(t, "new")
	var requestedURL string
	client := releaseTestClient(func(url string) (int, []byte) {
		requestedURL = url
		if strings.HasSuffix(url, ".tar.gz") {
			return 200, archive
		}
		return 200, []byte(fmt.Sprintf("%s  kei-cli_0.2.0_%s_%s.tar.gz\n", sum, testReleaseOS(), testReleaseArch()))
	})
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand([]string{"--version", "v0.2.0"}, &stdout, &stderr, client, func() (string, error) { return current, nil }, func(string) string { return "https://release.test" })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(requestedURL, "/v0.2.0/") || !strings.Contains(requestedURL, "/0.2.0/") {
		t.Fatalf("pinned release URL = %s", requestedURL)
	}
	if !strings.Contains(stdout.String(), "0.2.0") {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
}

func TestUpgradeInPlaceWhenRunningInstalledBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kei")
	if err := os.WriteFile(bin, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, sum := releaseTestArchive(t, "replacement")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		if strings.HasSuffix(url, ".tar.gz") {
			return 200, archive
		}
		return 200, []byte(fmt.Sprintf("%s  kei-cli_0.2.0_%s_%s.tar.gz\n", sum, testReleaseOS(), testReleaseArch()))
	})
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return bin, nil }, func(string) string { return "https://release.test" })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Fatalf("current binary content = %q", data)
	}
}

func TestUpgradeReportsLatestReleaseFailure(t *testing.T) {
	client := releaseTestClient(func(string) (int, []byte) { return http.StatusNotFound, nil })
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return "/tmp/kei", nil }, func(string) string { return "https://release.test" })
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "resolve latest release") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUpgradeReportsInstallFailure(t *testing.T) {
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		return http.StatusNotFound, nil
	})
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return "/tmp/kei", nil }, func(string) string { return "https://release.test" })
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "download release archive") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUpgradeRejectsPositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand([]string{"extra"}, &stdout, &stderr, nil, func() (string, error) { return "/tmp/kei", nil }, func(string) string { return "" })
	if code != 2 {
		t.Fatalf("upgrade exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no positional arguments") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
