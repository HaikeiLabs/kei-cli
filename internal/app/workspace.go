package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type workspaceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

func runWorkspaceCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "workspaces requires a subcommand: list, use, current or create")
		return 2
	}
	switch args[0] {
	case "list":
		return runWorkspaceListCommand(args[1:], stdout, stderr, client, store)
	case "use":
		return runWorkspaceUseCommand(args[1:], stdout, stderr, client, store)
	case "current":
		return runWorkspaceCurrentCommand(args[1:], stdout, stderr)
	case "create":
		return runWorkspaceCreateCommand(args[1:], stdout, stderr, stdin, client, store)
	default:
		fmt.Fprintf(stderr, "unknown workspaces command %q\n", args[0])
		return 2
	}
}

// runWorkspaceUseCommand saves the named workspace as the current one for the
// login on this host, so later commands can omit --workspace.
func runWorkspaceUseCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("workspaces use", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "workspaces use requires one workspace NAME")
		return 2
	}
	name := flags.Arg(0)

	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "workspaces use: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "workspaces use: not logged in; run kei login first")
		return 1
	}
	workspace, err := resolveWorkspaceByName(context.Background(), client, baseURL, cliToken, name)
	if err != nil {
		fmt.Fprintf(stderr, "workspaces use: %v\n", err)
		return 1
	}
	if err := saveCurrentWorkspace(baseURL, workspace.Name); err != nil {
		fmt.Fprintf(stderr, "workspaces use: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Now using workspace %s.\n", workspace.Name)
	return 0
}

// runWorkspaceCurrentCommand prints the current workspace saved for this host.
func runWorkspaceCurrentCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "workspaces current accepts no positional arguments")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "workspaces current: %v\n", err)
		return 2
	}
	name, err := loadCurrentWorkspace(baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "workspaces current: %v\n", err)
		return 1
	}
	if name == "" {
		fmt.Fprintln(stdout, "No current workspace set; run kei workspaces use NAME.")
		return 0
	}
	fmt.Fprintln(stdout, name)
	return 0
}

func runWorkspaceListCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("workspaces list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "workspaces list accepts no positional arguments")
		return 2
	}

	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "workspaces list: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "workspaces list: not logged in; run kei login first")
		return 1
	}

	workspaces, err := listWorkspaces(context.Background(), client, baseURL, cliToken)
	if err != nil {
		fmt.Fprintf(stderr, "workspaces list: %v\n", err)
		return 1
	}

	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(workspaces)
		return 0
	}

	if len(workspaces) == 0 {
		fmt.Fprintln(stdout, "No workspaces found.")
		return 0
	}

	currentName, err := loadCurrentWorkspace(baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "workspaces list: %v\n", err)
		return 1
	}
	for _, w := range workspaces {
		if w.Name == currentName {
			fmt.Fprintf(stdout, "* %s\n", w.Name)
		} else {
			fmt.Fprintf(stdout, "  %s\n", w.Name)
		}
	}
	return 0
}

// runWorkspaceCreateCommand creates a workspace in the CLI token's
// organization. The body carries only the name: the server records the
// authenticated caller as the creator and grants them initial membership.
func runWorkspaceCreateCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("workspaces create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "workspace name (required)")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	yes := flags.Bool("yes", false, "confirm without prompting")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "workspaces create requires --name NAME")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Create workspace "+*name+"?") {
		return 2
	}
	session, ok := openAPISession("workspaces create", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPost, "/api/v1/organizations/"+url.PathEscape(session.orgID)+"/workspaces", nil, map[string]string{"name": *name})
	if err != nil {
		fmt.Fprintf(stderr, "workspaces create: %v\n", err)
		return 1
	}
	var created workspaceInfo
	if err := json.Unmarshal(payload, &created); err != nil || created.ID == "" {
		fmt.Fprintln(stderr, "workspaces create: decode response: missing workspace id")
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(created)
		return 0
	}
	fmt.Fprintf(stdout, "Created workspace %s.\nWorkspace ID: %s\n", created.Name, created.ID)
	return 0
}

func pickDefaultWorkspace(workspaces []workspaceInfo) (workspaceInfo, bool) {
	if len(workspaces) == 0 {
		return workspaceInfo{}, false
	}
	for _, ws := range workspaces {
		if ws.IsDefault {
			return ws, true
		}
	}
	return workspaces[0], true
}

func pickDefaultWorkspaceName(workspaces []workspaceInfo, err error) string {
	if err != nil {
		return ""
	}
	ws, ok := pickDefaultWorkspace(workspaces)
	if !ok {
		return ""
	}
	return ws.Name
}

func getOrganizationName(ctx context.Context, client *http.Client, baseURL, cliToken, orgID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/organizations", nil)
	if err != nil {
		return "", fmt.Errorf("build organizations request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	status, body, err := doRequest(client, req, 8<<10)
	if err != nil {
		return "", fmt.Errorf("list organizations: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("list organizations returned %d", status)
	}
	var orgs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil {
		return "", fmt.Errorf("decode organizations response: %w", err)
	}
	for _, o := range orgs {
		if o.ID == orgID {
			if o.Name == "" {
				return "", fmt.Errorf("organization name is empty")
			}
			return o.Name, nil
		}
	}
	return "", fmt.Errorf("organization %q not found in list", orgID)
}

func listWorkspaces(ctx context.Context, client *http.Client, baseURL, cliToken string) ([]workspaceInfo, error) {
	orgID, err := organizationIDFromCLIToken(cliToken)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/workspaces", nil)
	if err != nil {
		return nil, fmt.Errorf("build workspaces request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("list workspaces returned %d", statusCode)
	}
	var list []workspaceInfo
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode workspaces response: %w", err)
	}
	return list, nil
}

// workspaceSelection is the workspace a command targets and where the value
// came from. fromIDEnv marks the deprecated KEI_WORKSPACE_ID, the only
// selection that may carry a UUID. pickDefault is set when no flag, env var
// or saved workspace is present — the caller should list workspaces and
// use the default (is_default) or first accessible.
type workspaceSelection struct {
	value       string
	fromIDEnv   bool
	pickDefault bool
}

// selectWorkspace applies the workspace selection precedence for a command:
// the --workspace flag value, then the KEI_WORKSPACE name, then the
// deprecated KEI_WORKSPACE_ID, then the workspace saved with
// kei workspaces use. When none of these are set, pickDefault is true —
// the caller should list workspaces and use the default (or first accessible).
func selectWorkspace(flagValue, baseURL string) (workspaceSelection, error) {
	if flagValue != "" {
		return workspaceSelection{value: flagValue}, nil
	}
	if name := os.Getenv("KEI_WORKSPACE"); name != "" {
		return workspaceSelection{value: name}, nil
	}
	if id := os.Getenv("KEI_WORKSPACE_ID"); id != "" {
		return workspaceSelection{value: id, fromIDEnv: true}, nil
	}
	saved, err := loadCurrentWorkspace(baseURL)
	if err != nil {
		return workspaceSelection{}, err
	}
	if saved != "" {
		return workspaceSelection{value: saved}, nil
	}
	return workspaceSelection{pickDefault: true}, nil
}

// deprecationNote is the one-line notice printed when a command falls back
// to the deprecated KEI_WORKSPACE_ID.
const deprecationNote = "KEI_WORKSPACE_ID is deprecated; set KEI_WORKSPACE to a workspace name or run kei workspaces use"

// resolveWorkspaceSelection resolves a selection to a workspace ID. The
// deprecated KEI_WORKSPACE_ID still accepts UUIDs; every other selection must
// be a name. When pickDefault is set, the default workspace (is_default) is
// used, or the first accessible workspace if none is marked default.
func resolveWorkspaceSelection(ctx context.Context, client *http.Client, baseURL, cliToken string, sel workspaceSelection) (string, error) {
	if sel.pickDefault {
		workspaces, err := listWorkspaces(ctx, client, baseURL, cliToken)
		if err != nil {
			return "", err
		}
		ws, ok := pickDefaultWorkspace(workspaces)
		if !ok {
			return "", fmt.Errorf("no workspaces found; use kei workspaces list or set KEI_WORKSPACE")
		}
		return ws.ID, nil
	}
	if sel.fromIDEnv {
		return resolveWorkspaceID(ctx, client, baseURL, cliToken, sel.value)
	}
	ws, err := resolveWorkspaceByName(ctx, client, baseURL, cliToken, sel.value)
	if err != nil {
		return "", err
	}
	return ws.ID, nil
}

func resolveWorkspaceID(ctx context.Context, client *http.Client, baseURL, cliToken, workspace string) (string, error) {
	if _, err := uuid.Parse(workspace); err == nil {
		return workspace, nil
	}
	ws, err := resolveWorkspaceByName(ctx, client, baseURL, cliToken, workspace)
	if err != nil {
		return "", err
	}
	return ws.ID, nil
}

// resolveWorkspaceByName resolves a workspace NAME against the access-aware
// workspace list. UUID-shaped input is rejected: users select workspaces by
// name, and the deprecated KEI_WORKSPACE_ID is the only ID path.
func resolveWorkspaceByName(ctx context.Context, client *http.Client, baseURL, cliToken, name string) (workspaceInfo, error) {
	if _, err := uuid.Parse(name); err == nil {
		return workspaceInfo{}, fmt.Errorf("workspace %q looks like a UUID; use the workspace name instead (kei workspaces list)", name)
	}
	workspaces, err := listWorkspaces(ctx, client, baseURL, cliToken)
	if err != nil {
		return workspaceInfo{}, fmt.Errorf("resolve workspace: %w", err)
	}

	var matches []workspaceInfo
	for _, w := range workspaces {
		if w.Name == name {
			matches = append(matches, w)
		}
	}

	if len(matches) == 0 {
		if len(workspaces) == 0 {
			return workspaceInfo{}, fmt.Errorf("no workspaces found; use kei workspaces list to see available workspaces")
		}
		names := make([]string, len(workspaces))
		for i, w := range workspaces {
			names[i] = w.Name
		}
		return workspaceInfo{}, fmt.Errorf("workspace %q not found; available workspaces: %s", name, strings.Join(names, ", "))
	}

	if len(matches) > 1 {
		return workspaceInfo{}, fmt.Errorf("multiple workspaces match name %q; pick a unique name", name)
	}

	return matches[0], nil
}

// currentWorkspacesPath is the file that stores the current workspace name
// per server URL, so each login on this host remembers its own choice.
func currentWorkspacesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kei", "current-workspaces.json"), nil
}

func readCurrentWorkspaces() (map[string]string, error) {
	path, err := currentWorkspacesPath()
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read current workspaces: %w", err)
	}
	byServer := map[string]string{}
	if err := json.Unmarshal(contents, &byServer); err != nil {
		return nil, fmt.Errorf("read current workspaces: %w", err)
	}
	return byServer, nil
}

// saveCurrentWorkspace records name as the current workspace for the login on
// serverURL.
func saveCurrentWorkspace(serverURL, name string) error {
	base, err := normalizedKeiWebURL(serverURL)
	if err != nil {
		return err
	}
	byServer, err := readCurrentWorkspaces()
	if err != nil {
		return err
	}
	byServer[base] = name
	path, err := currentWorkspacesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	contents, err := json.MarshalIndent(byServer, "", "  ")
	if err != nil {
		return fmt.Errorf("encode current workspaces: %w", err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("write current workspaces: %w", err)
	}
	return nil
}

// loadCurrentWorkspace returns the workspace name saved as current for
// serverURL, or "" when none is saved.
func loadCurrentWorkspace(serverURL string) (string, error) {
	base, err := normalizedKeiWebURL(serverURL)
	if err != nil {
		return "", err
	}
	byServer, err := readCurrentWorkspaces()
	if err != nil {
		return "", err
	}
	return byServer[base], nil
}
