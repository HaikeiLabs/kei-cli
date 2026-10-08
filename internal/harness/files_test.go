package harness

import (
	"bytes"
	"strings"
	"testing"
)

func TestHarnessNonPromptingModesWarnAndDoNotBlock(t *testing.T) {
	cases := []struct {
		kind, config string
		want         string
	}{
		{"codex", "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n", "codex"},
		{"claude_code", `{"permissions":{"defaultMode":"bypassPermissions"}}`, "claude_code"},
		{"claude_code", `{"permissions":{"defaultMode":"dontAsk"}}`, "claude_code"},
		{"opencode", `{"permission":{"*":"allow"}}`, "opencode"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+tc.config, func(t *testing.T) {
			var out bytes.Buffer
			if !warnNonPromptingMode(tc.kind, []byte(tc.config), &out) || !strings.Contains(out.String(), "ADR-029 OQ1") || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("warning = %q", out.String())
			}
		})
	}
	var out bytes.Buffer
	if warnNonPromptingMode("codex", []byte("approval_policy = \"on-request\"\nsandbox_mode = \"workspace-write\""), &out) || out.Len() != 0 {
		t.Fatalf("unexpected warning for prompting config: %q", out.String())
	}
}
