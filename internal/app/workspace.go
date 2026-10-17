package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		fmt.Fprintln(stderr, "workspaces requires a subcommand: list or create")
		return 2
	}
	switch args[0] {
	case "list":
		return runWorkspaceListCommand(args[1:], stdout, stderr, client, store)
	case "create":
		return runWorkspaceCreateCommand(args[1:], stdout, stderr, stdin, client, store)
	default:
		fmt.Fprintf(stderr, "unknown workspaces command %q\n", args[0])
		return 2
	}
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

	for _, w := range workspaces {
		fmt.Fprintf(stdout, "  %s  %s\n", w.ID, w.Name)
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

func resolveWorkspaceID(ctx context.Context, client *http.Client, baseURL, cliToken, workspace string) (string, error) {
	if _, err := uuid.Parse(workspace); err == nil {
		return workspace, nil
	}

	workspaces, err := listWorkspaces(ctx, client, baseURL, cliToken)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}

	var matches []workspaceInfo
	for _, w := range workspaces {
		if w.Name == workspace {
			matches = append(matches, w)
		}
	}

	if len(matches) == 0 {
		if len(workspaces) == 0 {
			return "", fmt.Errorf("no workspaces found; use kei workspaces list to see available workspaces")
		}
		names := make([]string, len(workspaces))
		for i, w := range workspaces {
			names[i] = w.Name
		}
		return "", fmt.Errorf("workspace %q not found; available workspaces: %s", workspace, strings.Join(names, ", "))
	}

	if len(matches) > 1 {
		return "", fmt.Errorf("multiple workspaces match name %q; use workspace ID instead", workspace)
	}

	return matches[0].ID, nil
}
