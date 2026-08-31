package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type azureDoctorConfig struct {
	ResourceGroup, Location, KeyVaultName, RuntimeControlPlaneURL, InstallationID string
	TeamsAppPasswordSecretName, TeamsAppID, TeamsTenantID                         string
}

type azureDoctorCheck struct{ Name, Status, Detail string }
type azureDoctorReport struct{ Checks []azureDoctorCheck }

func (r *azureDoctorReport) add(status, name, detail string) {
	r.Checks = append(r.Checks, azureDoctorCheck{Name: name, Status: status, Detail: detail})
}
func (r azureDoctorReport) failures() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == "FAIL" {
			n++
		}
	}
	return n
}

func runBotDoctorCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore, runner azureCommandRunner) int {
	if len(args) == 0 || args[0] != "azure" {
		fmt.Fprintln(stderr, "bot doctor currently supports only: kei bot doctor azure")
		return 2
	}
	flags := flag.NewFlagSet("bot doctor azure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	resourceGroup := flags.String("resource-group", "", "Azure resource group")
	location := flags.String("location", "", "Azure region")
	keyVault := flags.String("key-vault", "", "customer-owned Azure Key Vault name")
	runtimeURL := flags.String("runtime-control-plane-url", "", "public Kei runtime control-plane URL")
	installation := flags.String("installation", "", "optional Kei installation ID to verify")
	passwordSecret := flags.String("teams-app-password-secret", "", "optional Key Vault secret name for the Teams app password")
	appID := flags.String("teams-app-id", "", "optional Microsoft Entra application (client) ID")
	tenantID := flags.String("teams-tenant-id", "", "optional Microsoft Entra tenant ID")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	config, err := newAzureDoctorConfig(azureDoctorConfig{ResourceGroup: *resourceGroup, Location: *location, KeyVaultName: *keyVault, RuntimeControlPlaneURL: *runtimeURL, InstallationID: *installation, TeamsAppPasswordSecretName: *passwordSecret, TeamsAppID: *appID, TeamsTenantID: *tenantID})
	if err != nil {
		fmt.Fprintf(stderr, "bot doctor azure: %v\n", err)
		return 2
	}
	report := doctorAzure(context.Background(), *apiURL, config, client, store, runner)
	fmt.Fprintln(stdout, "Azure Teams bot doctor")
	for _, check := range report.Checks {
		fmt.Fprintf(stdout, "[%s] %s: %s\n", check.Status, check.Name, check.Detail)
	}
	if failures := report.failures(); failures > 0 {
		fmt.Fprintf(stdout, "Doctor found %d blocking issue(s).\n", failures)
		return 1
	}
	fmt.Fprintln(stdout, "Doctor checks completed; no blocking issues found.")
	return 0
}

func newAzureDoctorConfig(c azureDoctorConfig) (azureDoctorConfig, error) {
	if c.ResourceGroup == "" || c.Location == "" || c.KeyVaultName == "" {
		return c, errors.New("--resource-group, --location, and --key-vault are required")
	}
	if !azureKeyVaultNamePattern.MatchString(c.KeyVaultName) {
		return c, errors.New("--key-vault must be a valid Azure Key Vault name")
	}
	if c.RuntimeControlPlaneURL != "" {
		if err := validateRuntimeControlPlaneURL(c.RuntimeControlPlaneURL); err != nil {
			return c, err
		}
	}
	if c.InstallationID != "" {
		if _, err := uuid.Parse(c.InstallationID); err != nil {
			return c, errors.New("--installation must be a UUID")
		}
	}
	anyApp := c.TeamsAppPasswordSecretName != "" || c.TeamsAppID != "" || c.TeamsTenantID != ""
	if anyApp && (c.TeamsAppPasswordSecretName == "" || c.TeamsAppID == "" || c.TeamsTenantID == "") {
		return c, errors.New("Teams app checks require --teams-app-password-secret, --teams-app-id, and --teams-tenant-id together")
	}
	if c.TeamsAppID != "" {
		if _, err := uuid.Parse(c.TeamsAppID); err != nil {
			return c, errors.New("--teams-app-id must be a UUID")
		}
	}
	if c.TeamsTenantID != "" {
		if _, err := uuid.Parse(c.TeamsTenantID); err != nil {
			return c, errors.New("--teams-tenant-id must be a UUID")
		}
	}
	if c.TeamsAppPasswordSecretName != "" && !azureSecretNamePattern.MatchString(c.TeamsAppPasswordSecretName) {
		return c, errors.New("--teams-app-password-secret must be a valid Key Vault secret name")
	}
	return c, nil
}

func doctorAzure(ctx context.Context, apiURL string, c azureDoctorConfig, client *http.Client, store credentialStore, runner azureCommandRunner) azureDoctorReport {
	var r azureDoctorReport
	account, err := runner.Output(ctx, "az", "account", "show", "--query", "{subscriptionId:id,tenantId:tenantId}", "--output", "json", "--only-show-errors")
	if err != nil {
		r.add("FAIL", "Azure login", "could not read the active Azure account; run az login")
	} else {
		var a struct{ SubscriptionID, TenantID string }
		_ = json.Unmarshal(account, &a)
		r.add("PASS", "Azure login", "an active Azure account is available")
	}
	for _, ns := range []string{"Microsoft.App", "Microsoft.ManagedIdentity", "Microsoft.OperationalInsights", "Microsoft.BotService"} {
		out, e := runner.Output(ctx, "az", "provider", "show", "--namespace", ns, "--query", "registrationState", "--output", "tsv", "--only-show-errors")
		state := strings.TrimSpace(string(out))
		if e != nil {
			r.add("FAIL", ns, "could not read provider registration state")
		} else if !strings.EqualFold(state, "Registered") {
			r.add("FAIL", ns, fmt.Sprintf("provider is %q; register it before deploying", state))
		} else {
			r.add("PASS", ns, "provider is registered")
		}
	}
	vaultOut, vaultErr := runner.Output(ctx, "az", "keyvault", "show", "--name", c.KeyVaultName, "--resource-group", c.ResourceGroup, "--query", "{id:id,rbac:properties.enableRbacAuthorization}", "--output", "json", "--only-show-errors")
	var vault struct {
		ID   string `json:"id"`
		RBAC bool   `json:"rbac"`
	}
	if vaultErr != nil {
		r.add("FAIL", "Key Vault", "could not read the Key Vault; confirm its name, resource group, and Azure access")
	} else if json.Unmarshal(vaultOut, &vault) != nil {
		r.add("FAIL", "Key Vault", "Azure returned invalid Key Vault metadata")
	} else if !vault.RBAC {
		r.add("FAIL", "Key Vault RBAC", "Azure RBAC authorization is disabled")
	} else {
		r.add("PASS", "Key Vault RBAC", "Azure RBAC authorization is enabled")
	}
	if vault.ID != "" {
		if _, e := runner.Output(ctx, "az", "role", "assignment", "list", "--scope", vault.ID, "--include-inherited", "--query", "[].id", "--output", "tsv", "--only-show-errors"); e != nil {
			r.add("FAIL", "Role assignments", "could not inspect role assignments at the Key Vault scope")
		} else {
			r.add("PASS", "Role assignments", "Key Vault assignments are readable (write permission is exercised during deploy)")
		}
	} else {
		r.add("WARN", "Role assignments", "Key Vault ID unavailable; role assignments were not checked")
	}
	if c.TeamsAppID == "" {
		r.add("WARN", "Teams app prerequisites", "skipped; provide app IDs and the password secret name for an existing-app deployment")
	} else {
		app, e := runner.Output(ctx, "az", "ad", "app", "show", "--id", c.TeamsAppID, "--query", "appId", "--output", "tsv", "--only-show-errors")
		if e != nil || strings.TrimSpace(string(app)) != c.TeamsAppID {
			r.add("FAIL", "Teams app registration", "the supplied Entra application could not be read")
		} else {
			r.add("PASS", "Teams app registration", "the supplied Entra application is readable")
		}
		secret, e := runner.Output(ctx, "az", "keyvault", "secret", "show", "--vault-name", c.KeyVaultName, "--name", c.TeamsAppPasswordSecretName, "--query", "id", "--output", "tsv", "--only-show-errors")
		if e != nil || strings.TrimSpace(string(secret)) == "" {
			r.add("FAIL", "Teams app password", "the configured password secret was not found in Key Vault")
		} else {
			r.add("PASS", "Teams app password", "the configured password secret exists in Key Vault")
		}
	}
	if c.RuntimeControlPlaneURL != "" {
		r.add("PASS", "Runtime control plane URL", "URL is absolute HTTPS with no query or fragment")
	}
	if c.InstallationID != "" {
		base, e := normalizedKeiWebURL(apiURL)
		if e != nil {
			r.add("FAIL", "Kei installation", "invalid --api-url")
		} else if token, e := store.Load(base); e != nil {
			r.add("FAIL", "Kei installation", "not logged in; run kei login before checking an installation")
		} else if status, e := getRuntimeInstallationStatus(ctx, client, base, token, c.InstallationID); e != nil {
			r.add("FAIL", "Kei installation", e.Error())
		} else if status.ID != c.InstallationID || !strings.EqualFold(status.Platform, "teams") {
			r.add("FAIL", "Kei installation", "installation is not a Teams installation")
		} else {
			r.add("PASS", "Kei installation", fmt.Sprintf("installation is %q (%s)", status.Status, status.BindingStatus))
		}
	}
	r.add("WARN", "Deployment quota and network", "Container Apps quota, egress policy, and Teams tenant consent require deployment-specific validation")
	return r
}
