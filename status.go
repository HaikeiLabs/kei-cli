package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

type runtimeInstallationStatus struct {
	ID              string         `json:"id"`
	Platform        string         `json:"platform,omitempty"`
	DisplayName     string         `json:"display_name,omitempty"`
	Status          string         `json:"status"`
	BindingStatus   string         `json:"binding_status"`
	Deployment      map[string]any `json:"deployment,omitempty"`
	RuntimeVersion  *string        `json:"runtime_version,omitempty"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
}

// runBotStatusCommand prints installation state and safe deployment metadata.
// The runtime credential is never returned by the status endpoint.
func runBotStatusCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "Kei installation ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot status requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot status: --installation must be a UUID")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(*apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot status: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot status: not logged in; run kei login first")
		return 1
	}
	status, err := getRuntimeInstallationStatus(context.Background(), client, baseURL, cliToken, *installationID)
	if err != nil {
		fmt.Fprintf(stderr, "bot status: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(status); err != nil {
		fmt.Fprintf(stderr, "bot status: write response: %v\n", err)
		return 1
	}
	return 0
}

func getRuntimeInstallationStatus(ctx context.Context, client *http.Client, baseURL, cliToken, installationID string) (*runtimeInstallationStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID), nil)
	if err != nil {
		return nil, fmt.Errorf("build runtime status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get runtime status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime status returned %d", response.StatusCode)
	}
	var status runtimeInstallationStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode runtime status: %w", err)
	}
	if status.ID == "" {
		return nil, errors.New("runtime status response is incomplete")
	}
	return &status, nil
}
