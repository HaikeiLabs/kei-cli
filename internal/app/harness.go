package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
	"github.com/google/uuid"
)

type harnessRenderer interface {
	Render(kind, harnessID string, bundle Bundle, files map[string][]byte) (renderedHarness, error)
	HookSpec(kind string, harnessID string) (map[string][]byte, error)
}

type renderedHarness struct {
	Files        map[string][]byte
	AllowEntries map[string][]string
	DenyEntries  map[string][]string
}

// Bundle is the unsigned policy-bundle/v1 payload fetched by both the CLI and runtime.
type Bundle struct {
	Schema         string `json:"schema"`
	BundleID       string `json:"bundle_id"`
	BundleVersion  int64  `json:"bundle_version"`
	PolicyRevision int64  `json:"policy_revision"`
	Audience       struct {
		InstallationID string `json:"installation_id"`
		OrgID          string `json:"org_id"`
		WorkspaceID    string `json:"workspace_id"`
	} `json:"audience"`
	NotAfter              time.Time       `json:"not_after"`
	HarnessMatchSemantics string          `json:"harness_match_semantics"`
	Harnesses             []bundleHarness `json:"harnesses"`
	PolicySet             json.RawMessage `json:"policy_set"`
	PayloadDigest         string          `json:"-"`
}

type bundlePolicy struct {
	ID         string `json:"id"`
	SrcPattern string `json:"src_pattern"`
	DstPattern string `json:"dst_pattern"`
	Effect     string `json:"effect"`
	Action     string `json:"action"`
	Enabled    bool   `json:"enabled"`
	Scope      struct {
		AgentID *string `json:"agent_id"`
	} `json:"scope"`
}

type bundleHarness struct {
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind"`
}

type harnessResource struct {
	InstallationID string `json:"installation_id"`
	AgentID        string `json:"agent_id"`
	Kind           string `json:"kind"`
	AgentName      string `json:"agent_name"`
	LastSyncedAt   string `json:"last_synced_at,omitempty"`
}

func runHarnessCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "harness requires a subcommand: add, list, remove, or sync")
		return 2
	}
	switch args[0] {
	case "add":
		return runHarnessAdd(args[1:], stdout, stderr, client, store)
	case "list":
		return runHarnessList(args[1:], stdout, stderr, client, store)
	case "remove":
		return runHarnessRemove(args[1:], stdout, stderr, client, store)
	case "sync":
		return runHarnessSync(args[1:], stdout, stderr, client, store, nativeHarnessRenderer{})
	default:
		fmt.Fprintf(stderr, "unknown harness command %q\n", args[0])
		return 2
	}
}

func harnessCollectionPath(installation string) string {
	return "/api/v1/runtime-installations/" + installation + "/harnesses"
}

func harnessSession(store credentialStore, stderr io.Writer) (string, string, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	return baseURL, token, ok
}

func harnessRequest(ctx context.Context, client *http.Client, baseURL, token, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return payload, resp.StatusCode, err
}

func validHarnessKind(kind string) bool {
	switch kind {
	case "claude_code", "codex", "opencode", "custom":
		return true
	}
	return false
}

func runHarnessAdd(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("harness add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installation := flags.String("installation", "", "runtime installation ID")
	kind := flags.String("kind", "", "claude_code, codex, opencode, or custom")
	agentID := flags.String("agent", "", "assigned agent ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if !validHarnessKind(*kind) {
		fmt.Fprintln(stderr, "harness add requires --kind claude_code|codex|opencode|custom")
		return 2
	}
	if *installation == "" {
		resolved, err := defaultInstallationID(client, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "harness add: %v; specify --installation\n", err)
			return 1
		}
		installation = &resolved
	}
	if !isUUID(*installation) {
		fmt.Fprintln(stderr, "harness add: --installation must be a UUID")
		return 2
	}
	baseURL, token, ok := harnessSession(store, stderr)
	if !ok {
		return 1
	}
	if *agentID == "" {
		resolved, err := defaultAgentID(client, baseURL, token, *installation)
		if err != nil {
			fmt.Fprintf(stderr, "harness add: %v; specify --agent\n", err)
			return 1
		}
		agentID = &resolved
	}
	if !isUUID(*agentID) {
		fmt.Fprintln(stderr, "harness add: --agent must be a UUID")
		return 2
	}
	body := struct {
		Kind    string `json:"kind"`
		AgentID string `json:"agent_id"`
	}{*kind, *agentID}
	payload, status, err := harnessRequest(context.Background(), client, baseURL, token, http.MethodPost, harnessCollectionPath(*installation), body)
	if err != nil || status < 200 || status > 299 {
		msg := harnessResponseError(payload, status, err)
		fmt.Fprintf(stderr, "harness add: %s\n", msg)
		if status == http.StatusConflict && (strings.Contains(msg, "already_exists") || strings.Contains(msg, "agent_harness_exists")) {
			fmt.Fprintln(stderr, "Hint: one harness per agent; for another harness on this machine, register it on another agent or create a new installation: kei bot init --platform cli --name <kind>@<host>")
		} else if status >= 400 && status < 500 && (strings.Contains(msg, "agent") || strings.Contains(msg, "assign")) {
			fmt.Fprintln(stderr, "Hint: ensure the agent is assigned to the installation: kei bot agents add")
		}
		return 1
	}
	var result harnessResource
	if err := json.Unmarshal(payload, &result); err != nil {
		fmt.Fprintf(stderr, "harness add: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added %s harness (%s) for agent %s.\n", result.Kind, result.AgentName, result.AgentID)
	return 0
}

// defaultInstallationID resolves the runtime installation ID from the local
// runtime config by verifying the runtime token against the control plane.
func defaultInstallationID(client *http.Client, stderr io.Writer) (string, error) {
	configPath, err := defaultConfigPath()
	if err != nil {
		return "", err
	}
	config, err := loadRuntimeConfig(configPath)
	if err != nil {
		return "", err
	}
	if err := config.validate(); err != nil {
		return "", err
	}
	identity, err := verifyRuntimeToken(context.Background(), client, config.ControlPlaneURL, config.RuntimeToken)
	if err != nil {
		return "", err
	}
	return identity.ID, nil
}

// defaultAgentID resolves the default agent for a runtime installation via
// the organization-scoped agents list endpoint.
func defaultAgentID(client *http.Client, baseURL, token, installationID string) (string, error) {
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations/"+url.PathEscape(installationID)+"/agents", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	statusCode, body, err := doRequest(client, req, 4<<10)
	if err != nil {
		return "", err
	}
	if statusCode != http.StatusOK {
		return "", fmt.Errorf("list agents returned HTTP %d", statusCode)
	}
	var agents []runtimeInstallationAgent
	if err := json.Unmarshal(body, &agents); err != nil {
		var wrapper struct {
			Agents []runtimeInstallationAgent `json:"agents"`
		}
		if err2 := json.Unmarshal(body, &wrapper); err2 != nil || len(wrapper.Agents) == 0 {
			return "", fmt.Errorf("could not parse agents list")
		}
		agents = wrapper.Agents
	}
	for _, a := range agents {
		if a.IsDefault {
			return a.AgentID, nil
		}
	}
	return "", fmt.Errorf("no default agent found for installation %s; set one with 'kei bot agents add --default'", installationID)
}

func runHarnessList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("harness list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installation := flags.String("installation", "", "runtime installation ID")
	jsonOutput := flags.Bool("json", false, "output JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if !isUUID(*installation) {
		fmt.Fprintln(stderr, "harness list requires --installation UUID")
		return 2
	}
	baseURL, token, ok := harnessSession(store, stderr)
	if !ok {
		return 1
	}
	var harnesses []harnessResource
	pageToken := ""
	seenTokens := map[string]bool{}
	for {
		query := url.Values{"page_size": {"100"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		payload, status, err := harnessRequest(context.Background(), client, baseURL, token, http.MethodGet, harnessCollectionPath(*installation)+"?"+query.Encode(), nil)
		if err != nil || status < 200 || status > 299 {
			fmt.Fprintf(stderr, "harness list: %s\n", harnessResponseError(payload, status, err))
			return 1
		}
		var page struct {
			Harnesses     []harnessResource `json:"harnesses"`
			NextPageToken string            `json:"next_page_token"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "harness list: decode response: %v\n", err)
			return 1
		}
		harnesses = append(harnesses, page.Harnesses...)
		if page.NextPageToken == "" {
			break
		}
		if seenTokens[page.NextPageToken] {
			fmt.Fprintln(stderr, "harness list: repeated page token")
			return 1
		}
		seenTokens[page.NextPageToken] = true
		pageToken = page.NextPageToken
	}
	if *jsonOutput {
		out, err := json.Marshal(struct {
			Harnesses     []harnessResource `json:"harnesses"`
			NextPageToken string            `json:"next_page_token"`
		}{Harnesses: harnesses})
		if err != nil {
			fmt.Fprintf(stderr, "harness list: encode output: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(out)
		return 0
	}
	if len(harnesses) == 0 {
		fmt.Fprintln(stdout, "No harnesses found.")
		return 0
	}
	for _, h := range harnesses {
		if h.LastSyncedAt != "" {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", h.AgentName, h.Kind, h.AgentID, h.LastSyncedAt)
		} else {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", h.AgentName, h.Kind, h.AgentID)
		}
	}
	return 0
}

func runHarnessRemove(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("harness remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installation := flags.String("installation", "", "runtime installation ID")
	agentID, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "harness remove: %v\n", err)
		return 2
	}
	if !isUUID(*installation) || !isUUID(agentID) {
		fmt.Fprintln(stderr, "harness remove requires AGENT_ID and --installation UUID")
		return 2
	}
	baseURL, token, ok := harnessSession(store, stderr)
	if !ok {
		return 1
	}
	path := harnessCollectionPath(*installation) + "/" + agentID
	payload, status, err := harnessRequest(context.Background(), client, baseURL, token, http.MethodDelete, path, nil)
	if err != nil || status < 200 || status > 299 {
		fmt.Fprintf(stderr, "harness remove: %s\n", harnessResponseError(payload, status, err))
		return 1
	}
	if err := removeLocalHarness(agentID); err != nil {
		fmt.Fprintf(stderr, "harness remove: removed remotely but could not clean local entries: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed harness for agent %s and its Kei-managed local entries.\n", agentID)
	return 0
}

func isUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }

func responseError(payload []byte, status int, err error) string {
	if err != nil {
		return err.Error()
	}
	if len(payload) != 0 {
		return policyErrorMessage(payload)
	}
	return fmt.Sprintf("request failed (HTTP %d)", status)
}

func harnessResponseError(payload []byte, status int, err error) string {
	if err != nil {
		return err.Error()
	}
	var body struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Subject string `json:"subject"`
	}
	if json.Unmarshal(payload, &body) == nil {
		if body.Reason != "" && body.Message != "" {
			return body.Reason + ": " + body.Message
		}
		if body.Message != "" {
			return body.Message
		}
		if body.Detail != "" {
			hint := bundleFetchHint(body.Detail)
			if hint != "" {
				return body.Detail + ": " + body.Subject + " (" + hint + ")"
			}
			if body.Subject != "" {
				return body.Detail + ": " + body.Subject
			}
			return body.Detail
		}
	}
	return responseError(payload, status, nil)
}

// bundleFetchHint maps known catalog bundle-fetch detail reason codes to
// one-line user-facing hints that are appended to the error message.
func bundleFetchHint(detail string) string {
	switch detail {
	case "bundle_version_conflict":
		return "try 'kei harness sync' again"
	case "runtime_not_bound":
		return "run 'kei bot bind' to assign a workspace"
	case "token_revoked":
		return "rotate the runtime credential with 'kei bot credential --rotate'"
	case "installation_not_found":
		return "check that the runtime installation ID is correct"
	default:
		return ""
	}
}

func runHarnessSync(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, renderer harnessRenderer) int {
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("harness sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "runtime configuration path")
	kindFilter := flags.String("harness", "", "only sync this harness kind")
	dryRun := flags.Bool("dry-run", false, "show changes without writing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *kindFilter != "" && !validHarnessKind(*kindFilter) {
		fmt.Fprintln(stderr, "--harness must be claude_code, codex, opencode, or custom")
		return 2
	}
	config, err := loadRuntimeConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: read config: %v\n", err)
		return 1
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	return syncHarnessBundle(context.Background(), config, *kindFilter, *dryRun, stdout, stderr, client, renderer, time.Now)
}

func syncHarnessBundle(ctx context.Context, config runtimeConfig, kindFilter string, dryRun bool, stdout, stderr io.Writer, client *http.Client, renderer harnessRenderer, now func() time.Time) int {
	controlPlane, parseErr := url.Parse(config.ControlPlaneURL)
	if parseErr != nil || !strings.EqualFold(controlPlane.Scheme, "https") && !isLoopbackHost(controlPlane.Hostname()) {
		fmt.Fprintln(stderr, "harness sync: runtime policy bundles require HTTPS")
		return 1
	}
	identity, err := verifyRuntimeToken(ctx, client, config.ControlPlaneURL, config.RuntimeToken)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: verify runtime token: %v\n", err)
		return 1
	}
	endpoint := strings.TrimRight(config.ControlPlaneURL, "/") + "/api/v1/runtime/policy-bundles/current"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+config.RuntimeToken)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: fetch bundle: %v\n", err)
		return 1
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		fmt.Fprintf(stderr, "harness sync: read bundle: %v\n", readErr)
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "harness sync: fetch bundle: %s\n", harnessResponseError(payload, resp.StatusCode, nil))
		return 1
	}
	var bundle Bundle
	if err := json.Unmarshal(payload, &bundle); err != nil || bundle.Schema != "kei.policy-bundle/v1" || bundle.BundleID == "" || bundle.BundleVersion < 1 || bundle.PolicyRevision < 1 || bundle.NotAfter.IsZero() {
		fmt.Fprintln(stderr, "harness sync: invalid policy bundle")
		return 1
	}
	digest := sha256.Sum256(payload)
	bundle.PayloadDigest = "sha256:" + hex.EncodeToString(digest[:])
	if bundle.Audience.InstallationID != identity.ID || bundle.Audience.OrgID != identity.OrgID || bundle.Audience.WorkspaceID == "" {
		fmt.Fprintln(stderr, "harness sync: policy bundle audience does not match runtime installation")
		return 1
	}
	if len(bundle.Harnesses) > 0 && bundle.HarnessMatchSemantics != "kei.harness-match/v1" {
		fmt.Fprintln(stderr, "harness sync: unsupported harness match semantics")
		return 1
	}
	if !bundle.NotAfter.After(now()) {
		if err := expireHarnessEntries(now()); err != nil {
			fmt.Fprintf(stderr, "harness sync: remove expired allows: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Harness policy bundle expired; removed Kei-written allow entries. Run kei harness sync after renewing the runtime bundle.")
		return 0
	}
	selected := make([]bundleHarness, 0, len(bundle.Harnesses))
	for _, h := range bundle.Harnesses {
		if kindFilter == "" || h.Kind == kindFilter {
			selected = append(selected, h)
		}
	}
	if len(selected) == 0 {
		fmt.Fprintln(stdout, "No registered harnesses selected.")
		return 0
	}
	for _, h := range selected {
		if !validHarnessKind(h.Kind) || !isUUID(h.AgentID) {
			fmt.Fprintf(stderr, "harness sync: invalid harness in bundle\n")
			return 1
		}
		if h.Kind == "custom" {
			fmt.Fprintln(stdout, "custom harness: skipped; no native allowlist (authorization is enforced by kei-proxy per ADR-011).")
		} else {
			if err := syncOneHarness(bundle, h, dryRun, stdout, renderer, now()); err != nil {
				fmt.Fprintf(stderr, "harness sync: %v\n", err)
				return 1
			}
			_ = warnNonPromptingMode(h.Kind, harnessNonPromptingConfig(h.Kind), stderr)
		}
		if !dryRun {
			if err := reportHarnessSync(ctx, client, config, h, bundle, payload, now()); err != nil {
				fmt.Fprintf(stderr, "harness sync: report sync for %s: %v\n", h.AgentID, err)
				return 1
			}
		}
		if h.Kind == "codex" {
			fmt.Fprintln(stdout, "Codex may skip the reporting hook until you trust it with /hooks.")
		}
	}
	return 0
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func reportHarnessSync(ctx context.Context, client *http.Client, config runtimeConfig, h bundleHarness, bundle Bundle, payload []byte, now time.Time) error {
	digest := sha256.Sum256(payload)
	body := map[string]any{"last_synced_at": now.UTC().Format(time.RFC3339Nano), "last_synced_bundle_version": bundle.BundleVersion, "last_synced_digest": "sha256:" + hex.EncodeToString(digest[:])}
	path := "/api/v1/runtime/harnesses/" + h.AgentID + "?update_mask=last_synced_at,last_synced_bundle_version,last_synced_digest"
	_, status, err := harnessRequest(ctx, client, strings.TrimRight(config.ControlPlaneURL, "/"), config.RuntimeToken, http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("runtime sync report returned HTTP %d", status)
	}
	return nil
}

type syncLedger struct {
	HarnessID string                `json:"harness_id"`
	Kind      string                `json:"kind"`
	Bundle    int64                 `json:"bundle_version"`
	Digest    string                `json:"bundle_digest"`
	NotAfter  time.Time             `json:"not_after"`
	Files     map[string]fileLedger `json:"files"`
}
type fileLedger struct {
	Hash         string   `json:"hash"`
	Content      []byte   `json:"content"`
	AllowEntries []string `json:"allow_entries,omitempty"`
	DenyEntries  []string `json:"deny_entries,omitempty"`
}

func syncOneHarness(bundle Bundle, h bundleHarness, dryRun bool, stdout io.Writer, renderer harnessRenderer, timestamp time.Time) error {
	files, err := harnessFiles(h.Kind, h.AgentID)
	if err != nil {
		return err
	}
	ledgerPath := harnessLedgerPath(h.AgentID)
	prior := readLedger(ledgerPath)
	if prior.Bundle > bundle.BundleVersion {
		return fmt.Errorf("bundle version rollback: local %d, fetched %d", prior.Bundle, bundle.BundleVersion)
	}
	if prior.Bundle == bundle.BundleVersion && prior.Digest != "" && prior.Digest != bundle.PayloadDigest {
		return fmt.Errorf("bundle payload changed without a version increase")
	}
	ledger := syncLedger{HarnessID: h.AgentID, Kind: h.Kind, Bundle: bundle.BundleVersion, Digest: bundle.PayloadDigest, NotAfter: bundle.NotAfter, Files: map[string]fileLedger{}}
	inputs := map[string][]byte{}
	for _, path := range files {
		cfg, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if previous, ok := prior.Files[path]; ok && len(cfg) > 0 {
			cfg, err = removeManagedPermissionEntries(h.Kind, cfg, previous.AllowEntries, previous.DenyEntries)
			if err != nil {
				return fmt.Errorf("remove previous managed entries from %s: %w", path, err)
			}
		}
		inputs[path] = cfg
	}
	rendered, err := renderer.Render(h.Kind, h.AgentID, bundle, inputs)
	if err != nil {
		return err
	}
	outputs := rendered.Files
	hooks, err := renderer.HookSpec(h.Kind, h.AgentID)
	if err != nil {
		return err
	}
	for path, content := range hooks {
		base := outputs[path]
		if len(base) == 0 {
			base = inputs[path]
		}
		merged, err := mergeHookSpec(h.Kind, base, content)
		if err != nil {
			return err
		}
		outputs[path] = merged
	}
	for path, managed := range outputs {
		cfg := inputs[path]
		digest := sha256.Sum256(managed)
		ledger.Files[path] = fileLedger{Hash: hex.EncodeToString(digest[:]), Content: managed, AllowEntries: rendered.AllowEntries[path], DenyEntries: rendered.DenyEntries[path]}
		if bytes.Equal(cfg, managed) {
			continue
		}
		printHarnessDiff(stdout, path, cfg, managed)
		if !dryRun {
			if len(cfg) > 0 {
				backup := path + ".kei-backup-" + timestamp.UTC().Format("20060102T150405.000000000Z")
				if err := os.WriteFile(backup, cfg, 0o600); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, managed, 0o600); err != nil {
				return err
			}
		}
	}
	if !dryRun {
		if err := writeLedger(ledgerPath, ledger); err != nil {
			return err
		}
	}
	return nil
}

func printHarnessDiff(out io.Writer, path string, before, after []byte) {
	fmt.Fprintf(out, "--- %s\n+++ %s (Kei render)\n", path, path)
	for _, line := range strings.Split(strings.TrimSuffix(string(before), "\n"), "\n") {
		if line != "" {
			fmt.Fprintf(out, "-%s\n", line)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(after), "\n"), "\n") {
		if line != "" {
			fmt.Fprintf(out, "+%s\n", line)
		}
	}
}

func harnessFiles(kind, id string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	switch kind {
	case "claude_code":
		return []string{filepath.Join(home, ".claude", "settings.json")}, nil
	case "codex":
		return []string{filepath.Join(home, ".codex", "rules", "kei.rules"), filepath.Join(home, ".codex", "hooks.json")}, nil
	case "opencode":
		return []string{filepath.Join(home, ".config", "opencode", "opencode.json"), filepath.Join(home, ".config", "opencode", "plugins", "kei-audit.js")}, nil
	default:
		return nil, fmt.Errorf("unsupported harness kind %q", kind)
	}
}

func harnessNonPromptingConfig(kind string) []byte {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var path string
	switch kind {
	case "codex":
		path = filepath.Join(home, ".codex", "config.toml")
	case "claude_code":
		path = filepath.Join(home, ".claude", "settings.json")
	case "opencode":
		path = filepath.Join(home, ".config", "opencode", "opencode.json")
	}
	data, _ := os.ReadFile(path)
	return data
}

func warnNonPromptingMode(kind string, config []byte, out io.Writer) bool {
	nonPrompting := false
	switch kind {
	case "codex":
		for _, line := range strings.Split(string(config), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "approval_policy" && strings.Trim(strings.TrimSpace(value), "\"'") == "never" {
				nonPrompting = true
			}
		}
	case "claude_code":
		var root map[string]any
		_ = json.Unmarshal(config, &root)
		if permissions, ok := root["permissions"].(map[string]any); ok {
			mode, _ := permissions["defaultMode"].(string)
			nonPrompting = mode == "bypassPermissions" || mode == "dontAsk"
		}
	case "opencode":
		var root map[string]any
		_ = json.Unmarshal(config, &root)
		if permission, ok := root["permission"].(map[string]any); ok {
			mode, _ := permission["*"].(string)
			nonPrompting = mode == "allow"
		}
	}
	if nonPrompting {
		fmt.Fprintf(out, "Warning: %s is configured not to ask for unmatched commands; see ADR-029 OQ1.\n", kind)
	}
	return nonPrompting
}

func harnessLedgerPath(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "kei", "harness-sync", id+".json")
}
func readLedger(path string) syncLedger {
	var ledger syncLedger
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &ledger)
	return ledger
}
func writeLedger(path string, ledger syncLedger) error {
	b, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func removeLocalHarness(id string) error {
	path := harnessLedgerPath(id)
	ledger := readLedger(path)
	if ledger.HarnessID == "" {
		return nil
	}
	now := time.Now().UTC()
	for target, record := range ledger.Files {
		cfg, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var cleaned []byte
		if strings.HasSuffix(target, filepath.Join(".codex", "rules", "kei.rules")) || strings.HasSuffix(target, filepath.Join("opencode", "plugins", "kei-audit.js")) {
			if !bytes.Contains(cfg, []byte("managed by kei harness sync")) {
				continue
			}
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		cleaned, err = removeManagedPermissionEntries(ledger.Kind, cfg, record.AllowEntries, record.DenyEntries)
		if err != nil {
			return err
		}
		if bytes.Contains(cfg, []byte("--harness "+id)) {
			cleaned, err = removeHookJSONEntries(cleaned, id)
			if err != nil {
				return err
			}
		}
		if bytes.Equal(cfg, cleaned) {
			continue
		}
		backup := target + ".kei-backup-" + now.Format("20060102T150405.000000000Z")
		if err := os.WriteFile(backup, cfg, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(target, cleaned, 0o600); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func removeHookJSONEntries(config []byte, id string) ([]byte, error) {
	var root any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	needle := "kei-proxy hook "
	idNeedle := "--harness " + id
	var prune func(any) any
	prune = func(value any) any {
		switch node := value.(type) {
		case []any:
			kept := make([]any, 0, len(node))
			for _, child := range node {
				remove := false
				if object, ok := child.(map[string]any); ok {
					if command, ok := object["command"].(string); ok && strings.Contains(command, needle) && strings.Contains(command, idNeedle) {
						remove = true
					}
				}
				if command, ok := child.(string); ok && strings.Contains(command, needle) && strings.Contains(command, idNeedle) {
					remove = true
				}
				if !remove {
					kept = append(kept, prune(child))
				}
			}
			return kept
		case map[string]any:
			for key, child := range node {
				node[key] = prune(child)
			}
			return node
		default:
			return value
		}
	}
	return json.MarshalIndent(prune(root), "", "  ")
}

func mergeHookSpec(kind string, cfg, spec []byte) ([]byte, error) {
	if kind == "opencode" {
		return spec, nil
	}
	var current map[string]any
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &current); err != nil {
			return nil, fmt.Errorf("decode %s config: %w", kind, err)
		}
	} else {
		current = map[string]any{}
	}
	var incoming map[string]any
	if err := json.Unmarshal(spec, &incoming); err != nil {
		return nil, err
	}
	target := current
	if kind == "claude_code" {
		target, _ = current["hooks"].(map[string]any)
		if target == nil {
			target = map[string]any{}
		}
		current["hooks"] = target
	} else if nested, ok := incoming["hooks"].(map[string]any); ok {
		target, _ = current["hooks"].(map[string]any)
		if target == nil {
			target = map[string]any{}
		}
		current["hooks"] = target
		incoming = nested
	}
	for key, value := range incoming {
		old, _ := target[key].([]any)
		added, ok := value.([]any)
		if !ok {
			target[key] = value
			continue
		}
		for _, item := range added {
			duplicate := false
			for _, prior := range old {
				a, _ := json.Marshal(prior)
				b, _ := json.Marshal(item)
				if bytes.Equal(a, b) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				old = append(old, item)
			}
		}
		target[key] = old
	}
	return json.MarshalIndent(current, "", "  ")
}

func expireHarnessEntries(now time.Time) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ledgerDir := filepath.Join(home, ".config", "kei", "harness-sync")
	entries, err := os.ReadDir(ledgerDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(ledgerDir, entry.Name())
		ledger := readLedger(path)
		if ledger.NotAfter.After(now) {
			continue
		}
		for target, record := range ledger.Files {
			if len(record.AllowEntries) == 0 {
				continue
			}
			cfg, err := os.ReadFile(target)
			if err != nil {
				continue
			}
			cleaned, err := removeManagedAllowEntries(ledger.Kind, cfg, record.AllowEntries)
			if err != nil {
				return err
			}
			if bytes.Equal(cfg, cleaned) {
				continue
			}
			backup := target + ".kei-backup-" + now.UTC().Format("20060102T150405.000000000Z")
			if err := os.WriteFile(backup, cfg, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(target, cleaned, 0o600); err != nil {
				return err
			}
		}
		ledger.NotAfter = time.Time{}
		if err := writeLedger(path, ledger); err != nil {
			return err
		}
	}
	return nil
}

func removeManagedAllowEntries(kind string, config []byte, entries []string) ([]byte, error) {
	return removeManagedPermissionEntries(kind, config, entries, nil)
}

func removeManagedPermissionEntries(kind string, config []byte, allows, denies []string) ([]byte, error) {
	if len(allows) == 0 && len(denies) == 0 {
		return config, nil
	}
	if kind == "codex" {
		if len(denies) == 0 {
			lines := strings.Split(string(config), "\n")
			kept := make([]string, 0, len(lines))
			for _, line := range lines {
				remove := false
				for _, entry := range allows {
					if strings.Contains(line, "pattern="+entry) && strings.Contains(line, `decision="allow"`) {
						remove = true
						break
					}
				}
				if remove {
					continue
				}
				kept = append(kept, line)
			}
			return []byte(strings.Join(kept, "\n")), nil
		}
		return config, nil
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	if kind == "opencode" {
		permission, ok := root["permission"].(map[string]any)
		if !ok {
			return config, nil
		}
		bash, ok := permission["bash"].(map[string]any)
		if !ok {
			return config, nil
		}
		for _, entry := range allows {
			if bash[entry] == "allow" {
				delete(bash, entry)
			}
		}
		for _, entry := range denies {
			if bash[entry] == "deny" {
				delete(bash, entry)
			}
		}
		permission["bash"] = bash
		root["permission"] = permission
		return json.MarshalIndent(root, "", "  ")
	}
	permissions, ok := root["permissions"].(map[string]any)
	if !ok {
		return config, nil
	}
	for key, entries := range map[string][]string{"allow": allows, "deny": denies} {
		values, ok := permissions[key].([]any)
		if !ok {
			continue
		}
		remove := map[string]int{}
		for _, s := range entries {
			remove[s]++
		}
		kept := make([]any, 0, len(values))
		for _, entry := range values {
			if s, ok := entry.(string); ok && remove[s] > 0 {
				remove[s]--
				continue
			}
			kept = append(kept, entry)
		}
		permissions[key] = kept
	}
	root["permissions"] = permissions
	return json.MarshalIndent(root, "", "  ")
}

type nativeHarnessRenderer struct{}

func (nativeHarnessRenderer) Render(kind, harnessID string, bundle Bundle, files map[string][]byte) (renderedHarness, error) {
	var set struct {
		Policies []bundlePolicy `json:"policies"`
	}
	if len(bundle.PolicySet) > 0 {
		if err := json.Unmarshal(bundle.PolicySet, &set); err != nil {
			return renderedHarness{}, fmt.Errorf("decode bundle policies: %w", err)
		}
	}
	var selected *bundleHarness
	for i := range bundle.Harnesses {
		if bundle.Harnesses[i].AgentID == harnessID && bundle.Harnesses[i].Kind == kind {
			selected = &bundle.Harnesses[i]
			break
		}
	}
	allows, _ := nativePermissionEntries(kind, harnessID, selected, set.Policies)
	result := renderedHarness{Files: map[string][]byte{}, AllowEntries: map[string][]string{}, DenyEntries: map[string][]string{}}
	for path, cfg := range files {
		switch {
		case strings.HasSuffix(path, filepath.Join(".codex", "rules", "kei.rules")):
			var rules strings.Builder
			rules.WriteString("# managed by kei harness sync; edits are overwritten\n")
			for _, entry := range allows {
				fmt.Fprintf(&rules, "prefix_rule(pattern=%s, decision=\"allow\", justification=\"kei policy\")\n", entry)
			}
			result.Files[path] = []byte(rules.String())
			result.AllowEntries[path] = append([]string(nil), allows...)
		case strings.HasSuffix(path, filepath.Join("opencode", "opencode.json")), strings.HasSuffix(path, filepath.Join(".claude", "settings.json")):
			out, err := mergePermissionJSON(kind, cfg, allows, nil)
			if err != nil {
				return renderedHarness{}, err
			}
			result.Files[path] = out
			result.AllowEntries[path] = newlyManagedEntries(kind, cfg, allows)
		default:
			result.Files[path] = cfg
		}
	}
	return result, nil
}
func (nativeHarnessRenderer) HookSpec(kind, harnessID string) (map[string][]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	short := map[string]string{"claude_code": "claude", "codex": "codex", "opencode": "opencode"}[kind]
	command := "kei-proxy hook " + short + " --harness " + harnessID
	hook := map[string]any{"type": "command", "command": command, "timeout": 5}
	switch kind {
	case "claude_code":
		return map[string][]byte{filepath.Join(home, ".claude", "settings.json"): mustJSON(map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{hook}}}, "PostToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{hook}}}})}, nil
	case "codex":
		return map[string][]byte{filepath.Join(home, ".codex", "hooks.json"): mustJSON(map[string]any{"hooks": map[string]any{"PreToolUse": []any{hook}, "PostToolUse": []any{hook}}})}, nil
	case "opencode":
		plugin := fmt.Sprintf("// managed by kei harness sync\nconst report = async (phase, event) => { try { const child = Bun.spawn(['kei-proxy','hook','opencode','--harness','%s'], { stdin: 'pipe', stdout: 'ignore', stderr: 'ignore' }); child.stdin.write(JSON.stringify({phase, ...event})); child.stdin.end(); } catch {} };\nexport const KeiAudit = async () => ({ 'tool.execute.before': async (event) => { void report('pre', event); }, 'tool.execute.after': async (event) => { void report('post', event); }, 'permission.ask': async (event) => { void report('ask', event); }, 'permission.replied': async (event) => { void report('permission_reply', event); } });\n", harnessID)
		return map[string][]byte{filepath.Join(home, ".config", "opencode", "plugins", "kei-audit.js"): []byte(plugin)}, nil
	default:
		return nil, fmt.Errorf("unsupported harness kind %q", kind)
	}
}

func mustJSON(value any) []byte { data, _ := json.MarshalIndent(value, "", "  "); return data }

func nativePermissionEntries(kind, harnessID string, harness *bundleHarness, policies []bundlePolicy) (allows, denies []string) {
	for _, policy := range policies {
		effect := policy.Effect
		if effect == "" {
			effect = policy.Action
		}
		if !policy.Enabled || (effect != "permit" && effect != "deny") {
			continue
		}
		dst := policy.DstPattern
		call := rendererCall(kind, harnessID, harness, dst)
		scope := policy.Scope.AgentID
		matchPolicy := harnessmatch.Policy{ID: policy.ID, Src: policy.SrcPattern, Dst: dst, Action: effect, Enabled: policy.Enabled, Scope: scope}
		if applies, _ := harnessmatch.MatchSrc(matchPolicy.Src, call); !applies {
			continue
		}
		result := harnessmatch.Evaluate(call, []harnessmatch.Policy{matchPolicy})
		if result.Outcome != harnessmatch.OutcomePermit && result.Outcome != harnessmatch.OutcomeDeny {
			continue
		}
		if result.Outcome == harnessmatch.OutcomePermit && effect != "permit" || result.Outcome == harnessmatch.OutcomeDeny && effect != "deny" {
			continue
		}
		entry := ""
		switch {
		case strings.HasPrefix(dst, "shell:"):
			tokens := strings.Fields(strings.TrimPrefix(dst, "shell:"))
			if len(tokens) > 0 && tokens[0] != "*" {
				entry = shellNativeEntry(kind, strings.Join(tokens, " "))
			} else if effect == "deny" {
				entry = shellNativeEntry(kind, "*")
			}
		case dst == "*":
			if effect == "deny" {
				entry = shellNativeEntry(kind, "*")
			}
		case strings.HasPrefix(dst, "skill:"):
			if kind != "opencode" {
				continue
			}
			name := strings.TrimPrefix(dst, "skill:")
			if name != "" {
				entry = "Skill(" + name + ")"
			}
		case strings.HasPrefix(dst, "tool:"):
			prefix := kind + "."
			name := strings.TrimPrefix(dst, "tool:")
			if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, ".bash") || strings.HasSuffix(name, ".shell")) {
				if effect == "deny" {
					entry = shellNativeEntry(kind, "*")
				} else {
					continue
				}
			}
		case strings.HasPrefix(dst, "path:"):
			if kind != "opencode" {
				continue
			}
			entry = "path:" + strings.TrimPrefix(dst, "path:")
		}
		if entry == "" {
			continue
		}
		if effect == "permit" && strings.HasPrefix(dst, "shell:") && len(strings.Fields(strings.TrimPrefix(dst, "shell:"))) == 0 {
			continue
		}
		if result.Outcome == harnessmatch.OutcomePermit {
			allows = append(allows, entry)
		} else {
			denies = append(denies, entry)
		}
	}
	sort.Strings(allows)
	sort.Strings(denies)
	return
}

func rendererCall(kind, harnessID string, harness *bundleHarness, dst string) harnessmatch.Call {
	call := harnessmatch.Call{Kind: kind, HarnessID: harnessID}
	if harness != nil {
		call.AgentID = harness.AgentID
	}
	switch {
	case strings.HasPrefix(dst, "shell:"):
		call.Argv = strings.Fields(strings.TrimPrefix(dst, "shell:"))
		if len(call.Argv) == 0 || call.Argv[0] == "*" {
			call.Argv = []string{"kei-proxy", "run"}
		}
	case strings.HasPrefix(dst, "skill:"):
		call.Skill = strings.TrimPrefix(dst, "skill:")
	case strings.HasPrefix(dst, "path:"):
		call.Path = "/kei-render-path"
	case strings.HasPrefix(dst, "mcp:"):
		v := strings.TrimPrefix(dst, "mcp:")
		call.MCPServer, call.MCPTool, _ = strings.Cut(v, "/")
	case strings.HasPrefix(dst, "tool:"):
		call.Tool = strings.TrimPrefix(dst, "tool:")
	case dst == "*":
		call.Tool = "kei.render"
	}
	return call
}

func shellNativeEntry(kind, prefix string) string {
	switch kind {
	case "claude_code":
		if prefix == "*" {
			return "Bash(*)"
		}
		return "Bash(" + prefix + ":*)"
	case "opencode":
		return prefix
	default:
		tokens := strings.Fields(prefix)
		quoted := make([]string, len(tokens))
		for i, token := range tokens {
			quoted[i] = strconv.Quote(token)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
}

func newlyManagedEntries(kind string, cfg []byte, entries []string) []string {
	return filterAbsentPermissionEntries(kind, cfg, "allow", entries)
}
func newlyManagedDenies(kind string, cfg []byte, entries []string) []string {
	return filterAbsentPermissionEntries(kind, cfg, "deny", entries)
}
func filterAbsentPermissionEntries(kind string, cfg []byte, key string, entries []string) []string {
	var root map[string]any
	_ = json.Unmarshal(cfg, &root)
	present := map[string]bool{}
	if kind == "opencode" {
		if p, ok := root["permission"].(map[string]any); ok {
			if b, ok := p["bash"].(map[string]any); ok {
				for k := range b {
					present[k] = true
				}
			}
		}
	} else {
		if p, ok := root["permissions"].(map[string]any); ok {
			if xs, ok := p[key].([]any); ok {
				for _, x := range xs {
					if s, ok := x.(string); ok {
						present[s] = true
					}
				}
			}
		}
	}
	var out []string
	for _, entry := range entries {
		if !present[entry] {
			out = append(out, entry)
		}
	}
	return out
}

func mergePermissionJSON(kind string, cfg []byte, allows, denies []string) ([]byte, error) {
	root := map[string]any{}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &root); err != nil {
			return nil, err
		}
	}
	if kind == "opencode" {
		permission, _ := root["permission"].(map[string]any)
		if permission == nil {
			permission = map[string]any{}
		}
		bash, _ := permission["bash"].(map[string]any)
		if bash == nil {
			bash = map[string]any{}
		}
		_, userStarExists := bash["*"]
		if !userStarExists {
			bash["*"] = "ask"
		}
		skill, _ := permission["skill"].(map[string]any)
		if skill == nil {
			skill = map[string]any{}
		}
		external, _ := permission["external_directory"].(map[string]any)
		if external == nil {
			external = map[string]any{}
		}
		apply := func(entry, decision string) {
			switch {
			case strings.HasPrefix(entry, "Skill(") && strings.HasSuffix(entry, ")"):
				name := strings.TrimSuffix(strings.TrimPrefix(entry, "Skill("), ")")
				if _, exists := skill[name]; !exists {
					skill[name] = decision
				}
			case strings.HasPrefix(entry, "path:"):
				pattern := strings.TrimPrefix(entry, "path:")
				if _, exists := external[pattern]; !exists {
					external[pattern] = decision
				}
			default:
				for _, pattern := range []string{entry, entry + " *"} {
					if pattern == "*" && !userStarExists {
						bash[pattern] = decision
					} else if _, exists := bash[pattern]; !exists {
						bash[pattern] = decision
					}
				}
			}
		}
		for _, entry := range allows {
			apply(entry, "allow")
		}
		for _, entry := range denies {
			apply(entry, "deny")
		}
		permission["bash"] = bash
		if len(skill) > 0 {
			permission["skill"] = skill
		}
		if len(external) > 0 {
			permission["external_directory"] = external
		}
		root["permission"] = permission
	} else {
		permissions, _ := root["permissions"].(map[string]any)
		if permissions == nil {
			permissions = map[string]any{}
		}
		for key, entries := range map[string][]string{"allow": allows, "deny": denies} {
			existing, _ := permissions[key].([]any)
			seen := map[string]bool{}
			for _, value := range existing {
				if item, ok := value.(string); ok {
					seen[item] = true
				}
			}
			for _, item := range entries {
				if !seen[item] {
					existing = append(existing, item)
				}
			}
			if existing != nil {
				permissions[key] = existing
			}
		}
		root["permissions"] = permissions
	}
	return json.MarshalIndent(root, "", "  ")
}
