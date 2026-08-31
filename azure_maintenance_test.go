package main

import (
	"strings"
	"testing"
	"time"
)

func TestAzureDoctorConfigValidation(t *testing.T) {
	if _, err := newAzureDoctorConfig(azureDoctorConfig{ResourceGroup: "rg", Location: "eastus", KeyVaultName: "vault", TeamsAppID: "bad"}); err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("partial Teams app inputs error = %v", err)
	}
	if _, err := newAzureDoctorConfig(azureDoctorConfig{ResourceGroup: "rg", Location: "eastus", KeyVaultName: "vault", RuntimeControlPlaneURL: "http://insecure.example"}); err == nil {
		t.Fatal("expected insecure control-plane URL to be rejected")
	}
}

func TestAzureUpgradeConfigUsesRecordedDeployment(t *testing.T) {
	status := &runtimeInstallationStatus{ID: "12345678-1234-1234-1234-123456789012", Platform: "teams", Status: "active", Deployment: map[string]any{
		"provider": "azure", "resource_group": "rg", "location": "eastus", "key_vault_name": "vaultname", "runtime_secret_name": "runtime-secret",
		"teams_app_password_secret_name": "teams-password", "teams_app_id": "22345678-1234-1234-1234-123456789012", "teams_tenant_id": "32345678-1234-1234-1234-123456789012",
		"runtime_control_plane_url": "https://runtime.example", "container_app_name": "kei-app", "container_apps_environment_name": "kei-app-env", "managed_identity_name": "kei-app-identity", "bot_resource_name": "kei-app",
	}}
	config, err := newAzureUpgradeConfig(status, "ghcr.io/example/runtime:v2", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeImage != "ghcr.io/example/runtime:v2" || config.RuntimeSecretName != "runtime-secret" || config.ContainerAppName != "kei-app" {
		t.Fatalf("upgrade config did not preserve metadata: %#v", config)
	}
}
