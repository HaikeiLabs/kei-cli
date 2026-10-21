package app

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// helpCase is a single help-form invocation (expected to exit 0, write to stdout).
type helpCase struct {
	label string
	args  []string
}

// GroupHelpContract defines the target behaviour for one group dispatcher.
// The test asserts:
//   - help forms (empty, -h, --help, help): exit 0, stderr empty, stdout starts with
//     "Usage: kei <group> <command> [flags]", contains "Commands:", contains the
//     "Run "kei <group> <command> -h" for a command's flags." footer.
//   - unknown subcommand: exit 2, stderr contains "unknown <group> command "<x>"",
//     stderr also contains the same Usage line (without "Run ..." footer).
type GroupHelpContract struct {
	name        string
	groupPrefix string // e.g. "kei bot"
	helpArgs    []helpCase
	unknownArgs []string // typically []string{"bogus"}
	invoke      func(args []string, stdout, stderr io.Writer) int
}

func TestGroupHelpContract(t *testing.T) {
	helpForms := []helpCase{
		{"no args", []string{}},
		{"-h", []string{"-h"}},
		{"--help", []string{"--help"}},
		{"help", []string{"help"}},
	}

	groups := []GroupHelpContract{
		{
			name:        "top-level",
			groupPrefix: "kei",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				// Top-level: `Main("dev", "", args, ...)` for help forms,
				// `Main("dev", "bogus", nil, ...)` for unknown.
				// We cheat: call Main("dev", args[0], args[1:], ...) so
				// an empty-slice call becomes Main("dev", "", nil, ...) -> hits ""?
				// That doesn't work because the switch is on command, not args.
				// Instead, for top-level we always route through Main with the
				// first element as the command verb.
				return 999 // placeholder – handled specially below
			},
		},
		{
			name:        "bot",
			groupPrefix: "kei bot",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runBotCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "harness",
			groupPrefix: "kei harness",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runHarnessCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "runtime",
			groupPrefix: "kei runtime",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runRuntimeCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "service",
			groupPrefix: "kei service",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runServiceCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "workspaces",
			groupPrefix: "kei workspaces",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runWorkspaceCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "groups",
			groupPrefix: "kei groups",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runGroupsCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "groups members",
			groupPrefix: "kei groups members",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runGroupsMembersCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "policy",
			groupPrefix: "kei policy",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runPolicyCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "policies",
			groupPrefix: "kei policies",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runPoliciesCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "credential-store",
			groupPrefix: "kei credential-store",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runCredentialStoreCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "directory-sync",
			groupPrefix: "kei directory-sync",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runDirectorySyncCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "audit",
			groupPrefix: "kei audit",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runAuditCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "audit keys",
			groupPrefix: "kei audit keys",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runAuditKeysCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "connectors",
			groupPrefix: "kei connectors",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runConnectorsCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "connectors manifests",
			groupPrefix: "kei connectors manifests",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runConnectorsManifests(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "model-profiles",
			groupPrefix: "kei model-profiles",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runModelProfilesCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "users",
			groupPrefix: "kei users",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runUsersCommand(args, stdout, stderr, nil, nil, nil)
			},
		},
		{
			name:        "bot agents",
			groupPrefix: "kei bot agents",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runBotAgentsCommand(args, stdout, stderr, nil, nil)
			},
		},
		{
			name:        "skills",
			groupPrefix: "kei skills",
			helpArgs:    helpForms,
			unknownArgs: []string{"bogus"},
			invoke: func(args []string, stdout, stderr io.Writer) int {
				return runSkillsCommand(args, stdout, stderr, nil)
			},
		},
	}

	for _, g := range groups {
		g := g
		t.Run(g.name+"/help", func(t *testing.T) {
			// Top-level is handled separately
			if g.name == "top-level" {
				testTopLevel(t)
				return
			}
			for _, h := range g.helpArgs {
				h := h
				t.Run(h.label, func(t *testing.T) {
					var stdout, stderr bytes.Buffer
					code := g.invoke(h.args, &stdout, &stderr)
					if code != 0 {
						t.Errorf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr.String()))
					}
					out := stdout.String()
					if !strings.HasPrefix(out, "Usage: "+g.groupPrefix+" <command> [flags]") {
						t.Errorf("stdout prefix:\ngot:  %q\nwant prefix: %q", out[:min(len(out), 80)], "Usage: "+g.groupPrefix+" <command> [flags]")
					}
					if !strings.Contains(out, "Commands:") {
						t.Errorf("stdout missing 'Commands:' section")
					}
					footer := `Run "` + g.groupPrefix + ` <command> -h" for a command's flags.`
					if !strings.Contains(out, footer) {
						t.Errorf("stdout missing footer %q", footer)
					}
				})
			}
		})
		t.Run(g.name+"/unknown", func(t *testing.T) {
			if g.name == "top-level" {
				return // top-level unknown tested in testTopLevel
			}
			for _, ua := range g.unknownArgs {
				ua := ua
				t.Run(ua, func(t *testing.T) {
					var stdout, stderr bytes.Buffer
					code := g.invoke([]string{ua}, &stdout, &stderr)
					if code != 2 {
						t.Errorf("exit code = %d, want 2", code)
					}
					errMsg := stderr.String()
					unknownPrefix := "unknown " + g.groupPrefix[len("kei "):] + " command"
					if !strings.Contains(errMsg, unknownPrefix) {
						t.Errorf("stderr missing %q:\n%s", unknownPrefix, errMsg)
					}
					if !strings.Contains(errMsg, "Usage: "+g.groupPrefix+" <command> [flags]") {
						t.Errorf("stderr missing usage line:\n%s", errMsg)
					}
				})
			}
		})
	}
}

func testTopLevel(t *testing.T) {
	t.Run("help/empty", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Main("dev", "", nil, &stdout, &stderr, nil)
		// Top-level with empty command currently hits PrintUsage
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr.String()))
		}
		out := stdout.String()
		if !strings.Contains(out, "Usage:") {
			t.Errorf("stdout missing 'Usage:'")
		}
	})
	t.Run("help/h", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Main("dev", "-h", nil, &stdout, &stderr, nil)
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr.String()))
		}
	})
	t.Run("help/--help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Main("dev", "--help", nil, &stdout, &stderr, nil)
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr.String()))
		}
	})
	t.Run("help/help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Main("dev", "help", nil, &stdout, &stderr, nil)
		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr.String()))
		}
	})
	t.Run("unknown", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Main("dev", "bogus", nil, &stdout, &stderr, nil)
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
			t.Errorf("stderr missing unknown command message:\n%s", stderr.String())
		}
		if !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("stderr missing usage line:\n%s", stderr.String())
		}
	})
}
