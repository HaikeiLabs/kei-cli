package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/browser"
	"github.com/zalando/go-keyring"
)

const defaultKeiWebURL = "https://api.haikeilabs.com"

const (
	keychainService       = "kei"
	legacyKeychainService = "kei-cli"
)

type credentialStore interface {
	Save(serverURL, token string) error
	Load(serverURL string) (string, error)
	Delete(serverURL string) (bool, error)
}

type osKeychainStore struct{}

func (osKeychainStore) Save(serverURL, token string) error {
	return keyring.Set(keychainService, keychainAccount(serverURL), token)
}

func (osKeychainStore) Load(serverURL string) (string, error) {
	token, err := keyring.Get(keychainService, keychainAccount(serverURL))
	if err == nil {
		return token, nil
	}
	// Existing installations used kei-cli. A successful login writes the
	// token under the canonical service above.
	return keyring.Get(legacyKeychainService, keychainAccount(serverURL))
}

func (osKeychainStore) Delete(serverURL string) (bool, error) {
	account := keychainAccount(serverURL)
	removed := false
	for _, service := range []string{keychainService, legacyKeychainService} {
		err := keyring.Delete(service, account)
		if err == nil {
			removed = true
			continue
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			return removed, fmt.Errorf("delete %s keychain entry: %w", service, err)
		}
	}
	return removed, nil
}

type deviceAuthorizationStartRequest struct {
	ClientName string `json:"client_name"`
}

type deviceAuthorizationStartResponse struct {
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int       `json:"interval_seconds"`
	// AIP fields
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in,omitempty"`
	Interval                int    `json:"interval,omitempty"`
}

type deviceAuthorizationPollRequest struct {
	DeviceCode string `json:"device_code"`
}

type deviceAuthorizationPollResponse struct {
	Status      string `json:"status"`
	AccessToken string `json:"access_token"`
	OrgID       string `json:"org_id,omitempty"`
}

type deviceAuthorizationExchangeRequest struct {
	DeviceCode string `json:"device_code"`
}

type deviceAuthorizationExchangeResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type aipError struct {
	Detail aipErrorDetail `json:"error"`
}

func (e *aipError) Error() string {
	return fmt.Sprintf("%s: %s", e.Detail.Reason, e.Detail.Message)
}

type aipErrorDetail struct {
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Main dispatches CLI commands and returns the process exit code.
func Main(version, command string, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	switch command {
	case "setup":
		return runSetupCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 15 * time.Second})
	case "login":
		return runLoginCommand(version, args, stdout, stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "logout":
		return runLogoutCommand(args, stdout, stderr, osKeychainStore{})
	case "bot":
		return runBotCommand(args, stdout, stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "runtime":
		return runRuntimeCommand(args, stdout, stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "service":
		// "kei service" is a hidden alias for "kei runtime service"
		return runRuntimeCommand(append([]string{"service"}, args...), stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "model-profiles":
		return runModelProfilesCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "credential-store":
		return runCredentialStoreCommand(args, stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "workspaces":
		return runWorkspaceCommand(args, stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "users":
		return runUsersCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "groups":
		return runGroupsCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "connectors":
		return runConnectorsCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "policies":
		return runPoliciesCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "harness":
		return runHarnessCommand(args, stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "audit":
		return runAuditCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "upgrade":
		return runUpgradeCommand(args, stdout, stderr, nil, os.Executable, os.Getenv)
	case "version", "--version", "-v":
		printVersion(stdout, version)
		return 0
	case "help", "--help", "-h":
		PrintUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", command)
		PrintUsage(stderr)
		return 2
	}
}

func PrintUsage(w io.Writer) {
	fmt.Fprintln(w, "Kei CLI")
	fmt.Fprintln(w, "\nUsage:\n  kei setup [--config PATH] [--control-plane-url URL] [--runtime-token TOKEN]\n  kei runtime bootstrap [--config PATH] [--proxy-path PATH]\n  kei login [--no-browser]\n  kei logout\n  kei upgrade [--version VERSION]\n  kei bot list [--json] [--all]\n  kei bot init --platform cli|teams|discord|slack|whatsapp|openwebui --name NAME [--agent ID] [--workspace WORKSPACE]\n  kei bot credential --installation ID --workspace WORKSPACE [--rotate]\n  kei bot agents list|add|remove --installation ID [--agent ID] [--default]\n  kei bot status --installation ID\n  kei bot delete --installation ID --yes\n  kei workspaces list [--json]\n  kei users list [--json] | get USER [--json] | create --email EMAIL [--name NAME] [--yes] | update USER [--email EMAIL] [--name NAME] [--yes] | resolve --email EMAIL [--json]\n  kei users identities list USER [--json]\n  kei users remote-identities list USER [--json]\n  kei groups members list GROUP [--workspace WORKSPACE] [--json] | add GROUP USER [--workspace WORKSPACE] [--yes] | remove GROUP USER [--workspace WORKSPACE] [--yes]\n  kei connectors create|list|get|reconnect|delete --workspace WORKSPACE\n  kei policies list|get|create|update|delete|import --workspace WORKSPACE (get/update/delete accept ID or name)\n  kei model-profiles list [--workspace WORKSPACE] [--json]\n  kei model-profiles get|create|update|delete|set-default [--workspace WORKSPACE] (PROFILE is an ID or name; API keys are read from stdin)\n  kei model-profiles assign PROFILE --workspace WORKSPACE --agent AGENT_ID\n  kei model-profiles unassign|assignment --workspace WORKSPACE --agent AGENT_ID\n  kei model-profiles readiness PROFILE --workspace WORKSPACE\n  kei model-profiles test --endpoint URL | test PROFILE [--workspace WORKSPACE] (validates the endpoint like the console; no provider call)\n  kei credential-store get|put|update\n  kei --version")
	fmt.Fprintln(w, "  kei audit keys create [--identity-out PATH] [--name NAME] [--force]")
	fmt.Fprintln(w, "  kei audit keys list")
	fmt.Fprintln(w, "  kei audit keys disable KEY")
	fmt.Fprintln(w, "  kei audit decrypt --record ID --identity PATH (--out FILE | --stdout)")
	fmt.Fprintln(w, "  See https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md")
	fmt.Fprintln(w, "  kei harness add|list|remove|sync [--installation ID] [--harness kind] [--dry-run]")
	fmt.Fprintln(w, "  kei runtime heartbeat")
	fmt.Fprintln(w, "  kei runtime service install [--config PATH]")
	fmt.Fprintln(w, "  kei runtime service uninstall")
	fmt.Fprintln(w, "  kei runtime service status")
	fmt.Fprintln(w, "users connect their own accounts through their chat harness")
}

func printVersion(w io.Writer, version string) {
	fmt.Fprintf(w, "kei %s\n", version)
}

func runLoginCommand(version string, args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	noBrowser := flags.Bool("no-browser", false, "print the approval URL without opening a browser")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "login accepts no positional arguments")
		return 2
	}
	openBrowser := browser.OpenURL
	if *noBrowser {
		openBrowser = func(string) error { return nil }
	}
	if err := login(context.Background(), keiWebURL(), "kei", version, stdout, client, store, time.Sleep, openBrowser); err != nil {
		fmt.Fprintf(stderr, "login failed: %v\n", err)
		return 1
	}
	return 0
}

func runBotCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "bot requires a subcommand")
		return 2
	}
	switch args[0] {
	case "list":
		return runBotListCommand(args[1:], stdout, stderr, client, store)
	case "init":
		return runBotInitCommand(args[1:], stdout, stderr, client, store)
	case "agents":
		return runBotAgentsCommand(args[1:], stdout, stderr, client, store)
	case "status":
		return runBotStatusCommand(args[1:], stdout, stderr, client, store)
	case "delete":
		return runBotDeleteCommand(args[1:], stdout, stderr, client, store)
	case "credential":
		return runBotCredentialCommand(args[1:], stdout, stderr, client, store)
	case "bind":
		return runBotBindCommand(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown bot command %q\n", args[0])
		return 2
	}
}

func runBotCredentialCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot credential", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installationID := flags.String("installation", "", "installation ID")
	workspace := flags.String("workspace", "", "workspace ID or name (or set KEI_WORKSPACE_ID)")
	rotate := flags.Bool("rotate", false, "rotate an existing runtime credential")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot credential requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot credential: --installation must be a UUID")
		return 2
	}
	wid := *workspace
	if wid == "" {
		wid = os.Getenv("KEI_WORKSPACE_ID")
	}
	if wid == "" {
		fmt.Fprintln(stderr, "bot credential requires --workspace or KEI_WORKSPACE_ID environment variable")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "bot credential: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot credential: not logged in; run kei login first")
		return 1
	}
	wid, err = resolveWorkspaceID(context.Background(), client, baseURL, cliToken, wid)
	if err != nil {
		fmt.Fprintf(stderr, "bot credential: %v\n", err)
		return 1
	}
	if writerIsTerminal(stdout) {
		fmt.Fprintln(stderr, "refusing to write a runtime credential to an interactive terminal; pipe stdout to another command")
		return 2
	}
	action := "credential"
	if *rotate {
		action = "rotate"
	}
	runtimeToken, status, err := requestRuntimeCredentialAction(context.Background(), client, baseURL, cliToken, *installationID, wid, action)
	if err != nil {
		fmt.Fprintf(stderr, "bot credential failed: %v\n", err)
		return 1
	}
	if status == http.StatusConflict {
		fmt.Fprintln(stderr, "bot credential already exists; use --rotate to replace it")
		return 1
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "bot credential returned %d\n", status)
		return 1
	}
	fmt.Fprintln(stdout, runtimeToken)
	return 0
}

func writerIsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runBotInitCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	platform := flags.String("platform", "", "runtime platform (cli, teams, discord, slack, whatsapp, or openwebui)")
	agentID := flags.String("agent", "", "Kei agent ID")
	displayName := flags.String("name", "", "installation name")
	workspace := flags.String("workspace", "", "workspace ID or name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *displayName == "" || !validRuntimePlatform(*platform) {
		fmt.Fprintln(stderr, "bot init requires --platform cli|teams|discord|slack|whatsapp|openwebui and --name NAME")
		return 2
	}
	wid := *workspace
	if wid != "" {
		baseURL, err := normalizedKeiWebURL(keiWebURL())
		if err != nil {
			fmt.Fprintf(stderr, "bot init: %v\n", err)
			return 2
		}
		cliToken, err := store.Load(baseURL)
		if err != nil {
			fmt.Fprintln(stderr, "bot init: not logged in; run kei login first")
			return 1
		}
		wid, err = resolveWorkspaceID(context.Background(), client, baseURL, cliToken, wid)
		if err != nil {
			fmt.Fprintf(stderr, "bot init: %v\n", err)
			return 1
		}
	}
	installation, err := createBotInstallation(context.Background(), keiWebURL(), *agentID, *platform, *displayName, wid, io.Discard, client, store)
	if err != nil {
		fmt.Fprintf(stderr, "bot init failed: %v\n", err)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(map[string]string{"installation_id": installation.ID})
	return 0
}

func validRuntimePlatform(platform string) bool {
	return platform == "cli" || platform == "teams" || platform == "discord" || platform == "slack" || platform == "whatsapp" || platform == "openwebui"
}

type createRuntimeInstallationRequest struct {
	AgentID     string `json:"agent_id,omitempty"`
	Platform    string `json:"platform"`
	DisplayName string `json:"display_name"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type createRuntimeInstallationResponse struct {
	ID            string `json:"id"`
	Platform      string `json:"platform"`
	DisplayName   string `json:"display_name"`
	Status        string `json:"status"`
	BindingStatus string `json:"binding_status"`
}

// initBot creates public installation metadata only. Runtime credentials are
// not handled by this CLI.
func initBot(ctx context.Context, apiURL, agentID, platform, displayName, workspaceID string, stdout io.Writer, client *http.Client, store credentialStore) error {
	_, err := createBotInstallation(ctx, apiURL, agentID, platform, displayName, workspaceID, stdout, client, store)
	return err
}

func createBotInstallation(ctx context.Context, apiURL, agentID, platform, displayName, workspaceID string, stdout io.Writer, client *http.Client, store credentialStore) (*createRuntimeInstallationResponse, error) {
	return createBotInstallationWithOptions(ctx, apiURL, agentID, platform, displayName, workspaceID, stdout, client, store)
}

func createBotInstallationWithOptions(ctx context.Context, apiURL, agentID, platform, displayName, workspaceID string, stdout io.Writer, client *http.Client, store credentialStore) (*createRuntimeInstallationResponse, error) {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return nil, err
	}
	token, err := store.Load(baseURL)
	if err != nil {
		return nil, errors.New("not logged in; run kei login first")
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		return nil, fmt.Errorf("determine organization from login: %w", err)
	}
	body, err := json.Marshal(createRuntimeInstallationRequest{AgentID: agentID, Platform: platform, DisplayName: displayName, WorkspaceID: workspaceID})
	if err != nil {
		return nil, fmt.Errorf("encode installation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build installation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		return nil, fmt.Errorf("create installation: %w", err)
	}
	if statusCode != http.StatusOK && statusCode != http.StatusCreated {
		return nil, fmt.Errorf("create installation returned %d", statusCode)
	}
	var installation createRuntimeInstallationResponse
	if err := json.Unmarshal(body, &installation); err != nil {
		return nil, fmt.Errorf("decode installation response: %w", err)
	}
	if installation.ID == "" {
		return nil, errors.New("installation response is incomplete")
	}
	fmt.Fprintf(stdout, "Created pending %s installation %q (%s).\n", platform, displayName, installation.ID)
	return &installation, nil
}

func login(ctx context.Context, apiURL, clientName, version string, stdout io.Writer, client *http.Client, store credentialStore, sleep func(time.Duration), openBrowser func(string) error) error {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return err
	}

	// Try the AIP device-authorization start endpoint.
	start, err := startDeviceAuthorization(ctx, client, baseURL, version)
	if err != nil {
		var sErr *httpStatusError
		if errors.As(err, &sErr) && sErr.StatusCode == http.StatusNotFound {
			fmt.Fprintln(stdout, "Note: AIP device authorization not available; falling back to legacy flow")
			webURL, _ := legacyWebHost(apiURL)
			return legacyLogin(ctx, webURL, baseURL, clientName, stdout, client, store, sleep, openBrowser)
		}
		return err
	}

	verificationURL := start.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = start.VerificationURI + "?user_code=" + url.QueryEscape(start.UserCode)
	}

	fmt.Fprintln(stdout, "Open this URL in a browser and approve the CLI:")
	fmt.Fprintln(stdout, verificationURL)
	fmt.Fprintf(stdout, "Verification code: %s\n", start.UserCode)
	if err := openBrowser(verificationURL); err != nil {
		fmt.Fprintf(stdout, "Could not open a browser automatically; use the URL above. (%v)\n", err)
	}

	interval := time.Duration(start.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		sleep(interval)

		exch, err := exchangeDeviceAuthorization(ctx, client, baseURL, start.DeviceCode)
		if err != nil {
			var ae *aipError
			if errors.As(err, &ae) {
				switch ae.Detail.Reason {
				case "AUTHORIZATION_PENDING":
					continue
				case "SLOW_DOWN":
					interval += 5 * time.Second
					continue
				case "ACCESS_DENIED":
					return errors.New("device approval was denied")
				case "EXPIRED_TOKEN":
					return errors.New("device approval expired")
				default:
					return fmt.Errorf("device authorization error: %s", ae.Detail.Message)
				}
			}
			return err
		}

		if exch.AccessToken == "" {
			return errors.New("approved login response is incomplete")
		}
		if err := store.Save(baseURL, exch.AccessToken); err != nil {
			return fmt.Errorf("save CLI token in OS keychain: %w", err)
		}
		orgID, _ := organizationIDFromCLIToken(exch.AccessToken)
		if orgID != "" {
			fmt.Fprintf(stdout, "Logged in to Kei for organization %s.\n", orgID)
		} else {
			fmt.Fprintln(stdout, "Logged in to Kei.")
		}
		return nil
	}
	return errors.New("device approval expired")
}

// startDeviceAuthorization posts to the AIP device-authorization endpoint and
// returns the start response. A 404 is propagated as *httpStatusError so the
// caller can fall back to the legacy flow.
func startDeviceAuthorization(ctx context.Context, client *http.Client, baseURL, version string) (*deviceAuthorizationStartResponse, error) {
	body, err := json.Marshal(map[string]any{
		"client": map[string]any{
			"name":    "kei",
			"version": version,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode device authorization start: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/deviceAuthorizations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build device authorization start: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	status, respBody, err := doRequest(client, req, 4<<10)
	if err != nil {
		return nil, fmt.Errorf("start device authorization: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, &httpStatusError{StatusCode: status, body: respBody}
	}
	var start deviceAuthorizationStartResponse
	if err := json.Unmarshal(respBody, &start); err != nil {
		return nil, fmt.Errorf("decode device authorization start: %w", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" || start.ExpiresIn <= 0 {
		return nil, errors.New("device authorization start response is incomplete")
	}
	return &start, nil
}

// exchangeDeviceAuthorization polls the AIP exchange endpoint. An AIP error
// body is returned as *aipError so the caller can react to specific reason
// codes.
func exchangeDeviceAuthorization(ctx context.Context, client *http.Client, baseURL, deviceCode string) (*deviceAuthorizationExchangeResponse, error) {
	body, err := json.Marshal(deviceAuthorizationExchangeRequest{DeviceCode: deviceCode})
	if err != nil {
		return nil, fmt.Errorf("encode device authorization exchange: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/deviceAuthorizations:exchange", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build device authorization exchange: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	status, respBody, err := doRequest(client, req, 4<<10)
	if err != nil {
		return nil, fmt.Errorf("exchange device authorization: %w", err)
	}
	if status == http.StatusOK {
		var exch deviceAuthorizationExchangeResponse
		if err := json.Unmarshal(respBody, &exch); err != nil {
			return nil, fmt.Errorf("decode device authorization exchange: %w", err)
		}
		return &exch, nil
	}
	// Try to parse an AIP error body.
	var ae aipError
	if json.Unmarshal(respBody, &ae) == nil && ae.Detail.Reason != "" {
		return nil, &ae
	}
	return nil, &httpStatusError{StatusCode: status, body: respBody}
}

// legacyLogin implements the original /api/cli/device/authorize + /api/cli/device/token flow.
// webURL is the console's frontend URL for the activation page; apiBaseURL is the API base.
func legacyLogin(ctx context.Context, webURL, apiBaseURL, clientName string, stdout io.Writer, client *http.Client, store credentialStore, sleep func(time.Duration), openBrowser func(string) error) error {
	startRequestBody, err := json.Marshal(deviceAuthorizationStartRequest{ClientName: clientName})
	if err != nil {
		return fmt.Errorf("encode device authorization request: %w", err)
	}
	startResponse, err := client.Post(apiBaseURL+"/api/cli/device/authorize", "application/json", bytes.NewReader(startRequestBody))
	if err != nil {
		return fmt.Errorf("start device authorization: %w", err)
	}
	defer startResponse.Body.Close()
	if startResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("start device authorization returned %d", startResponse.StatusCode)
	}

	var start deviceAuthorizationStartResponse
	if err := json.NewDecoder(startResponse.Body).Decode(&start); err != nil {
		return fmt.Errorf("decode device authorization response: %w", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" || start.ExpiresAt.IsZero() {
		return errors.New("device authorization response is incomplete")
	}

	verificationURL := webURL + "/cli/activate?user_code=" + url.QueryEscape(start.UserCode)
	fmt.Fprintln(stdout, "Open this URL in a browser and approve the CLI:")
	fmt.Fprintln(stdout, verificationURL)
	fmt.Fprintf(stdout, "Verification code: %s\n", start.UserCode)
	if err := openBrowser(verificationURL); err != nil {
		fmt.Fprintf(stdout, "Could not open a browser automatically; use the URL above. (%v)\n", err)
	}

	interval := time.Duration(start.IntervalSeconds) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	for time.Now().Before(start.ExpiresAt) {
		sleep(interval)
		poll, err := pollLegacyDeviceAuthorization(ctx, client, apiBaseURL, start.DeviceCode)
		if err != nil {
			return err
		}
		switch poll.Status {
		case "pending":
			continue
		case "approved":
			if poll.AccessToken == "" || poll.OrgID == "" {
				return errors.New("approved login response is incomplete")
			}
			if err := store.Save(apiBaseURL, poll.AccessToken); err != nil {
				return fmt.Errorf("save CLI token in OS keychain: %w", err)
			}
			fmt.Fprintf(stdout, "Logged in to Kei for organization %s.\n", poll.OrgID)
			return nil
		case "denied":
			return errors.New("device approval was denied")
		case "expired":
			return errors.New("device approval expired")
		default:
			return fmt.Errorf("unexpected device approval status %q", poll.Status)
		}
	}
	return errors.New("device approval expired")
}

// legacyWebHost derives the console front-end URL for the legacy activation page
// from the raw keiWebURL value. When the host starts with "api." it rewrites to
// "app." on the same domain; otherwise it returns the URL unchanged.
func legacyWebHost(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return apiURL, err
	}
	if strings.HasPrefix(u.Hostname(), "api.") {
		u.Host = strings.Replace(u.Host, "api.", "app.", 1)
	}
	return u.String(), nil
}

func pollLegacyDeviceAuthorization(ctx context.Context, client *http.Client, baseURL, deviceCode string) (*deviceAuthorizationPollResponse, error) {
	body, err := json.Marshal(deviceAuthorizationPollRequest{DeviceCode: deviceCode})
	if err != nil {
		return nil, fmt.Errorf("encode device authorization poll: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/device/token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build device authorization poll: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll device authorization: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll device authorization returned %d", response.StatusCode)
	}
	var poll deviceAuthorizationPollResponse
	if err := json.NewDecoder(response.Body).Decode(&poll); err != nil {
		return nil, fmt.Errorf("decode device authorization poll: %w", err)
	}
	return &poll, nil
}

// httpStatusError records an unexpected HTTP status code.
type httpStatusError struct {
	StatusCode int
	body       []byte
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func keiWebURL() string {
	if value := os.Getenv("KEI_WEB_URL"); value != "" {
		return value
	}
	return defaultKeiWebURL
}

func normalizedKeiWebURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("KEI_WEB_URL must be an absolute http(s) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("KEI_WEB_URL must not include a query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func keychainAccount(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Host == "" {
		return serverURL
	}
	return parsed.Host
}

func hostname() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "kei"
	}
	return "kei@" + host
}

// isJSONResponse checks whether an HTTP response has a JSON content type.
func isJSONResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "application/json") || strings.Contains(ct, "application/problem+json")
}

// isHTMLBody checks whether a response body looks like HTML (starts with '<').
func isHTMLBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '<'
}

// checkHTMLFallthrough returns a user-facing error when an API endpoint
// returns HTML instead of JSON, which indicates the route is not wired to
// AIP in the console yet.
func checkHTMLFallthrough(resp *http.Response, body []byte) error {
	if !isJSONResponse(resp) && isHTMLBody(body) {
		urlStr := "<unknown>"
		if resp.Request != nil && resp.Request.URL != nil {
			urlStr = resp.Request.URL.String()
		}
		return fmt.Errorf("endpoint %s returned HTML (status %d); this API is not available yet, check kei-policy-catalog for AIP migration status", urlStr, resp.StatusCode)
	}
	return nil
}

// doRequest performs an HTTP request and reads the response body (up to
// maxBody bytes). The cap exists for bounded memory: error bodies only feed
// user-facing messages and needn't be large.
//
// Known risk: io.LimitReader truncates silently, so a 2xx body over maxBody
// produces truncated JSON that fails to parse. For non-GET requests the server
// may have already applied the change even if the response is discarded.
// TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates.
func doRequest(client *http.Client, req *http.Request, maxBody int64) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, body, fmt.Errorf("read response: %w", err)
	}
	if err := checkHTMLFallthrough(resp, body); err != nil {
		return resp.StatusCode, body, err
	}
	return resp.StatusCode, body, nil
}
