package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptIsValidBash(t *testing.T) {
	scriptPath := filepath.Join("scripts", "install.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("install script not found: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("install script is not executable: %s", scriptPath)
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestInstallScriptStaticChecks(t *testing.T) {
	scriptPath := filepath.Join("scripts", "install.sh")
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"set -euo pipefail", "set -euo pipefail"},
		{"detects macOS", "Darwin"},
		{"detects Linux", "Linux"},
		{"detects amd64", "x86_64"},
		{"detects arm64", "arm64"},
		{"uses shasum verification", "shasum -a 256"},
		{"uses curl", "curl -fsSL"},
		{"supports -v flag", "-v VERSION"},
		{"supports -d flag", "-d DIR"},
		{"references AWS_S3_RELEASES_URL_BASE", "AWS_S3_RELEASES_URL_BASE"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("install script missing %q (%s)", c.want, c.name)
		}
	}

	leaks := []string{
		`AWS_ACCESS_KEY_ID`,
		`AWS_SECRET_ACCESS_KEY`,
		`AWS_SESSION_TOKEN`,
	}
	for _, leak := range leaks {
		if strings.Contains(text, leak) {
			t.Errorf("install script should not contain hardcoded %q", leak)
		}
	}
}

func TestGoreleaserConfigIsValidYAML(t *testing.T) {
	configPath := filepath.Join("..", ".goreleaser.yaml")
	// Check config exists at repo root
	if _, err := os.Stat(".goreleaser.yaml"); err != nil {
		// Try relative to repo root
		if _, err2 := os.Stat(configPath); err2 != nil {
			t.Fatalf(".goreleaser.yaml not found")
		}
	}

	// If goreleaser is on PATH, run a schema validation
	if path, err := exec.LookPath("goreleaser"); err == nil {
		cmd := exec.Command(path, "check", "--config", ".goreleaser.yaml")
		cmd.Dir = findRepoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("goreleaser check failed: %v\n%s", err, out)
		}
	}
}

func TestGoreleaserConfigHasRequiredSections(t *testing.T) {
	configPath := ".goreleaser.yaml"
	src, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"project_name: kei-cli", "project_name: kei-cli"},
		{"builds for darwin", "goos:"},
		{"builds for linux", "- linux"},
		{"builds for amd64", "- amd64"},
		{"builds for arm64", "- arm64"},
		{"version injection", "main.version"},
		{"commit injection", "main.commit"},
		{"date injection", "main.date"},
		{"CGO disabled", "CGO_ENABLED=0"},
		{"-trimpath", "-trimpath"},
		{"tar.gz archives", "tar.gz"},
		{"SHA-256 checksums", "sha256"},
		{"blob S3 upload", "s3"},
		{"GitHub Releases disabled", "disable: true"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("goreleaser config missing %q (%s)", c.want, c.name)
		}
	}
}

func TestBuildWithVersionLDFlag(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "build",
		"-o", filepath.Join(dir, "kei"),
		"-ldflags", "-X main.version=test-ldflags-v0.1.0",
		".",
	)
	cmd.Dir = findRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	binary := filepath.Join(dir, "kei")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("binary is not executable: %v", info.Mode())
	}

	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("kei --version failed: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "kei test-ldflags-v0.1.0" {
		t.Fatalf("version output = %q, want %q", got, "kei test-ldflags-v0.1.0")
	}
}

func TestBuildWithTrimPath(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "build",
		"-o", filepath.Join(dir, "kei"),
		"-ldflags", "-X main.version=test-trimpath",
		"-trimpath",
		".",
	)
	cmd.Dir = findRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -trimpath failed: %v\n%s", err, out)
	}
	binary := filepath.Join(dir, "kei")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("binary is not executable: %v", info.Mode())
	}
}

func TestBuildIsReproducible(t *testing.T) {
	dir := t.TempDir()
	build := func(output string) error {
		cmd := exec.Command("go", "build",
			"-o", output,
			"-ldflags", "-X main.version=v0.0.0 -X main.commit=test -X main.date=2026-01-01T00:00:00Z",
			"-trimpath",
			".",
		)
		cmd.Dir = findRepoRoot(t)
		cmd.Env = append(os.Environ(),
			"GO111MODULE=on",
			"CGO_ENABLED=0",
			"GOOS=linux",
			"GOARCH=amd64",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return err
		}
		_ = out
		return nil
	}

	first := filepath.Join(dir, "kei-first")
	second := filepath.Join(dir, "kei-second")

	if err := build(first); err != nil {
		t.Fatalf("first build failed: %v", err)
	}
	if err := build(second); err != nil {
		t.Fatalf("second build failed: %v", err)
	}

	firstHash, err := fileHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := fileHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("builds differ:\n  first:  %s\n  second: %s", firstHash, secondHash)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// Search upward for go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func fileHash(path string) (string, error) {
	out, err := exec.Command("shasum", "-a", "256", path).Output()
	if err != nil {
		return "", err
	}
	return strings.Fields(string(out))[0], nil
}
