package app

import (
	"bytes"
	"strings"
	"testing"
)

// TestLeafHelpMiscCommands drives Main for every migrated leaf command in
// STEP 1 files (runtime.go, service.go, upgrade.go, status.go, setup.go,
// logout.go) and verifies standardised help, unknown-flag error, and
// missing-required-flag behaviour.
//
// Skip (c) — missing-required-flag — for any command that would hit the
// network first.  Among STEP-1 commands only bot status has a required flag
// (--installation); its check fires before any network call.
func TestLeafHelpMiscCommands(t *testing.T) {
	tests := []leafCase{
		// ---- runtime bootstrap ----
		{
			command:  "runtime",
			args:     []string{"bootstrap", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei runtime bootstrap [flags]",
			wantErr:  "",
		},
		{
			command:  "runtime",
			args:     []string{"bootstrap", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei runtime bootstrap [flags]",
		},
		// (c) skipped: bootstrap has no required flags; would exec kei-proxy.

		// ---- runtime heartbeat ----
		{
			command:  "runtime",
			args:     []string{"heartbeat", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei runtime heartbeat [flags]",
			wantErr:  "",
		},
		{
			command:  "runtime",
			args:     []string{"heartbeat", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei runtime heartbeat [flags]",
		},
		// (c) skipped: heartbeat has no required flags; would exec kei-proxy.

		// ---- runtime service install ----
		{
			command:  "runtime",
			args:     []string{"service", "install", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei runtime service install [flags]",
			wantErr:  "",
		},
		{
			command:  "runtime",
			args:     []string{"service", "install", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei runtime service install [flags]",
		},
		// (c) skipped: service install has no required flags; would hit
		// filesystem (config read, service file write).

		// ---- runtime service uninstall ----
		{
			command:  "runtime",
			args:     []string{"service", "uninstall", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei runtime service uninstall [flags]",
			wantErr:  "",
		},
		{
			command:  "runtime",
			args:     []string{"service", "uninstall", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei runtime service uninstall [flags]",
		},
		// (c) skipped: service uninstall has no required flags; would unload
		// launchd/systemd.

		// ---- runtime service status ----
		{
			command:  "runtime",
			args:     []string{"service", "status", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei runtime service status [flags]",
			wantErr:  "",
		},
		{
			command:  "runtime",
			args:     []string{"service", "status", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei runtime service status [flags]",
		},
		// (c) skipped: service status has no required flags; would query
		// launchd/systemctl.

		// ---- upgrade ----
		{
			command:  "upgrade",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei upgrade [flags]",
			wantErr:  "",
		},
		{
			command:  "upgrade",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei upgrade [flags]",
		},
		// (c) skipped: upgrade has no required flags; would fetch release
		// metadata from S3.

		// ---- bot status ----
		{
			command:  "bot",
			args:     []string{"status", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot status [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"status", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot status [flags]",
		},
		// (c) bot status requires --installation; no flags exits 2 before
		// network.
		{
			command:  "bot",
			args:     []string{"status"},
			required: []string{"installation"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- setup ----
		{
			command:  "setup",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei setup [flags]",
			wantErr:  "",
		},
		{
			command:  "setup",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei setup [flags]",
		},
		// (c) skipped: setup has no required flags; would prompt interactively
		// and call the Kei API to verify the token.

		// ---- logout ----
		{
			command:  "logout",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei logout [flags]",
			wantErr:  "",
		},
		{
			command:  "logout",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei logout [flags]",
		},
		// (c) skipped: logout has no required flags.
	}

	for _, tt := range tests {
		name := strings.ReplaceAll(tt.command+" "+strings.Join(tt.args, " "), " ", "_")
		t.Run(name, func(t *testing.T) {
			stdout := new(bytes.Buffer)
			stderr := new(bytes.Buffer)
			gotCode := Main("test", tt.command, tt.args, stdout, stderr, strings.NewReader(""))

			if gotCode != tt.wantCode {
				t.Errorf("Main(%q, %q) exit code = %d, want %d", tt.command, tt.args, gotCode, tt.wantCode)
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
