# Kei CLI

The Kei CLI provisions and manages customer-hosted Kei bot runtimes. The MVP
supports Microsoft Teams on Azure. The customer owns the Azure subscription,
resource group, Key Vault, and deployed runtime; Kei provides the control-plane
installation and runtime image.

## Build

The CLI is currently built from this repository:

```sh
cd cmd/kei
go build -o tmp/kei .
```

Run `tmp/kei` below, or install the binary on `PATH` as `kei`.

## Prerequisites

1. Install and sign in to the [Azure CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli):

   ```sh
   az login
   az account set --subscription SUBSCRIPTION_ID
   ```

2. Register the resource providers used by the Azure template if the
   subscription has not used them before:

   ```sh
   for provider in Microsoft.KeyVault Microsoft.ManagedIdentity \
     Microsoft.OperationalInsights Microsoft.App Microsoft.BotService
   do
     az provider register --namespace "$provider"
   done
   ```

3. Log in to Kei. The CLI prints a browser URL and one-time verification code:

   ```sh
   tmp/kei login --api-url https://app.haikeilabs.com
   ```

The CLI login token is short-lived. Re-run `kei login` before a retry if a long
Azure deployment ends with a 401 from the Kei API.

## One-shot Azure + Teams installation

For a new customer deployment, use `bot install`. It creates the pending Kei
installation, creates or reuses the Azure resource group and Key Vault, creates
or reuses a Microsoft Entra app, writes the Teams password and Kei runtime
credential to Key Vault, applies the Bicep template, waits for a runtime
heartbeat, and binds the installation.

```sh
tmp/kei bot install azure \
  --platform teams \
  --name "Customer Teams" \
  --location westus2 \
  --runtime-control-plane-url https://app.haikeilabs.com \
  --image ghcr.io/haikeilabs/kei-teams-runtime:IMAGE_TAG \
  --create-teams-app \
  --teams-manifest ./kei-teams-manifest.json
```

The published runtime image is public in GHCR. Replace `IMAGE_TAG` with the
version being tested. The generated Teams manifest is written to the path
provided by `--teams-manifest`.

After deployment, review and install the generated manifest in Teams using the
customer tenant's normal app-upload process.

If deployment fails after the installation is created, the CLI prints its ID.
Resume with `bot deploy azure` instead of creating another installation.

## Split install/deploy flow

Use this flow when Azure details or an existing Teams app are supplied
separately:

```sh
tmp/kei bot init --platform teams --name "Customer Teams"

tmp/kei bot deploy azure \
  --installation INSTALLATION_ID \
  --resource-group RESOURCE_GROUP \
  --location REGION \
  --key-vault KEY_VAULT_NAME \
  --runtime-control-plane-url https://app.haikeilabs.com \
  --image ghcr.io/haikeilabs/kei-teams-runtime:IMAGE_TAG \
  --create-teams-app \
  --teams-manifest ./kei-teams-manifest.json
```

For an existing Entra/Teams app, omit `--create-teams-app` and provide
`--teams-app-password-secret`, `--teams-app-id`, and `--teams-tenant-id`.
The existing Key Vault must use Azure RBAC. The CLI writes secrets directly to
Key Vault; it never prints the secret values or stores them in the Kei control
plane.

Run the read-only Azure preflight before deployment:

```sh
tmp/kei bot doctor azure \
  --resource-group RESOURCE_GROUP --location REGION --key-vault KEY_VAULT_NAME \
  --teams-app-password-secret teams-app-password \
  --teams-app-id ENTRA_APP_ID --teams-tenant-id TENANT_ID
```

After publishing a new runtime image, upgrade an existing deployment with:

```sh
tmp/kei bot upgrade azure --installation INSTALLATION_ID \
  --image ghcr.io/haikeilabs/kei-teams-runtime:NEW_TAG
```

`upgrade` reuses the recorded Azure deployment and Key Vault runtime-secret
reference; it never requests or prints a new runtime credential. Deployments
created before complete deployment metadata was recorded must be redeployed
once before they can be upgraded.

Useful inspection commands:

```sh
tmp/kei bot status --installation INSTALLATION_ID
tmp/kei bot agents list --installation INSTALLATION_ID
tmp/kei bot agents add --installation INSTALLATION_ID --agent AGENT_ID
```

## Regional retry and teardown

Azure can fail to create a managed environment because the selected region is
temporarily out of capacity. Clean up a failed regional attempt while keeping
the installation available for redeployment:

```sh
tmp/kei bot destroy azure \
  --installation INSTALLATION_ID \
  --resource-group RESOURCE_GROUP \
  --environment-name ENVIRONMENT_NAME \
  --delete-environment \
  --preserve-installation
```

The command prints a plan and requires typing the installation name or ID.
`--preserve-installation` skips disabling the control-plane installation, so
the same ID and runtime credential can be used for a different region. The
resource group and Key Vault remain available; use new environment, app,
identity, and bot names in the new region.

For final teardown, omit `--preserve-installation`. The installation is then
disabled. The destroy command intentionally retains the customer Key Vault and
secrets. Microsoft Entra app registrations are tenant-level objects and are
also not removed by this command; review them separately.

## Azure resources and permissions

The Bicep deployment creates or configures:

- A Container Apps managed environment and runtime Container App with public
  HTTPS ingress.
- A user-assigned managed identity and Log Analytics workspace.
- An Azure Bot resource used by the Teams channel.
- Key Vault references for the Kei runtime token and Teams app password.
- A Microsoft Entra application and service principal when
  `--create-teams-app` is used.

The operator needs permission to register providers and create/update these
resources, read/write the selected Key Vault secrets, and create or update the
Entra application/service principal. The deployed identity receives only the
Key Vault Secrets User role needed by the runtime; it does not receive Azure
resource-management permissions.

## Troubleshooting

### CLI and login

- **`unknown bot command "install"`**: the binary is stale. Rebuild from
  `cmd/kei` and run the newly built `tmp/kei`.
- **`missing state` or `failed to complete sign-in`**: use the exact
  `/cli/activate?...` URL printed by the CLI and ensure the deployed Kei web
  service contains the CLI login routing fix. Re-run `kei login` afterward.
- **Installation or deployment returns 401**: run `kei login` again with the
  correct `--api-url`. A long Azure operation can outlive the CLI token.

### Azure prerequisites and secrets

- **`MissingSubscriptionRegistration` for `Microsoft.KeyVault` (or another
  provider)**: register the provider with `az provider register --namespace`
  and wait for it to reach `Registered`.
- **Provider registration appears to hang**: omit `--wait` and poll directly:

  ```sh
  az provider show --namespace Microsoft.KeyVault \
    --query registrationState --output tsv
  ```

  Repeat for each provider until the state is `Registered`.
- **Key Vault `403 ForbiddenByRbac`**: the signed-in operator needs a data
  plane role such as `Key Vault Secrets Officer` on the vault. The runtime’s
  managed identity separately needs `Key Vault Secrets User`.
- **`specified vault already exists`**: use the current CLI; same-resource-group
  RBAC-enabled vaults are reused. A vault in another resource group or without
  RBAC must be corrected explicitly.

### Azure deployment

- **`ManagedEnvironmentCapacityHeavyUsageError`**: this is a regional Azure
  capacity constraint, not a Kei image or credential failure. Retry later or
  select another region with new environment/app names.
- **Generic ARM `DeploymentFailed`**: inspect the nested operation and the
  environment’s `deploymentErrors`:

  ```sh
  az deployment group list -g RESOURCE_GROUP -o table
  az deployment operation group list -g RESOURCE_GROUP -n DEPLOYMENT_NAME -o jsonc
  az rest --method get --url \
    "https://management.azure.com/subscriptions/SUBSCRIPTION_ID/resourceGroups/RESOURCE_GROUP/providers/Microsoft.App/managedEnvironments/ENVIRONMENT_NAME?api-version=2025-07-01" \
    -o jsonc
  ```

- **`installation has no Azure resource group metadata`**: the deployment
  failed before metadata was recorded. Pass `--resource-group` to
  `bot destroy azure`.
- **`could not list Azure resources tagged for this installation`**: rebuild
  the current CLI. It falls back to listing the resource group and filtering
  the `keiInstallationId` tag locally when Azure rejects server-side tag
  filtering.
- **`runtime deployment returned 400`**: the control plane rejected deployment
  metadata. Deploy the current `abac-engine`; the Teams one-shot flow requires
  the `teams_app_created_by_kei` and `teams_app_id` metadata fields.
- **Azure CLI reports an invalid Container Apps API version**: update the
  `containerapp` extension or use `az rest` with an API version listed as
  supported by the error response.

## Current scope

The MVP supports Azure Container Apps + Microsoft Teams, public runtime
ingress, Azure Key Vault, and customer-owned Azure subscriptions. Discord,
Slack, AWS, GCP, Azure/AWS deployment buttons, Terraform, and Helm remain
future deployment options.
