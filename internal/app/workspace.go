package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type workspaceInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

type workspaceListResponse struct {
	Workspaces []workspaceInfo `json:"workspaces"`
}

func runWorkspaceCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "workspaces requires a subcommand: list")
		return 2
	}
	switch args[0] {
	case "list":
		return runWorkspaceListCommand(args[1:], stdout, stderr, client, store)
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
		_ = enc.Encode(workspaceListResponse{Workspaces: workspaces})
		return 0
	}

	if len(workspaces) == 0 {
		fmt.Fprintln(stdout, "No workspaces found.")
		return 0
	}

	for _, w := range workspaces {
		admin := ""
		if w.IsAdmin {
			admin = " (admin)"
		}
		fmt.Fprintf(stdout, "  %s  %s%s\n", w.ID, w.Name, admin)
	}
	return 0
}

func listWorkspaces(ctx context.Context, client *http.Client, baseURL, cliToken string) ([]workspaceInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/cli/workspaces", nil)
	if err != nil {
		return nil, fmt.Errorf("build workspaces request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list workspaces returned %d", resp.StatusCode)
	}
	var list workspaceListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode workspaces response: %w", err)
	}
	return list.Workspaces, nil
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
