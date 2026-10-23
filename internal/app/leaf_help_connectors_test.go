package app

import (
	"bytes"
	"strings"
	"testing"
)

// TestLeafHelpConnectors verifies that every migrated connector leaf command
// produces the standardised help/error output: -h -> stdout + exit 0,
// unknown flag -> stderr + exit 2. (c) rows (missing required flag) are
// skipped: no connector command has a simple required flag — workspace is
// optional (env-var fallback), provider/name on create are checked with
// custom logic (XOR provider/template), and manifests commands use
// positional args rather than required flags.
func TestLeafHelpConnectors(t *testing.T) {
	tests := []leafCase{
		// ---- connectors list ----
		{
			command:  "connectors",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors list [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors list [flags]",
		},

		// ---- connectors get ----
		{
			command:  "connectors",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors get [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors get [flags]",
		},

		// ---- connectors connect ----
		{
			command:  "connectors",
			args:     []string{"connect", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors connect [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"connect", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors connect [flags]",
		},

		// ---- connectors reconnect ----
		{
			command:  "connectors",
			args:     []string{"reconnect", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors reconnect [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"reconnect", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors reconnect [flags]",
		},

		// ---- connectors delete ----
		{
			command:  "connectors",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors delete [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors delete [flags]",
		},

		// ---- connectors test ----
		{
			command:  "connectors",
			args:     []string{"test", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors test [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"test", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors test [flags]",
		},

		// ---- connectors create ----
		{
			command:  "connectors",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors create [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors create [flags]",
		},

		// ---- connectors manifests list ----
		{
			command:  "connectors",
			args:     []string{"manifests", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors manifests list [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests list [flags]",
		},

		// ---- connectors manifests show ----
		{
			command:  "connectors",
			args:     []string{"manifests", "show", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors manifests show [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "show", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests show [flags]",
		},

		// ---- connectors manifests approve ----
		{
			command:  "connectors",
			args:     []string{"manifests", "approve", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors manifests approve [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "approve", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests approve [flags]",
		},

		// ---- connectors templates ----
		{
			command:  "connectors",
			args:     []string{"templates", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors templates [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"templates", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors templates [flags]",
		},
	}

	for _, tt := range tests {
		name := strings.ReplaceAll(tt.command+" "+strings.Join(tt.args, " "), " ", "_")
		t.Run(name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			gotCode := Main("test", tt.command, tt.args, stdout, stderr, strings.NewReader(""))

			if gotCode != tt.wantCode {
				t.Errorf("Main(%q, %q) exit code = %d, want %d\nstdout=%q\nstderr=%q",
					tt.command, tt.args, gotCode, tt.wantCode, stdout.String(), stderr.String())
			}
			if tt.wantOut != "" && !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout missing %q:\n%s", tt.wantOut, stdout.String())
			}
			if tt.wantOut == "" && stdout.Len() > 0 {
				t.Errorf("unexpected stdout:\n%s", stdout.String())
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
			if tt.wantErr == "" && stderr.Len() > 0 {
				t.Errorf("unexpected stderr:\n%s", stderr.String())
			}
		})
	}
}
