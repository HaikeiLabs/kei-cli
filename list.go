package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func runBotListCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	apiURL := keiWebURL()
	if len(args) == 2 && args[0] == "--api-url" {
		apiURL = args[1]
	} else if len(args) != 0 {
		fmt.Fprintln(stderr, "bot list accepts only --api-url URL")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot list: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot list: not logged in; run kei login first")
		return 1
	}
	installations, err := listRuntimeInstallations(context.Background(), client, baseURL, cliToken)
	if err != nil {
		fmt.Fprintf(stderr, "bot list: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(installations); err != nil {
		fmt.Fprintf(stderr, "bot list: write response: %v\n", err)
		return 1
	}
	return 0
}

func listRuntimeInstallations(ctx context.Context, client *http.Client, baseURL, cliToken string) ([]runtimeInstallationStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/cli/runtime-installations", nil)
	if err != nil {
		return nil, fmt.Errorf("build runtime installations request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list runtime installations: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime installation list returned %d", response.StatusCode)
	}
	var installations []runtimeInstallationStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&installations); err != nil {
		return nil, fmt.Errorf("decode runtime installation list: %w", err)
	}
	return installations, nil
}
