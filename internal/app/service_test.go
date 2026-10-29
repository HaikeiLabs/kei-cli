package app

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("KEI_SERVICE_TEST_GUARD", "1")
	os.Exit(m.Run())
}

func TestServiceDefinitionPath(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	svcPath := serviceDefinitionPath()
	switch runtime.GOOS {
	case "darwin":
		want := "/testhome/Library/LaunchAgents/com.haikeilabs.kei-runtime.plist"
		if svcPath != want {
			t.Fatalf("serviceDefinitionPath = %q, want %q", svcPath, want)
		}
	case "linux":
		want := "/testhome/.config/systemd/user/kei.service"
		if svcPath != want {
			t.Fatalf("serviceDefinitionPath = %q, want %q", svcPath, want)
		}
	default:
		if svcPath != "" {
			t.Fatalf("serviceDefinitionPath = %q, want empty string on %s", svcPath, runtime.GOOS)
		}
	}
}

func TestServiceName(t *testing.T) {
	name := serviceName()
	switch runtime.GOOS {
	case "darwin":
		if name != "com.haikeilabs.kei-runtime" {
			t.Fatalf("serviceName = %q, want com.haikeilabs.kei-runtime", name)
		}
	case "linux":
		if name != "kei" {
			t.Fatalf("serviceName = %q, want kei", name)
		}
	default:
		if name != "kei" {
			t.Fatalf("serviceName = %q, want kei", name)
		}
	}
}

func TestServiceCommandRequiresSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServiceCommand([]string{}, &stdout, &stderr, http.DefaultClient, nil); code != 0 {
		t.Fatalf("expected exit 0 (help to stdout), got %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected help in stdout, got: %s", stdout.String())
	}
}

func TestServiceCommandRejectsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServiceCommand([]string{"unknown"}, &stdout, &stderr, http.DefaultClient, nil); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown service command") {
		t.Fatalf("expected unknown command error, got: %s", stderr.String())
	}
}

func TestServiceInstallRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServiceInstallCommand([]string{"extra"}, &stdout, &stderr, http.DefaultClient, nil); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments") {
		t.Fatalf("expected positional args error, got: %s", stderr.String())
	}
}

func TestServiceUninstallRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServiceUninstallCommand([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments") {
		t.Fatalf("expected positional args error, got: %s", stderr.String())
	}
}

func TestServiceStatusRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServiceStatusCommand([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments") {
		t.Fatalf("expected positional args error, got: %s", stderr.String())
	}
}

func TestServiceStatusNotInstalled(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/home/dir")
	svcPath := serviceDefinitionPath()
	if svcPath == "" {
		t.Skip("unsupported platform")
	}
	// Ensure the file does not exist
	os.Remove(svcPath)

	var stdout, stderr bytes.Buffer
	if code := runServiceStatusCommand([]string{}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not installed") {
		t.Fatalf("expected 'not installed', got: %s", stdout.String())
	}
}

func TestServiceInstallFailsWithBadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	badConfig := filepath.Join(tmpDir, "nonexistent.json")
	t.Setenv("HOME", tmpDir)

	var stdout, stderr bytes.Buffer
	if code := runServiceInstallCommand([]string{"--config", badConfig}, &stdout, &stderr, http.DefaultClient, nil); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "read config") {
		t.Fatalf("expected config read error, got: %s", stderr.String())
	}
}

func TestServiceInstallFailsUnsupportedPlatformByUnsettingHome(t *testing.T) {
	// Setting HOME to a non-existent dir makes serviceDefinitionPath return ""
	// but only if os.UserHomeDir() fails; on most platforms it won't fail.
	// Instead, we test the special case by checking the flag validation path first.
	// The unsupported platform branch is hit when runtime.GOOS is not darwin/linux.
	switch runtime.GOOS {
	case "darwin", "linux":
		t.Skip("this test only applies to unsupported platforms")
	}

	var stdout, stderr bytes.Buffer
	if code := runServiceInstallCommand([]string{}, &stdout, &stderr, http.DefaultClient, nil); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unsupported platform") {
		t.Fatalf("expected unsupported platform error, got: %s", stderr.String())
	}
}

func TestServiceUninstallFailsUnsupportedPlatform(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux":
		t.Skip("this test only applies to unsupported platforms")
	}

	var stdout, stderr bytes.Buffer
	if code := runServiceUninstallCommand([]string{}, &stdout, &stderr); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unsupported platform") {
		t.Fatalf("expected unsupported platform error, got: %s", stderr.String())
	}
}

func TestServiceDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	oldSysCommand := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	defer func() { sysCommand = oldSysCommand }()

	// Test that runServiceCommand dispatches to the correct subcommand
	tests := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{"install", []string{"install", "--config", "/tmp/test.json"}, "service install"},
		{"uninstall", []string{"uninstall"}, "service uninstall"},
		{"status", []string{"status"}, "service status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runServiceCommand(tt.args, &stdout, &stderr, http.DefaultClient, nil)
			_ = code // We just verify it dispatches without panicking
		})
	}
}

func TestServiceInstallConfigFlagDefault(t *testing.T) {
	// When HOME points to a directory with no config, the default path
	// resolves to an empty config and service install should fail.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	var stdout, stderr bytes.Buffer
	if code := runServiceInstallCommand([]string{}, &stdout, &stderr, http.DefaultClient, nil); code != 1 {
		t.Fatalf("expected exit 1 (config error), got %d", code)
	}
	if !strings.Contains(stderr.String(), "read config") && !strings.Contains(stderr.String(), "unsupported") {
		t.Fatalf("expected config read error or unsupported, got: %s", stderr.String())
	}
}

// TestRestartRuntimeServiceNotInstalled proves an upgrade/install never
// touches the service manager when the service is not installed.
func TestRestartRuntimeServiceNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	oldSysCommand := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		called = true
		return exec.Command("true")
	}
	defer func() { sysCommand = oldSysCommand }()

	restarted, err := restartRuntimeService()
	if err != nil {
		t.Fatalf("restart error: %v", err)
	}
	if restarted {
		t.Fatalf("expected no restart when the service is not installed")
	}
	if called {
		t.Fatalf("sysCommand must not run when the service is not installed")
	}
}

func TestServeAgentDefinitionPath(t *testing.T) {
	t.Setenv("HOME", "/testhome")
	agentPath := serveAgentDefinitionPathFor(runtime.GOOS)
	switch runtime.GOOS {
	case "darwin":
		want := "/testhome/Library/LaunchAgents/com.haikeilabs.kei-proxy-serve.plist"
		if agentPath != want {
			t.Fatalf("serveAgentDefinitionPathFor = %q, want %q", agentPath, want)
		}
	case "linux":
		want := "/testhome/.config/systemd/user/kei-proxy-serve.service"
		if agentPath != want {
			t.Fatalf("serveAgentDefinitionPathFor = %q, want %q", agentPath, want)
		}
	default:
		if agentPath != "" {
			t.Fatalf("serveAgentDefinitionPathFor = %q, want empty string on %s", agentPath, runtime.GOOS)
		}
	}
}

func TestServeAgentName(t *testing.T) {
	name := serveAgentNameFor(runtime.GOOS)
	switch runtime.GOOS {
	case "darwin":
		if name != "com.haikeilabs.kei-proxy-serve" {
			t.Fatalf("serveAgentNameFor = %q, want com.haikeilabs.kei-proxy-serve", name)
		}
	case "linux":
		if name != "kei-proxy-serve" {
			t.Fatalf("serveAgentNameFor = %q, want kei-proxy-serve", name)
		}
	default:
		if name != "kei-proxy-serve" {
			t.Fatalf("serveAgentNameFor = %q, want kei-proxy-serve", name)
		}
	}
}

// TestRestartServeAgentNotInstalled proves an upgrade/install never touches
// the service manager for the serve agent when it is not installed.
func TestRestartServeAgentNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	oldSysCommand := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		called = true
		return exec.Command("true")
	}
	defer func() { sysCommand = oldSysCommand }()

	restarted, err := restartServeAgent()
	if err != nil {
		t.Fatalf("restart error: %v", err)
	}
	if restarted {
		t.Fatalf("expected no restart when the serve agent is not installed")
	}
	if called {
		t.Fatalf("sysCommand must not run when the serve agent is not installed")
	}
}

// TestRestartServeAgentInstalled proves the restart goes through the
// sysCommand seam with the platform's restart command and never execs the real
// service manager.
func TestRestartServeAgentInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentPath := serveAgentDefinitionPathFor(runtime.GOOS)
	if agentPath == "" {
		t.Skip("unsupported platform")
	}
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, []byte("x"), 0o644); err != nil {
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

	restarted, err := restartServeAgent()
	if err != nil {
		t.Fatalf("restart error: %v", err)
	}
	if !restarted {
		t.Fatalf("expected a restart when the serve agent is installed")
	}
	var wantName string
	var wantArgs []string
	switch runtime.GOOS {
	case "darwin":
		wantName = "launchctl"
		wantArgs = []string{"kickstart", "-k", "gui/" + execUserID() + "/com.haikeilabs.kei-proxy-serve"}
	case "linux":
		wantName = "systemctl"
		wantArgs = []string{"--user", "restart", "kei-proxy-serve"}
	default:
		t.Skip("unsupported platform")
	}
	if gotName != wantName || strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("called %s %v, want %s %v", gotName, gotArgs, wantName, wantArgs)
	}
}

// TestRestartRuntimeServiceInstalled proves the restart goes through the
// sysCommand seam with the platform's restart command and never execs the real
// service manager.
func TestRestartRuntimeServiceInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	restarted, err := restartRuntimeService()
	if err != nil {
		t.Fatalf("restart error: %v", err)
	}
	if !restarted {
		t.Fatalf("expected a restart when the service is installed")
	}
	var wantName string
	var wantArgs []string
	switch runtime.GOOS {
	case "darwin":
		wantName = "launchctl"
		wantArgs = []string{"kickstart", "-k", "gui/" + execUserID() + "/com.haikeilabs.kei-runtime"}
	case "linux":
		wantName = "systemctl"
		wantArgs = []string{"--user", "restart", "kei"}
	default:
		t.Skip("unsupported platform")
	}
	if gotName != wantName || strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("called %s %v, want %s %v", gotName, gotArgs, wantName, wantArgs)
	}
}
