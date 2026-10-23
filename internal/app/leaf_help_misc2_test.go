package app

import (
	"bytes"
	"strings"
	"testing"
)

// TestLeafHelpMisc2Commands drives Main for every STEP 2 leaf command
// (feedback.go, delete.go, connect.go, bot_list.go, bind.go, agents.go,
// doctor.go) and verifies standardised help, unknown-flag error, and
// missing-required-flag behaviour.
//
// Skip (c) — missing-required-flag — for any command that would hit the
// network first.  Commands with --installation (bot delete, bot bind,
// bot agents list/add/remove) can be tested because the check fires
// before any network call.  feedback --description also fires before
// network.  Connect uses parsePositionals and would reach the network.
// Bot list and doctor have no required flags.
func TestLeafHelpMisc2Commands(t *testing.T) {
	tests := []leafCase{
		// ---- feedback ----
		{
			command:  "feedback",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei feedback [flags]",
			wantErr:  "",
		},
		{
			command:  "feedback",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei feedback [flags]",
		},
		// (c) feedback requires --description; check fires before network.
		{
			command:  "feedback",
			args:     []string{},
			required: []string{"description"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --description",
		},

		// ---- bot delete ----
		{
			command:  "bot",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot delete [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot delete [flags]",
		},
		// (c) bot delete requires --installation; check fires before network.
		{
			command:  "bot",
			args:     []string{"delete"},
			required: []string{"installation"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- connect ----
		{
			command:  "connect",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connect [flags]",
			wantErr:  "",
		},
		{
			command:  "connect",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connect [flags]",
		},
		// (c) skipped: connect uses parsePositionals; would resolve org/ws.

		// ---- bot list ----
		{
			command:  "bot",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot list [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot list [flags]",
		},
		// (c) skipped: bot list has no required flags; would hit network.

		// ---- bot bind ----
		{
			command:  "bot",
			args:     []string{"bind", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot bind [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"bind", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot bind [flags]",
		},
		// (c) bot bind requires --installation; check fires before network.
		{
			command:  "bot",
			args:     []string{"bind"},
			required: []string{"installation"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- bot agents list ----
		{
			command:  "bot",
			args:     []string{"agents", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot agents list [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"agents", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot agents list [flags]",
		},
		// (c) bot agents list requires --installation; check before network.
		{
			command:  "bot",
			args:     []string{"agents", "list"},
			required: []string{"installation"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- bot agents add ----
		{
			command:  "bot",
			args:     []string{"agents", "add", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot agents add [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"agents", "add", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot agents add [flags]",
		},
		// (c) bot agents add requires --installation and --agent.
		{
			command:  "bot",
			args:     []string{"agents", "add"},
			required: []string{"installation", "agent"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- bot agents remove ----
		{
			command:  "bot",
			args:     []string{"agents", "remove", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei bot agents remove [flags]",
			wantErr:  "",
		},
		{
			command:  "bot",
			args:     []string{"agents", "remove", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei bot agents remove [flags]",
		},
		// (c) bot agents remove requires --installation and --agent.
		{
			command:  "bot",
			args:     []string{"agents", "remove"},
			required: []string{"installation", "agent"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --installation",
		},

		// ---- doctor ----
		{
			command:  "doctor",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei doctor [flags]",
			wantErr:  "",
		},
		{
			command:  "doctor",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei doctor [flags]",
		},
		// (c) skipped: doctor has no required flags.
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
