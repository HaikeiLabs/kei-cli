package app

import (
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
			wantErr:  "Usage: kei audit keys create [flags]",
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
			wantErr:  "Usage: kei audit keys list [flags]",
		},

		// ---- audit keys disable ----
		{
			command:  "audit",
			args:     []string{"keys", "disable", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei audit keys disable KEY_ID [flags]",
		},
		{
			command:  "audit",
			args:     []string{"keys", "disable", "--definitely-unknown"},
			wantCode: 2,
			wantErr:  "Usage: kei audit keys disable KEY_ID [flags]",
		},
		// audit keys disable requires a positional key ID — no args exits 2 before network.
		{
			command:  "audit",
			args:     []string{"keys", "disable"},
			wantCode: 2,
			wantErr:  "requires exactly one argument",
		},
	}

	runLeafCases(t, tests)
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
			wantErr:  "Usage: kei audit decrypt [flags]",
		},
		// audit decrypt requires --record — no flags exits 2 before network.
		{
			command:  "audit",
			args:     []string{"decrypt"},
			wantCode: 2,
			wantErr:  "missing required flag --record",
		},
	}

	runLeafCases(t, tests)
}
