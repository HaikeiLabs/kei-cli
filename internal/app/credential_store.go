package app

import (
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
	SecretBackend    string            `json:"secret_backend"`
	BackendConfig    map[string]string `json:"backend_config,omitempty"`
	SecretNamePrefix string            `json:"secret_name_prefix,omitempty"`
}

// fieldMask is an AIP-134 update mask.
type fieldMask struct {
	Paths []string `json:"paths"`
}

// patchCredentialStoreRequest is the body of PATCH …/credential-store: only
// the fields named in update_mask change.
type patchCredentialStoreRequest struct {
	SecretBackend    *string           `json:"secret_backend,omitempty"`
	BackendConfig    map[string]string `json:"backend_config,omitempty"`
	SecretNamePrefix *string           `json:"secret_name_prefix,omitempty"`
	Status           *string           `json:"status,omitempty"`
	UpdateMask       fieldMask         `json:"update_mask"`
}

// The credential store is the organization AIP resource
// /api/v1/organizations/{org}/credential-store, reached through the console
// proxy with the CLI bearer. The organization comes from the CLI token.

func runCredentialStoreCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "credential-store requires a subcommand: get, put, or update")
		return 2
	}
	switch args[0] {
	case "get":
		return runCredentialStoreGet(args[1:], stdout, stderr, client, store)
	case "put":
		return runCredentialStorePut(args[1:], stdout, stderr, client, store)
	case "update":
		return runCredentialStoreUpdate(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown credential-store command %q\n", args[0])
		return 2
	}
}

func parseBackendConfig(command, raw string, stderr io.Writer) (map[string]string, bool) {
	var backendConfig map[string]string
	if err := json.Unmarshal([]byte(raw), &backendConfig); err != nil {
		fmt.Fprintf(stderr, "credential-store %s: --backend-config must be valid JSON with string values: %v\n", command, err)
		return nil, false
	}
	return backendConfig, true
}

func runCredentialStoreGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("credential-store get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store get takes no positional arguments")
		return 2
	}
	session, ok := openOrgSession("credential-store get", "", stderr, client, store)
	if !ok {
		return 1
	}
	status, payload, err := session.do(http.MethodGet, session.orgPath("/credential-store"), nil, nil)
	if status == http.StatusNotFound {
		fmt.Fprintln(stderr, "No credential store configured.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "credential-store get: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(payload)
	return 0
}

// runCredentialStorePut creates or replaces the credential store with PUT. The
// catalog marks PUT deprecated in favour of PATCH, but PATCH cannot create a
// store, so put remains the way to configure one the first time.
func runCredentialStorePut(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("credential-store put", flag.ContinueOnError)
	flags.SetOutput(stderr)
	secretBackend := flags.String("secret-backend", "", "secret backend (aws-secrets-manager, azure-key-vault, or gcp-secret-manager)")
	backendConfigRaw := flags.String("backend-config", "{}", "backend configuration as a JSON object of strings")
	secretNamePrefix := flags.String("secret-name-prefix", "", "prefix for secret names")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store put takes no positional arguments")
		return 2
	}
	if *secretBackend == "" {
		fmt.Fprintln(stderr, "credential-store put requires --secret-backend")
		return 2
	}
	backendConfig, ok := parseBackendConfig("put", *backendConfigRaw, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("credential-store put", "", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPut, session.orgPath("/credential-store"), nil, putCredentialStoreRequest{
		SecretBackend:    *secretBackend,
		BackendConfig:    backendConfig,
		SecretNamePrefix: *secretNamePrefix,
	})
	if err != nil {
		fmt.Fprintf(stderr, "credential-store put: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(payload)
	return 0
}

// runCredentialStoreUpdate changes an existing credential store with PATCH;
// update_mask names exactly the flags given.
func runCredentialStoreUpdate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("credential-store update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.String("secret-backend", "", "secret backend (aws-secrets-manager, azure-key-vault, or gcp-secret-manager)")
	flags.String("backend-config", "", "backend configuration as a JSON object of strings")
	flags.String("secret-name-prefix", "", "prefix for secret names")
	flags.String("status", "", "store status (for example active or disabled)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store update takes no positional arguments")
		return 2
	}
	var req patchCredentialStoreRequest
	valid := true
	flags.Visit(func(f *flag.Flag) {
		value := f.Value.String()
		switch f.Name {
		case "secret-backend":
			req.SecretBackend = &value
			req.UpdateMask.Paths = append(req.UpdateMask.Paths, "secret_backend")
		case "backend-config":
			config, ok := parseBackendConfig("update", value, stderr)
			valid = valid && ok
			req.BackendConfig = config
			req.UpdateMask.Paths = append(req.UpdateMask.Paths, "backend_config")
		case "secret-name-prefix":
			req.SecretNamePrefix = &value
			req.UpdateMask.Paths = append(req.UpdateMask.Paths, "secret_name_prefix")
		case "status":
			req.Status = &value
			req.UpdateMask.Paths = append(req.UpdateMask.Paths, "status")
		}
	})
	if !valid {
		return 2
	}
	if len(req.UpdateMask.Paths) == 0 {
		fmt.Fprintln(stderr, "credential-store update requires at least one of --secret-backend, --backend-config, --secret-name-prefix, or --status")
		return 2
	}
	session, ok := openOrgSession("credential-store update", "", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPatch, session.orgPath("/credential-store"), nil, req)
	if err != nil {
		fmt.Fprintf(stderr, "credential-store update: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(payload)
	return 0
}
