package app

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/tabwriter"
)

// leafCommand wraps a flag.FlagSet for a single leaf CLI command.
// It provides a consistent usage-format and parse flow that every leaf
// command in kei should use.
type leafCommand struct {
	fs      *flag.FlagSet
	stdout  io.Writer
	stderr  io.Writer
	summary string
	group   string
	name    string
}

// newCommandFlags creates a leafCommand for the command "kei <group> <name>"
// with the given one-line summary. The flag set uses flag.ContinueOnError and
// writes its usage banner to stderr (the default flag-package behaviour).
// parseLeaf handles the explicit -h/--help case and writes usage to stdout.
func newCommandFlags(group, name, summary string, stdout, stderr io.Writer) leafCommand {
	fullName := group + " " + name
	if group == "" {
		fullName = name
	}
	fs := flag.NewFlagSet(fullName, flag.ContinueOnError)
	fs.SetOutput(stderr) // flag-package error messages go to stderr

	lc := leafCommand{
		fs:      fs,
		stdout:  stdout,
		stderr:  stderr,
		summary: summary,
		group:   group,
		name:    name,
	}

	// fs.Usage always writes to stderr (the flag-package default). The
	// parseLeaf function handles -h/--help explicitly and writes to stdout.
	fs.Usage = func() {
		lc.printUsage(stderr)
	}

	return lc
}

// printUsage writes the standard leaf-command usage to w.
func (lc leafCommand) printUsage(w io.Writer) {
	prefix := "kei " + lc.group + " " + lc.name
	if lc.group == "" {
		prefix = "kei " + lc.name
	}
	fmt.Fprintf(w, "Usage: %s [flags]\n\n", prefix)
	if lc.summary != "" {
		fmt.Fprintln(w, lc.summary)
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "Flags:")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', tabwriter.DiscardEmptyColumns)
	lc.fs.VisitAll(func(f *flag.Flag) {
		flagCol := "--" + f.Name
		if typeName := flagTypeName(f); typeName != "" {
			flagCol += " " + typeName
		}
		fmt.Fprintf(tw, "  %s\t%s\n", flagCol, f.Usage)
	})
	tw.Flush()
}

// flagTypeName returns the Go type name for a flag value suitable for usage
// display (e.g. "string", "int", ""). Bool flags return "" so the type
// annotation is omitted.
func flagTypeName(f *flag.Flag) string {
	v := reflect.ValueOf(f.Value)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	switch v.Type().Name() {
	case "stringValue":
		return "string"
	case "boolValue":
		return ""
	case "intValue", "int64Value":
		return "int"
	case "uintValue", "uint64Value":
		return "uint"
	case "float64Value":
		return "float"
	case "durationValue":
		return "duration"
	default:
		return v.Type().Name()
	}
}

// parseLeaf parses args using the leaf command's flag set. It returns:
//   - (0, false) if -h or --help is given: writes usage to lc.stdout, exits 0.
//   - (2, false) if flag.Parse returns an error: the flag package (ContinueOnError)
//     already wrote the error message and usage to lc.stderr; we return 2.
//   - (0, true) on successful parse.
func parseLeaf(lc leafCommand, args []string) (code int, ok bool) {
	// Handle -h/--help explicitly so usage goes to stdout and we return 0.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0, false
		}
	}

	if err := lc.fs.Parse(args); err != nil {
		// flag.ContinueOnError already wrote the error + usage to stderr.
		return 2, false
	}
	return 0, true
}

// requireFlags checks that each named flag was set (via flag.Visit). For every
// missing flag it writes "missing required flag --<name>" and the usage to
// stderr. Returns true when all required flags are present.
func requireFlags(fs *flag.FlagSet, stderr io.Writer, names ...string) bool {
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		set[f.Name] = true
	})

	var missing []string
	for _, name := range names {
		if !set[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return true
	}

	for _, name := range missing {
		fmt.Fprintf(stderr, "missing required flag --%s\n", name)
	}
	fs.Usage()
	return false
}

// ---------------------------------------------------------------------------
// Shared flag definitions — single help string per common flag.
// ---------------------------------------------------------------------------

// workspaceFlag adds a --workspace flag with the canonical description.
func workspaceFlag(fs *flag.FlagSet) *string {
	return fs.String("workspace", "", "workspace name or ID")
}

// jsonFlag adds a --json flag with the canonical description.
func jsonFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "output as JSON")
}

// installationFlag adds a --installation flag with the canonical description.
func installationFlag(fs *flag.FlagSet) *string {
	return fs.String("installation", "", "runtime installation ID")
}

// yesFlag adds a --yes flag with the canonical description.
func yesFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("yes", false, "confirm without prompting")
}

// workspaceFlagHelp is like workspaceFlag but with a custom help string.
func workspaceFlagHelp(fs *flag.FlagSet, help string) *string {
	return fs.String("workspace", "", help)
}

// String returns the full command name for error messages.
func (lc leafCommand) String() string {
	return strings.TrimSpace(lc.group + " " + lc.name)
}
