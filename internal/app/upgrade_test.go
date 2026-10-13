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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type upgradeRoundTripper func(*http.Request) (*http.Response, error)

func (f upgradeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// releaseTestBundle builds a release archive from name/content pairs, in
// order, and returns it with its sha256.
func releaseTestBundle(t *testing.T, nameContent ...string) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tr := tar.NewWriter(gz)
	for i := 0; i+1 < len(nameContent); i += 2 {
		name, content := nameContent[i], nameContent[i+1]
		if err := tr.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
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

// releaseTestHandler serves a fake release endpoint. The kei-cli archive
// carries the kei-proxy pinned for that release; the standalone kei-proxy
// latest.txt and the published KEI_PROXY_VERSION file report decoy versions so
// tests can prove upgrade never uses them. Every request is recorded in
// requested.
func releaseTestHandler(keiLatest string, cliArchive []byte, cliSum string, requested *[]string) func(string) (int, []byte) {
	const base = "https://release.test"
	return func(url string) (int, []byte) {
		*requested = append(*requested, url)
		rel := strings.TrimPrefix(url, base+"/")
		parts := strings.SplitN(rel, "/", 3)
		if len(parts) < 2 {
			return http.StatusNotFound, nil
		}
		project, rest := parts[0], parts[1]
		switch {
		case project == "kei-cli" && rest == "latest.txt":
			return 200, []byte(keiLatest)
		case project == "kei-cli" && rest == "KEI_PROXY_VERSION":
			return 200, []byte("v0.8.8")
		case project == "kei-proxy" && rest == "latest.txt":
			return 200, []byte("0.9.9")
		}
		if len(parts) < 3 || project != "kei-cli" {
			return http.StatusNotFound, nil
		}
		file := parts[2]
		switch {
		case strings.HasSuffix(file, ".tar.gz"):
			return 200, cliArchive
		case strings.HasSuffix(file, "_checksums.txt"):
			archiveName := strings.TrimSuffix(file, "_checksums.txt") + "_" + testReleaseOS() + "_" + testReleaseArch() + ".tar.gz"
			return 200, []byte(fmt.Sprintf("%s  %s\n", cliSum, archiveName))
		}
		return http.StatusNotFound, nil
	}
}

// versionScriptBody is an executable stub that reports "name version" for any
// invocation, standing in for a real kei / kei-proxy binary.
func versionScriptBody(name, version string) string {
	return fmt.Sprintf("#!/bin/sh\necho '%s %s'\n", name, version)
}

func versionScript(t *testing.T, path, name, version string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(versionScriptBody(name, version)), 0o755); err != nil {
		t.Fatal(err)
	}
}

// installedPair stages a fake running kei and kei-proxy in a temporary
// directory, never the real install directory.
func installedPair(t *testing.T, keiVersion, proxyVersion string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	current := filepath.Join(dir, "kei")
	proxy := filepath.Join(dir, "kei-proxy")
	versionScript(t, current, "kei", keiVersion)
	versionScript(t, proxy, "kei-proxy", proxyVersion)
	return current, proxy
}

func runTestUpgrade(t *testing.T, args []string, current string, client *http.Client) (int, string, string) {
	t.Helper()
	// Isolate from any real Kei runtime service in the user's home so the
	// upgrade's service restart is a no-op unless a test opts in.
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(args, &stdout, &stderr, client, func() (string, error) { return current, nil }, func(string) string { return "https://release.test" })
	return code, stdout.String(), stderr.String()
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), want) {
		t.Fatalf("%s = %q, err=%v; want it to contain %q", path, data, err, want)
	}
}

func TestUpgradeAlreadyLatestNoWrite(t *testing.T) {
	current, proxy := installedPair(t, "0.2.0", "0.2.4")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "kei 0.2.0 is already the latest release") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	assertFileContains(t, current, "kei 0.2.0")
	assertFileContains(t, proxy, "kei-proxy 0.2.4")
	for _, u := range requested {
		if strings.HasSuffix(u, ".tar.gz") {
			t.Fatalf("no-op upgrade must not download archives, got %q", u)
		}
	}
}

func TestUpgradeLatestNewer(t *testing.T) {
	current, proxy := installedPair(t, "0.1.0", "0.2.4")
	bundled := versionScriptBody("kei-proxy", "0.2.5")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", bundled)
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "kei 0.1.0 -> 0.2.0, kei-proxy 0.2.4 -> 0.2.5") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != "new-binary" {
		t.Fatalf("current binary = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(proxy); err != nil || string(data) != bundled {
		t.Fatalf("proxy binary = %q, err=%v", data, err)
	}
}

// TestUpgradeRestartsServiceWhenInstalled proves a real upgrade restarts the
// runtime service (through the sysCommand seam) and reports the new versions.
func TestUpgradeRestartsServiceWhenInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svcPath := serviceDefinitionPath()
	if svcPath == "" {
		t.Skip("unsupported platform")
	}
	if err := os.MkdirAll(filepath.Dir(svcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svcPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotArgs []string
	oldSysCommand := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		gotName, gotArgs = name, args
		return exec.Command("true")
	}
	defer func() { sysCommand = oldSysCommand }()

	current, _ := installedPair(t, "0.1.0", "0.2.4")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)), func() (string, error) { return current, nil }, func(string) string { return "https://release.test" })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Restarted the Kei runtime service; it now runs kei 0.2.0 / kei-proxy 0.2.5.") {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
	switch runtime.GOOS {
	case "darwin":
		if gotName != "launchctl" || !strings.Contains(strings.Join(gotArgs, " "), "kickstart") {
			t.Fatalf("expected launchctl kickstart, got %s %v", gotName, gotArgs)
		}
	case "linux":
		if gotName != "systemctl" || !strings.Contains(strings.Join(gotArgs, " "), "restart") {
			t.Fatalf("expected systemctl restart, got %s %v", gotName, gotArgs)
		}
	default:
		t.Skip("unsupported platform")
	}
}

// TestUpgradeDoesNotRestartServiceWhenNotInstalled proves an upgrade that
// replaces the binaries does not install or touch an uninstalled service.
func TestUpgradeDoesNotRestartServiceWhenNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	oldSysCommand := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		called = true
		return exec.Command("true")
	}
	defer func() { sysCommand = oldSysCommand }()

	current, _ := installedPair(t, "0.1.0", "0.2.4")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)), func() (string, error) { return current, nil }, func(string) string { return "https://release.test" })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Restarted the Kei runtime service") {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
	if called {
		t.Fatalf("sysCommand must not run when the service is not installed")
	}
}

func TestUpgradePinnedProxyWins(t *testing.T) {
	current, proxy := installedPair(t, "0.1.0", "0.2.4")
	// The latest kei-proxy is 0.9.9 and the published pin file says 0.8.8,
	// but kei-cli 0.2.0 ships kei-proxy 0.2.5.
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	for _, u := range requested {
		if strings.Contains(u, "/kei-proxy/") || strings.HasSuffix(u, "/KEI_PROXY_VERSION") {
			t.Fatalf("upgrade must install the proxy bundled with the release, but fetched %q", u)
		}
	}
	if !strings.Contains(stdout, "kei-proxy 0.2.4 -> 0.2.5") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	assertFileContains(t, proxy, "kei-proxy 0.2.5")
}

func TestUpgradeExplicitVersionDowngrade(t *testing.T) {
	current, proxy := installedPair(t, "0.2.0", "0.2.5")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "old-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.3"))
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, []string{"--version", "0.1.0"}, current, releaseTestClient(releaseTestHandler("0.3.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(strings.Join(requested, "\n"), "/kei-cli/0.1.0/") {
		t.Fatalf("expected kei-cli 0.1.0 to be fetched, got %v", requested)
	}
	// The proxy is the one pinned for 0.1.0, not the latest release's pin.
	if !strings.Contains(stdout, "kei 0.2.0 -> 0.1.0, kei-proxy 0.2.5 -> 0.2.3") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	assertFileContains(t, proxy, "kei-proxy 0.2.3")
}

func TestUpgradeForceReinstall(t *testing.T) {
	current, _ := installedPair(t, "0.2.0", "0.2.5")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, []string{"--force"}, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "kei 0.2.0 -> 0.2.0, kei-proxy 0.2.5 -> 0.2.5") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != "new-binary" {
		t.Fatalf("current binary = %q, err=%v", data, err)
	}
}

func TestUpgradeRefusesOlderLatest(t *testing.T) {
	current, proxy := installedPair(t, "0.3.0", "0.2.5")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary", "kei-proxy", versionScriptBody("kei-proxy", "0.2.4"))
	var requested []string
	// The published latest (0.2.0) is older than the running 0.3.0.
	code, _, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "older than the running version") {
		t.Fatalf("stderr = %q", stderr)
	}
	assertFileContains(t, current, "kei 0.3.0")
	assertFileContains(t, proxy, "kei-proxy 0.2.5")
}

func TestUpgradeReleaseWithoutBundledProxyKeepsProxy(t *testing.T) {
	current, proxy := installedPair(t, "0.1.0", "0.2.4")
	cliArchive, cliSum := releaseTestBundle(t, "kei", "new-binary")
	var requested []string
	code, stdout, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, cliSum, &requested)))
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "kei 0.1.0 -> 0.2.0, kei-proxy 0.2.4 (unchanged") {
		t.Fatalf("upgrade output = %q", stdout)
	}
	assertFileContains(t, proxy, "kei-proxy 0.2.4")
}

func TestUpgradeReportsLatestReleaseFailure(t *testing.T) {
	current := filepath.Join(t.TempDir(), "kei")
	client := releaseTestClient(func(string) (int, []byte) { return http.StatusNotFound, nil })
	code, _, stderr := runTestUpgrade(t, nil, current, client)
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "resolve latest kei release") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestUpgradeReportsInstallFailure(t *testing.T) {
	current := filepath.Join(t.TempDir(), "kei")
	client := releaseTestClient(func(url string) (int, []byte) {
		if strings.HasSuffix(url, "/latest.txt") {
			return 200, []byte("0.2.0")
		}
		return http.StatusNotFound, nil
	})
	code, _, stderr := runTestUpgrade(t, nil, current, client)
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "kei release: download archive") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestUpgradeChecksumFailureLeavesBinariesUntouched(t *testing.T) {
	current, proxy := installedPair(t, "0.1.0", "0.2.4")
	cliArchive, _ := releaseTestBundle(t, "kei", "new-cli", "kei-proxy", versionScriptBody("kei-proxy", "0.2.5"))
	var requested []string
	code, _, stderr := runTestUpgrade(t, nil, current, releaseTestClient(releaseTestHandler("0.2.0", cliArchive, "bad-checksum", &requested)))
	if code != 1 || !strings.Contains(stderr, "kei release") {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	assertFileContains(t, current, "kei 0.1.0")
	assertFileContains(t, proxy, "kei-proxy 0.2.4")
}

func TestUpgradeRejectsPositionalArgs(t *testing.T) {
	code, _, stderr := runTestUpgrade(t, []string{"extra"}, filepath.Join(t.TempDir(), "kei"), nil)
	if code != 2 {
		t.Fatalf("upgrade exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no positional arguments") {
		t.Fatalf("stderr = %q", stderr)
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
