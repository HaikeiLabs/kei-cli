package app

import (
	"testing"
)

// TestLeafHelpModelProfiles drives Main for every model-profiles leaf command
// and verifies standardised help, unknown-flag error, and missing-required-flag
// behaviour.
func TestLeafHelpModelProfiles(t *testing.T) {
	tests := []leafCase{
		// ---- model-profiles list ----
		{
			command:  "model-profiles",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles list [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles list [flags]",
		},
		// model-profiles list has no required flags; skip (c).

		// ---- model-profiles get ----
		{
			command:  "model-profiles",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles get PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles get PROFILE [flags]",
		},
		// model-profiles get requires a positional PROFILE; skip (c) (not a flag).

		// ---- model-profiles create ----
		{
			command:  "model-profiles",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles create [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles create [flags]",
		},
		// model-profiles create requires --display-name, --endpoint, --auth-type,
		// and --default-model; exit 2 before network.
		{
			command:  "model-profiles",
			args:     []string{"create"},
			required: []string{"display-name", "endpoint", "auth-type", "default-model"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --display-name",
		},

		// ---- model-profiles update ----
		{
			command:  "model-profiles",
			args:     []string{"update", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles update PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"update", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles update PROFILE [flags]",
		},
		// model-profiles update requires a positional PROFILE; skip (c) (not a flag).

		// ---- model-profiles delete ----
		{
			command:  "model-profiles",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles delete PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles delete PROFILE [flags]",
		},
		// model-profiles delete --yes is required (flag); skip (c) because
		// parsePolicyFlags first checks for the positional PROFILE arg and
		// exits 2 before reaching requireFlags. With no PROFILE arg at all,
		// we exit 2 with "requires a profile ID or name".

		// ---- model-profiles set-default ----
		{
			command:  "model-profiles",
			args:     []string{"set-default", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles set-default PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"set-default", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles set-default PROFILE [flags]",
		},
		// model-profiles set-default requires a positional PROFILE; skip (c) (not a flag).

		// ---- model-profiles assign ----
		{
			command:  "model-profiles",
			args:     []string{"assign", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles assign PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"assign", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles assign PROFILE [flags]",
		},
		// assign requires --agent and --workspace; exit 2 before network.
		// Positional PROFILE checked before flags.
		{
			command:  "model-profiles",
			args:     []string{"assign", "x"},
			required: []string{"agent", "workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --agent",
		},

		// ---- model-profiles unassign ----
		{
			command:  "model-profiles",
			args:     []string{"unassign", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles unassign [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"unassign", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles unassign [flags]",
		},
		// unassign requires --agent and --workspace; exit 2 before network.
		{
			command:  "model-profiles",
			args:     []string{"unassign"},
			required: []string{"agent", "workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --agent",
		},

		// ---- model-profiles assignment ----
		{
			command:  "model-profiles",
			args:     []string{"assignment", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles assignment [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"assignment", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles assignment [flags]",
		},
		// assignment requires --agent and --workspace; exit 2 before network.
		{
			command:  "model-profiles",
			args:     []string{"assignment"},
			required: []string{"agent", "workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --agent",
		},

		// ---- model-profiles readiness ----
		{
			command:  "model-profiles",
			args:     []string{"readiness", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles readiness PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"readiness", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles readiness PROFILE [flags]",
		},
		// readiness requires --workspace; exit 2 before network.
		{
			command:  "model-profiles",
			args:     []string{"readiness", "x"},
			required: []string{"workspace"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --workspace",
		},

		// ---- model-profiles test ----
		{
			command:  "model-profiles",
			args:     []string{"test", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei model-profiles test PROFILE [flags]",
			wantErr:  "",
		},
		{
			command:  "model-profiles",
			args:     []string{"test", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei model-profiles test PROFILE [flags]",
		},
		// test requires either --endpoint or PROFILE arg; skip (c) because
		// the check is custom and exits 2 before network.
	}

	runLeafCases(t, tests)
}

// TestLeafHelpCredentialStore drives Main for every credential-store leaf
// command.
func TestLeafHelpCredentialStore(t *testing.T) {
	tests := []leafCase{
		// ---- credential-store get ----
		{
			command:  "credential-store",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei credential-store get [flags]",
			wantErr:  "",
		},
		{
			command:  "credential-store",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei credential-store get [flags]",
		},
		// credential-store get has no required flags; skip (c).

		// ---- credential-store put ----
		{
			command:  "credential-store",
			args:     []string{"put", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei credential-store put [flags]",
			wantErr:  "",
		},
		{
			command:  "credential-store",
			args:     []string{"put", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei credential-store put [flags]",
		},
		// credential-store put requires --secret-backend; exit 2 before network.
		{
			command:  "credential-store",
			args:     []string{"put"},
			required: []string{"secret-backend"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --secret-backend",
		},

		// ---- credential-store update ----
		{
			command:  "credential-store",
			args:     []string{"update", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei credential-store update [flags]",
			wantErr:  "",
		},
		{
			command:  "credential-store",
			args:     []string{"update", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei credential-store update [flags]",
		},
		// credential-store update requires at least one change flag; skip (c)
		// because the check is custom (not requireFlags) and exits 2 before network.
	}

	runLeafCases(t, tests)
}

// TestLeafHelpDirectorySync drives Main for every directory-sync leaf command.
func TestLeafHelpDirectorySync(t *testing.T) {
	tests := []leafCase{
		// ---- directory-sync get ----
		{
			command:  "directory-sync",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei directory-sync get [flags]",
			wantErr:  "",
		},
		{
			command:  "directory-sync",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei directory-sync get [flags]",
		},
		// directory-sync get has no required flags; skip (c).

		// ---- directory-sync set ----
		{
			command:  "directory-sync",
			args:     []string{"set", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei directory-sync set [flags]",
			wantErr:  "",
		},
		{
			command:  "directory-sync",
			args:     []string{"set", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei directory-sync set [flags]",
		},
		// directory-sync set requires at least one config flag; skip (c)
		// because check is custom.

		// ---- directory-sync enable ----
		{
			command:  "directory-sync",
			args:     []string{"enable", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei directory-sync enable [flags]",
			wantErr:  "",
		},
		{
			command:  "directory-sync",
			args:     []string{"enable", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei directory-sync enable [flags]",
		},
		// directory-sync enable has no required flags; skip (c).

		// ---- directory-sync disable ----
		{
			command:  "directory-sync",
			args:     []string{"disable", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei directory-sync disable [flags]",
			wantErr:  "",
		},
		{
			command:  "directory-sync",
			args:     []string{"disable", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei directory-sync disable [flags]",
		},
		// directory-sync disable has no required flags; skip (c).

		// ---- directory-sync report ----
		{
			command:  "directory-sync",
			args:     []string{"report", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei directory-sync report [flags]",
			wantErr:  "",
		},
		{
			command:  "directory-sync",
			args:     []string{"report", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei directory-sync report [flags]",
		},
		// directory-sync report has no required flags; skip (c).
	}

	runLeafCases(t, tests)
}

// TestLeafHelpUsers drives Main for every users leaf command.
func TestLeafHelpUsers(t *testing.T) {
	tests := []leafCase{
		// ---- users list ----
		{
			command:  "users",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users list [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users list [flags]",
		},
		// users list has no required flags; skip (c).

		// ---- users get ----
		{
			command:  "users",
			args:     []string{"get", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users get USER_ID [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users get USER_ID [flags]",
		},
		// users get requires a positional USER ID; skip (c) (not a flag).

		// ---- users create ----
		{
			command:  "users",
			args:     []string{"create", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users create [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"create", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users create [flags]",
		},
		// users create requires --email; exit 2 before network.
		{
			command:  "users",
			args:     []string{"create"},
			required: []string{"email"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --email",
		},

		// ---- users update ----
		{
			command:  "users",
			args:     []string{"update", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users update USER_ID [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"update", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users update USER_ID [flags]",
		},
		// users update requires a positional USER ID; skip (c) (not a flag).

		// ---- users resolve ----
		{
			command:  "users",
			args:     []string{"resolve", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users resolve [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"resolve", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users resolve [flags]",
		},
		// users resolve requires --email; exit 2 before network.
		{
			command:  "users",
			args:     []string{"resolve"},
			required: []string{"email"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "missing required flag --email",
		},

		// ---- users identities list ----
		{
			command:  "users",
			args:     []string{"identities", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users identities list USER_ID [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"identities", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users identities list USER_ID [flags]",
		},
		// users identities list requires a positional USER ID; skip (c) (not a flag).

		// ---- users remote-identities list ----
		{
			command:  "users",
			args:     []string{"remote-identities", "list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei users remote-identities list USER_ID [flags]",
			wantErr:  "",
		},
		{
			command:  "users",
			args:     []string{"remote-identities", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei users remote-identities list USER_ID [flags]",
		},
		// users remote-identities list requires a positional USER ID; skip (c) (not a flag).
	}

	runLeafCases(t, tests)
}
