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

const defaultKeiWebURL = "https://app.haikeilabs.com"

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
}

type deviceAuthorizationPollRequest struct {
	DeviceCode string `json:"device_code"`
}

type deviceAuthorizationPollResponse struct {
	Status      string `json:"status"`
	AccessToken string `json:"access_token"`
	OrgID       string `json:"org_id"`
}

// Main dispatches CLI commands and returns the process exit code.
func Main(version, command string, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	switch command {
	case "setup":
		return runSetupCommand(args, stdout, stderr, stdin, &http.Client{Timeout: 15 * time.Second})
	case "login":
		return runLoginCommand(args, stdout, stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "logout":
		return runLogoutCommand(args, stdout, stderr, osKeychainStore{})
	case "bot":
		return runBotCommand(args, stdout, stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{})
	case "runtime":
		return runRuntimeCommand(args, stdout, stderr)
	case "model-profiles":
		return runModelProfilesCommand(args, stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "credential-store":
		return runCredentialStoreCommand(args, stdout, stderr, &http.Client{Timeout: 30 * time.Second}, osKeychainStore{})
	case "upgrade":
		return runUpgradeCommand(args, stdout, stderr, osExecRunner{}, os.Executable, gopathBinDir)
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
	fmt.Fprintln(w, "\nUsage:\n  kei setup [--config PATH] [--control-plane-url URL] [--runtime-token TOKEN]\n  kei runtime bootstrap [--config PATH] [--proxy-path PATH]\n  kei login [--api-url URL] [--no-browser]\n  kei logout [--api-url URL]\n  kei upgrade [--version VERSION]\n  kei bot init --platform cli|teams|discord|slack --name NAME [--agent ID] [--api-url URL]\n  kei bot credential --installation ID [--rotate] [--api-url URL]\n  kei bot agents list|add|remove --installation ID [--agent ID] [--default] [--api-url URL]\n  kei bot status --installation ID [--api-url URL]\n  kei bot delete --installation ID --yes [--api-url URL]\n  kei model-profiles list|get|create|update|delete|test|set-default [--api-url URL]\n  kei credential-store get|put [--api-url URL]\n  kei --version")
}

func printVersion(w io.Writer, version string) {
	fmt.Fprintf(w, "kei %s\n", version)
}

func runLoginCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
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
	if err := login(context.Background(), *apiURL, hostname(), stdout, client, store, time.Sleep, openBrowser); err != nil {
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
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "installation ID")
	rotate := flags.Bool("rotate", false, "rotate an existing runtime credential")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot credential requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot credential: --installation must be a UUID")
		return 2
	}
	if writerIsTerminal(stdout) {
		fmt.Fprintln(stderr, "refusing to write a runtime credential to an interactive terminal; pipe stdout to another command")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(*apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot credential: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot credential: not logged in; run kei login first")
		return 1
	}
	action := "credential"
	if *rotate {
		action = "rotate"
	}
	runtimeToken, status, err := requestRuntimeCredentialAction(context.Background(), client, baseURL, cliToken, *installationID, action)
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
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	platform := flags.String("platform", "", "runtime platform (cli, teams, discord, or slack)")
	agentID := flags.String("agent", "", "Kei agent ID")
	displayName := flags.String("name", "", "installation name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *displayName == "" || !validRuntimePlatform(*platform) {
		fmt.Fprintln(stderr, "bot init requires --platform cli|teams|discord|slack and --name NAME")
		return 2
	}
	installation, err := createBotInstallation(context.Background(), *apiURL, *agentID, *platform, *displayName, io.Discard, client, store)
	if err != nil {
		fmt.Fprintf(stderr, "bot init failed: %v\n", err)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(map[string]string{"installation_id": installation.ID})
	return 0
}

func validRuntimePlatform(platform string) bool {
	return platform == "cli" || platform == "teams" || platform == "discord" || platform == "slack"
}

type createRuntimeInstallationRequest struct {
	AgentID     string `json:"agent_id,omitempty"`
	Platform    string `json:"platform"`
	DisplayName string `json:"display_name"`
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
func initBot(ctx context.Context, apiURL, agentID, platform, displayName string, stdout io.Writer, client *http.Client, store credentialStore) error {
	_, err := createBotInstallation(ctx, apiURL, agentID, platform, displayName, stdout, client, store)
	return err
}

func createBotInstallation(ctx context.Context, apiURL, agentID, platform, displayName string, stdout io.Writer, client *http.Client, store credentialStore) (*createRuntimeInstallationResponse, error) {
	return createBotInstallationWithOptions(ctx, apiURL, agentID, platform, displayName, stdout, client, store)
}

func createBotInstallationWithOptions(ctx context.Context, apiURL, agentID, platform, displayName string, stdout io.Writer, client *http.Client, store credentialStore) (*createRuntimeInstallationResponse, error) {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return nil, err
	}
	token, err := store.Load(baseURL)
	if err != nil {
		return nil, errors.New("not logged in; run kei login first")
	}
	body, err := json.Marshal(createRuntimeInstallationRequest{AgentID: agentID, Platform: platform, DisplayName: displayName})
	if err != nil {
		return nil, fmt.Errorf("encode installation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build installation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("create installation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create installation returned %d", response.StatusCode)
	}
	var installation createRuntimeInstallationResponse
	if err := json.NewDecoder(response.Body).Decode(&installation); err != nil {
		return nil, fmt.Errorf("decode installation response: %w", err)
	}
	if installation.ID == "" {
		return nil, errors.New("installation response is incomplete")
	}
	fmt.Fprintf(stdout, "Created pending %s installation %q (%s).\n", platform, displayName, installation.ID)
	return &installation, nil
}

func login(ctx context.Context, apiURL, clientName string, stdout io.Writer, client *http.Client, store credentialStore, sleep func(time.Duration), openBrowser func(string) error) error {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return err
	}
	startRequestBody, err := json.Marshal(deviceAuthorizationStartRequest{ClientName: clientName})
	if err != nil {
		return fmt.Errorf("encode device authorization request: %w", err)
	}
	startResponse, err := client.Post(baseURL+"/api/cli/device/authorize", "application/json", bytes.NewReader(startRequestBody))
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

	verificationURL := baseURL + "/cli/activate?user_code=" + url.QueryEscape(start.UserCode)
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
		poll, err := pollDeviceAuthorization(ctx, client, baseURL, start.DeviceCode)
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
			if err := store.Save(baseURL, poll.AccessToken); err != nil {
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

func pollDeviceAuthorization(ctx context.Context, client *http.Client, baseURL, deviceCode string) (*deviceAuthorizationPollResponse, error) {
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

func keiWebURL() string {
	if value := os.Getenv("KEI_WEB_URL"); value != "" {
		return value
	}
	return defaultKeiWebURL
}

func normalizedKeiWebURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("--api-url must be an absolute http(s) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--api-url must not include a query or fragment")
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
