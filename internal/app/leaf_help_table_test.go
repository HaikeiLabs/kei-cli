package app

import (
	"bytes"
	"strings"
	"testing"
)

// leafCase is one row in the leaf-command help/output table test.
type leafCase struct {
	command  string
	args     []string
	required []string    // required flags this command expects (for the "no flags" case)
	wantCode int         // expected exit code
	wantOut  string      // substring expected on stdout
	wantErr  string      // substring expected on stderr
}

// TestLeafCommandsHelp drives Main for every migrated leaf command and
// verifies standardised help, unknown-flag error, and missing-required-flag
// behaviour.
func TestLeafCommandsHelp(t *testing.T) {
	// Every row runs Main("test", row.command, row.args, …) and checks the
	// exit code, stdout, and stderr against the expected substrings.
	tests := []leafCase{
		// ---- workspaces list ----
		{
			command:  "workspaces",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei workspaces list [flags]",
			wantErr:  "",
		},
		{
			command:  "workspaces",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei workspaces list [flags]",
		},

		// ---- workspaces create ----
		{
			command:  "workspaces",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei workspaces create [flags]",
			wantErr:  "",
		},
		{
			command:  "workspaces",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei workspaces create [flags]",
		},
		// workspaces create --name is required; no-name exits 2 before network.
		{
			command:  "workspaces",
			args:     []string{"create"},
			required: []string{"name"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --name",
		},

		// ---- workspaces use ----
		{
			command:  "workspaces",
			args:     []string{"use", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei workspaces use NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "workspaces",
			args:     []string{"use", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei workspaces use NAME [flags]",
		},
		// workspaces use has no required flags; skip (c).

		// ---- workspaces current ----
		{
			command:  "workspaces",
			args:     []string{"current", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei workspaces current [flags]",
			wantErr:  "",
		},
		{
			command:  "workspaces",
			args:     []string{"current", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei workspaces current [flags]",
		},
		// workspaces current has no required flags; skip (c).

		// ---- groups list ----
		{
			command:  "groups",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups list [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups list [flags]",
		},
		// groups list requires --workspace; exit 2 before network.
		{
			command:  "groups",
			args:     []string{"list"},
			required: []string{"workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --workspace",
		},

		// ---- groups create ----
		{
			command:  "groups",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups create [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups create [flags]",
		},
		// groups create requires --name and --workspace; exit 2 before network.
		{
			command:  "groups",
			args:     []string{"create"},
			required: []string{"name", "workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --name",
		},

		// ---- groups delete ----
		{
			command:  "groups",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups delete GROUP [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups delete GROUP [flags]",
		},
		// groups delete requires --workspace; pass a positional arg so
		// parsePositionals succeeds and requireFlags fires before network.
		{
			command:  "groups",
			args:     []string{"delete", "mygroup"},
			required: []string{"workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --workspace",
		},

		// ---- groups members list ----
		{
			command:  "groups",
			args:     []string{"members", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups members list GROUP [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"members", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups members list GROUP [flags]",
		},
		// groups members list has no required flags; skip (c) (positional GROUP is required, checked before network).

		// ---- groups members add ----
		{
			command:  "groups",
			args:     []string{"members", "add", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups members add GROUP USER [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"members", "add", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups members add GROUP USER [flags]",
		},
		// groups members add has no required flags; skip (c) (positionals checked before network).

		// ---- groups members remove ----
		{
			command:  "groups",
			args:     []string{"members", "remove", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei groups members remove GROUP USER [flags]",
			wantErr:  "",
		},
		{
			command:  "groups",
			args:     []string{"members", "remove", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei groups members remove GROUP USER [flags]",
		},
		// groups members remove has no required flags; skip (c).

		// ---- policy list (policy group, no deprecation) ----
		{
			command:  "policy",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policy list [flags]",
			wantErr:  "",
		},
		{
			command:  "policy",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policy list [flags]",
		},
		// policy list has no required flags; skip (c).

		// ---- policy get ----
		{
			command:  "policy",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policy get ID [flags]",
			wantErr:  "",
		},
		{
			command:  "policy",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policy get ID [flags]",
		},
		// policy get has no required flags; skip (c).

		// ---- policy create (--group/--allow/--deny simplified command) ----
		{
			command:  "policy",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policy create [flags]",
			wantErr:  "",
		},
		{
			command:  "policy",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policy create [flags]",
		},
		// policy create requires --group.
		{
			command:  "policy",
			args:     []string{"create"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policy create: --group is required",
		},

		// ---- policy delete ----
		{
			command:  "policy",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policy delete ID [flags]",
			wantErr:  "",
		},
		{
			command:  "policy",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policy delete ID [flags]",
		},
		// policy delete requires --yes confirmation.
		{
			command:  "policy",
			args:     []string{"delete", "some-policy-id"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policies delete removes the policy for all agents; pass --yes to confirm",
		},

		// ---- policy test ----
		{
			command:  "policy",
			args:     []string{"test", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policy test TARGET [flags]",
			wantErr:  "",
		},
		{
			command:  "policy",
			args:     []string{"test", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policy test TARGET [flags]",
		},
		// policy test requires --as and a positional target.
		{
			command:  "policy",
			args:     []string{"test"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policy test requires --as",
		},

		// ---- policies update (unique to policies group) ----
		{
			command:  "policies",
			args:     []string{"update", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies update ID [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"update", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies update ID [flags]",
		},
		// policies update has no required flags; skip (c).

		// ---- policies import (unique to policies group) ----
		{
			command:  "policies",
			args:     []string{"import", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies import [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"import", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies import [flags]",
		},
		// policies import requires --from.
		{
			command:  "policies",
			args:     []string{"import"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policies import requires --from",
		},

		// ---- policies create (detailed version, unique to policies group) ----
		{
			command:  "policies",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies create [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies create [flags]",
		},
		// policies create requires --name, --src-pattern, --dst-pattern, and --effect.
		{
			command:  "policies",
			args:     []string{"create"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policies create requires --name, --src-pattern, --dst-pattern, and --effect",
		},

		// ---- policies get (via policies group) ----
		{
			command:  "policies",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies get ID [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies get ID [flags]",
		},
		// policies get has no required flags; skip (c).

		// ---- policies delete (via policies group) ----
		{
			command:  "policies",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies delete ID [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies delete ID [flags]",
		},
		// policies delete requires --yes confirmation.
		{
			command:  "policies",
			args:     []string{"delete", "some-policy-id"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "policies delete removes the policy for all agents; pass --yes to confirm",
		},

		// ---- policies list (via policies group) ----
		{
			command:  "policies",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei policies list [flags]",
			wantErr:  "Deprecated:",
		},
		{
			command:  "policies",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei policies list [flags]",
		},
		// policies list has no required flags; skip (c).
	}

	runLeafCases(t, tests)
}

// runLeafCases runs every leafCase in tests as a subtest, checking exit code,
// stdout, and stderr against the expected substrings.
func runLeafCases(t *testing.T, tests []leafCase) {
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
