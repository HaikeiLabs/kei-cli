package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	configPath := filepath.Join(dir, "kei.yaml")
	content := `control_plane_url: "https://example.com"
runtime_token: "test-runtime-token-abc123"
harness_url: "https://example.com/harness"
proxy_path: "/usr/local/bin/kei-proxy"
proxy_registry: "ghcr.io/haikeilabs"
model_endpoint: "https://api.example.com/v1"
model: "gpt-4"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestRuntimeHeartbeatConfigNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", "/nonexistent/kei.yaml"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "read config") {
		t.Fatalf("expected config read error, got: %s", stderr.String())
	}
}

func TestRuntimeHeartbeatRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments") {
		t.Fatalf("expected positional args error, got: %s", stderr.String())
	}
}

func TestRuntimeHeartbeatPassesRuntimeTokenInEnv(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "captured.env")
	configPath := writeTestConfig(t, tmpDir)

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "env > "+envFile)
		cmd.Env = os.Environ()
		return cmd
	}
	defer func() { runCommand = oldRunCommand }()

	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("heartbeat exit = %d, stderr=%s", code, stderr.String())
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	if !strings.Contains(env, "KEI_RUNTIME_TOKEN=test-runtime-token-abc123") {
		t.Fatalf("expected KEI_RUNTIME_TOKEN in env, got:\n%s", env)
	}
	if !strings.Contains(env, "KEI_RUNTIME_CONTROL_PLANE_URL=https://example.com") {
		t.Fatalf("expected KEI_RUNTIME_CONTROL_PLANE_URL in env, got:\n%s", env)
	}
}

func TestRuntimeHeartbeatTokenNotInArgv(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeTestConfig(t, tmpDir)
	var capturedArgs []string

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = args
		cmd := exec.CommandContext(ctx, "true")
		return cmd
	}
	defer func() { runCommand = oldRunCommand }()

	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("heartbeat exit = %d, stderr=%s", code, stderr.String())
	}
	for _, arg := range capturedArgs {
		if strings.Contains(arg, "test-runtime-token") {
			t.Fatalf("token found in argv: %q", arg)
		}
	}
}

func TestRuntimeHeartbeatNoKeyringAccess(t *testing.T) {
	// The new runRuntimeHeartbeatCommand does not accept a credentialStore
	// parameter, so keyring access is structurally impossible.
	// This test verifies the function runs without needing keyring credentials.
	tmpDir := t.TempDir()
	configPath := writeTestConfig(t, tmpDir)

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "true")
		return cmd
	}
	defer func() { runCommand = oldRunCommand }()

	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("heartbeat exit = %d, stderr=%s", code, stderr.String())
	}
}

func TestRuntimeHeartbeatPropagatesCommandFailure(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := writeTestConfig(t, tmpDir)

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "false")
		return cmd
	}
	defer func() { runCommand = oldRunCommand }()

	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
}

func TestRuntimeHeartbeatInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(configPath, []byte("invalid: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runRuntimeHeartbeatCommand([]string{"--config", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for bad config, got %d", code)
	}
}

func TestRuntimeCommandRequiresSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runRuntimeCommand([]string{}, &stdout, &stderr, nil, nil); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "subcommand") {
		t.Fatalf("expected subcommand error, got: %s", stderr.String())
	}
}

func TestRuntimeCommandRejectsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runRuntimeCommand([]string{"unknown"}, &stdout, &stderr, nil, nil); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown runtime command") {
		t.Fatalf("expected unknown command error, got: %s", stderr.String())
	}
}

func TestRuntimeCommandDispatchesService(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// "runtime service" should dispatch without error (will fail on config read, not unknown command)
	code := runRuntimeCommand([]string{"service", "status"}, &stdout, &stderr, nil, nil)
	if code != 0 {
		// It's OK if it fails for other reasons, as long as it's not "unknown runtime command"
		if strings.Contains(stderr.String(), "unknown runtime command") {
			t.Fatalf("service was treated as unknown runtime command: %s", stderr.String())
		}
	}
}
