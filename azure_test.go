package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type recordingAzureSecretStore struct {
	exists      bool
	existsCalls int
	setCalls    int
	vaultURL    string
	secretName  string
	secretValue string
	err         error
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (s *recordingAzureSecretStore) Exists(_ context.Context, vaultURL, name string) (bool, error) {
	s.existsCalls++
	s.vaultURL = vaultURL
	s.secretName = name
	return s.exists, s.err
}

func (s *recordingAzureSecretStore) Set(_ context.Context, vaultURL, name, value string) error {
	s.setCalls++
	s.vaultURL = vaultURL
	s.secretName = name
	s.secretValue = value
	return s.err
}

type recordingAzureCommandRunner struct {
	name              string
	args              []string
	parametersContent string
	templateContent   string
	keyVaultRBAC      string
	err               error
}

func (r *recordingAzureCommandRunner) Run(_ context.Context, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	for index, arg := range args {
		if arg == "--template-file" && index+1 < len(args) {
			contents, err := os.ReadFile(args[index+1])
			if err != nil {
				return err
			}
			r.templateContent = string(contents)
		}
		if arg == "--parameters" && index+1 < len(args) {
			contents, err := os.ReadFile(strings.TrimPrefix(args[index+1], "@"))
			if err != nil {
				return err
			}
			r.parametersContent = string(contents)
		}
	}
	return r.err
}

func (r *recordingAzureCommandRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "az" || len(args) < 3 || args[0] != "keyvault" || args[1] != "show" {
		return nil, errors.New("unexpected Azure preflight command")
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.keyVaultRBAC == "" {
		r.keyVaultRBAC = "true"
	}
	return []byte(r.keyVaultRBAC), nil
}

func testAzureDeploymentConfig(t *testing.T) azureDeploymentConfig {
	t.Helper()
	config, err := newAzureDeploymentConfig(azureDeploymentConfig{
		InstallationID:             "12345678-1234-1234-1234-123456789012",
		ResourceGroup:              "customer-kei",
		Location:                   "eastus",
		KeyVaultName:               "customerkeivault",
		TeamsAppPasswordSecretName: "teams-app-password",
		TeamsAppID:                 "22345678-1234-1234-1234-123456789012",
		TeamsTenantID:              "32345678-1234-1234-1234-123456789012",
		RuntimeControlPlaneURL:     "https://runtime-api.example.test",
		RuntimeImage:               "ghcr.io/haikeilabs/kei-bot-runtime:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestDeployAzureWritesRuntimeCredentialOnlyToKeyVault(t *testing.T) {
	const runtimeToken = "kh_live_never_appear_in_output"
	config := testAzureDeploymentConfig(t)
	store := &memoryCredentialStore{token: "cli-access-token"}
	var deploymentBody string
	bound := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cli-access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/credential":
			json.NewEncoder(w).Encode(runtimeCredentialResponse{RuntimeToken: runtimeToken})
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/deployment":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			deploymentBody = string(body)
			w.WriteHeader(http.StatusNoContent)
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012":
			_, _ = w.Write([]byte(`{"id":"12345678-1234-1234-1234-123456789012","status":"pending","binding_status":"unverified","last_heartbeat_at":"2026-08-26T00:00:00Z"}`))
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/bind":
			bound = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	store.server = server.URL
	vault := &recordingAzureSecretStore{}
	runner := &recordingAzureCommandRunner{}
	var output bytes.Buffer

	if err := deployAzure(context.Background(), server.URL, config, &output, server.Client(), store, vault, runner); err != nil {
		t.Fatal(err)
	}
	if vault.setCalls != 1 || vault.secretValue != runtimeToken {
		t.Fatalf("runtime token was not delivered directly to Key Vault: %#v", vault)
	}
	if vault.vaultURL != "https://customerkeivault.vault.azure.net" || vault.secretName != config.RuntimeSecretName {
		t.Fatalf("unexpected Key Vault target: %#v", vault)
	}
	if runner.name != "az" {
		t.Fatalf("deployment runner command = %q, want az", runner.name)
	}
	for _, value := range append(append([]string{}, runner.args...), output.String(), runner.parametersContent, runner.templateContent) {
		if strings.Contains(value, runtimeToken) {
			t.Fatalf("runtime token leaked outside Key Vault: %q", value)
		}
	}
	if strings.Contains(deploymentBody, runtimeToken) || !strings.Contains(deploymentBody, `"runtime_secret_name"`) {
		t.Fatalf("unsafe or incomplete deployment metadata: %s", deploymentBody)
	}
	if !bound {
		t.Fatal("installation was not bound after its heartbeat")
	}
	if !strings.Contains(runner.templateContent, "keyVaultUrl") || !strings.Contains(runner.templateContent, "KEI_RUNTIME_TOKEN") {
		t.Fatal("Bicep template does not configure the Key Vault runtime-token reference")
	}
}

func TestDeployAzureReusesExistingSecretWithoutRequestingCredential(t *testing.T) {
	config := testAzureDeploymentConfig(t)
	vault := &recordingAzureSecretStore{exists: true}
	runner := &recordingAzureCommandRunner{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/credential":
			t.Fatal("credential endpoint must not be called")
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/deployment", "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012/bind":
			w.WriteHeader(http.StatusNoContent)
		case "/api/cli/runtime-installations/12345678-1234-1234-1234-123456789012":
			_, _ = w.Write([]byte(`{"id":"12345678-1234-1234-1234-123456789012","status":"pending","binding_status":"unverified","last_heartbeat_at":"2026-08-26T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	store := &memoryCredentialStore{server: server.URL, token: "cli-access-token"}
	var output bytes.Buffer

	if err := deployAzure(context.Background(), store.server, config, &output, server.Client(), store, vault, runner); err != nil {
		t.Fatal(err)
	}
	if vault.setCalls != 0 {
		t.Fatalf("existing secret was overwritten: %#v", vault)
	}
	if runner.name != "az" || !strings.Contains(output.String(), "Using the existing") {
		t.Fatalf("existing-secret deployment did not proceed: command=%q output=%q", runner.name, output.String())
	}
}

func TestAzureDeploymentConfigRejectsInsecureControlPlaneURL(t *testing.T) {
	config := azureDeploymentConfig{
		InstallationID:             "12345678-1234-1234-1234-123456789012",
		ResourceGroup:              "customer-kei",
		Location:                   "eastus",
		KeyVaultName:               "customerkeivault",
		TeamsAppPasswordSecretName: "teams-app-password",
		TeamsAppID:                 "22345678-1234-1234-1234-123456789012",
		TeamsTenantID:              "32345678-1234-1234-1234-123456789012",
		RuntimeControlPlaneURL:     "http://runtime-api.example.test",
		RuntimeImage:               "ghcr.io/haikeilabs/kei-bot-runtime:v1",
	}
	if _, err := newAzureDeploymentConfig(config); err == nil {
		t.Fatal("expected insecure runtime control plane URL to be rejected")
	}
}

func TestDeployAzureRejectsVaultWithoutRBACBeforeCredentialRequest(t *testing.T) {
	config := testAzureDeploymentConfig(t)
	store := &memoryCredentialStore{server: "https://kei.example.test", token: "cli-access-token"}
	vault := &recordingAzureSecretStore{}
	runner := &recordingAzureCommandRunner{keyVaultRBAC: "false"}
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("credential endpoint must not be called")
	})}
	if err := deployAzure(context.Background(), store.server, config, io.Discard, client, store, vault, runner); err == nil || !strings.Contains(err.Error(), "RBAC") {
		t.Fatalf("expected Key Vault RBAC preflight error, got %v", err)
	}
	if vault.existsCalls != 0 || vault.setCalls != 0 {
		t.Fatalf("vault credential operations ran before RBAC preflight: %#v", vault)
	}
}
