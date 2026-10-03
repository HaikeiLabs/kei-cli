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
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"filippo.io/age"
)

type auditEncryptionKey struct {
	Name        string     `json:"name"`
	KeyID       string     `json:"key_id"`
	PublicKey   string     `json:"public_key"`
	DisplayName string     `json:"display_name,omitempty"`
	State       string     `json:"state"`
	Kind        string     `json:"kind"`
	CreateTime  *time.Time `json:"create_time,omitempty"`
	DisableTime *time.Time `json:"disable_time,omitempty"`
}

type createAuditKeyRequest struct {
	DisplayName string `json:"display_name,omitempty"`
	PublicKey   string `json:"public_key"`
}

type auditKeyListResponse struct {
	Keys          []auditEncryptionKey `json:"audit_encryption_keys"`
	NextPageToken string               `json:"next_page_token"`
}

func defaultIdentityPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kei", "audit-identity.txt"), nil
}

// runAuditCommand dispatches audit subcommands.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "audit requires a subcommand")
		return 2
	}
	switch args[0] {
	case "keys":
		return runAuditKeysCommand(args[1:], stdout, stderr, client, store)
	case "decrypt":
		return runAuditDecryptCommand(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown audit command %q\n", args[0])
		return 2
	}
}

// runAuditKeysCommand dispatches audit keys subcommands.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditKeysCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "audit keys requires a subcommand (create|list|disable)")
		return 2
	}
	switch args[0] {
	case "create":
		return runAuditKeysCreateCommand(args[1:], stdout, stderr, client, store)
	case "list":
		return runAuditKeysListCommand(args[1:], stdout, stderr, client, store)
	case "disable":
		return runAuditKeysDisableCommand(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown audit keys command %q\n", args[0])
		return 2
	}
}

// runAuditKeysCreateCommand generates an age X25519 key pair, uploads the
// public key to Kei, and writes the private identity file to disk.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditKeysCreateCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("audit keys create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	identityOut := flags.String("identity-out", "", "path to write the age identity file (default ~/.config/kei/audit-identity.txt)")
	name := flags.String("name", "", "display name for the key")
	force := flags.Bool("force", false, "overwrite existing identity file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "audit keys create accepts no positional arguments")
		return 2
	}

	identityPath := *identityOut
	if identityPath == "" {
		var err error
		identityPath, err = defaultIdentityPath()
		if err != nil {
			fmt.Fprintf(stderr, "audit keys create: %v\n", err)
			return 1
		}
	}

	if _, err := os.Stat(identityPath); err == nil && !*force {
		fmt.Fprintf(stderr, "audit keys create: identity file %q already exists; use --force to overwrite\n", identityPath)
		return 2
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		fmt.Fprintf(stderr, "audit keys create: generate key: %v\n", err)
		return 1
	}

	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys create: cannot determine organization; run kei login again\n")
		return 1
	}

	reqBody := createAuditKeyRequest{PublicKey: identity.Recipient().String()}
	if *name != "" {
		reqBody.DisplayName = *name
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys create: %v\n", err)
		return 1
	}

	target := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/auditEncryptionKeys"
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "audit keys create: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys create: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "audit keys create: server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	var key auditEncryptionKey
	if err := json.NewDecoder(resp.Body).Decode(&key); err != nil {
		fmt.Fprintf(stderr, "audit keys create: decode response: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "audit keys create: create directory: %v\n", err)
		return 1
	}
	identityData := fmt.Sprintf("# age identity file for Kei audit decryption\n# key_id: %s\n# If you lose this file, audit records encrypted with this key\n# cannot be decrypted. Keep it private and backed up.\n%s\n", key.KeyID, identity.String())
	if err := os.WriteFile(identityPath, []byte(identityData), 0o600); err != nil {
		fmt.Fprintf(stderr, "audit keys create: write identity: %v\n", err)
		return 1
	}
	if err := os.Chmod(identityPath, 0o600); err != nil {
		fmt.Fprintf(stderr, "audit keys create: set permissions: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "key_id: %s\npublic_key: %s\n", key.KeyID, key.PublicKey)
	return 0
}

// runAuditKeysListCommand lists audit encryption keys for the logged-in organization.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditKeysListCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("audit keys list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "audit keys list takes no positional arguments")
		return 2
	}

	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys list: cannot determine organization; run kei login again\n")
		return 1
	}

	target := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/auditEncryptionKeys"
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys list: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys list: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintln(stderr, "audit keys list: not logged in; run kei login first")
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "audit keys list: server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	var page auditKeyListResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		fmt.Fprintf(stderr, "audit keys list: decode response: %v\n", err)
		return 1
	}

	if len(page.Keys) == 0 {
		fmt.Fprintln(stdout, "No audit encryption keys found.")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY ID\tPUBLIC KEY\tNAME\tSTATE\tKIND\tCREATED")
	for _, k := range page.Keys {
		created := "-"
		if k.CreateTime != nil {
			created = k.CreateTime.Format(time.RFC3339)
		}
		displayName := k.DisplayName
		if displayName == "" {
			displayName = k.Name
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.KeyID, k.PublicKey, displayName, k.State, k.Kind, created)
	}
	_ = tw.Flush()
	return 0
}

// runAuditKeysDisableCommand disables an audit encryption key so new records
// no longer use it. Previously encrypted records remain decryptable with the
// corresponding private identity.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditKeysDisableCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("audit keys disable", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "audit keys disable requires exactly one argument: the key ID to disable")
		return 2
	}
	keyID := flags.Arg(0)

	fmt.Fprintf(stderr, "WARNING: Disabling audit encryption key %q.\n", keyID)
	fmt.Fprintln(stderr, "Audit records previously encrypted with this key will still require")
	fmt.Fprintln(stderr, "the private key for decryption. New records will not use this key.")
	fmt.Fprintln(stderr, "Type the key ID again to confirm:")
	var confirmation string
	fmt.Scanln(&confirmation)
	if strings.TrimSpace(confirmation) != keyID {
		fmt.Fprintln(stderr, "confirmation does not match; aborting")
		return 2
	}

	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys disable: cannot determine organization; run kei login again\n")
		return 1
	}

	target := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/auditEncryptionKeys/" + url.PathEscape(keyID) + ":disable"
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys disable: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(stderr, "audit keys disable: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "audit keys disable: server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	var key auditEncryptionKey
	if err := json.NewDecoder(resp.Body).Decode(&key); err != nil {
		fmt.Fprintf(stderr, "audit keys disable: decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Audit encryption key %q disabled.\n", keyID)
	return 0
}
