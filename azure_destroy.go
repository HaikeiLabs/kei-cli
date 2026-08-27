package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// azureManagedResource is a tagged resource returned by Azure CLI. The
// destroy path uses resource IDs returned by Azure rather than rebuilding IDs
// from user input, then applies an additional allowlist before deletion.
type azureManagedResource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type azureDestroyPlan struct {
	InstallationID string
	DisplayName    string
	SubscriptionID string
	ResourceGroup  string
	Resources      []azureManagedResource
	Retained       []string
}

func runBotDestroyCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore, runner azureCommandRunner) int {
	if len(args) == 0 || args[0] != "azure" {
		fmt.Fprintln(stderr, "bot destroy currently supports only: kei bot destroy azure")
		return 2
	}
	flags := flag.NewFlagSet("bot destroy azure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "Kei installation ID")
	confirmation := flags.String("confirm-destroy", "", "non-interactive confirmation; must exactly equal --installation")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *installationID == "" {
		fmt.Fprintln(stderr, "bot destroy azure requires --installation ID")
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot destroy azure: --installation must be a UUID")
		return 2
	}
	if *confirmation != "" && *confirmation != *installationID {
		fmt.Fprintln(stderr, "bot destroy azure: --confirm-destroy must exactly match --installation")
		return 2
	}
	if err := destroyAzure(context.Background(), *apiURL, *installationID, *confirmation, stdout, stderr, stdin, client, store, runner); err != nil {
		fmt.Fprintf(stderr, "bot destroy azure failed: %v\n", err)
		return 1
	}
	return 0
}

func destroyAzure(ctx context.Context, apiURL, installationID, confirmation string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore, runner azureCommandRunner) error {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		return err
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		return errors.New("not logged in; run kei login first")
	}
	status, err := getRuntimeInstallationStatus(ctx, client, baseURL, cliToken, installationID)
	if err != nil {
		return err
	}
	subscriptionID, err := currentAzureSubscription(ctx, runner)
	if err != nil {
		return err
	}
	resources, err := listAzureInstallationResources(ctx, runner, status)
	if err != nil {
		return err
	}
	plan, err := newAzureDestroyPlan(status, subscriptionID, resources)
	if err != nil {
		return err
	}
	printAzureDestroyPlan(stdout, plan)
	if err := confirmAzureDestroy(stdin, stderr, confirmation, plan); err != nil {
		return err
	}

	if status.Status == "pending" || status.Status == "active" {
		if err := runtimeInstallationAction(ctx, client, baseURL, cliToken, installationID, "disable", nil); err != nil {
			return fmt.Errorf("disable installation before resource deletion: %w", err)
		}
		fmt.Fprintln(stdout, "Disabled the Kei runtime installation.")
	} else if status.Status != "disabled" && status.Status != "revoked" {
		return fmt.Errorf("installation is %q and cannot be destroyed", status.Status)
	}

	for _, resource := range plan.Resources {
		fmt.Fprintf(stdout, "Deleting %s (%s)...\n", resource.Name, resource.Type)
		if err := runner.Run(ctx, "az", "resource", "delete", "--ids", resource.ID, "--only-show-errors"); err != nil {
			return fmt.Errorf("delete %s: %w", resource.ID, err)
		}
	}
	if len(plan.Resources) == 0 {
		fmt.Fprintln(stdout, "No matching Kei-owned Azure resources were found; the installation remains disabled.")
		return nil
	}
	fmt.Fprintln(stdout, "Azure runtime resources deleted. The Kei installation remains disabled for audit and recovery.")
	return nil
}

func currentAzureSubscription(ctx context.Context, runner azureCommandRunner) (string, error) {
	output, err := runner.Output(ctx, "az", "account", "show", "--query", "id", "--output", "tsv", "--only-show-errors")
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return "", errors.New("could not determine the active Azure subscription; run az login and select the intended subscription")
	}
	return strings.TrimSpace(string(output)), nil
}

func listAzureInstallationResources(ctx context.Context, runner azureCommandRunner, status *runtimeInstallationStatus) ([]azureManagedResource, error) {
	resourceGroup, ok := runtimeDeploymentString(status, "resource_group")
	if !ok {
		return nil, errors.New("installation has no Azure resource group metadata")
	}
	output, err := runner.Output(ctx, "az", "resource", "list", "--resource-group", resourceGroup, "--tag", "keiInstallationId="+status.ID, "--query", "[].{id:id,type:type,name:name}", "--output", "json", "--only-show-errors")
	if err != nil {
		return nil, errors.New("could not list Azure resources tagged for this installation")
	}
	var resources []azureManagedResource
	if err := json.Unmarshal(output, &resources); err != nil {
		return nil, errors.New("Azure returned invalid tagged-resource data")
	}
	return resources, nil
}

func newAzureDestroyPlan(status *runtimeInstallationStatus, subscriptionID string, resources []azureManagedResource) (azureDestroyPlan, error) {
	if status == nil || status.ID == "" {
		return azureDestroyPlan{}, errors.New("installation status is incomplete")
	}
	if provider, ok := runtimeDeploymentString(status, "provider"); !ok || provider != "azure" {
		return azureDestroyPlan{}, errors.New("installation is not an Azure runtime")
	}
	resourceGroup, ok := runtimeDeploymentString(status, "resource_group")
	if !ok {
		return azureDestroyPlan{}, errors.New("installation has no Azure resource group metadata")
	}
	installation, err := uuid.Parse(status.ID)
	if err != nil {
		return azureDestroyPlan{}, errors.New("installation status has an invalid ID")
	}
	baseName := "kei-" + strings.ReplaceAll(installation.String(), "-", "")[:12]
	allowed := map[string]string{
		"Microsoft.App/containerApps":                      baseName,
		"Microsoft.BotService/botServices":                 baseName,
		"Microsoft.ManagedIdentity/userAssignedIdentities": baseName + "-identity",
		"Microsoft.OperationalInsights/workspaces":         baseName + "-logs",
	}
	plan := azureDestroyPlan{
		InstallationID: status.ID,
		DisplayName:    status.DisplayName,
		SubscriptionID: subscriptionID,
		ResourceGroup:  resourceGroup,
		Retained: []string{
			"customer Key Vault and all secrets",
			"Container Apps environment (it may be shared)",
			"resources with custom names or without this installation tag",
		},
	}
	for _, resource := range resources {
		if expectedName, ok := allowed[resource.Type]; ok && resource.Name == expectedName && resource.ID != "" {
			plan.Resources = append(plan.Resources, resource)
		}
	}
	sort.Slice(plan.Resources, func(i, j int) bool {
		return azureDestroyResourceOrder(plan.Resources[i].Type) < azureDestroyResourceOrder(plan.Resources[j].Type)
	})
	return plan, nil
}

func runtimeDeploymentString(status *runtimeInstallationStatus, key string) (string, bool) {
	if status == nil || status.Deployment == nil {
		return "", false
	}
	value, ok := status.Deployment[key].(string)
	return value, ok && value != ""
}

func azureDestroyResourceOrder(resourceType string) int {
	switch resourceType {
	case "Microsoft.App/containerApps":
		return 0
	case "Microsoft.BotService/botServices":
		return 1
	case "Microsoft.ManagedIdentity/userAssignedIdentities":
		return 2
	case "Microsoft.OperationalInsights/workspaces":
		return 3
	default:
		return 4
	}
}

func printAzureDestroyPlan(stdout io.Writer, plan azureDestroyPlan) {
	displayName := plan.DisplayName
	if displayName == "" {
		displayName = plan.InstallationID
	}
	fmt.Fprintf(stdout, "\nAzure destroy plan for Kei installation %q (%s)\n", displayName, plan.InstallationID)
	fmt.Fprintf(stdout, "Subscription: %s\nResource group: %s\n", plan.SubscriptionID, plan.ResourceGroup)
	fmt.Fprintln(stdout, "Resources to delete:")
	if len(plan.Resources) == 0 {
		fmt.Fprintln(stdout, "  (none found that are safe to delete automatically)")
	}
	for _, resource := range plan.Resources {
		fmt.Fprintf(stdout, "  - %s [%s]\n", resource.ID, resource.Type)
	}
	fmt.Fprintln(stdout, "Resources retained:")
	for _, retained := range plan.Retained {
		fmt.Fprintf(stdout, "  - %s\n", retained)
	}
}

func confirmAzureDestroy(stdin io.Reader, stderr io.Writer, confirmation string, plan azureDestroyPlan) error {
	if confirmation != "" {
		if confirmation != plan.InstallationID {
			return errors.New("--confirm-destroy must exactly match the installation ID")
		}
		return nil
	}
	if stdin == nil {
		return errors.New("interactive confirmation is required; pass --confirm-destroy with the installation ID for non-interactive use")
	}
	displayName := plan.DisplayName
	if displayName == "" {
		displayName = plan.InstallationID
	}
	fmt.Fprintf(stderr, "\nTo permanently delete the listed Azure resources, type %q or the installation ID: ", displayName)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return errors.New("could not read destroy confirmation")
	}
	answer := strings.TrimSpace(line)
	if answer != displayName && answer != plan.InstallationID {
		return errors.New("destroy confirmation did not match the installation name or ID")
	}
	return nil
}
