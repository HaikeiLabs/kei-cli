package app

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// shape returns a canonical test leaf command and exercises the helpers.
// We test the public API of leaf_flags.go: newCommandFlags, parseLeaf,
// requireFlags, and the shared flag definitions.

func TestNewCommandFlags_usageToStdout(t *testing.T) {
	tests := []struct {
		label   string
		args    []string
		wantOut string // substring expected on stdout
		wantErr string // substring expected on stderr
		wantOK  bool
		wantRC  int
	}{
		{
			label:   "-h prints usage to stdout and exits 0",
			args:    []string{"-h"},
			wantOut: "Usage: kei connectors list [flags]",
			wantErr: "",
			wantOK:  false, // parseLeaf returns ok=false for help
			wantRC:  0,
		},
		{
			label:   "--help prints usage to stdout and exits 0",
			args:    []string{"--help"},
			wantOut: "Usage: kei connectors list [flags]",
			wantErr: "",
			wantOK:  false,
			wantRC:  0,
		},
		{
			label:   "unknown flag prints parse error and usage to stderr",
			args:    []string{"--bogus"},
			wantOut: "",
			wantErr: "flag provided but not defined",
			wantOK:  false,
			wantRC:  2,
		},
		{
			label:   "valid parse returns ok=true",
			args:    []string{"--workspace", "myws"},
			wantOut: "",
			wantErr: "",
			wantOK:  true,
			wantRC:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			lc := newCommandFlags("connectors", "list", "List data connectors", stdout, stderr)

			_ = workspaceFlag(lc.fs)
			rc, ok := parseLeaf(lc, tt.args)

			if ok != tt.wantOK {
				t.Errorf("parseLeaf ok = %v, want %v", ok, tt.wantOK)
			}
			if rc != tt.wantRC {
				t.Errorf("parseLeaf rc = %d, want %d", rc, tt.wantRC)
			}
			if tt.wantOut != "" && !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout missing %q:\n%s", tt.wantOut, stdout.String())
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
			if tt.wantOut == "" && stdout.Len() > 0 {
				t.Errorf("unexpected stdout:\n%s", stdout.String())
			}
			if tt.wantErr == "" && stderr.Len() > 0 {
				t.Errorf("unexpected stderr:\n%s", stderr.String())
			}
		})
	}
}

func TestNewCommandFlags_usageFormat(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("policies", "create", "Create a new policy", stdout, stderr)

	_ = workspaceFlag(lc.fs)
	_ = jsonFlag(lc.fs)
	_ = installationFlag(lc.fs)

	// Trigger help
	rc, ok := parseLeaf(lc, []string{"--help"})
	if ok {
		t.Fatal("parseLeaf should return ok=false for help")
	}
	if rc != 0 {
		t.Fatalf("parseLeaf rc = %d, want 0", rc)
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr output:\n%s", stderr.String())
	}

	out := stdout.String()
	// Must contain the usage header
	if !strings.Contains(out, "Usage: kei policies create [flags]") {
		t.Errorf("missing usage header in:\n%s", out)
	}
	// Must contain the summary
	if !strings.Contains(out, "Create a new policy") {
		t.Errorf("missing summary in:\n%s", out)
	}
	// Must contain "Flags:" label
	if !strings.Contains(out, "Flags:") {
		t.Errorf("missing Flags: label in:\n%s", out)
	}
	// Must contain each flag
	if !strings.Contains(out, "--workspace") {
		t.Errorf("missing --workspace in:\n%s", out)
	}
	if !strings.Contains(out, "--json") {
		t.Errorf("missing --json in:\n%s", out)
	}
	if !strings.Contains(out, "--installation") {
		t.Errorf("missing --installation in:\n%s", out)
	}
}

func TestParseLeaf_unknownFlag(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("groups", "list", "List groups", stdout, stderr)
	_ = workspaceFlag(lc.fs)

	rc, ok := parseLeaf(lc, []string{"--xyz"})
	if ok {
		t.Error("parseLeaf should return ok=false for unknown flag")
	}
	if rc != 2 {
		t.Errorf("parseLeaf rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("stderr missing parse error:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: kei groups list [flags]") {
		t.Errorf("stderr missing usage:\n%s", stderr.String())
	}
	if stdout.Len() > 0 {
		t.Errorf("unexpected stdout:\n%s", stdout.String())
	}
}

func TestRequireFlags(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("bot", "init", "Init a bot", stdout, stderr)
	w := workspaceFlag(lc.fs)
	i := installationFlag(lc.fs)

	// Parse with --workspace but no --installation
	rc, ok := parseLeaf(lc, []string{"--workspace", "myws"})
	if !ok {
		t.Fatalf("parseLeaf failed: rc=%d, stderr=%s", rc, stderr.String())
	}
	_ = w
	_ = i

	if requireFlags(lc.fs, stderr, "installation") {
		t.Error("requireFlags should return false when flag is missing")
	}
	if !strings.Contains(stderr.String(), "missing required flag --installation") {
		t.Errorf("stderr missing required-flag message:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: kei bot init [flags]") {
		t.Errorf("stderr missing usage:\n%s", stderr.String())
	}
}

func TestRequireFlags_allPresent(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("test", "cmd", "Test", stdout, stderr)
	n := lc.fs.String("name", "", "name")

	rc, ok := parseLeaf(lc, []string{"--name", "foo"})
	if !ok {
		t.Fatalf("parseLeaf failed: rc=%d, stderr=%s", rc, stderr.String())
	}
	_ = n

	if !requireFlags(lc.fs, stderr, "name") {
		t.Error("requireFlags should return true when flag is present")
	}
	if stderr.Len() > 0 {
		t.Errorf("unexpected stderr:\n%s", stderr.String())
	}
}

func TestRequireFlags_multipleMissing(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("test", "cmd", "Test", stdout, stderr)
	_ = lc.fs.String("a", "", "")
	_ = lc.fs.String("b", "", "")

	rc, ok := parseLeaf(lc, []string{})
	if !ok {
		t.Fatalf("parseLeaf failed: rc=%d, stderr=%s", rc, stderr.String())
	}
	_ = rc

	if requireFlags(lc.fs, stderr, "a", "b") {
		t.Error("requireFlags should return false when flags are missing")
	}
	if !strings.Contains(stderr.String(), "missing required flag --a") {
		t.Errorf("stderr missing --a:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing required flag --b") {
		t.Errorf("stderr missing --b:\n%s", stderr.String())
	}
}

func TestSharedFlagHelpText(t *testing.T) {
	// Verify the help text of each shared flag definition.
	tests := []struct {
		name    string
		setup   func(fs *flag.FlagSet)
		flag    string
		wantUse string
	}{
		{
			name:    "workspaceFlag",
			setup:   func(fs *flag.FlagSet) { workspaceFlag(fs) },
			flag:    "--workspace",
			wantUse: "workspace name or ID",
		},
		{
			name:    "jsonFlag",
			setup:   func(fs *flag.FlagSet) { jsonFlag(fs) },
			flag:    "--json",
			wantUse: "output as JSON",
		},
		{
			name:    "installationFlag",
			setup:   func(fs *flag.FlagSet) { installationFlag(fs) },
			flag:    "--installation",
			wantUse: "runtime installation ID",
		},
		{
			name:    "yesFlag",
			setup:   func(fs *flag.FlagSet) { yesFlag(fs) },
			flag:    "--yes",
			wantUse: "confirm without prompting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			tt.setup(fs)

			var found bool
			fs.VisitAll(func(f *flag.Flag) {
				if f.Name == tt.flag || "--"+f.Name == tt.flag {
					found = true
					if !strings.Contains(f.Usage, tt.wantUse) {
						t.Errorf("flag %q usage = %q, want substring %q", f.Name, f.Usage, tt.wantUse)
					}
				}
			})
			if !found {
				t.Errorf("flag %s not found in flag set", tt.flag)
			}
		})
	}
}

func TestLeafCommand_parseErrorAndMissingFlag(t *testing.T) {
	// Parse error (unknown flag) reports the error and usage to stderr.
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	lc := newCommandFlags("users", "list", "List users", stdout, stderr)
	_ = jsonFlag(lc.fs)

	rc, ok := parseLeaf(lc, []string{"--json", "--bogus"})
	if ok {
		t.Error("parseLeaf should fail on unknown flag")
	}
	if rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("stderr missing parse error:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: kei users list [flags]") {
		t.Errorf("stderr missing usage:\n%s", stderr.String())
	}
	if stdout.Len() > 0 {
		t.Errorf("unexpected stdout:\n%s", stdout.String())
	}
}
