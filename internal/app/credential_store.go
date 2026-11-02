package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// credentialStoreSummary is the safe, metadata-only output of credential-store
// get. In particular, backend_config is deliberately excluded because it may
// contain provider credentials, and a backend response must never expose a
// stored secret value through this command.
type credentialStoreSummary struct {
	ID               string `json:"id,omitempty"`
	OrgID            string `json:"org_id,omitempty"`
	SecretBackend    string `json:"secret_backend,omitempty"`
	SecretNamePrefix string `json:"secret_name_prefix,omitempty"`
	Status           string `json:"status,omitempty"`
	Version          int    `json:"version,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

// secretNamePrefixHelp explains --secret-name-prefix: it names generated
// secrets, it is not the name or value of an existing secret.
const secretNamePrefixHelp = "prefix Kei appends -<id> to when naming each secret, e.g. my-chat -> my-chat-<uuid> (letters, digits, hyphens; start with a letter); not a secret value"

// olderCatalogHint is appended when PATCH-create is refused in a way an
// older catalog (before HAI-954) refuses it.
const olderCatalogHint = "the catalog may predate HAI-954 and cannot create a credential store with PATCH and allow_missing; it needs the HAI-954 update"

// credentialStorePath is the organization's store, or with a workspace
// session the workspace's override (ADR-036 §10 HAI-489 test 5).
func credentialStorePath(session *orgSession) string {
	if session.workspaceID != "" {
		return session.orgPath("/workspaces/" + url.PathEscape(session.workspaceID) + "/credential-store")
	}
	return session.orgPath("/credential-store")
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
	// AllowMissing creates the store when none exists (AIP-134). Only put
	// sets it; update must not create a store by accident.
	AllowMissing bool `json:"allow_missing,omitempty"`
}

// The credential store is the organization AIP resource
// /api/v1/organizations/{org}/credential-store, reached through the console
// proxy with the CLI bearer. The organization comes from the CLI token. With
// --workspace, get and put address the workspace's override instead.

var credentialStoreGroup = commandGroup{
	name: "credential-store",
	commands: []groupCommand{
		{name: "get", summary: "Get the credential store configuration"},
		{name: "put", summary: "Create the credential store, or replace its configuration"},
		{name: "update", summary: "Update the credential store configuration"},
	},
}

func runCredentialStoreCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := credentialStoreGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "get":
		return runCredentialStoreGet(rest, stdout, stderr, client, store)
	case "put":
		return runCredentialStorePut(rest, stdout, stderr, client, store)
	case "update":
		return runCredentialStoreUpdate(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
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
	lc := newCommandFlags("credential-store", "get", "Get the credential store configuration", stdout, stderr)
	workspace := workspaceFlagHelp(lc.fs, "workspace name: read its override instead of the organization's store")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store get takes no positional arguments")
		return 2
	}
	session, ok := openOrgSession("credential-store get", workspaceSelection{value: *workspace}, stderr, client, store)
	if !ok {
		return 1
	}
	status, payload, err := session.do(http.MethodGet, credentialStorePath(session), nil, nil)
	if status == http.StatusNotFound {
		if session.workspaceID != "" {
			fmt.Fprintln(stderr, "No credential store override for this workspace; it uses the organization's store.")
			return 1
		}
		fmt.Fprintln(stderr, "No credential store configured.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "credential-store get: %v\n", err)
		return 1
	}
	var installation credentialStoreInstallation
	if err := json.Unmarshal(payload, &installation); err != nil {
		fmt.Fprintf(stderr, "credential-store get: invalid response: %v\n", err)
		return 1
	}
	// Print only the store reference and non-sensitive metadata, like cloud
	// secret describe commands. Never forward the API response verbatim.
	summary, err := json.Marshal(credentialStoreSummary{
		ID: installation.ID, OrgID: installation.OrgID,
		SecretBackend: installation.SecretBackend, SecretNamePrefix: installation.SecretNamePrefix,
		Status: installation.Status, Version: installation.Version,
		CreatedAt: installation.CreatedAt, UpdatedAt: installation.UpdatedAt,
	})
	if err != nil {
		fmt.Fprintf(stderr, "credential-store get: encode response: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, string(summary))
	return 0
}

// runCredentialStorePut creates the credential store, or replaces its backend,
// backend config and name prefix, with PATCH, all three fields in update_mask
// and allow_missing=true (AIP-134). The catalog answers 201 when it created
// the store and 200 when it updated one.
func runCredentialStorePut(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("credential-store", "put", "Create the credential store, or replace its configuration", stdout, stderr)
	secretBackend := lc.fs.String("secret-backend", "", "secret backend (aws-secrets-manager, azure-key-vault, or gcp-secret-manager)")
	backendConfigRaw := lc.fs.String("backend-config", "{}", "backend configuration as a JSON object of strings")
	secretNamePrefix := lc.fs.String("secret-name-prefix", "", secretNamePrefixHelp)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store put takes no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "secret-backend", "secret-name-prefix") {
		return 2
	}
	backendConfig, ok := parseBackendConfig("put", *backendConfigRaw, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("credential-store put", workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	req := patchCredentialStoreRequest{
		SecretBackend:    secretBackend,
		BackendConfig:    backendConfig,
		SecretNamePrefix: secretNamePrefix,
		UpdateMask:       fieldMask{Paths: []string{"secret_backend", "backend_config", "secret_name_prefix"}},
		AllowMissing:     true,
	}
	// allow_missing goes in the query as well as the body so either spelling
	// of the AIP-134 request field reaches the catalog.
	query := url.Values{"allow_missing": {"true"}}
	status, payload, err := session.do(http.MethodPatch, session.orgPath("/credential-store"), query, req)
	if err != nil {
		switch status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed:
			fmt.Fprintf(stderr, "credential-store put: %v; %s\n", err, olderCatalogHint)
		default:
			fmt.Fprintf(stderr, "credential-store put: %v\n", err)
		}
		return 1
	}
	_, _ = stdout.Write(payload)
	return 0
}

// runCredentialStoreUpdate changes an existing credential store with PATCH;
// update_mask names exactly the flags given.
func runCredentialStoreUpdate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("credential-store", "update", "Update the credential store configuration", stdout, stderr)
	lc.fs.String("secret-backend", "", "secret backend (aws-secrets-manager, azure-key-vault, or gcp-secret-manager)")
	lc.fs.String("backend-config", "", "backend configuration as a JSON object of strings")
	lc.fs.String("secret-name-prefix", "", secretNamePrefixHelp)
	lc.fs.String("status", "", "store status (for example active or disabled)")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store update takes no positional arguments")
		return 2
	}
	var req patchCredentialStoreRequest
	valid := true
	lc.fs.Visit(func(f *flag.Flag) {
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
	session, ok := openOrgSession("credential-store update", workspaceSelection{}, stderr, client, store)
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
