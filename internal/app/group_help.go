package app

import (
	"fmt"
	"io"
	"strings"
)

// groupCommand describes a subcommand within a command group.
type groupCommand struct {
	name    string // subcommand name, e.g. "list"
	summary string // one-line description, e.g. "List runtime bots"
}

// commandGroup models a group of subcommands (kei <group> <command>).
// Use its dispatch method to handle help routing and unknown subcommands.
type commandGroup struct {
	name     string // group name, e.g. "bot" or "bot agents"
	commands []groupCommand
	// aliases maps alias subcommand names to their canonical name (which must
	// appear in commands).  For example, {"tools": "manifests"} means that
	// "tools" is treated as "manifests" but only "manifests" appears in help.
	aliases map[string]string
}

// dispatch inspects args and returns one of:
//
//   - (sub, rest, 0, false) — args[0] is a known subcommand.  The caller should
//     switch on sub and pass rest to the subcommand function.
//
//   - ("", nil, code, true) — no args, -h, --help, or help was given → usage is
//     written to stdout with exit 0; or args[0] is not a known subcommand →
//     "unknown <group> command" + usage is written to stderr with exit 2.
func (g commandGroup) dispatch(args []string, stdout, stderr io.Writer) (sub string, rest []string, code int, handled bool) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		g.printUsage(stdout)
		return "", nil, 0, true
	}
	for _, c := range g.commands {
		if c.name == args[0] {
			return args[0], args[1:], 0, false
		}
	}
	if alias, ok := g.aliases[args[0]]; ok {
		return alias, args[1:], 0, false
	}
	fmt.Fprintf(stderr, "unknown %s command %q\n", g.name, args[0])
	g.printUsage(stderr)
	return "", nil, 2, true
}

// printUsage writes the standard group help banner to w.
func (g commandGroup) printUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: kei %s <command> [flags]\n\nCommands:\n", g.name)
	longest := 0
	for _, c := range g.commands {
		if len(c.name) > longest {
			longest = len(c.name)
		}
	}
	fmtStr := fmt.Sprintf("  %%%ds  %%s\n", -longest)
	for _, c := range g.commands {
		fmt.Fprintf(w, fmtStr, c.name, c.summary)
	}
	fmt.Fprintf(w, "\nRun \"kei %s <command> -h\" for a command's flags.\n", g.name)
}

// ensure plural group names (workspaces, groups, connectors, policies) and
// singular equivalents (workspace, group, connector, policy) are both accepted.

// singularOf returns the singular form for well-known plural group names,
// or the input unchanged.
func singularOf(group string) string {
	switch group {
	case "workspaces":
		return "workspace"
	case "groups":
		return "group"
	case "connectors":
		return "connector"
	case "policies":
		return "policy"
	}
	return group
}

// DisplayName returns the group name that appears after "kei " in usage.
func (g commandGroup) displayName() string {
	return strings.TrimSpace(g.name)
}
