package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type runtimeCredentialResponse struct {
	RuntimeToken string `json:"runtime_token"`
}

func requestRuntimeCredential(ctx context.Context, client *http.Client, baseURL, cliToken, installationID string) (string, error) {
	token, status, err := requestRuntimeCredentialAction(ctx, client, baseURL, cliToken, installationID, "credential")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("credential request returned %d", status)
	}
	return token, nil
}

func requestRuntimeCredentialAction(ctx context.Context, client *http.Client, baseURL, cliToken, installationID, action string) (string, int, error) {
	if action != "credential" && action != "rotate" {
		return "", 0, fmt.Errorf("invalid credential action %q", action)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/"+action, bytes.NewReader(nil))
	if err != nil {
		return "", 0, fmt.Errorf("build credential request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request credential: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, nil
	}
	var credential runtimeCredentialResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&credential); err != nil {
		return "", resp.StatusCode, fmt.Errorf("decode credential response: %w", err)
	}
	if credential.RuntimeToken == "" {
		return "", resp.StatusCode, fmt.Errorf("credential response is incomplete")
	}
	return credential.RuntimeToken, resp.StatusCode, nil
}
