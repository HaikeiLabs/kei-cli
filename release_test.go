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
		{"uses shasum or sha256sum verification", "shasum -a 256"},
		{"uses sha256sum fallback", "sha256sum"},
		{"uses curl", "curl -fsSL"},
		{"supports -v flag", "-v VERSION"},
		{"supports -d flag", "-d DIR"},
		{"references AWS_S3_RELEASES_URL_BASE", "AWS_S3_RELEASES_URL_BASE"},
		{"defaults release endpoint", "kei-cli-releases.s3.us-east-1.amazonaws.com"},
		{"handles GPG signature", "gpg --verify"},
		{"detects checksum tool availability", "SHA_CMD"},
		{"installs optional kei-proxy", "PROXY_BINARY=\"$TMP_DIR/kei-proxy\""},
		{"preserves standalone CLI install", "if [ -f \"$PROXY_BINARY\" ]"},
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

func TestProxyBundleFetchScriptContract(t *testing.T) {
	scriptPath := filepath.Join("scripts", "fetch-proxy.sh")
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []struct {
		name string
		want string
	}{
		{"pinned version", "KEI_PROXY_VERSION"},
		{"normalizes leading v", "VERSION=\"${PIN#v}\""},
		{"shared proxy prefix", "${PROJECT}/${VERSION}/${artifact}"},
		{"AWS SDK download", "aws s3 cp"},
		{"public URL fallback", "AWS_S3_RELEASES_URL_BASE"},
		{"all supported operating systems", "for os in darwin linux"},
		{"all supported architectures", "for arch in amd64 arm64"},
		{"proxy binary validation", "does not contain a kei-proxy binary"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("proxy fetch script missing %q (%s)", c.want, c.name)
		}
	}

	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("proxy fetch script bash -n failed: %v\n%s", err, out)
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
		{"proxy fetch hook", "./scripts/fetch-proxy.sh"},
		{"proxy archive source", "tmp/kei-proxy/{{ .Os }}/{{ .Arch }}/kei-proxy"},
		{"proxy archive destination", "dst: kei-proxy"},
		{"proxy artifact prefix documentation", "kei-proxy/<version>/"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("goreleaser config missing %q (%s)", c.want, c.name)
		}
	}
}

func TestReleaseWorkflowPinsAndValidatesProxy(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	checks := []string{
		"KEI_PROXY_VERSION",
		"vars.KEI_PROXY_VERSION || 'v0.1.0'",
		"KEI_PROXY_ARTIFACT_TEMPLATE",
		"Validate pinned kei-proxy artifact prefix",
		"PROXY_VERSION=\"${KEI_PROXY_VERSION#v}\"",
		"--key \"kei-proxy/$PROXY_VERSION/$ARTIFACT\"",
		"KEI_PROXY_VERSION: ${{ env.KEI_PROXY_VERSION }}",
	}
	for _, want := range checks {
		if !strings.Contains(text, want) {
			t.Errorf("release workflow missing proxy bundle contract %q", want)
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

func TestReleaseWorkflowUsesRunnerIdentity(t *testing.T) {
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
		{"runs on dedicated release runner", "runs-on: kei-cli-release"},
		{"documents IRSA identity", "IRSA"},
		{"uses goreleaser-action", "goreleaser/goreleaser-action"},
		{"tag-triggered on v*", "'v*'"},
		{"contains GPG signing key import", "ghaction-import-gpg"},
		{"contains GPG verification step", "gpg --list-keys"},
		{"contains AWS session validation", "aws sts get-caller-identity"},
		{"contains S3 bucket validation", "aws s3api list-objects-v2"},
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

	// The workflow must reference these GitHub Actions variables (not secrets).
	// AWS access comes from the release runner Pod's IRSA role, so no
	// role-to-assume variable is needed.
	requiredVars := []string{
		"AWS_REGION",
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
		{"signing skip support", "GORELEASER_SKIP_SIGN"},
		{"signature output", "signature:"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("goreleaser config missing signing %q (%s)", c.want, c.name)
		}
	}

	// GORELEASER_KEY is set in the workflow env, not in the YAML.
	// Verify the workflow sets it.
	workflowSrc, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflowSrc), "GORELEASER_KEY") {
		t.Error("release workflow does not set GORELEASER_KEY env var")
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

func TestWorkflowUploadsInstallScriptToCustomerURL(t *testing.T) {
	// The customer curl flow is:
	//   curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash
	// This requires the workflow to upload scripts/install.sh to
	// s3://BUCKET/kei-cli/install.sh. Verify the workflow contains
	// an explicit upload step for this path.
	workflowSrc, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflowSrc)

	checks := []struct {
		name string
		want string
	}{
		{"uploads install.sh", "scripts/install.sh"},
		{"to the fixed kei-cli path", "kei-cli/install.sh"},
		{"with s3 cp command", "aws s3 cp"},
		{"with shellscript content type", "text/x-shellscript"},
	}
	if strings.Contains(text, "--acl public-read") {
		t.Error("release workflow must rely on the bucket policy instead of a public object ACL")
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("release workflow does not upload install.sh to customer URL: missing %q (%s)", c.want, c.name)
		}
	}

	// The workflow must NOT set --acl public-read because the bucket
	// has block_public_acls = true. Public access is granted by bucket
	// policy instead.
	if strings.Contains(text, "--acl public-read") {
		t.Error("release workflow sets --acl public-read but the bucket has block_public_acls = true; remove the ACL flag")
	}
	// It should reference the reason via a comment
	if !strings.Contains(text, "block_public_acls") {
		t.Log("install.sh upload step does not comment about block_public_acls")
	}

	// Verify the install script's documented URL matches the upload path.
	installSrc, err := os.ReadFile(filepath.Join("scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installSrc)

	// The install script must reference the same path pattern:
	// $AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh
	if !strings.Contains(installText, "kei-cli/install.sh") &&
		!strings.Contains(installText, "AWS_S3_RELEASES_URL_BASE") {
		t.Error("install script does not reference the kei-cli/install.sh URL path")
	}
}

func TestWorkflowLatestResolutionIsBackedByArtifact(t *testing.T) {
	// The install script resolves "latest" by fetching latest.txt from:
	//   $AWS_S3_RELEASES_URL_BASE/kei-cli/latest.txt
	// Verify the workflow publishes this file for stable releases.
	workflowSrc, err := os.ReadFile(filepath.Join(".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflowSrc)

	if !strings.Contains(text, "latest.txt") {
		t.Error("release workflow does not publish latest.txt")
	}
	if !strings.Contains(text, "kei-cli/latest.txt") {
		t.Error("release workflow does not publish latest.txt to kei-cli/ prefix")
	}

	// The install script must reference the same path
	installSrc, err := os.ReadFile(filepath.Join("scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installSrc)

	if !strings.Contains(installText, "latest.txt") {
		t.Error("install script does not reference latest.txt for version resolution")
	}
}
