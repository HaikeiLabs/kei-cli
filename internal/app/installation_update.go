package app

import (
	"bytes"
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

func runBotUpdateCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installationID := flags.String("installation", "", "Kei installation ID")
	name := flags.String("name", "", "new installation name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	displayName := strings.TrimSpace(*name)
	if flags.NArg() != 0 || *installationID == "" || displayName == "" {
		fmt.Fprintln(stderr, "bot update requires --installation ID and --name NAME")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot update: --installation must be a UUID")
		return 2
	}
	if len([]rune(displayName)) > 255 {
		fmt.Fprintln(stderr, "bot update: --name must be 255 characters or fewer")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot update: %v\n", err)
		return 1
	}
	body, err := json.Marshal(map[string]string{"display_name": displayName})
	if err != nil {
		fmt.Fprintf(stderr, "bot update: encode request: %v\n", err)
		return 1
	}
	endpoint := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/runtime-installations/" + url.PathEscape(*installationID) + "?update_mask=display_name"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "bot update: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	status, response, err := doRequest(client, req, 16<<10)
	if err != nil {
		fmt.Fprintf(stderr, "bot update: request: %v\n", err)
		return 1
	}
	switch status {
	case http.StatusOK:
		fmt.Fprintf(stdout, "Renamed runtime installation %s to %q.\n", *installationID, displayName)
		return 0
	case http.StatusNotFound:
		fmt.Fprintln(stderr, "bot update: runtime installation not found")
	case http.StatusConflict:
		fmt.Fprintln(stderr, "bot update: another installation already uses that name")
	default:
		fmt.Fprintf(stderr, "bot update returned %d", status)
		if len(response) > 0 {
			fmt.Fprintf(stderr, ": %s", strings.TrimSpace(string(response)))
		}
		fmt.Fprintln(stderr)
	}
	return 1
}
