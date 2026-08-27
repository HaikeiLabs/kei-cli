package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingAzureDestroyRunner struct {
	resources []azureManagedResource
	runCalls  [][]string
}

func (r *recordingAzureDestroyRunner) Run(_ context.Context, name string, args ...string) error {
	if name != "az" {
		return errors.New("unexpected command")
	}
	r.runCalls = append(r.runCalls, append([]string(nil), args...))
	return nil
}

func (r *recordingAzureDestroyRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "az" || len(args) < 2 {
		return nil, errors.New("unexpected Azure command")
	}
	switch args[0] + " " + args[1] {
	case "account show":
		return []byte("subscription-123\n"), nil
	case "resource list":
		return json.Marshal(r.resources)
	default:
		return nil, errors.New("unexpected Azure query")
	}
}

func TestDestroyAzureDisablesInstallationAndDeletesOnlySafeTaggedResources(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	baseName := "kei-123456781234"
	disableCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /api/cli/runtime-installations/" + installationID:
			_, _ = io.WriteString(w, `{"id":"12345678-1234-1234-1234-123456789012","display_name":"production-teams","status":"active","binding_status":"verified","deployment":{"provider":"azure","resource_group":"customer-kei"}}`)
		case http.MethodPost + " /api/cli/runtime-installations/" + installationID + "/disable":
			disableCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected control-plane request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	runner := &recordingAzureDestroyRunner{resources: []azureManagedResource{
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.App/containerApps/" + baseName, Type: "Microsoft.App/containerApps", Name: baseName},
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.BotService/botServices/" + baseName, Type: "Microsoft.BotService/botServices", Name: baseName},
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + baseName + "-identity", Type: "Microsoft.ManagedIdentity/userAssignedIdentities", Name: baseName + "-identity"},
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.OperationalInsights/workspaces/" + baseName + "-logs", Type: "Microsoft.OperationalInsights/workspaces", Name: baseName + "-logs"},
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.App/managedEnvironments/" + baseName + "-env", Type: "Microsoft.App/managedEnvironments", Name: baseName + "-env"},
		{ID: "/subscriptions/sub/resourceGroups/customer-kei/providers/Microsoft.App/containerApps/customer-app", Type: "Microsoft.App/containerApps", Name: "customer-app"},
	}}
	store := &memoryCredentialStore{server: server.URL, token: "cli-access-token"}
	var stdout, stderr bytes.Buffer
	if err := destroyAzure(context.Background(), server.URL, installationID, "", &stdout, &stderr, strings.NewReader("production-teams\n"), server.Client(), store, runner); err != nil {
		t.Fatal(err)
	}
	if !disableCalled {
		t.Fatal("installation was not disabled before deletion")
	}
	if len(runner.runCalls) != 4 {
		t.Fatalf("delete commands = %#v, want four safe resources", runner.runCalls)
	}
	for _, call := range runner.runCalls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "managedEnvironments") || strings.Contains(joined, "customer-app") || !strings.Contains(joined, "resource delete") {
			t.Fatalf("unsafe delete command: %q", joined)
		}
	}
	if !strings.Contains(stdout.String(), "subscription-123") || !strings.Contains(stdout.String(), "customer Key Vault") || !strings.Contains(stdout.String(), "Container Apps environment") {
		t.Fatalf("destroy plan did not disclose scope and retained resources: %s", stdout.String())
	}
}

func TestDestroyAzureRequiresExactConfirmationBeforeDisabling(t *testing.T) {
	installationID := "12345678-1234-1234-1234-123456789012"
	disableCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /api/cli/runtime-installations/" + installationID:
			_, _ = io.WriteString(w, `{"id":"12345678-1234-1234-1234-123456789012","display_name":"production-teams","status":"active","deployment":{"provider":"azure","resource_group":"customer-kei"}}`)
		case http.MethodPost + " /api/cli/runtime-installations/" + installationID + "/disable":
			disableCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected control-plane request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	runner := &recordingAzureDestroyRunner{}
	store := &memoryCredentialStore{server: server.URL, token: "cli-access-token"}
	err := destroyAzure(context.Background(), server.URL, installationID, "", io.Discard, io.Discard, strings.NewReader("wrong-name\n"), server.Client(), store, runner)
	if err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	if disableCalled || len(runner.runCalls) != 0 {
		t.Fatalf("destroy ran before confirmation: disabled=%t commands=%#v", disableCalled, runner.runCalls)
	}
}

func TestDestroyCommandRejectsGenericNonInteractiveConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runBotDestroyCommand([]string{"azure", "--installation", "12345678-1234-1234-1234-123456789012", "--confirm-destroy", "yes"}, &stdout, &stderr, strings.NewReader(""), &http.Client{}, &memoryCredentialStore{}, &recordingAzureDestroyRunner{})
	if code != 2 || !strings.Contains(stderr.String(), "exactly match") {
		t.Fatalf("unexpected confirmation validation: exit=%d stderr=%q", code, stderr.String())
	}
}
