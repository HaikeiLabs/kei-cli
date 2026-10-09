package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

type botInstallation struct {
	ID              string     `json:"id"`
	WorkspaceIDs    []string   `json:"workspace_ids"`
	Platform        string     `json:"platform"`
	DisplayName     string     `json:"display_name"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type botInstallationPage struct {
	Installations []botInstallation `json:"runtime_installations"`
	NextPageToken string            `json:"next_page_token"`
}

func runBotListCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	all := flags.Bool("all", false, "fetch all pages")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "bot list accepts no positional arguments")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "bot list: %v\n", err)
		return 2
	}
	token, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot list: not logged in; run kei login first")
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot list: cannot determine organization from login; run kei login again\n")
		return 1
	}
	items, next, err := listBotInstallations(context.Background(), client, baseURL, token, orgID, *all)
	if err != nil {
		if err == errNotLoggedIn {
			fmt.Fprintln(stderr, "bot list: not logged in; run kei login first")
		} else if err == errNotOrganizationAdmin {
			fmt.Fprintln(stderr, "bot list: you must be an organization admin for your logged-in organization")
		} else {
			fmt.Fprintf(stderr, "bot list: %v\n", err)
		}
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			fmt.Fprintf(stderr, "bot list: write JSON: %v\n", err)
			return 1
		}
		return 0
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "No runtime installations found.")
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPLATFORM\tSTATUS\tWORKSPACES\tLAST HEARTBEAT")
		for _, item := range items {
			workspaces := "-"
			if len(item.WorkspaceIDs) > 0 {
				workspaces = strings.Join(item.WorkspaceIDs, ",")
			}
			heartbeat := "never"
			if item.LastHeartbeatAt != nil {
				heartbeat = harness.RelativeTime(time.Since(*item.LastHeartbeatAt))
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.DisplayName, item.Platform, item.Status, workspaces, heartbeat)
		}
		_ = tw.Flush()
	}
	if next != "" && !*all {
		fmt.Fprintln(stderr, "More results available; use --all to fetch every page.")
	}
	return 0
}

// organizationIDFromCLIToken reads the tenant selector from the JWT payload. This is
// not an authentication check: the server validates the bearer and enforces its org scope.
func organizationIDFromCLIToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid login token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("invalid login token")
	}
	var claims struct {
		OrgID string `json:"org_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || strings.TrimSpace(claims.OrgID) == "" {
		return "", fmt.Errorf("organization claim missing")
	}
	return claims.OrgID, nil
}

var (
	errNotLoggedIn          = fmt.Errorf("not logged in")
	errNotOrganizationAdmin = fmt.Errorf("not an organization admin")
)

func listBotInstallations(ctx context.Context, client *http.Client, baseURL, token, orgID string, all bool) ([]botInstallation, string, error) {
	var items []botInstallation
	pageToken := ""
	for {
		query := url.Values{"page_size": {"50"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		target := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/runtime-installations?" + query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, "", fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("request runtime installations: %w", err)
		}
		var page botInstallationPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, "", errNotLoggedIn
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, "", errNotOrganizationAdmin
		}
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("list runtime installations returned %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, "", fmt.Errorf("decode runtime installations response: %w", decodeErr)
		}
		items = append(items, page.Installations...)
		if page.NextPageToken == "" || !all {
			return items, page.NextPageToken, nil
		}
		pageToken = page.NextPageToken
	}
}
