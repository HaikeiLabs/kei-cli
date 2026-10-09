package app

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// controlPlaneInstallations adapts the existing runtime-installations list
// and get calls to harness.InstallationSource.
type controlPlaneInstallations struct {
	client              *http.Client
	baseURL, token, org string
}

func newControlPlaneInstallations(client *http.Client, baseURL, token string) (controlPlaneInstallations, error) {
	orgID, err := organizationIDFromCLIToken(token)
	return controlPlaneInstallations{client: client, baseURL: baseURL, token: token, org: orgID}, err
}

func (c controlPlaneInstallations) ListInstallations(ctx context.Context) ([]harness.RuntimeInstallation, error) {
	items, _, err := listBotInstallations(ctx, c.client, c.baseURL, c.token, c.org, true)
	if err != nil {
		return nil, err
	}
	out := make([]harness.RuntimeInstallation, len(items))
	for i, item := range items {
		out[i] = harness.RuntimeInstallation{ID: item.ID, Platform: item.Platform, DisplayName: item.DisplayName, Status: item.Status, LastHeartbeatAt: item.LastHeartbeatAt}
	}
	return out, nil
}

func (c controlPlaneInstallations) InstallationHealth(ctx context.Context, id string) (harness.RuntimeHealth, error) {
	status, err := getRuntimeInstallationStatus(ctx, c.client, c.baseURL, c.token, c.org, id)
	if err != nil {
		return harness.RuntimeHealth{}, err
	}
	health := harness.RuntimeHealth{RuntimeVersion: status.RuntimeVersion, LastHeartbeatAt: status.LastHeartbeatAt}
	if bundle := status.PolicyBundle; bundle != nil {
		health.PolicyRevision = bundle.PolicyRevision
		if bundle.State != "" {
			health.BundleState = &bundle.State
		}
	}
	return health, nil
}

// listOpenWebUIInstallations lists the organization's Open WebUI runtime
// installations with their health.
func listOpenWebUIInstallations(ctx context.Context, client *http.Client, baseURL, token string) ([]harness.OpenWebUIInstallation, error) {
	src, err := newControlPlaneInstallations(client, baseURL, token)
	if err != nil {
		return nil, err
	}
	return harness.ListOpenWebUIInstallations(ctx, src)
}

// checkOpenWebUI is `kei harness sync --harness openwebui --url BASE`: a
// read-only report of the functions kei-openwebui seeds. It exits 0 in sync,
// 1 on drift and 2 on error. The admin token is sent only to Open WebUI.
func checkOpenWebUI(ctx context.Context, baseURL, token string, stdout, stderr io.Writer, client *http.Client) int {
	report, err := harness.OpenWebUI{BaseURL: baseURL}.CheckFunctions(ctx, client, token)
	if err != nil {
		fmt.Fprintf(stderr, "harness sync: openwebui: %v\n", err)
		return 2
	}
	report.WriteText(stdout)
	if report.Drift() {
		return 1
	}
	return 0
}
