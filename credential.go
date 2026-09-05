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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/credential", bytes.NewReader(nil))
	if err != nil {
		return "", fmt.Errorf("build credential request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request credential: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", fmt.Errorf("credential request returned %d: %s", resp.StatusCode, string(body))
	}
	var credential runtimeCredentialResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&credential); err != nil {
		return "", fmt.Errorf("decode credential response: %w", err)
	}
	if credential.RuntimeToken == "" {
		return "", fmt.Errorf("credential response is incomplete")
	}
	return credential.RuntimeToken, nil
}
