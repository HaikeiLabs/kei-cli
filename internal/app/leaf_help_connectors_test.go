package app

import (
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
			wantOut:  "Usage: kei connectors get NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"get", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors get NAME [flags]",
		},

		// ---- connectors connect ----
		{
			command:  "connectors",
			args:     []string{"connect", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors connect NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"connect", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors connect NAME [flags]",
		},

		// ---- connectors reconnect ----
		{
			command:  "connectors",
			args:     []string{"reconnect", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors reconnect NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"reconnect", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors reconnect NAME [flags]",
		},

		// ---- connectors delete ----
		{
			command:  "connectors",
			args:     []string{"delete", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors delete NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"delete", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors delete NAME [flags]",
		},

		// ---- connectors test ----
		{
			command:  "connectors",
			args:     []string{"test", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors test NAME [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"test", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors test NAME [flags]",
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
			wantOut:  "Usage: kei connectors manifests list CONNECTOR_ID [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests list CONNECTOR_ID [flags]",
		},

		// ---- connectors manifests show ----
		{
			command:  "connectors",
			args:     []string{"manifests", "show", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors manifests show CONNECTOR_ID [MANIFEST_ID] [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "show", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests show CONNECTOR_ID [MANIFEST_ID] [flags]",
		},

		// ---- connectors manifests approve ----
		{
			command:  "connectors",
			args:     []string{"manifests", "approve", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei connectors manifests approve CONNECTOR_ID [MANIFEST_ID] [flags]",
			wantErr:  "",
		},
		{
			command:  "connectors",
			args:     []string{"manifests", "approve", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei connectors manifests approve CONNECTOR_ID [MANIFEST_ID] [flags]",
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

	runLeafCases(t, tests)
}
