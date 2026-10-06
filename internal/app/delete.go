package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

func runBotDeleteCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installationID := flags.String("installation", "", "Kei installation ID")
	yes := flags.Bool("yes", false, "confirm permanent deletion")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot delete requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot delete: --installation must be a UUID")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "bot delete permanently removes the installation and revokes its credential; repeat with --yes")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot delete: %v\n", err)
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations/"+url.PathEscape(*installationID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "bot delete: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	statusCode, _, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		fmt.Fprintf(stderr, "bot delete: request: %v\n", err)
		return 1
	}
	if statusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "bot delete returned %d\n", statusCode)
		return 1
	}
	fmt.Fprintf(stdout, "Deleted runtime installation %s and revoked its credential.\n", *installationID)
	return 0
}
