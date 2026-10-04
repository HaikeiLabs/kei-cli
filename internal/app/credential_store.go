package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
)

type credentialStoreInstallation struct {
	ID               string         `json:"id"`
	OrgID            string         `json:"org_id"`
	SecretBackend    string         `json:"secret_backend"`
	BackendConfig    map[string]any `json:"backend_config"`
	SecretNamePrefix string         `json:"secret_name_prefix"`
	Status           string         `json:"status"`
	Version          int            `json:"version"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type putCredentialStoreRequest struct {
	SecretBackend    string         `json:"secret_backend"`
	BackendConfig    map[string]any `json:"backend_config,omitempty"`
	SecretNamePrefix string         `json:"secret_name_prefix,omitempty"`
}

func runCredentialStoreCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "credential-store requires a subcommand: get or put")
		return 2
	}
	switch args[0] {
	case "get":
		return runCredentialStoreGet(args[1:], stdout, stderr, client, store)
	case "put":
		return runCredentialStorePut(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown credential-store command %q\n", args[0])
		return 2
	}
}

func runCredentialStoreGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("credential-store get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store get takes no positional arguments")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/cli/credential-store", nil)
	if err != nil {
		fmt.Fprintf(stderr, "credential-store get: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doCredentialStoreGetRequest(req, stdout, stderr, client)
}

func runCredentialStorePut(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("credential-store put", flag.ContinueOnError)
	flags.SetOutput(stderr)
	secretBackend := flags.String("secret-backend", "", "secret backend type (e.g. aws_secrets_manager, azure_key_vault, hashicorp_vault)")
	backendConfigRaw := flags.String("backend-config", "{}", "backend configuration as JSON")
	secretNamePrefix := flags.String("secret-name-prefix", "", "prefix for secret names")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store put takes no positional arguments")
		return 2
	}
	if *secretBackend == "" {
		fmt.Fprintln(stderr, "credential-store put requires --secret-backend")
		return 2
	}
	var backendConfig map[string]any
	if err := json.Unmarshal([]byte(*backendConfigRaw), &backendConfig); err != nil {
		fmt.Fprintf(stderr, "credential-store put: --backend-config must be valid JSON: %v\n", err)
		return 2
	}
	body, err := json.Marshal(putCredentialStoreRequest{
		SecretBackend:    *secretBackend,
		BackendConfig:    backendConfig,
		SecretNamePrefix: *secretNamePrefix,
	})
	if err != nil {
		fmt.Fprintf(stderr, "credential-store put: %v\n", err)
		return 1
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, baseURL+"/api/cli/credential-store", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "credential-store put: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doCredentialStorePutRequest(req, stdout, stderr, client)
}

// TODO: When the console allowlist (kei-console internal/http/proxy/aip/allowlist.go)
// adds IsCLIResource: true for credential-store, change this path from /api/cli/ to
// /api/v1/organizations/{org}/credential-store using the org ID from
// organizationIDFromCLIToken (see bot_list.go).

func doCredentialStoreGetRequest(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		fmt.Fprintf(stderr, "credential-store request: %v\n", err)
		return 1
	}
	if statusCode == http.StatusNotFound {
		fmt.Fprintln(stderr, "No credential store configured.")
		return 1
	}
	if statusCode != http.StatusOK {
		fmt.Fprintf(stderr, "credential-store request returned %d: %s\n", statusCode, bytes.TrimSpace(body))
		return 1
	}
	_, _ = stdout.Write(body)
	return 0
}

func doCredentialStorePutRequest(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		fmt.Fprintf(stderr, "credential-store request: %v\n", err)
		return 1
	}
	if statusCode == http.StatusNoContent {
		fmt.Fprintln(stdout, "Credential store updated.")
		return 0
	}
	if statusCode != http.StatusOK && statusCode != http.StatusCreated {
		fmt.Fprintf(stderr, "credential-store request returned %d: %s\n", statusCode, bytes.TrimSpace(body))
		return 1
	}
	_, _ = stdout.Write(body)
	return 0
}
