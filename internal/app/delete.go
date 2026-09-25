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
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(*installationID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "bot delete: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "bot delete: request: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "bot delete returned %d\n", response.StatusCode)
		return 1
	}
	fmt.Fprintf(stdout, "Deleted runtime installation %s and revoked its credential.\n", *installationID)
	return 0
}
