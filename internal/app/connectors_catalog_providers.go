package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
)

// Providers the CLI has no contract entry for (postgres, mysql, mssql:
// HAI-493) are set up from the catalog's connector-providers listing. The
// catalog is the source of truth for which providers exist and what their
// setup fields are; this CLI never lists them itself.
const connectorProvidersPath = "/api/v1/connector-providers"

// catalogProviderSummary is one entry of GET /api/v1/connector-providers.
type catalogProviderSummary struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	AccountModels []string `json:"account_models"`
}

// catalogProviderDetail is GET /api/v1/connector-providers/{provider}.
type catalogProviderDetail struct {
	ID           string                `json:"id"`
	Capabilities []contract.Capability `json:"capabilities"`
	SetupSchema  *setup.SetupSchema    `json:"setup_schema"`
}

// connectorNamePattern mirrors the contract's identifier rule for a
// connector name, which the contract does not export.
var connectorNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)

// catalogConnectorCreate is a connectors create for a catalog-listed provider.
type catalogConnectorCreate struct {
	provider      contract.Provider
	name          string
	accountModel  contract.AccountModel
	values        setupValues
	credentialRef string
	secretFile    string
	capabilities  []string
	httpAPI       httpAPIFlags
	ws            workspaceSelection
	access        connectorCreateAccess
	yes           bool
	jsonOutput    bool
}

// sharedSecretOnly reports whether the catalog provider is one this CLI can
// set up without contract knowledge: one shared account over opaque_ref and
// exactly one secret credential field (a database provider's DSN).
func (d catalogProviderDetail) sharedSecretOnly() (setup.SetupField, bool) {
	s := d.SetupSchema
	if s == nil || len(s.Auth) != 1 || len(s.Fields) != 1 {
		return setup.SetupField{}, false
	}
	auth, field := s.Auth[0], s.Fields[0]
	if auth.CredentialSource != contract.CredentialSourceOpaqueRef || !slices.Equal(auth.AccountModels, []contract.AccountModel{contract.AccountModelShared}) {
		return setup.SetupField{}, false
	}
	if !field.Secret || field.Location != setup.SetupLocationCredential {
		return setup.SetupField{}, false
	}
	return field, true
}

func runConnectorsCreateFromCatalog(in catalogConnectorCreate, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors create: %v\n", err)
		return 1
	}
	if in.name == "" {
		fmt.Fprintln(stderr, "connectors create requires --name")
		return 2
	}
	if !in.httpAPI.check(in.provider, stderr) {
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	session, err := newConnectorSession(client, baseURL, token, in.ws)
	if err != nil {
		return fail(err)
	}

	listed, err := session.catalogProviders()
	if err != nil {
		return fail(fmt.Errorf("read connector providers from the catalog: %w", err))
	}
	if !slices.ContainsFunc(listed, func(p catalogProviderSummary) bool { return p.ID == string(in.provider) }) {
		fmt.Fprintf(stderr, "connectors create: --provider must be one of %s\n", catalogProviderNames(listed))
		return 2
	}
	detail, err := session.catalogProvider(in.provider)
	if err != nil {
		return fail(fmt.Errorf("read connector provider %q from the catalog: %w", in.provider, err))
	}
	secretField, ok := detail.sharedSecretOnly()
	if !ok {
		return fail(fmt.Errorf("provider %q is listed by the catalog, but this kei cannot set it up; use the console or upgrade kei", in.provider))
	}
	if in.accountModel != "" && in.accountModel != contract.AccountModelShared {
		fmt.Fprintf(stderr, "connectors create: provider %q supports only the shared account model (one read-only connection for the workspace); --account-model %s is not available\n", in.provider, in.accountModel)
		return 2
	}
	if !connectorNamePattern.MatchString(in.name) {
		return fail(fmt.Errorf("connector name %q must use letters, digits, and . _ : / -", in.name))
	}
	// The provider's only field is its secret: any --set is rejected, a secret
	// one without echoing the value.
	if _, err := collectSetupConfig(in.provider, detail.SetupSchema.Fields, in.values, false, nil, stdout); err != nil {
		return fail(err)
	}
	capabilities, err := catalogCapabilities(in.provider, detail.Capabilities, in.capabilities)
	if err != nil {
		return fail(err)
	}

	secret := ""
	switch {
	case in.credentialRef != "" && in.secretFile != "":
		return fail(errors.New("--secret-file applies only to a provider's secret field, without --credential-ref"))
	case in.credentialRef != "":
		if err := contract.ValidateCredentialRef(in.credentialRef); err != nil {
			return fail(err)
		}
	default:
		if secret, err = readConnectorSecret(secretField, in.secretFile, bufio.NewReader(stdin), stdin, stdout); err != nil {
			return fail(err)
		}
	}

	scopes := make([]string, len(capabilities))
	var actions []contract.Action
	for i, capability := range capabilities {
		scopes[i] = capability.Name
		if !slices.Contains(actions, capability.Action) {
			actions = append(actions, capability.Action)
		}
	}
	request := connectorCreateRequest{
		Name: in.name, Provider: in.provider, CredentialSource: contract.CredentialSourceOpaqueRef, CredentialRef: in.credentialRef,
		AccountModel: contract.AccountModelShared, Scopes: scopes, Resources: []string{}, Capabilities: capabilities,
		Policy: connectorPolicy{AllowedActions: actions, AllowedResources: []string{}},
	}
	return completeConnectorCreate(session, connectorCreatePlan{
		name: in.name, request: request, model: contract.AccountModelShared, secretField: secretField, secret: secret,
		access: in.access, ws: in.ws, yes: in.yes, jsonOutput: in.jsonOutput,
	}, stdout, stderr, stdin)
}

// catalogProviderNames is the --provider choices: the providers this CLI sets
// up from its contract, then the catalog-listed providers set up from the
// catalog (one shared account).
func catalogProviderNames(listed []catalogProviderSummary) string {
	names := []string{offeredProviderNames()}
	for _, p := range listed {
		if slices.Contains(offeredConnectorProviders, contract.Provider(p.ID)) || !slices.Equal(p.AccountModels, []string{string(contract.AccountModelShared)}) {
			continue
		}
		names = append(names, p.ID)
	}
	return strings.Join(names, ", ")
}

// catalogCapabilities returns the named capabilities, or by default the
// provider's read capabilities, from the catalog's provider entry.
func catalogCapabilities(provider contract.Provider, defined []contract.Capability, names []string) ([]contract.Capability, error) {
	if len(names) > 0 {
		capabilities := make([]contract.Capability, 0, len(names))
		for _, name := range names {
			i := slices.IndexFunc(defined, func(c contract.Capability) bool { return c.Name == name })
			if i < 0 {
				return nil, fmt.Errorf("capability %q is not defined for provider %q", name, provider)
			}
			capabilities = append(capabilities, defined[i])
		}
		return capabilities, nil
	}
	var capabilities []contract.Capability
	for _, capability := range defined {
		if capability.Action == contract.ActionRead {
			capabilities = append(capabilities, capability)
		}
	}
	if len(capabilities) == 0 {
		return nil, fmt.Errorf("the catalog lists no read capability for provider %q; pass --capabilities", provider)
	}
	return capabilities, nil
}

func (s *connectorSession) catalogProviders() ([]catalogProviderSummary, error) {
	payload, err := s.doPath(http.MethodGet, connectorProvidersPath, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		ConnectorProviders []catalogProviderSummary `json:"connector_providers"`
	}
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return list.ConnectorProviders, nil
}

func (s *connectorSession) catalogProvider(provider contract.Provider) (catalogProviderDetail, error) {
	payload, err := s.doPath(http.MethodGet, connectorProvidersPath+"/"+url.PathEscape(string(provider)), nil)
	if err != nil {
		return catalogProviderDetail{}, err
	}
	var got struct {
		ConnectorProvider catalogProviderDetail `json:"connector_provider"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		return catalogProviderDetail{}, fmt.Errorf("decode response: %w", err)
	}
	return got.ConnectorProvider, nil
}

// catalogSecretField is the one secret field of a provider the CLI knows only
// from the catalog (a database provider's DSN, Snowflake's key_pair).
func (s *connectorSession) catalogSecretField(provider contract.Provider) (setup.SetupField, error) {
	detail, err := s.catalogProvider(provider)
	if err != nil {
		return setup.SetupField{}, fmt.Errorf("read connector provider %q from the catalog: %w", provider, err)
	}
	field, ok := detail.sharedSecretOnly()
	if !ok {
		return setup.SetupField{}, fmt.Errorf("provider %q is listed by the catalog, but this kei cannot set its secret; use the console or upgrade kei", provider)
	}
	return field, nil
}

// readConnectorSecret reads a secret field from --secret-file when one is
// given, and otherwise from the no-echo prompt or one line of stdin. Errors
// name the field or the file, never the value.
func readConnectorSecret(field setup.SetupField, path string, reader *bufio.Reader, stdin io.Reader, stdout io.Writer) (string, error) {
	if path == "" {
		return readSecretField(field, reader, stdin, stdout)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret field %q from --secret-file: %w", field.Name, errors.Unwrap(err))
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if err := validateSecretValue(field, secret); err != nil {
		return "", err
	}
	return secret, nil
}

// newConnectorSession resolves the CLI token's organization and the selected
// workspace into a connector session.
func newConnectorSession(client *http.Client, baseURL, token string, sel workspaceSelection) (*connectorSession, error) {
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		return nil, fmt.Errorf("%w; run kei login again", err)
	}
	var workspaceID string
	if sel.pickDefault {
		workspaceID, err = resolveWorkspaceSelection(context.Background(), client, baseURL, token, sel)
	} else {
		workspaceID, err = resolveScopedWorkspace(context.Background(), client, baseURL, token, sel)
	}
	if err != nil {
		return nil, err
	}
	return &connectorSession{client: client, baseURL: baseURL, token: token, orgID: orgID, workspaceID: workspaceID}, nil
}

// connectorCreateAccess is who a new connector is allowed to.
type connectorCreateAccess struct {
	allowGroups     []string
	defaultAccess   bool
	noDefaultAccess bool
}

// connectorCreatePlan is a validated connector create, ready to confirm and send.
type connectorCreatePlan struct {
	name        string
	request     connectorCreateRequest
	model       contract.AccountModel
	secretField setup.SetupField
	secret      string
	access      connectorCreateAccess
	ws          workspaceSelection
	yes         bool
	jsonOutput  bool
}

// completeConnectorCreate confirms, creates the connector, allows its groups,
// delivers its sealed secret, and reports what to do next. The secret's
// plaintext never leaves the CLI.
func completeConnectorCreate(session *connectorSession, plan connectorCreatePlan, stdout, stderr io.Writer, stdin io.Reader) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors create: %v\n", err)
		return 1
	}
	request, secret, secretField, ws := plan.request, plan.secret, plan.secretField, plan.ws
	allowGroups, defaultAccess := plan.access.allowGroups, plan.access.defaultAccess
	_, err := resolveConnectorGroupIDs(session, allowGroups)
	if err != nil {
		if defaultAccess && strings.Contains(err.Error(), `group "admins" was not found`) {
			return fail(fmt.Errorf("workspace group %q was not found; pass --allow-group NAME or --no-default-access", "admins"))
		}
		return fail(err)
	}
	orgName, err := getOrganizationName(context.Background(), session.client, session.baseURL, session.token, session.orgID)
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not fetch organization name: %v\n", err)
		orgName = session.orgID
	}
	workspaceName := workspaceNameByID(context.Background(), session.client, session.baseURL, session.token, session.orgID, session.workspaceID)
	fmt.Fprintf(stdout, "Organization: %s\nWorkspace: %s\n", orgName, workspaceName)
	confirmText := "Create connector " + plan.name + "? This may grant the selected groups access."
	if defaultAccess {
		confirmText = "Create connector " + plan.name + "? This will allow the admins group to use it."
	}
	if !confirmWrite(stdin, stderr, plan.yes, confirmText) {
		return 2
	}
	var sealed setSecretRequest
	switch {
	case secret != "":
		if sealed, err = session.sealForWorkspace(secretField.Name, secret); err != nil {
			return fail(fmt.Errorf("%w. Nothing was created", err))
		}
	case plan.request.CredentialRef != "":
		// An external secret needs the store (it names the secret
		// manager) but no credential-sync runtime (owner decision,
		// HAI-1023 Q3).
		if err := session.preflightCredentialStore(false); err != nil {
			return fail(fmt.Errorf("%w. Nothing was created", err))
		}
	}
	payload, err := session.do(http.MethodPost, "", request)
	if err != nil {
		return fail(err)
	}
	var created connector
	if err := json.Unmarshal(payload, &created); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	capabilityNames := make([]string, 0, len(request.Capabilities))
	for _, capability := range request.Capabilities {
		capabilityNames = append(capabilityNames, capability.Name)
	}
	policyNames, err := createConnectorGroupAllows(session, allowGroups, created.ID, plan.name, string(request.Provider), capabilityNames)
	if err != nil {
		return fail(fmt.Errorf("connector created, but group allow policy failed: %w", err))
	}
	for attempt := 0; len(allowGroups) > 0 && attempt < 3; attempt++ {
		latestBody, pollErr := session.do(http.MethodGet, "/"+url.PathEscape(created.ID), nil)
		if pollErr != nil {
			break
		}
		var latest connector
		if json.Unmarshal(latestBody, &latest) != nil {
			break
		}
		created.Status = latest.Status
		if latest.Status == string(contract.StatusActive) {
			break
		}
		if attempt < 2 {
			connectPollSleep(250 * time.Millisecond)
		}
	}
	var delivery secretStatus
	if secret != "" {
		if delivery, err = session.setSecret(created.ID, sealed); err != nil {
			return fail(fmt.Errorf("connector %s was created, but its secret %q was not set: %w. Retry with: kei connectors reconnect %s --workspace %s", created.ID, secretField.Name, err, created.ID, ws.value))
		}
	}
	if plan.jsonOutput {
		_, _ = stdout.Write(payload)
		if plan.access.noDefaultAccess {
			fmt.Fprintln(stderr, "Warning: nobody can connect until access is granted.")
		}
		return 0
	}
	fmt.Fprintf(stdout, "Created connector %s (%s, %s).\nConnector instance ID: %s\n", created.Name, created.Provider, connectorState(created.Status), created.ID)
	for _, policy := range policyNames {
		fmt.Fprintf(stdout, "Allowed group %s to use %s (policy %s).\n", policy.GroupName, plan.name, policy.Name)
	}
	if plan.access.noDefaultAccess {
		fmt.Fprintln(stdout, "Warning: nobody can connect until access is granted.")
	}
	switch {
	case request.CredentialSource == contract.CredentialSourceOAuth && plan.model == contract.AccountModelShared:
		fmt.Fprintf(stdout, "Next, connect the shared account: kei connectors reconnect %s --workspace %s\n", created.ID, ws.value)
	case request.CredentialSource == contract.CredentialSourceOAuth:
		fmt.Fprintf(stdout, "Next, connect your account: kei connectors connect %s --workspace %s\n", created.ID, ws.value)
	case secret != "":
		fmt.Fprintf(stdout, "Secret %q sealed and queued for delivery (generation %d); the connector becomes connected when the runtime stores it.\n", secretField.Name, delivery.Generation)
	default:
		fmt.Fprintf(stdout, "Once the secret is stored at its reference, activate the connector: kei connectors reconnect %s --workspace %s --keep-secret\n", created.ID, ws.value)
	}
	return 0
}
