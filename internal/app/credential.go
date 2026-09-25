package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type runtimeCredentialRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

type runtimeCredentialResponse struct {
	RuntimeToken string `json:"runtime_token"`
}

func requestRuntimeCredential(ctx context.Context, client *http.Client, baseURL, cliToken, installationID, workspaceID string) (string, error) {
	token, status, err := requestRuntimeCredentialAction(ctx, client, baseURL, cliToken, installationID, workspaceID, "credential")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("credential request returned %d", status)
	}
	return token, nil
}

func requestRuntimeCredentialAction(ctx context.Context, client *http.Client, baseURL, cliToken, installationID, workspaceID, action string) (string, int, error) {
	if action != "credential" && action != "rotate" {
		return "", 0, fmt.Errorf("invalid credential action %q", action)
	}
	body, err := json.Marshal(runtimeCredentialRequest{WorkspaceID: workspaceID})
	if err != nil {
		return "", 0, fmt.Errorf("encode credential request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/"+action, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("build credential request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
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
