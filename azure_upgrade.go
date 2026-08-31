package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func runBotUpgradeCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, runner azureCommandRunner) int {
	if len(args) == 0 || args[0] != "azure" {
		fmt.Fprintln(stderr, "bot upgrade currently supports only: kei bot upgrade azure")
		return 2
	}
	flags := flag.NewFlagSet("bot upgrade azure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installation := flags.String("installation", "", "existing Kei installation ID")
	image := flags.String("image", "", "published Kei Bot Runtime OCI image")
	manifest := flags.String("teams-manifest", "", "write a refreshed Teams app manifest")
	timeout := flags.Duration("heartbeat-timeout", 5*time.Minute, "maximum heartbeat wait")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *installation == "" || *image == "" {
		fmt.Fprintln(stderr, "bot upgrade azure requires --installation ID and --image OCI_IMAGE")
		return 2
	}
	if _, err := uuid.Parse(*installation); err != nil {
		fmt.Fprintln(stderr, "bot upgrade azure: --installation must be a UUID")
		return 2
	}
	if *timeout < time.Second {
		fmt.Fprintln(stderr, "bot upgrade azure: --heartbeat-timeout must be at least one second")
		return 2
	}
	base, err := normalizedKeiWebURL(*apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot upgrade azure: %v\n", err)
		return 2
	}
	token, err := store.Load(base)
	if err != nil {
		fmt.Fprintln(stderr, "bot upgrade azure: not logged in; run kei login first")
		return 1
	}
	ctx := context.Background()
	status, err := getRuntimeInstallationStatus(ctx, client, base, token, *installation)
	if err != nil {
		fmt.Fprintf(stderr, "bot upgrade azure: %v\n", err)
		return 1
	}
	config, err := newAzureUpgradeConfig(status, *image, *manifest, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "bot upgrade azure: %v\n", err)
		return 2
	}
	if err := upgradeAzure(ctx, base, token, status, config, stdout, client, runner); err != nil {
		fmt.Fprintf(stderr, "bot upgrade azure failed: %v\n", err)
		return 1
	}
	return 0
}

func newAzureUpgradeConfig(status *runtimeInstallationStatus, image, manifest string, timeout time.Duration) (azureDeploymentConfig, error) {
	if status == nil || status.ID == "" {
		return azureDeploymentConfig{}, errors.New("installation status is incomplete")
	}
	if _, err := uuid.Parse(status.ID); err != nil {
		return azureDeploymentConfig{}, errors.New("installation status has an invalid ID")
	}
	if status.Status != "pending" && status.Status != "active" {
		return azureDeploymentConfig{}, fmt.Errorf("installation is %q and cannot be upgraded", status.Status)
	}
	if provider, ok := runtimeDeploymentString(status, "provider"); !ok || provider != "azure" {
		return azureDeploymentConfig{}, errors.New("installation is not an Azure runtime")
	}
	keys := map[string]string{"resource_group": "resource group", "location": "location", "key_vault_name": "Key Vault name", "runtime_secret_name": "runtime secret name", "teams_app_password_secret_name": "Teams app password secret name", "teams_app_id": "Teams app ID", "teams_tenant_id": "Teams tenant ID", "runtime_control_plane_url": "runtime control-plane URL", "container_app_name": "Container App name", "container_apps_environment_name": "Container Apps environment name", "managed_identity_name": "managed identity name", "bot_resource_name": "Azure Bot resource name"}
	values := map[string]string{}
	for key, label := range keys {
		value, ok := runtimeDeploymentString(status, key)
		if !ok {
			return azureDeploymentConfig{}, fmt.Errorf("deployment metadata is missing the %s; redeploy before using upgrade", label)
		}
		values[key] = value
	}
	return newAzureDeploymentConfig(azureDeploymentConfig{InstallationID: status.ID, ResourceGroup: values["resource_group"], Location: values["location"], KeyVaultName: values["key_vault_name"], RuntimeSecretName: values["runtime_secret_name"], TeamsAppPasswordSecretName: values["teams_app_password_secret_name"], TeamsAppID: values["teams_app_id"], TeamsTenantID: values["teams_tenant_id"], TeamsManifestPath: manifest, RuntimeControlPlaneURL: values["runtime_control_plane_url"], RuntimeImage: image, ContainerAppName: values["container_app_name"], ContainerAppsEnvironmentName: values["container_apps_environment_name"], ManagedIdentityName: values["managed_identity_name"], BotResourceName: values["bot_resource_name"], RuntimeHeartbeatTimeout: timeout})
}

// upgradeAzure never calls the credential endpoint; the existing Key Vault
// runtime secret reference is preserved while Bicep updates the image.
func upgradeAzure(ctx context.Context, baseURL, token string, status *runtimeInstallationStatus, config azureDeploymentConfig, stdout io.Writer, client *http.Client, runner azureCommandRunner) error {
	if err := requireAzureKeyVaultRBAC(ctx, config, runner); err != nil {
		return err
	}
	if err := applyAzureBicep(ctx, config, runner); err != nil {
		return fmt.Errorf("apply Azure deployment: %w", err)
	}
	if config.TeamsManifestPath != "" {
		if err := writeTeamsManifest(ctx, config, runner); err != nil {
			return err
		}
	}
	if err := recordAzureDeployment(ctx, client, baseURL, token, config); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Upgraded the Azure Teams runtime to %s.\n", config.RuntimeImage)
	fmt.Fprintln(stdout, "Waiting for the upgraded runtime to report its installation heartbeat...")
	if err := waitForRuntimeHeartbeat(ctx, client, baseURL, token, config.InstallationID, config.RuntimeHeartbeatTimeout); err != nil {
		return err
	}
	if status.Status == "pending" {
		if err := runtimeInstallationAction(ctx, client, baseURL, token, config.InstallationID, "bind", nil); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "Azure Teams runtime upgrade completed.")
	return nil
}
