package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

// runBotInstallCommand is the one-shot Azure experience. It creates the Kei
// installation first, then provisions/reuses the Azure prerequisites and runs
// the normal deployment pipeline. If deployment fails, the installation ID is
// printed so the operator can resume with bot deploy instead of creating a
// duplicate installation.
func runBotInstallCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 || args[0] != "azure" {
		fmt.Fprintln(stderr, "bot install currently supports only: kei bot install azure")
		return 2
	}

	flags := flag.NewFlagSet("bot install azure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	platform := flags.String("platform", "teams", "bot platform (currently teams)")
	agentID := flags.String("agent", "", "optional Kei agent ID")
	displayName := flags.String("name", "", "installation name")
	resourceGroup := flags.String("resource-group", "", "Azure resource group (created when absent)")
	location := flags.String("location", "eastus", "Azure region")
	keyVaultName := flags.String("key-vault", "", "Azure Key Vault name (created when absent)")
	runtimeSecretName := flags.String("runtime-secret-name", "", "Key Vault secret name for the Kei runtime token")
	teamsAppPasswordSecretName := flags.String("teams-app-password-secret", "", "existing Key Vault secret name for the Teams app password")
	teamsAppID := flags.String("teams-app-id", "", "Microsoft Entra application (client) ID")
	teamsTenantID := flags.String("teams-tenant-id", "", "Microsoft Entra tenant ID")
	createTeamsApp := flags.Bool("create-teams-app", true, "create a single-tenant Microsoft Entra app")
	teamsAppDisplayName := flags.String("teams-app-display-name", "", "display name for a newly created Teams app")
	teamsManifestPath := flags.String("teams-manifest", "", "write a Teams app manifest with the deployed bot endpoint")
	runtimeControlPlaneURL := flags.String("runtime-control-plane-url", defaultKeiWebURL, "public Kei runtime control-plane URL")
	runtimeImage := flags.String("image", "", "published Kei Bot Runtime OCI image")
	containerAppName := flags.String("app-name", "", "Container App name")
	containerAppsEnvironmentName := flags.String("environment-name", "", "Container Apps environment name")
	managedIdentityName := flags.String("identity-name", "", "user-assigned managed identity name")
	botResourceName := flags.String("bot-name", "", "Azure Bot resource name")
	heartbeatTimeout := flags.Duration("heartbeat-timeout", 5*time.Minute, "maximum time to wait for the runtime heartbeat")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *displayName == "" || *platform != "teams" {
		fmt.Fprintln(stderr, "bot install azure requires --name NAME and currently supports --platform teams")
		return 2
	}
	if *runtimeImage == "" {
		fmt.Fprintln(stderr, "bot install azure requires --image OCI_IMAGE")
		return 2
	}
	if err := validateRuntimeControlPlaneURL(*runtimeControlPlaneURL); err != nil {
		fmt.Fprintf(stderr, "bot install azure: %v\n", err)
		return 2
	}
	if !*createTeamsApp && (*teamsAppPasswordSecretName == "" || *teamsAppID == "" || *teamsTenantID == "") {
		fmt.Fprintln(stderr, "bot install azure existing-app deployments require --teams-app-password-secret, --teams-app-id, and --teams-tenant-id")
		return 2
	}

	installation, err := createBotInstallationIdempotent(context.Background(), *apiURL, *agentID, *platform, *displayName, stdout, client, store)
	if err != nil {
		fmt.Fprintf(stderr, "bot install azure failed: %v\n", err)
		return 1
	}

	config, err := newAzureDeploymentConfig(azureDeploymentConfig{
		InstallationID:               installation.ID,
		ResourceGroup:                *resourceGroup,
		CreateResourceGroup:          true,
		Location:                     *location,
		KeyVaultName:                 *keyVaultName,
		CreateKeyVault:               true,
		RuntimeSecretName:            *runtimeSecretName,
		TeamsAppPasswordSecretName:   *teamsAppPasswordSecretName,
		TeamsAppID:                   *teamsAppID,
		TeamsTenantID:                *teamsTenantID,
		CreateTeamsApp:               *createTeamsApp,
		TeamsAppDisplayName:          *teamsAppDisplayName,
		TeamsManifestPath:            *teamsManifestPath,
		RuntimeControlPlaneURL:       *runtimeControlPlaneURL,
		RuntimeImage:                 *runtimeImage,
		ContainerAppName:             *containerAppName,
		ContainerAppsEnvironmentName: *containerAppsEnvironmentName,
		ManagedIdentityName:          *managedIdentityName,
		BotResourceName:              *botResourceName,
		RuntimeHeartbeatTimeout:      *heartbeatTimeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bot install azure failed: %v\n", err)
		fmt.Fprintf(stderr, "The installation remains pending: %s\n", installation.ID)
		return 2
	}

	if err := deployAzure(context.Background(), *apiURL, config, stdout, client, store, azureKeyVaultSecretStore{}, osAzureCommandRunner{stdout: stdout, stderr: stderr}); err != nil {
		fmt.Fprintf(stderr, "bot install azure failed: %v\n", err)
		fmt.Fprintf(stderr, "The installation remains pending; resume with bot deploy azure using --installation %s.\n", installation.ID)
		return 1
	}
	return 0
}
