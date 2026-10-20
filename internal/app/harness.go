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
	"sort"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
	"github.com/google/uuid"
)

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
		return runHarnessAdd(args[1:], stdout, stderr, client, store, harness.Default)
	case "list":
		return runHarnessList(args[1:], stdout, stderr, client, store)
	case "remove":
		return runHarnessRemove(args[1:], stdout, stderr, client, store, harness.Default)
	case "sync":
		return runHarnessSync(args[1:], stdout, stderr, client, store, harness.Default)
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

func runHarnessAdd(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, registry *harness.Registry) int {
	flags := flag.NewFlagSet("harness add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Register a custom (SDK) harness on a runtime installation.")
		fmt.Fprintln(stderr, "Desktop harnesses (Claude Code, Codex, OpenCode) are sessions of the installation and are configured by 'kei harness sync'; they do not need 'kei harness add'.")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Usage: kei harness add --kind custom [--installation ID] [--agent AGENT_ID]")
		flags.PrintDefaults()
	}
	installation := flags.String("installation", "", "runtime installation ID")
	kind := flags.String("kind", "", harness.JoinOr(registry.Kinds()))
	agentID := flags.String("agent", "", "assigned agent ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *kind == (harness.OpenWebUI{}).Kind() {
		fmt.Fprintln(stderr, "harness add: Open WebUI configures itself on deploy; use kei harness list/sync --harness openwebui")
		return 2
	}
	if _, ok := registry.Get(*kind); !ok {
		fmt.Fprintf(stderr, "harness add requires --kind %s\n", strings.Join(registry.Kinds(), "|"))
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
			fmt.Fprintln(stderr, "Hint: Claude Code, Codex and OpenCode don't need kei harness add; run kei harness sync. For another custom (SDK) harness, pass --agent with a different agent assigned to this installation.")
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
	installation := flags.String("installation", "", "runtime installation ID (omit to list only Open WebUI installations)")
	jsonOutput := flags.Bool("json", false, "output JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *installation != "" && !isUUID(*installation) {
		fmt.Fprintln(stderr, "harness list: --installation must be a UUID")
		return 2
	}
	baseURL, token, ok := harnessSession(store, stderr)
	if !ok {
		return 1
	}
	harnesses, ok := listInstallationHarnesses(client, baseURL, token, *installation, stderr)
	if !ok {
		return 1
	}
	// Open WebUI installations are listed from the existing
	// runtime-installations calls; with --installation they are best effort.
	openWebUI, err := listOpenWebUIInstallations(context.Background(), client, baseURL, token)
	if err != nil {
		if *installation == "" {
			fmt.Fprintf(stderr, "harness list: Open WebUI installations: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "harness list: Open WebUI installations unavailable: %v\n", err)
	}
	if *jsonOutput {
		out, err := json.Marshal(struct {
			Harnesses              []harnessResource               `json:"harnesses"`
			NextPageToken          string                          `json:"next_page_token"`
			OpenWebUIInstallations []harness.OpenWebUIInstallation `json:"openwebui_installations,omitempty"`
		}{Harnesses: harnesses, OpenWebUIInstallations: openWebUI})
		if err != nil {
			fmt.Fprintf(stderr, "harness list: encode output: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(out)
		return 0
	}
	if *installation != "" {
		if len(harnesses) == 0 {
			fmt.Fprintln(stdout, "No harnesses found.")
		}
		for _, h := range harnesses {
			if h.LastSyncedAt != "" {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", h.AgentName, h.Kind, h.AgentID, h.LastSyncedAt)
			} else {
				fmt.Fprintf(stdout, "%s\t%s\t%s\n", h.AgentName, h.Kind, h.AgentID)
			}
		}
	}
	if len(openWebUI) > 0 {
		if *installation != "" {
			fmt.Fprintln(stdout)
		}
		harness.WriteOpenWebUIInstallations(stdout, openWebUI, time.Now())
	} else if *installation == "" {
		fmt.Fprintln(stdout, "No Open WebUI installations found.")
	}
	return 0
}

// listInstallationHarnesses pages through the harnesses registered on one
// installation; it returns none when installation is empty.
func listInstallationHarnesses(client *http.Client, baseURL, token, installation string, stderr io.Writer) ([]harnessResource, bool) {
	var harnesses []harnessResource
	if installation == "" {
		return nil, true
	}
	pageToken := ""
	seenTokens := map[string]bool{}
	for {
		query := url.Values{"page_size": {"100"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		payload, status, err := harnessRequest(context.Background(), client, baseURL, token, http.MethodGet, harnessCollectionPath(installation)+"?"+query.Encode(), nil)
		if err != nil || status < 200 || status > 299 {
			fmt.Fprintf(stderr, "harness list: %s\n", harnessResponseError(payload, status, err))
			return nil, false
		}
		var page struct {
			Harnesses     []harnessResource `json:"harnesses"`
			NextPageToken string            `json:"next_page_token"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "harness list: decode response: %v\n", err)
			return nil, false
		}
		harnesses = append(harnesses, page.Harnesses...)
		if page.NextPageToken == "" {
			break
		}
		if seenTokens[page.NextPageToken] {
			fmt.Fprintln(stderr, "harness list: repeated page token")
			return nil, false
		}
		seenTokens[page.NextPageToken] = true
		pageToken = page.NextPageToken
	}
	return harnesses, true
}

func runHarnessRemove(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, registry *harness.Registry) int {
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
	if err := registry.RemoveLocal(harness.OSEnv(), agentID); err != nil {
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
		return apiErrorMessage(payload)
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

// toolDefinition captures the fields of a single tool in a tool-manifest/v2
// document for --manifest dry-run listing and the eventual catalog PUT.
type toolDefinition struct {
	Name                 string   `json:"name"`
	Route                string   `json:"route"`
	Source               string   `json:"source,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	ResourceTypes        []string `json:"resource_types,omitempty"`
	OperationClass       string   `json:"operation_class,omitempty"`
	Service              string   `json:"service,omitempty"`
	Description          string   `json:"description,omitempty"`
	Enabled              *bool    `json:"enabled,omitempty"`
}

func runHarnessSync(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, registry *harness.Registry) int {
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("harness sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "runtime configuration path")
	kindFilter := flags.String("harness", "", "only sync this harness kind")
	manifestPath := flags.String("manifest", "", "path to a tool manifest JSON file (requires --harness custom)")
	dryRun := flags.Bool("dry-run", false, "show changes without writing")
	noHook := flags.Bool("no-hook", false, "skip audit-hook installation")
	check := flags.Bool("check", false, "report drift between native config, the sync ledger and the current bundle; exit 1 on drift, 2 on error")
	jsonOutput := flags.Bool("json", false, "with --check, print a machine-readable report ("+harness.DriftSchema+")")
	openWebUIURL := flags.String("url", "", "with --harness openwebui, the Open WebUI base URL to report on (admin token from OPENWEBUI_ADMIN_TOKEN)")
	installSkills := flags.Bool("install-skills", false, "check the Haikei skills after the sync and offer to update them now")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *kindFilter == (harness.OpenWebUI{}).Kind() {
		// Read-only: kei-openwebui seeds its own functions and kei-proxy
		// decides every call live, so there is no bundle to render.
		if *openWebUIURL == "" || *jsonOutput {
			fmt.Fprintln(stderr, "harness sync --harness openwebui requires --url BASE_URL and does not take --json")
			return 2
		}
		return checkOpenWebUI(context.Background(), *openWebUIURL, os.Getenv("OPENWEBUI_ADMIN_TOKEN"), stdout, stderr, client)
	}
	if *openWebUIURL != "" {
		fmt.Fprintln(stderr, "harness sync: --url requires --harness openwebui")
		return 2
	}
	if *jsonOutput && !*check {
		fmt.Fprintln(stderr, "harness sync: --json requires --check")
		return 2
	}
	if *check && *dryRun {
		fmt.Fprintln(stderr, "harness sync: --check and --dry-run are mutually exclusive")
		return 2
	}
	if *check && *manifestPath != "" {
		fmt.Fprintln(stderr, "harness sync: --check and --manifest are mutually exclusive")
		return 2
	}
	if _, ok := registry.Get(*kindFilter); *kindFilter != "" && !ok {
		fmt.Fprintf(stderr, "--harness must be %s\n", harness.JoinOr(registry.Kinds()))
		return 2
	}
	var manifestToolDefs []toolDefinition // populated by --manifest, used after config
	if *manifestPath != "" {
		if *kindFilter != "custom" {
			fmt.Fprintln(stderr, "harness sync: --manifest requires --harness custom")
			return 2
		}
		if _, err := os.Stat(*manifestPath); err != nil {
			fmt.Fprintf(stderr, "harness sync: --manifest: %v\n", err)
			return 2
		}
		data, err := os.ReadFile(*manifestPath)
		if err != nil {
			fmt.Fprintf(stderr, "harness sync: --manifest: read: %v\n", err)
			return 2
		}
		var raw struct {
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			fmt.Fprintf(stderr, "harness sync: --manifest: invalid JSON: %v\n", err)
			return 2
		}
		if len(raw.Tools) == 0 {
			fmt.Fprintln(stderr, "harness sync: --manifest: missing 'tools' field")
			return 2
		}
		if err := json.Unmarshal(raw.Tools, &manifestToolDefs); err != nil {
			fmt.Fprintf(stderr, "harness sync: --manifest: 'tools' must be a JSON array: %v\n", err)
			return 2
		}
		if len(manifestToolDefs) == 0 {
			fmt.Fprintln(stderr, "harness sync: --manifest: tools array is empty; nothing to sync")
			return 2
		}
		if *dryRun {
			fmt.Fprintf(stdout, "Would sync %d tool(s) from %s:\n", len(manifestToolDefs), *manifestPath)
			for _, t := range manifestToolDefs {
				fmt.Fprintf(stdout, "  %s (%s)\n", t.Name, t.Route)
			}
			return 0
		}
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
	if *check {
		return checkHarnessDrift(context.Background(), config, *kindFilter, *jsonOutput, stdout, stderr, client, registry, harness.OSEnv(), time.Now)
	}
	if manifestToolDefs != nil {
		return syncManifestTools(context.Background(), config, *manifestPath, manifestToolDefs, stdout, stderr, client)
	}
	code := syncHarnessBundle(context.Background(), config, *kindFilter, *dryRun, *noHook, stdout, stderr, client, registry, harness.OSEnv(), time.Now)
	if code == 0 && !*dryRun {
		maybePromptSkillsUpdate(skillsPromptStdin, isTerminalReader(skillsPromptStdin), stdout, stderr, client, *installSkills)
	}
	return code
}

// syncManifestTools PUTs the --manifest tool definitions to the Kei catalog.
// It exits 0 on success, 1 on server error, 2 on client error.
func syncManifestTools(ctx context.Context, config runtimeConfig, manifestPath string, tools []toolDefinition, stdout, stderr io.Writer, client *http.Client) int {
	body := struct {
		Schema   string           `json:"schema"`
		SyncMode string           `json:"sync_mode"`
		Tools    []toolDefinition `json:"tools"`
	}{
		Schema:   "kei.tool-manifest/v2",
		SyncMode: "full",
		Tools:    tools,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: encode manifest: %v\n", err)
		return 2
	}

	endpoint := strings.TrimRight(config.ControlPlaneURL, "/") + "/api/v1/runtime/tools"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: create request: %v\n", err)
		return 2
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.RuntimeToken)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: read response: %v\n", err)
		return 1
	}

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		fmt.Fprintf(stdout, "Synced %d tool(s) from %s to the Kei catalog.\n", len(tools), manifestPath)
		return 0
	}

	msg := strings.TrimSpace(string(respBody))
	if msg != "" {
		fmt.Fprintf(stderr, "harness sync: %s\n", msg)
	} else {
		fmt.Fprintf(stderr, "harness sync: server returned HTTP %d\n", resp.StatusCode)
	}
	return 1
}

// checkHarnessDrift is `kei harness sync --check`: it compares each harness's
// native config with its sync ledger and the current bundle and writes
// nothing. It exits 0 in sync, 1 on drift and 2 on error, so a bundle
// refresher can run sync only when needed.
func checkHarnessDrift(ctx context.Context, config runtimeConfig, kindFilter string, jsonOutput bool, stdout, stderr io.Writer, client *http.Client, registry *harness.Registry, env harness.Env, now func() time.Time) int {
	bundle, _, err := fetchHarnessBundle(ctx, config, client)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync --check: %v\n", err)
		return 2
	}
	report := harness.NewDriftReport(bundle, now())
	if report.Bundle.Expired {
		report.Drift = harness.LedgersUnexpired(env)
	} else {
		harnesses, err := selectHarnesses(registry, env, kindFilter)
		if err != nil {
			fmt.Fprintf(stderr, "harness sync --check: %v\n", err)
			return 2
		}
		for _, h := range harnesses {
			rendered, err := h.Render(bundle)
			if err != nil {
				fmt.Fprintf(stderr, "harness sync --check: %v\n", err)
				return 2
			}
			drift, err := harness.CheckDrift(h, env, rendered)
			if err != nil {
				fmt.Fprintf(stderr, "harness sync --check: %s: %v\n", h.Kind(), err)
				return 2
			}
			report.Add(drift)
		}
	}
	if jsonOutput {
		out, err := json.Marshal(report)
		if err != nil {
			fmt.Fprintf(stderr, "harness sync --check: encode report: %v\n", err)
			return 2
		}
		fmt.Fprintln(stdout, string(out))
	} else {
		if len(report.Harnesses) == 0 && !report.Bundle.Expired {
			fmt.Fprintln(stdout, "No harness kinds detected locally; use --harness KIND to check a specific one.")
		}
		report.WriteText(stdout)
	}
	if report.Drift {
		return 1
	}
	return 0
}

// fetchHarnessBundle verifies the runtime token and fetches the current
// policy bundle, checking that it is addressed to this installation.
func fetchHarnessBundle(ctx context.Context, config runtimeConfig, client *http.Client) (harness.Bundle, []byte, error) {
	var bundle harness.Bundle
	controlPlane, parseErr := url.Parse(config.ControlPlaneURL)
	if parseErr != nil || !strings.EqualFold(controlPlane.Scheme, "https") && !isLoopbackHost(controlPlane.Hostname()) {
		return bundle, nil, errors.New("runtime policy bundles require HTTPS")
	}
	identity, err := verifyRuntimeToken(ctx, client, config.ControlPlaneURL, config.RuntimeToken)
	if err != nil {
		return bundle, nil, fmt.Errorf("verify runtime token: %w", err)
	}
	endpoint := strings.TrimRight(config.ControlPlaneURL, "/") + "/api/v1/runtime/policy-bundles/current"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return bundle, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.RuntimeToken)
	req.Header.Set("Accept", "application/vnd.kei.policy-bundle.v2+json, application/vnd.kei.policy-bundle.v1+json")
	resp, err := client.Do(req)
	if err != nil {
		return bundle, nil, fmt.Errorf("fetch bundle: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return bundle, nil, fmt.Errorf("read bundle: %w", readErr)
	}
	// Older catalogs may reject v2 negotiation. Retry explicitly as v1; do not
	// mask authentication, authorization, or transient server failures.
	if resp.StatusCode == http.StatusNotAcceptable || resp.StatusCode == http.StatusUnsupportedMediaType {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return bundle, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+config.RuntimeToken)
		req.Header.Set("Accept", "application/vnd.kei.policy-bundle.v1+json")
		resp, err = client.Do(req)
		if err != nil {
			return bundle, nil, fmt.Errorf("fetch v1 bundle: %w", err)
		}
		payload, readErr = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return bundle, nil, fmt.Errorf("read v1 bundle: %w", readErr)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return bundle, nil, fmt.Errorf("fetch bundle: %s", harnessResponseError(payload, resp.StatusCode, nil))
	}
	if err := json.Unmarshal(payload, &bundle); err != nil || (bundle.Schema != "kei.policy-bundle/v1" && bundle.Schema != "kei.policy-bundle/v2") || bundle.BundleID == "" || bundle.BundleVersion < 1 || bundle.PolicyRevision < 1 || bundle.NotAfter.IsZero() {
		return harness.Bundle{}, nil, errors.New("invalid policy bundle")
	}
	if bundle.Schema == "kei.policy-bundle/v2" {
		var v2 struct {
			PolicySet struct {
				HarnessPolicies []struct {
					PolicyID   string `json:"policy_id"`
					Name       string `json:"name"`
					SrcPattern string `json:"src_pattern"`
					DstPattern string `json:"dst_pattern"`
					Action     string `json:"action"`
					Priority   int    `json:"priority"`
					Scope      struct {
						AgentID *string `json:"agent_id"`
					} `json:"scope"`
				} `json:"harness_policies"`
				// Connectors are the workspace's connector descriptors; the
				// skills step reads the skills_repo ones (HAI-431).
				Connectors json.RawMessage `json:"connectors"`
			} `json:"policy_set"`
		}
		if err := json.Unmarshal(payload, &v2); err != nil {
			return harness.Bundle{}, nil, errors.New("invalid v2 harness policies")
		}
		policies := make([]map[string]any, 0, len(v2.PolicySet.HarnessPolicies))
		for _, p := range v2.PolicySet.HarnessPolicies {
			policies = append(policies, map[string]any{"id": p.PolicyID, "name": p.Name, "src_pattern": p.SrcPattern, "dst_pattern": p.DstPattern, "effect": p.Action, "enabled": true, "priority": p.Priority, "scope": p.Scope})
		}
		set := map[string]any{"policies": policies}
		if len(v2.PolicySet.Connectors) > 0 {
			set["connectors"] = v2.PolicySet.Connectors
		}
		bundle.PolicySet, err = json.Marshal(set)
		if err != nil {
			return harness.Bundle{}, nil, errors.New("invalid v2 harness policies")
		}
	}
	digest := sha256.Sum256(payload)
	bundle.PayloadDigest = "sha256:" + hex.EncodeToString(digest[:])
	if bundle.Audience.InstallationID != identity.ID || bundle.Audience.OrgID != identity.OrgID || bundle.Audience.WorkspaceID == "" {
		return harness.Bundle{}, nil, errors.New("policy bundle audience does not match runtime installation")
	}
	if len(bundle.Harnesses) > 0 && bundle.HarnessMatchSemantics != "kei.harness-match/v1" {
		return harness.Bundle{}, nil, errors.New("unsupported harness match semantics")
	}
	return bundle, payload, nil
}

// selectHarnesses returns the harness of kindFilter, or every harness
// installed in env when kindFilter is empty (HP-C11).
func selectHarnesses(registry *harness.Registry, env harness.Env, kindFilter string) ([]harness.Harness, error) {
	if kindFilter == "" {
		return registry.Detected(env), nil
	}
	h, ok := registry.Get(kindFilter)
	if !ok {
		return nil, fmt.Errorf("invalid kind %q", kindFilter)
	}
	return []harness.Harness{h}, nil
}

func syncHarnessBundle(ctx context.Context, config runtimeConfig, kindFilter string, dryRun, noHook bool, stdout, stderr io.Writer, client *http.Client, registry *harness.Registry, env harness.Env, now func() time.Time) int {
	bundle, payload, err := fetchHarnessBundle(ctx, config, client)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	if !bundle.NotAfter.After(now()) {
		if err := registry.ExpireEntries(env, now()); err != nil {
			fmt.Fprintf(stderr, "harness sync: remove expired allows: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Harness policy bundle expired; removed Kei-written allow entries. Run kei harness sync after renewing the runtime bundle.")
		return 0
	}
	// HP-C11: sync no longer requires a registered harness. It renders for the
	// harness kinds installed locally, or for a single --harness KIND.
	harnesses, err := selectHarnesses(registry, env, kindFilter)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: %v\n", err)
		return 1
	}
	if len(harnesses) == 0 {
		fmt.Fprintln(stdout, "No harness kinds detected locally; use --harness KIND to sync a specific one.")
		return 0
	}
	for _, h := range harnesses {
		rendered, err := h.Render(bundle)
		if err != nil {
			fmt.Fprintf(stderr, "harness sync: %v\n", err)
			return 1
		}
		result, err := h.Apply(ctx, rendered, harness.ApplyOpts{Env: env, DryRun: dryRun, NoHook: noHook, Out: stdout, Now: now()})
		if err != nil {
			fmt.Fprintf(stderr, "harness sync: %v\n", err)
			return 1
		}
		if result.Skipped {
			printLines(stdout, result.Notes)
			continue
		}
		printLines(stderr, result.Warnings)
		if !dryRun {
			// Report only when the bundle still carries a registered harness for
			// this kind; sync itself does not depend on one.
			if registered := findRegisteredHarness(bundle, h.Kind()); registered != nil {
				if err := reportHarnessSync(ctx, client, config, *registered, bundle, payload, now()); err != nil {
					fmt.Fprintf(stderr, "harness sync: report sync for %s: %v\n", registered.AgentID, err)
					return 1
				}
				// HAI-641: the skills inventory is advisory. Until the catalog
				// accepts installed_skills, a 400/404 only warns.
				if err := reportInstalledSkills(ctx, client, config, registered.AgentID); err != nil {
					fmt.Fprintf(stderr, "harness sync: report installed skills for %s: %v\n", registered.AgentID, err)
				}
			}
		}
		printLines(stdout, result.Notes)
		if !dryRun {
			printLines(stdout, harnessSyncGuidance(h.Kind(), bundle.Audience.WorkspaceID, bundle.PolicySet, bundle.Connectors))
		}
	}
	// HAI-431: after rendering, install the org skills the bundle permits
	// into each harness's skill dir, read through kei-proxy. The permission
	// entries are already in force, so a skills failure (a deny, or a
	// kei-proxy without skills_repo) is reported without failing the sync;
	// kei skills install exits non-zero on it.
	if err := installOrgSkills(ctx, config, bundle, harnesses, dryRun, true, stdout, stderr, env, now()); err != nil {
		fmt.Fprintf(stderr, "harness sync: skills not installed: %v (retry with kei skills install)\n", err)
	}
	return 0
}

// harnessSyncGuidance adds actionable setup steps to a successful sync. It
// only reads bundle metadata for display; policy rendering and evaluation stay
// in the harness renderer and runtime.
func harnessSyncGuidance(kind, workspaceID string, policySet json.RawMessage, connectors []harness.BundleConnector) []string {
	var decoded struct {
		Policies []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			SrcPattern string `json:"src_pattern"`
			DstPattern string `json:"dst_pattern"`
			Enabled    bool   `json:"enabled"`
		} `json:"policies"`
	}
	if len(policySet) > 0 && json.Unmarshal(policySet, &decoded) != nil {
		return nil
	}
	type missingPolicy struct{ id, label string }
	var missing []missingPolicy
	for _, p := range decoded.Policies {
		if !p.Enabled || !strings.HasPrefix(p.SrcPattern, "harness:") || p.SrcPattern == "harness:*" || p.SrcPattern == "harness:"+kind {
			continue
		}
		label := p.Name
		if label == "" {
			label = p.DstPattern
		}
		missing = append(missing, missingPolicy{id: p.ID, label: fmt.Sprintf("%s (%s)", label, p.DstPattern)})
	}
	var mcp []string
	for _, c := range connectors {
		if c.Kind == "mcp" && c.Slug != "" {
			mcp = append(mcp, c.Slug)
		}
	}
	if len(missing) == 0 && kind != "codex" && len(mcp) == 0 {
		return nil
	}
	var lines []string
	n := 1
	if len(missing) > 0 {
		lines = append(lines, fmt.Sprintf("Next steps (%s):", harnessDisplayName(kind)))
		for _, p := range missing {
			lines = append(lines, fmt.Sprintf("  %d. Share %s with this harness: kei policies update %s --workspace %s --src-pattern harness:*", n, p.label, p.id, workspaceID))
			n++
		}
	}
	if kind == "codex" {
		if len(lines) == 0 {
			lines = append(lines, "Next steps (Codex):")
		}
		lines = append(lines, fmt.Sprintf("  %d. Open Codex and run /hooks to trust the Kei hooks.", n))
		n++
	}
	for _, slug := range mcp {
		if len(lines) == 0 {
			lines = append(lines, "Next steps:")
		}
		lines = append(lines, fmt.Sprintf("  %d. If no manifest exists, ask %s: \"List tools from the %s MCP server\"; this first tools/list call creates a candidate manifest for admin review.", n, harnessDisplayName(kind), slug))
		n++
	}
	return lines
}

func harnessDisplayName(kind string) string {
	switch kind {
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	default:
		return kind
	}
}

func printLines(out io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
}

// findRegisteredHarness returns the bundle's registered harness for a kind, or
// nil when the bundle has none (HP-C11: sync works without one).
func findRegisteredHarness(bundle harness.Bundle, kind string) *harness.BundleHarness {
	for i := range bundle.Harnesses {
		if bundle.Harnesses[i].Kind == kind {
			return &bundle.Harnesses[i]
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func reportHarnessSync(ctx context.Context, client *http.Client, config runtimeConfig, h harness.BundleHarness, bundle harness.Bundle, payload []byte, now time.Time) error {
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

// installedSkill is one HaikeiLabs/skills entry reported at sync: the folder
// name and the first 12 characters of its installed folder hash. No file
// contents ever leave the machine.
type installedSkill struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// reportInstalledSkills replaces the harness's installed-skill set with the
// Haikei skills in the local lock files. No lock file reports an empty set.
func reportInstalledSkills(ctx context.Context, client *http.Client, config runtimeConfig, agentID string) error {
	installed, err := installedHaikeiSkills()
	if err != nil {
		return err
	}
	skills := make([]installedSkill, 0, len(installed))
	for name, hash := range installed {
		skills = append(skills, installedSkill{Name: name, Version: skillVersion(hash)})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	body := map[string]any{"installed_skills": skills}
	path := "/api/v1/runtime/harnesses/" + agentID + "?update_mask=installed_skills"
	_, status, err := harnessRequest(ctx, client, strings.TrimRight(config.ControlPlaneURL, "/"), config.RuntimeToken, http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("runtime installed skills report returned HTTP %d", status)
	}
	return nil
}

func skillVersion(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
