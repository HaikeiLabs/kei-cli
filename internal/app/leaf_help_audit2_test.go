package app

import (
	"bytes"
	"strings"
	"testing"
)

// TestLeafHelpAuditKeys drives audit keys subcommands through Main and
// verifies standardised help, unknown-flag error, and
// missing-required-flag behaviour.
func TestLeafHelpAuditKeys(t *testing.T) {
	tests := []leafCase{
		// ---- audit keys create ----
		{
			command:  "audit",
			args:     []string{"keys", "create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei audit keys create [flags]",
		},
		{
			command:  "audit",
			args:     []string{"keys", "create", "--definitely-unknown"},
			wantCode: 2,
		},

		// ---- audit keys list ----
		{
			command:  "audit",
			args:     []string{"keys", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei audit keys list [flags]",
		},
		{
			command:  "audit",
			args:     []string{"keys", "list", "--definitely-unknown"},
			wantCode: 2,
		},

		// ---- audit keys disable ----
		{
			command:  "audit",
			args:     []string{"keys", "disable", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei audit keys disable [flags]",
		},
		{
			command:  "audit",
			args:     []string{"keys", "disable", "--definitely-unknown"},
			wantCode: 2,
		},
		// audit keys disable requires a positional key ID — no args exits 2 before network.
		{
			command:  "audit",
			args:     []string{"keys", "disable"},
			wantCode: 2,
			wantErr:  "requires exactly one argument",
		},
	}

	for _, tt := range tests {
		name := strings.ReplaceAll(tt.command+"_"+strings.Join(tt.args, "_"), " ", "_")
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
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
		})
	}
}

// TestLeafHelpAuditDecrypt drives audit decrypt through Main and verifies
// standardised help, unknown-flag error, and missing-required-flag behaviour.
func TestLeafHelpAuditDecrypt(t *testing.T) {
	tests := []leafCase{
		// ---- audit decrypt ----
		{
			command:  "audit",
			args:     []string{"decrypt", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei audit decrypt [flags]",
		},
		{
			command:  "audit",
			args:     []string{"decrypt", "--definitely-unknown"},
			wantCode: 2,
		},
		// audit decrypt requires --record — no flags exits 2 before network.
		{
			command:  "audit",
			args:     []string{"decrypt"},
			wantCode: 2,
			wantErr:  "missing required flag --record",
		},
	}

	for _, tt := range tests {
		name := strings.ReplaceAll(tt.command+"_"+strings.Join(tt.args, "_"), " ", "_")
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
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, stderr.String())
			}
		})
	}
}
