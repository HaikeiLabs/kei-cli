# kei-cli command design

This document describes the architecture and conventions for building CLI
commands in kei-cli. It covers group help, leaf‑command flag helpers,
stdout/stderr/exit‑code rules, shared flag definitions, and a step‑by‑step
recipe for adding a new command.

## Where kei-cli keeps docs

| Path | Purpose |
|---|---|
| `README.md` | Project overview, install, quick‑start, release process. |
| `docs/release.md` | Release runbook (triggering a release, verifying it). |
| `docs/cli-command-design.md` | **This file.** Command architecture and conventions. |

Code‑level documentation lives in doc comments on exported Go identifiers.
The `internal/app/` package is the command layer; the `cmd/kei/` main package
is a thin shell that calls into `app.Main`.

## Group help

Every command group (e.g. `workspaces`, `groups`, `policy`, `connectors`) is
modelled by a [`commandGroup`](../internal/app/group_help.go) value:

```go
var workspaceGroup = commandGroup{
    name: "workspaces",
    commands: []groupCommand{
        {name: "list",   summary: "List workspaces"},
        {name: "create", summary: "Create a workspace"},
        {name: "use",    summary: "Switch the current workspace"},
        {name: "current",summary: "Show the current workspace"},
    },
}
```

### `dispatch`

`commandGroup.dispatch(args, stdout, stderr)` reads the first element of `args`
and returns one of:

| `args` | Returns | Effect |
|---|---|---|
| empty, `-h`, `--help`, `help` | `("", nil, 0, true)` | Writes group usage to **stdout**, exit **0**. |
| known subcommand (e.g. `"list"`) | `("list", ["rest..."], 0, false)` | Caller switches on `"list"` and passes the rest. |
| unknown command | `("", nil, 2, true)` | Writes `"unknown <group> command"` + usage to **stderr**, exit **2**. |

### Group help text

`commandGroup.printUsage(w)` writes:

```
Usage: kei <group> <command> [flags]

Commands:
  <name>  <summary>
  ...

Run "kei <group> <command> -h" for a command's flags.
```

Groups whose name has a well‑known singular form (`workspaces` → `workspace`,
`groups` → `group`, `connectors` → `connector`, `policies` → `policy`) accept
BOTH the plural and singular form at the dispatch level. The singular and
plural group names share the same leaf‑command functions via a `group` string
parameter (see `runPoliciesList("/policy", …)` vs `runPoliciesList("/policies", …)`).

## Leaf‑command helpers

Every individual leaf command (e.g. `kei workspaces list`) is built with the
helpers in [`leaf_flags.go`](../internal/app/leaf_flags.go).

### `newCommandFlags`

```go
func newCommandFlags(group, name, summary string, stdout, stderr io.Writer) leafCommand
```

Creates a `leafCommand` wrapping a `flag.FlagSet` with
`flag.ContinueOnError`. The `group`/`name`/`summary` fields are used to
render the usage line:

```
Usage: kei <group> <name> [flags]
```

### `WithArgs`

Commands that accept positional arguments (e.g. `kei groups delete GROUP`)
chain `.WithArgs("GROUP")`:

```go
lc := newCommandFlags("groups", "delete", "Delete a group", stdout, stderr).
    WithArgs("GROUP")
```

The placeholder appears in the usage line:

```
Usage: kei groups delete GROUP [flags]
```

`WithArgs` also updates the underlying `flag.FlagSet.Usage` function so that
error‑driven usage (unknown flags, parse errors) shows the placeholder.

### `parseLeaf`

```go
func parseLeaf(lc leafCommand, args []string) (code int, ok bool)
```

Parses `args` using the command's flag set:

| Condition | Returns | Output |
|---|---|---|
| `-h` or `--help` in args | `(0, false)` | Writes usage to **stdout**, exit **0**. |
| `flag.Parse` error | `(2, false)` | `flag.ContinueOnError` already wrote the error to **stderr**, exit **2**. |
| Successful parse | `(0, true)` | Proceed to command logic. |

### `requireFlags`

```go
func requireFlags(fs *flag.FlagSet, stderr io.Writer, names ...string) bool
```

Checks that every named flag was explicitly set (via `flag.Visit`). For each
missing flag it writes `"missing required flag --<name>"` and the usage to
**stderr** and returns `false`. Returns `true` when all are present.

**When to use:** Commands that genuinely require a flag (e.g. `--workspace`
for groups commands that **do not** auto‑detect). Commands that auto‑detect
workspace (e.g. policy commands) should **not** call `requireFlags` for
`--workspace`; instead they let `requirePoliciesWorkspace` /
`requireAccessGrantWorkspace` fall through to `selectWorkspace`.

### `parsePositionals`

```go
func parsePositionals(flags *flag.FlagSet, args []string, n int) ([]string, error)
```

Collects exactly `n` positional arguments from the beginning of `args`
(non‑flag tokens before the first `"-"`), then passes the remainder to
`flags.Parse`. The collected positionals are returned. If fewer or more
than `n` are found, an error is returned.

**Usage pattern in commands with positionals:**

```go
pos, err := parsePositionals(lc.fs, args, 1)
if err != nil {
    fmt.Fprintln(stderr, "group name requires an ID")
    return 2
}
id := pos[0]
```

## Stdout / stderr / exit‑code rules

Every leaf command follows these conventions consistently:

| Scenario | Exit | Output stream | Content |
|---|---|---|---|
| `-h` / `--help` | **0** | stdout | Usage line, summary, flags table. |
| Unknown flag | **2** | stderr | `flag` package error + usage. |
| Parse error | **2** | stderr | `flag` package error + usage. |
| Missing required flag | **2** | stderr | `"missing required flag --<name>"` + usage. |
| Command‑specific validation failure | **2** | stderr | Plain‑English error message. |
| Network / API failure | **1** | stderr | Error prefixed with the command name. |
| Success (data) | **0** | stdout | The requested data (table or JSON). |
| Success (action) | **0** | stdout | One‑line confirmation. |

**Rationale:** stdin/stdout are the data pipe; stderr is for diagnostics,
warnings, and human‑readable errors. Exit 0 means success, exit 1 means
something went wrong interacting with a remote service, exit 2 means the
user supplied invalid input.

## Shared flag definitions

| Function | Flag | Default | Help text |
|---|---|---|---|
| `workspaceFlag(fs)` | `--workspace` | `""` | `"workspace name or ID"` |
| `workspaceFlagHelp(fs, help)` | `--workspace` | `""` | Custom help string. |
| `jsonFlag(fs)` | `--json` | `false` | `"output as JSON"` |
| `installationFlag(fs)` | `--installation` | `""` | `"runtime installation ID"` |
| `yesFlag(fs)` | `--yes` | `false` | `"confirm without prompting"` |

**Always use these helpers** instead of writing `fs.String("workspace", …)`
inline. This ensures every command's `--workspace`, `--json`, `--yes`, and
`--installation` flags have identical help text and behaviour.

`workspaceFlagHelp` exists for the rare case where a command needs different
wording (e.g. an additional hint about auto‑detection).

## How to add a new command

This recipe assumes your group already exists and you want to add a leaf
command under it.

### 1. Register the command name and summary

Find the `commandGroup` variable for your group (e.g. `workspaceGroup` in
`workspace.go`) and add an entry:

```go
{name: "destroy", summary: "Destroy a workspace"},
```

### 2. Add a dispatch case

In the group's dispatch function, add a `case`:

```go
case "destroy":
    return runWorkspaceDestroy(rest, stdout, stderr, client, store)
```

### 3. Write the leaf function

```go
func runWorkspaceDestroy(args []string, stdout, stderr io.Writer,
    client *http.Client, store credentialStore) int {

    lc := newCommandFlags("workspaces", "destroy",
        "Destroy a workspace", stdout, stderr).WithArgs("WORKSPACE")
    workspace := workspaceFlag(lc.fs)
    yes := yesFlag(lc.fs)
    jsonOutput := jsonFlag(lc.fs)

    rc, ok := parseLeaf(lc, args)
    if !ok {
        return rc
    }

    pos, err := parsePositionals(lc.fs, args, 1)
    if err != nil {
        fmt.Fprintln(stderr, "workspaces destroy requires a workspace name")
        return 2
    }
    name := pos[0]

    if !*yes {
        fmt.Fprintln(stderr, "workspaces destroy removes the workspace; pass --yes to confirm")
        return 2
    }

    // … API call …
    // On success: fmt.Fprintln(stdout, "Workspace destroyed."); return 0
    // On API failure: fmt.Fprintf(stderr, "workspaces destroy: %v\n", err); return 1
}
```

### 4. Add help‑table tests

Create (or extend) rows in `TestLeafCommandsHelp` in
`leaf_help_table_test.go`:

```go
// ---- workspaces destroy ----
{
    command:  "workspaces",
    args:     []string{"destroy", "-h"},
    wantCode: 0,
    wantOut:  "Usage: kei workspaces destroy WORKSPACE [flags]",
    wantErr:  "",
},
{
    command:  "workspaces",
    args:     []string{"destroy", "--definitely-unknown"},
    wantCode: 2,
    wantOut:  "",
    wantErr:  "Usage: kei workspaces destroy WORKSPACE [flags]",
},
// workspaces destroy requires --yes confirmation.
{
    command:  "workspaces",
    args:     []string{"destroy", "my-workspace"},
    wantCode: 2,
    wantOut:  "",
    wantErr:  "workspaces destroy removes the workspace; pass --yes to confirm",
},
```

The three test cases verify:
1. **(a) Help** — `-h` writes usage to stdout, exit 0.
2. **(b) Unknown flag** — garbage flags write error + usage to stderr, exit 2.
3. **(c) Missing required input** — whichever validation fires first on an
   empty call (missing flag, missing positional, missing confirmation, etc.)
   writes the appropriate error message to stderr, exit 2.

When a command has **no required flags** (e.g. `workspaces current`),
omit case (c) and add a comment `// … has no required flags; skip (c).`.

### 5. Run the tests

```bash
go test ./internal/app/ -run TestLeafCommandsHelp -v
go test ./internal/app/ -skip TestServiceDispatch
go vet ./internal/app/
```

## Guidelines for workspace auto‑detection

Some commands require `--workspace` to be explicit (groups commands because
creating a group in the wrong workspace is destructive); others auto‑detect
the workspace from the saved preference or the `KEI_WORKSPACE` environment
variable (policy commands, access‑grant commands).

The rule of thumb:

- **Do NOT require `--workspace`** for read‑only commands that display or
  test data (`policy list`, `policy get`, `access grant list`).
- **Do NOT require `--workspace`** for commands that create or modify resources
  that are always scoped to the current workspace (`policy create`, `policy delete`,
  `access grant create`, `access grant revoke`).
- **DO require `--workspace`** for group administration (`groups create`,
  `groups list`, `groups delete`) because these operate on identity‑layer
  resources that could affect agent access.
- When a command auto‑detects, it still accepts an explicit `--workspace` to
  override the default.

Auto‑detection runs through `selectWorkspace` (in `workspace.go`), which
checks (in order): explicit flag → `KEI_WORKSPACE` env var →
`KEI_WORKSPACE_ID` env var → saved current workspace → pick the org's
default workspace.

## Deprecation strategy

When a group name is superseded (e.g. `policies` → `policy`), the old group
remains as an alias that prints a deprecation notice:

```go
func runPoliciesCommand(args []string, …) int {
    fmt.Fprintln(stderr, "Deprecated: use `kei policy` instead of `kei policies`.")
    // … forward to the same leaf functions with group="policies" …
}
```

The old group's leaf‑command tests include a `wantErr: "Deprecated:"` check
to confirm the deprecation message appears.
