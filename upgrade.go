package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// upgradeModule is this CLI's own Go module; upgrades always install it.
const upgradeModule = "github.com/HaikeiLabs/kei-cli"

type upgradeRunner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

type osExecRunner struct{}

func (osExecRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func gopathBinDir() (string, error) {
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOPATH: %w", err)
	}
	gopath := strings.TrimSpace(string(out))
	if gopath == "" {
		return "", errors.New("go env GOPATH returned an empty path")
	}
	return filepath.Join(gopath, "bin"), nil
}

// runUpgradeCommand installs the latest published version of the kei CLI
// module with `go install` and replaces the currently running binary with it.
func runUpgradeCommand(args []string, stdout, stderr io.Writer, runner upgradeRunner, executable func() (string, error), binDir func() (string, error)) int {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	upgradeVersion := flags.String("version", "latest", "module version to install")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "upgrade accepts no positional arguments")
		return 2
	}
	fmt.Fprintf(stdout, "Installing %s@%s...\n", upgradeModule, *upgradeVersion)
	if err := runner.Run(context.Background(), stdout, stderr, "go", "install", upgradeModule+"@"+*upgradeVersion); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintln(stderr, "upgrade failed: the go command was not found; install Go and ensure it is on PATH")
			return 1
		}
		fmt.Fprintf(stderr, "upgrade failed: %v\n", err)
		return 1
	}
	installedDir, err := binDir()
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: %v\n", err)
		return 1
	}
	installed := filepath.Join(installedDir, "kei-cli")
	current, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: locate current executable: %v\n", err)
		return 1
	}
	same, err := sameFile(current, installed)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: compare binaries: %v\n", err)
		return 1
	}
	if same {
		fmt.Fprintf(stdout, "Upgraded kei in place at %s.\n", current)
		return 0
	}
	if err := replaceExecutable(current, installed); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", current, err)
		fmt.Fprintf(stderr, "The new binary is at %s; copy it over %s manually if that location is not writable.\n", installed, current)
		return 1
	}
	fmt.Fprintf(stdout, "Upgraded kei at %s.\n", current)
	return 0
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

func replaceExecutable(target, source string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".kei-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	in, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("open installed binary: %w", err)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		in.Close()
		tmp.Close()
		return fmt.Errorf("copy installed binary: %w", err)
	}
	if err := in.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("set executable permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("replace current binary: %w", err)
	}
	return nil
}
