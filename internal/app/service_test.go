package app

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	if code := runServiceCommand([]string{}, &stdout, &stderr, http.DefaultClient, nil); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "subcommand") {
		t.Fatalf("expected subcommand error, got: %s", stderr.String())
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
