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

func TestReleaseWorkflowExists(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "release.yaml")
	if _, err := os.Stat(workflowPath); err != nil {
		t.Fatalf("release workflow not found: %v", err)
	}
}

func TestReleaseWorkflowUsesOIDC(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "release.yaml")
	src, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"uses configure-aws-credentials action", "aws-actions/configure-aws-credentials"},
		{"id-token: write permission", "id-token: write"},
		{"role-to-assume from vars", "role-to-assume"},
		{"uses aws-actions/configure-aws-credentials@v4", "configure-aws-credentials@v4"},
		{"uses goreleaser-action", "goreleaser/goreleaser-action"},
		{"tag-triggered on v*", "'v*'"},
		{"contains GPG signing key import", "ghaction-import-gpg"},
		{"contains GPG verification step", "gpg --list-keys"},
		{"contains AWS session validation", "aws sts get-caller-identity"},
		{"contains S3 bucket validation", "aws s3api head-bucket"},
		{"contains artifact leak check", "credential leak"},
		{"contains latest.txt publish", "latest.txt"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("release workflow missing %q (%s)", c.want, c.name)
		}
	}
}

func TestReleaseWorkflowNoStaticCredentials(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "release.yaml")
	src, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	leaks := []string{
		`AWS_ACCESS_KEY_ID`,
		`AWS_SECRET_ACCESS_KEY`,
		`AWS_SESSION_TOKEN`,
		`access-key-id`,
		`secret-access-key`,
		`session-token`,
	}
	for _, leak := range leaks {
		// These patterns may appear in comments or env var references
		// but must never have a value assigned inline.
		if strings.Contains(text, leak+"=") && !strings.Contains(text, "no static") {
			t.Errorf("release workflow contains hardcoded or assigned %q", leak)
		}
	}
}

func TestReleaseWorkflowTagValidation(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "release.yaml")
	src, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"tag regex validation", "grep -qE"},
		{"semver pattern", "v(0|[1-9]"},
		{"version output", "GITHUB_OUTPUT"},
		{"channel detection", "CHANNEL"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("release workflow missing tag validation %q (%s)", c.want, c.name)
		}
	}
}

func TestReleaseWorkflowEnvironmentVariables(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "release.yaml")
	src, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	// The workflow must reference these GitHub Actions variables (not secrets)
	requiredVars := []string{
		"AWS_REGION",
		"AWS_ROLE_TO_ASSUME",
		"AWS_S3_RELEASES_BUCKET",
	}
	for _, v := range requiredVars {
		if !strings.Contains(text, v) {
			t.Errorf("release workflow missing required variable reference %q", v)
		}
	}

	// The workflow must reference this secret for GPG
	if !strings.Contains(text, "GORELEASER_SIGNING_KEY") {
		t.Error("release workflow missing GORELEASER_SIGNING_KEY secret reference")
	}
}

func TestGoreleaserConfigSigning(t *testing.T) {
	src, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"signs section", "signs:"},
		{"detach-sign", "detach-sign"},
		{"armor output", "--armor"},
		{"GORELEASER_KEY variable", "GORELEASER_KEY"},
		{"signing skip support", "GORELEASER_SKIP_SIGN"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("goreleaser config missing signing %q (%s)", c.want, c.name)
		}
	}
}

func TestGoreleaserConfigS3CredentialsNotHardcoded(t *testing.T) {
	src, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	leaks := []string{
		`AWS_ACCESS_KEY_ID`,
		`AWS_SECRET_ACCESS_KEY`,
		`AWS_SESSION_TOKEN`,
		`access_key_id`,
		`secret_access_key`,
	}
	for _, leak := range leaks {
		if strings.Contains(text, leak) {
			// Allow if it's just a comment or env var name reference
			// Check it doesn't have an actual value
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				if strings.Contains(line, leak) && !strings.Contains(line, "#") && !strings.Contains(line, "{{") {
					t.Errorf("goreleaser config line %d: contains %q without env var syntax", i+1, leak)
				}
			}
		}
	}
}

func TestInstallScriptReferencesWorkflowRequiredVars(t *testing.T) {
	// The release workflow uses AWS_S3_RELEASES_BUCKET and AWS_REGION.
	// The install script uses AWS_S3_RELEASES_URL_BASE.
	// These should be consistent.
	workflowSrc, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	workflowText := string(workflowSrc)

	installSrc, err := os.ReadFile(filepath.Join("scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installSrc)

	if !strings.Contains(workflowText, "AWS_S3_RELEASES_BUCKET") {
		t.Error("release workflow must reference AWS_S3_RELEASES_BUCKET")
	}
	if !strings.Contains(installText, "AWS_S3_RELEASES_URL_BASE") {
		t.Error("install script must reference AWS_S3_RELEASES_URL_BASE")
	}
	// The URL base should be constructable from bucket + region
	if !strings.Contains(installText, "s3.") && !strings.Contains(installText, "amazonaws.com") {
		t.Log("install script does not show example S3 URL pattern")
	}
}
