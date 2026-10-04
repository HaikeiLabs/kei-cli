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
	return releaseTestNamedArchive(t, "kei", binary)
}

func releaseTestNamedArchive(t *testing.T, name, binary string) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tr := tar.NewWriter(gz)
	if err := tr.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
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
	proxyArchive, proxySum := releaseTestNamedArchive(t, "kei-proxy", "proxy")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		if strings.Contains(url, "/kei-proxy/") && strings.HasSuffix(url, ".tar.gz") {
			return 200, proxyArchive
		}
		if strings.Contains(url, "/kei-proxy/") {
			return 200, []byte(fmt.Sprintf("%s  kei-proxy_0.2.0_%s_%s.tar.gz\n", proxySum, testReleaseOS(), testReleaseArch()))
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
	if !strings.Contains(stdout.String(), "Upgraded kei to 0.2.0 and kei-proxy to 0.2.0") {
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
	proxyArchive, proxySum := releaseTestNamedArchive(t, "kei-proxy", "proxy")
	var requestedURLs []string
	client := releaseTestClient(func(url string) (int, []byte) {
		requestedURLs = append(requestedURLs, url)
		if strings.HasSuffix(url, "/kei-proxy/latest.txt") {
			return 200, []byte("0.3.0")
		}
		if strings.Contains(url, "/kei-proxy/") && strings.HasSuffix(url, ".tar.gz") {
			return 200, proxyArchive
		}
		if strings.Contains(url, "/kei-proxy/") {
			return 200, []byte(fmt.Sprintf("%s  kei-proxy_0.3.0_%s_%s.tar.gz\n", proxySum, testReleaseOS(), testReleaseArch()))
		}
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
	if !strings.Contains(strings.Join(requestedURLs, "\n"), "/kei-cli/0.2.0/") || !strings.Contains(strings.Join(requestedURLs, "\n"), "/kei-proxy/0.3.0/") {
		t.Fatalf("release URLs = %v", requestedURLs)
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
	proxyArchive, proxySum := releaseTestNamedArchive(t, "kei-proxy", "proxy")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		if strings.Contains(url, "/kei-proxy/") && strings.HasSuffix(url, ".tar.gz") {
			return 200, proxyArchive
		}
		if strings.Contains(url, "/kei-proxy/") {
			return 200, []byte(fmt.Sprintf("%s  kei-proxy_0.2.0_%s_%s.tar.gz\n", proxySum, testReleaseOS(), testReleaseArch()))
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
	proxy, err := os.ReadFile(filepath.Join(dir, "kei-proxy"))
	if err != nil || string(proxy) != "proxy" {
		t.Fatalf("proxy binary = %q, err=%v", proxy, err)
	}
}

func TestUpgradeReportsLatestReleaseFailure(t *testing.T) {
	client := releaseTestClient(func(string) (int, []byte) { return http.StatusNotFound, nil })
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return "/tmp/kei", nil }, func(string) string { return "https://release.test" })
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "resolve latest kei release") {
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
	if !strings.Contains(stderr.String(), "kei release: download archive") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUpgradeProxyVerificationFailureLeavesBinariesUntouched(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "kei")
	proxy := filepath.Join(dir, "kei-proxy")
	if err := os.WriteFile(cli, []byte("old-cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proxy, []byte("old-proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, sum := releaseTestArchive(t, "new-cli")
	proxyArchive, _ := releaseTestNamedArchive(t, "kei-proxy", "new-proxy")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		if strings.Contains(url, "/kei-proxy/") && strings.HasSuffix(url, ".tar.gz") {
			return 200, proxyArchive
		}
		if strings.Contains(url, "/kei-proxy/") {
			return 200, []byte("bad-checksum  kei-proxy_0.2.0_" + testReleaseOS() + "_" + testReleaseArch() + ".tar.gz\n")
		}
		if strings.HasSuffix(url, ".tar.gz") {
			return 200, archive
		}
		return 200, []byte(fmt.Sprintf("%s  kei-cli_0.2.0_%s_%s.tar.gz\n", sum, testReleaseOS(), testReleaseArch()))
	})
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, client, func() (string, error) { return cli, nil }, func(string) string { return "https://release.test" })
	if code != 1 || !strings.Contains(stderr.String(), "kei-proxy release") {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for path, want := range map[string]string{cli: "old-cli", proxy: "old-proxy"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v", path, got, err)
		}
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
