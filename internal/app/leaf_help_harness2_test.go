package app

import (
	"bytes"
	"strings"
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
			wantOut:  "Usage: kei harness remove [flags]",
			wantErr:  "",
		},
		{
			command:  "harness",
			args:     []string{"remove", "--definitely-unknown"},
			wantCode: 2,
			wantOut:  "",
			wantErr:  "Usage: kei harness remove [flags]",
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
