package app

import (
	"testing"
)

func TestLeafHelpHarness2(t *testing.T) {
	tests := []leafCase{
		// ---- harness add ----
		{
			command:  "harness",
			args:     []string{"add", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei harness add [flags]",
			wantErr:  "",
		},
		{
			command:  "harness",
			args:     []string{"add", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei harness add [flags]",
		},

		// ---- harness list ----
		{
			command:  "harness",
			args:     []string{"list", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei harness list [flags]",
			wantErr:  "",
		},
		{
			command:  "harness",
			args:     []string{"list", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei harness list [flags]",
		},

		// ---- harness remove ----
		{
			command:  "harness",
			args:     []string{"remove", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei harness remove [AGENT_ID] [flags]",
			wantErr:  "",
		},
		{
			command:  "harness",
			args:     []string{"remove", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei harness remove [AGENT_ID] [flags]",
		},

		// ---- harness sync ----
		{
			command:  "harness",
			args:     []string{"sync", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei harness sync [flags]",
			wantErr:  "",
		},
		{
			command:  "harness",
			args:     []string{"sync", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei harness sync [flags]",
		},

		// ---- skills install ----
		{
			command:  "skills",
			args:     []string{"install", "-h"},
			wantCode: 0,
			wantOut:  "Usage: kei skills install [flags]",
			wantErr:  "",
		},
		{
			command:  "skills",
			args:     []string{"install", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei skills install [flags]",
		},

		// ---- docs ----
		{
			command:  "docs",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  "Usage: kei docs [flags]",
			wantErr:  "",
		},
		{
			command:  "docs",
			args:     []string{"--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei docs [flags]",
		},
	}

	runLeafCases(t, tests)
}
