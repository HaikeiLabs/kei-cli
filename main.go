package main

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

	"github.com/zalando/go-keyring"
)

const defaultKeiWebURL = "https://app.haikeilabs.com"

type credentialStore interface {
	Save(serverURL, token string) error
	Load(serverURL string) (string, error)
}

type osKeychainStore struct{}

func (osKeychainStore) Save(serverURL, token string) error {
	return keyring.Set("kei-cli", keychainAccount(serverURL), token)
}

func (osKeychainStore) Load(serverURL string) (string, error) {
	return keyring.Get("kei-cli", keychainAccount(serverURL))
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

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "login":
		os.Exit(runLoginCommand(os.Args[2:], os.Stdout, os.Stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{}))
	case "bot":
		os.Exit(runBotCommand(os.Args[2:], os.Stdout, os.Stderr, &http.Client{Timeout: 15 * time.Second}, osKeychainStore{}))
	case "help", "--help", "-h":
		printUsage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Kei deployment CLI")
	fmt.Fprintln(w, "\nUsage:\n  kei login [--api-url URL]\n  kei bot init --platform teams|discord|slack --agent ID --name NAME [--api-url URL]\n  kei bot status --installation ID [--api-url URL]\n  kei bot deploy azure --installation ID --resource-group NAME --location REGION --key-vault NAME --runtime-control-plane-url URL --image OCI_IMAGE [--create-teams-app --teams-app-display-name NAME]\n  kei bot deploy azure --installation ID --resource-group NAME --location REGION --key-vault NAME --teams-app-password-secret NAME --teams-app-id ID --teams-tenant-id ID --runtime-control-plane-url URL --image OCI_IMAGE [--teams-manifest PATH]\n  kei bot destroy azure --installation ID [--confirm-destroy ID] [--api-url URL]")
}

func runLoginCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "login accepts no positional arguments")
		return 2
	}
	if err := login(context.Background(), *apiURL, hostname(), stdout, client, store, time.Sleep); err != nil {
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
	case "deploy":
		return runBotDeployCommand(args[1:], stdout, stderr, client, store)
	case "status":
		return runBotStatusCommand(args[1:], stdout, stderr, client, store)
	case "destroy":
		return runBotDestroyCommand(args[1:], stdout, stderr, os.Stdin, client, store, osAzureCommandRunner{stdout: stdout, stderr: stderr})
	default:
		fmt.Fprintf(stderr, "unknown bot command %q\n", args[0])
		return 2
	}
}

func runBotInitCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	platform := flags.String("platform", "", "bot platform")
	agentID := flags.String("agent", "", "Kei agent ID")
	displayName := flags.String("name", "", "installation name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *agentID == "" || *displayName == "" || (*platform != "teams" && *platform != "discord" && *platform != "slack") {
		fmt.Fprintln(stderr, "bot init requires --platform teams|discord|slack, --agent ID, and --name NAME")
		return 2
	}
	if err := initBot(context.Background(), *apiURL, *agentID, *platform, *displayName, stdout, client, store); err != nil {
		fmt.Fprintf(stderr, "bot init failed: %v\n", err)
		return 1
	}
	return 0
}

type createRuntimeInstallationRequest struct {
	AgentID     string `json:"agent_id"`
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
// intentionally created later by `kei bot deploy` and delivered directly to a
// customer secret manager, never to terminal output or local CLI config.
func initBot(ctx context.Context, apiURL, agentID, platform, displayName string, stdout io.Writer, client *http.Client, store credentialStore) error {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return err
	}
	token, err := store.Load(baseURL)
	if err != nil {
		return errors.New("not logged in; run kei login first")
	}
	body, err := json.Marshal(createRuntimeInstallationRequest{AgentID: agentID, Platform: platform, DisplayName: displayName})
	if err != nil {
		return fmt.Errorf("encode installation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build installation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("create installation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("create installation returned %d", response.StatusCode)
	}
	var installation createRuntimeInstallationResponse
	if err := json.NewDecoder(response.Body).Decode(&installation); err != nil {
		return fmt.Errorf("decode installation response: %w", err)
	}
	if installation.ID == "" {
		return errors.New("installation response is incomplete")
	}
	fmt.Fprintf(stdout, "Created pending %s installation %q (%s).\n", platform, displayName, installation.ID)
	fmt.Fprintln(stdout, "Run kei bot deploy azure to deliver its runtime credential to Key Vault and deploy the bot.")
	return nil
}

func login(ctx context.Context, apiURL, clientName string, stdout io.Writer, client *http.Client, store credentialStore, sleep func(time.Duration)) error {
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
		return "kei-cli"
	}
	return "kei-cli@" + host
}
