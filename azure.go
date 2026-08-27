package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/google/uuid"
)

//go:embed azure/teams-runtime.bicep
var teamsRuntimeBicep []byte

const runtimeTokenContentType = "application/vnd.haikei.runtime-token"

var (
	azureKeyVaultNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9-]{3,24}$`)
	azureSecretNamePattern    = regexp.MustCompile(`^[a-zA-Z0-9-]{1,127}$`)
	azureContainerNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
)

type azureSecretStore interface {
	Exists(ctx context.Context, vaultURL, name string) (bool, error)
	Set(ctx context.Context, vaultURL, name, value string) error
}

type azureKeyVaultSecretStore struct{}

func (azureKeyVaultSecretStore) Exists(ctx context.Context, vaultURL, name string) (bool, error) {
	client, err := newAzureKeyVaultClient(vaultURL)
	if err != nil {
		return false, err
	}
	// GetSecret returns the value as part of the data-plane response. We discard
	// it immediately; the CLI never renders or persists it locally.
	_, err = client.GetSecret(ctx, name, "", nil)
	if err == nil {
		return true, nil
	}
	var responseError *azcore.ResponseError
	if errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("check Key Vault secret: %w", err)
}

func (azureKeyVaultSecretStore) Set(ctx context.Context, vaultURL, name, value string) error {
	client, err := newAzureKeyVaultClient(vaultURL)
	if err != nil {
		return err
	}
	contentType := runtimeTokenContentType
	_, err = client.SetSecret(ctx, name, azsecrets.SetSecretParameters{Value: &value, ContentType: &contentType}, nil)
	if err != nil {
		return fmt.Errorf("set Key Vault secret: %w", err)
	}
	return nil
}

func newAzureKeyVaultClient(vaultURL string) (*azsecrets.Client, error) {
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure credential: %w", err)
	}
	client, err := azsecrets.NewClient(vaultURL, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create Key Vault client: %w", err)
	}
	return client, nil
}

type azureCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osAzureCommandRunner struct {
	stdout io.Writer
	stderr io.Writer
}

func (r osAzureCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = r.stdout
	command.Stderr = r.stderr
	return command.Run()
}

func (r osAzureCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type azureDeploymentConfig struct {
	InstallationID               string
	ResourceGroup                string
	Location                     string
	KeyVaultName                 string
	RuntimeSecretName            string
	TeamsAppPasswordSecretName   string
	TeamsAppID                   string
	TeamsTenantID                string
	RuntimeControlPlaneURL       string
	RuntimeImage                 string
	ContainerAppName             string
	ContainerAppsEnvironmentName string
	ManagedIdentityName          string
	BotResourceName              string
	RuntimeHeartbeatTimeout      time.Duration
}

type runtimeCredentialResponse struct {
	RuntimeToken string `json:"runtime_token"`
}

func runBotDeployCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 || args[0] != "azure" {
		fmt.Fprintln(stderr, "bot deploy currently supports only: kei bot deploy azure")
		return 2
	}
	flags := flag.NewFlagSet("bot deploy azure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "pending Kei installation ID")
	resourceGroup := flags.String("resource-group", "", "Azure resource group")
	location := flags.String("location", "", "Azure region")
	keyVaultName := flags.String("key-vault", "", "customer-owned Azure Key Vault name")
	runtimeSecretName := flags.String("runtime-secret-name", "", "Key Vault secret name for the Kei runtime token")
	teamsAppPasswordSecretName := flags.String("teams-app-password-secret", "", "existing Key Vault secret name for the Teams app password")
	teamsAppID := flags.String("teams-app-id", "", "Microsoft Entra application (client) ID")
	teamsTenantID := flags.String("teams-tenant-id", "", "Microsoft Entra tenant ID")
	runtimeControlPlaneURL := flags.String("runtime-control-plane-url", "", "public Kei runtime control-plane URL")
	runtimeImage := flags.String("image", "", "published Kei Bot Runtime OCI image")
	containerAppName := flags.String("app-name", "", "Container App name")
	containerAppsEnvironmentName := flags.String("environment-name", "", "Container Apps environment name")
	managedIdentityName := flags.String("identity-name", "", "user-assigned managed identity name")
	botResourceName := flags.String("bot-name", "", "Azure Bot resource name")
	heartbeatTimeout := flags.Duration("heartbeat-timeout", 5*time.Minute, "maximum time to wait for the runtime heartbeat")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	config, err := newAzureDeploymentConfig(azureDeploymentConfig{
		InstallationID:               *installationID,
		ResourceGroup:                *resourceGroup,
		Location:                     *location,
		KeyVaultName:                 *keyVaultName,
		RuntimeSecretName:            *runtimeSecretName,
		TeamsAppPasswordSecretName:   *teamsAppPasswordSecretName,
		TeamsAppID:                   *teamsAppID,
		TeamsTenantID:                *teamsTenantID,
		RuntimeControlPlaneURL:       *runtimeControlPlaneURL,
		RuntimeImage:                 *runtimeImage,
		ContainerAppName:             *containerAppName,
		ContainerAppsEnvironmentName: *containerAppsEnvironmentName,
		ManagedIdentityName:          *managedIdentityName,
		BotResourceName:              *botResourceName,
		RuntimeHeartbeatTimeout:      *heartbeatTimeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bot deploy azure: %v\n", err)
		return 2
	}
	if err := deployAzure(context.Background(), *apiURL, config, stdout, client, store, azureKeyVaultSecretStore{}, osAzureCommandRunner{stdout: stdout, stderr: stderr}); err != nil {
		fmt.Fprintf(stderr, "bot deploy azure failed: %v\n", err)
		return 1
	}
	return 0
}

func newAzureDeploymentConfig(config azureDeploymentConfig) (azureDeploymentConfig, error) {
	installation, err := uuid.Parse(config.InstallationID)
	if err != nil {
		return config, errors.New("--installation must be a UUID")
	}
	if config.ResourceGroup == "" || config.Location == "" || config.KeyVaultName == "" || config.TeamsAppPasswordSecretName == "" || config.TeamsAppID == "" || config.TeamsTenantID == "" || config.RuntimeControlPlaneURL == "" || config.RuntimeImage == "" {
		return config, errors.New("--installation, --resource-group, --location, --key-vault, --teams-app-password-secret, --teams-app-id, --teams-tenant-id, --runtime-control-plane-url, and --image are required")
	}
	if !azureKeyVaultNamePattern.MatchString(config.KeyVaultName) {
		return config, errors.New("--key-vault must be a valid Azure Key Vault name")
	}
	if !azureSecretNamePattern.MatchString(config.TeamsAppPasswordSecretName) {
		return config, errors.New("--teams-app-password-secret must be a valid Key Vault secret name")
	}
	if _, err := uuid.Parse(config.TeamsAppID); err != nil {
		return config, errors.New("--teams-app-id must be a UUID")
	}
	if _, err := uuid.Parse(config.TeamsTenantID); err != nil {
		return config, errors.New("--teams-tenant-id must be a UUID")
	}
	if err := validateRuntimeControlPlaneURL(config.RuntimeControlPlaneURL); err != nil {
		return config, err
	}
	shortID := strings.ReplaceAll(installation.String(), "-", "")[:12]
	if config.RuntimeSecretName == "" {
		config.RuntimeSecretName = "kei-runtime-" + shortID
	}
	if !azureSecretNamePattern.MatchString(config.RuntimeSecretName) {
		return config, errors.New("--runtime-secret-name must be a valid Key Vault secret name")
	}
	if config.ContainerAppName == "" {
		config.ContainerAppName = "kei-" + shortID
	}
	if !azureContainerNamePattern.MatchString(config.ContainerAppName) {
		return config, errors.New("--app-name must be a valid Container App name")
	}
	if config.ContainerAppsEnvironmentName == "" {
		config.ContainerAppsEnvironmentName = config.ContainerAppName + "-env"
	}
	if config.ManagedIdentityName == "" {
		config.ManagedIdentityName = config.ContainerAppName + "-identity"
	}
	if config.BotResourceName == "" {
		config.BotResourceName = config.ContainerAppName
	}
	if config.RuntimeHeartbeatTimeout == 0 {
		config.RuntimeHeartbeatTimeout = 5 * time.Minute
	}
	if config.RuntimeHeartbeatTimeout < time.Second {
		return config, errors.New("--heartbeat-timeout must be at least one second")
	}
	return config, nil
}

func validateRuntimeControlPlaneURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("--runtime-control-plane-url must be an absolute HTTPS URL without a query or fragment")
	}
	return nil
}

// deployAzure only passes non-secret parameters to Bicep. The runtime token is
// fetched into this process when necessary and written directly to Key Vault
// through the Azure SDK before Azure CLI applies the infrastructure template.
func deployAzure(ctx context.Context, apiURL string, config azureDeploymentConfig, stdout io.Writer, client *http.Client, store credentialStore, vault azureSecretStore, runner azureCommandRunner) error {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return err
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		return errors.New("not logged in; run kei login first")
	}
	if err := requireAzureKeyVaultRBAC(ctx, config, runner); err != nil {
		return err
	}
	vaultURL := azureKeyVaultURL(config.KeyVaultName)
	exists, err := vault.Exists(ctx, vaultURL, config.RuntimeSecretName)
	if err != nil {
		return fmt.Errorf("check runtime secret in Azure Key Vault: %w", err)
	}
	if !exists {
		fmt.Fprintln(stdout, "Writing the Kei runtime credential directly to Azure Key Vault...")
		runtimeToken, err := requestRuntimeCredential(ctx, client, baseURL, cliToken, config.InstallationID)
		if err != nil {
			return err
		}
		if err := vault.Set(ctx, vaultURL, config.RuntimeSecretName, runtimeToken); err != nil {
			return errors.New("could not write the runtime credential to Azure Key Vault")
		}
	} else {
		fmt.Fprintln(stdout, "Using the existing Kei runtime credential reference in Azure Key Vault.")
	}

	if err := applyAzureBicep(ctx, config, runner); err != nil {
		return fmt.Errorf("apply Azure deployment: %w", err)
	}
	if err := recordAzureDeployment(ctx, client, baseURL, cliToken, config); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Waiting for the deployed runtime to report its installation heartbeat...")
	if err := waitForRuntimeHeartbeat(ctx, client, baseURL, cliToken, config.InstallationID, config.RuntimeHeartbeatTimeout); err != nil {
		return err
	}
	if err := runtimeInstallationAction(ctx, client, baseURL, cliToken, config.InstallationID, "bind", nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Azure Teams runtime deployment completed.")
	fmt.Fprintf(stdout, "Runtime credential reference: %s/secrets/%s\n", vaultURL, config.RuntimeSecretName)
	return nil
}

type runtimeInstallationStatus struct {
	ID              string         `json:"id"`
	Platform        string         `json:"platform,omitempty"`
	DisplayName     string         `json:"display_name,omitempty"`
	Status          string         `json:"status"`
	BindingStatus   string         `json:"binding_status"`
	Deployment      map[string]any `json:"deployment,omitempty"`
	RuntimeVersion  *string        `json:"runtime_version,omitempty"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
}

// runBotStatusCommand prints installation state and safe deployment metadata.
// The runtime credential is never returned by the status endpoint.
func runBotStatusCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("bot status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "Kei installation ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot status requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot status: --installation must be a UUID")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(*apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot status: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot status: not logged in; run kei login first")
		return 1
	}
	status, err := getRuntimeInstallationStatus(context.Background(), client, baseURL, cliToken, *installationID)
	if err != nil {
		fmt.Fprintf(stderr, "bot status: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(status); err != nil {
		fmt.Fprintf(stderr, "bot status: write response: %v\n", err)
		return 1
	}
	return 0
}

func recordAzureDeployment(ctx context.Context, client *http.Client, baseURL, cliToken string, config azureDeploymentConfig) error {
	metadata := map[string]string{
		"provider": "azure", "resource_group": config.ResourceGroup, "location": config.Location,
		"key_vault_name": config.KeyVaultName, "runtime_secret_name": config.RuntimeSecretName,
		"container_app_name": config.ContainerAppName, "container_apps_environment_name": config.ContainerAppsEnvironmentName,
		"managed_identity_name": config.ManagedIdentityName, "bot_resource_name": config.BotResourceName,
		"runtime_image": config.RuntimeImage, "runtime_control_plane_url": config.RuntimeControlPlaneURL,
	}
	return runtimeInstallationAction(ctx, client, baseURL, cliToken, config.InstallationID, "deployment", map[string]any{"deployment": metadata})
}

func waitForRuntimeHeartbeat(ctx context.Context, client *http.Client, baseURL, cliToken, installationID string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := getRuntimeInstallationStatus(ctx, client, baseURL, cliToken, installationID)
		if err != nil {
			return err
		}
		if status.LastHeartbeatAt != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("timed out waiting for the deployed runtime heartbeat; the installation remains pending")
		case <-ticker.C:
		}
	}
}

func getRuntimeInstallationStatus(ctx context.Context, client *http.Client, baseURL, cliToken, installationID string) (*runtimeInstallationStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID), nil)
	if err != nil {
		return nil, fmt.Errorf("build runtime status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get runtime status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime status returned %d", response.StatusCode)
	}
	var status runtimeInstallationStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode runtime status: %w", err)
	}
	if status.ID == "" {
		return nil, errors.New("runtime status response is incomplete")
	}
	return &status, nil
}

func runtimeInstallationAction(ctx context.Context, client *http.Client, baseURL, cliToken, installationID, action string, payload any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode runtime %s request: %w", action, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/"+action, body)
	if err != nil {
		return fmt.Errorf("build runtime %s request: %w", action, err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runtime %s request: %w", action, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("runtime %s returned %d", action, response.StatusCode)
	}
	return nil
}

// requireAzureKeyVaultRBAC ensures the managed identity role assignment in the
// Bicep package is effective. The first Azure deployment contract supports
// customer vaults in the target resource group with Azure RBAC authorization.
func requireAzureKeyVaultRBAC(ctx context.Context, config azureDeploymentConfig, runner azureCommandRunner) error {
	output, err := runner.Output(ctx, "az", "keyvault", "show", "--name", config.KeyVaultName, "--resource-group", config.ResourceGroup, "--query", "properties.enableRbacAuthorization", "--output", "tsv", "--only-show-errors")
	if err != nil {
		return errors.New("could not verify the Azure Key Vault; confirm its name, resource group, and your Azure access")
	}
	if strings.TrimSpace(string(output)) != "true" {
		return errors.New("the Azure Key Vault must have Azure RBAC authorization enabled for the runtime managed identity")
	}
	return nil
}

func requestRuntimeCredential(ctx context.Context, client *http.Client, baseURL, cliToken, installationID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/credential", nil)
	if err != nil {
		return "", fmt.Errorf("build runtime credential request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cliToken)
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request runtime credential: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("runtime credential request returned %d", response.StatusCode)
	}
	var credential runtimeCredentialResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<10)).Decode(&credential); err != nil {
		return "", fmt.Errorf("decode runtime credential response: %w", err)
	}
	if credential.RuntimeToken == "" {
		return "", errors.New("runtime credential response is incomplete")
	}
	return credential.RuntimeToken, nil
}

func applyAzureBicep(ctx context.Context, config azureDeploymentConfig, runner azureCommandRunner) error {
	dir, err := os.MkdirTemp("", "kei-azure-deploy-")
	if err != nil {
		return fmt.Errorf("create deployment workspace: %w", err)
	}
	defer os.RemoveAll(dir)
	templatePath := filepath.Join(dir, "teams-runtime.bicep")
	parametersPath := filepath.Join(dir, "parameters.json")
	if err := os.WriteFile(templatePath, teamsRuntimeBicep, 0o600); err != nil {
		return fmt.Errorf("write Bicep template: %w", err)
	}
	parameters, err := json.Marshal(azureBicepParameters(config))
	if err != nil {
		return fmt.Errorf("encode Bicep parameters: %w", err)
	}
	if err := os.WriteFile(parametersPath, parameters, 0o600); err != nil {
		return fmt.Errorf("write Bicep parameters: %w", err)
	}
	deploymentName := "kei-" + strings.ReplaceAll(config.InstallationID, "-", "")[:12]
	return runner.Run(ctx, "az", "deployment", "group", "create", "--name", deploymentName, "--resource-group", config.ResourceGroup, "--template-file", templatePath, "--parameters", "@"+parametersPath, "--only-show-errors", "--output", "none")
}

func azureBicepParameters(config azureDeploymentConfig) map[string]any {
	return map[string]any{
		"$schema":        "https://schema.management.azure.com/schemas/2019-04-01/deploymentParameters.json#",
		"contentVersion": "1.0.0.0",
		"parameters": map[string]map[string]string{
			"location":                     {"value": config.Location},
			"installationId":               {"value": config.InstallationID},
			"keyVaultName":                 {"value": config.KeyVaultName},
			"runtimeTokenSecretName":       {"value": config.RuntimeSecretName},
			"teamsAppPasswordSecretName":   {"value": config.TeamsAppPasswordSecretName},
			"teamsAppId":                   {"value": config.TeamsAppID},
			"teamsTenantId":                {"value": config.TeamsTenantID},
			"runtimeControlPlaneUrl":       {"value": config.RuntimeControlPlaneURL},
			"runtimeImage":                 {"value": config.RuntimeImage},
			"containerAppName":             {"value": config.ContainerAppName},
			"containerAppsEnvironmentName": {"value": config.ContainerAppsEnvironmentName},
			"managedIdentityName":          {"value": config.ManagedIdentityName},
			"botResourceName":              {"value": config.BotResourceName},
		},
	}
}

func azureKeyVaultURL(name string) string {
	return "https://" + name + ".vault.azure.net"
}
