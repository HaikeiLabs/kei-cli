package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeUpgradeRunner struct {
	args []string
	err  error
}

func (f *fakeUpgradeRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	f.args = append([]string{name}, args...)
	return f.err
}

func TestUpgradeReplacesCurrentBinary(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "kei")
	if err := os.WriteFile(current, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedDir := filepath.Join(dir, "gopath", "bin")
	if err := os.MkdirAll(installedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedDir, "kei"), []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeUpgradeRunner{}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, runner, func() (string, error) { return current, nil }, func() (string, error) { return installedDir, nil })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if len(runner.args) != 3 || runner.args[0] != "go" || runner.args[1] != "install" || runner.args[2] != upgradePackage+"@latest" {
		t.Fatalf("go install args = %v", runner.args)
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
	if !strings.Contains(stdout.String(), "Upgraded kei at "+current) {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
}

func TestUpgradePinsRequestedVersion(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "kei")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(installedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installedDir, "kei"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeUpgradeRunner{}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand([]string{"--version", "v0.2.0"}, &stdout, &stderr, runner, func() (string, error) { return current, nil }, func() (string, error) { return installedDir, nil })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if len(runner.args) != 3 || runner.args[2] != upgradePackage+"@v0.2.0" {
		t.Fatalf("go install args = %v", runner.args)
	}
}

func TestUpgradeInPlaceWhenRunningInstalledBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kei")
	if err := os.WriteFile(bin, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeUpgradeRunner{}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, runner, func() (string, error) { return bin, nil }, func() (string, error) { return dir, nil })
	if code != 0 {
		t.Fatalf("upgrade exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "in place at "+bin) {
		t.Fatalf("upgrade output = %q", stdout.String())
	}
}

func TestUpgradeReportsMissingGo(t *testing.T) {
	runner := &fakeUpgradeRunner{err: exec.ErrNotFound}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, runner, func() (string, error) { return "/tmp/kei", nil }, func() (string, error) { return "/tmp/bin", nil })
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "go command was not found") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUpgradeReportsInstallFailure(t *testing.T) {
	runner := &fakeUpgradeRunner{err: errors.New("module not found")}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand(nil, &stdout, &stderr, runner, func() (string, error) { return "/tmp/kei", nil }, func() (string, error) { return "/tmp/bin", nil })
	if code != 1 {
		t.Fatalf("upgrade exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "upgrade failed: module not found") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUpgradeRejectsPositionalArgs(t *testing.T) {
	runner := &fakeUpgradeRunner{}
	var stdout, stderr bytes.Buffer
	code := runUpgradeCommand([]string{"extra"}, &stdout, &stderr, runner, func() (string, error) { return "/tmp/kei", nil }, func() (string, error) { return "/tmp/bin", nil })
	if code != 2 {
		t.Fatalf("upgrade exit = %d, want 2", code)
	}
	if len(runner.args) != 0 {
		t.Fatalf("go install ran despite bad arguments: %v", runner.args)
	}
}
