package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

// runBotBindCommand activates an installation after its deployed runtime has
// reported a heartbeat. Binding is intentionally explicit and authenticated
// with the operator's Keychain-backed CLI session.
func runBotBindCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot bind", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "installation ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot bind requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot bind: --installation must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(*apiURL, store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(*installationID)+"/bind", nil)
	if err != nil {
		fmt.Fprintf(stderr, "bot bind: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "bot bind failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "bot bind failed: returned status %d\n", resp.StatusCode)
		return 1
	}
	fmt.Fprintf(stdout, "Bound runtime installation %s.\n", *installationID)
	return 0
}
