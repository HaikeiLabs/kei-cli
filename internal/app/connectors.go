package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
	"github.com/google/uuid"
	"github.com/pkg/browser"
	"golang.org/x/term"
)

// offeredConnectorProviders are the providers Kei has built connectors for,
// in the setup schema's display order (HAI-222). s3 is not a connector, and
// http_api and notion are not offered yet. The console refuses the same set.
var offeredConnectorProviders = []contract.Provider{
	contract.ProviderGmail,
	contract.ProviderGoogle,
	contract.ProviderLinear,
	contract.ProviderGitHub,
	contract.ProviderTito,
	contract.ProviderCRM,
}

// openConnectorConsentURL opens a shared connector's OAuth consent page.
var openConnectorConsentURL = browser.OpenURL

type connectorPolicy struct {
	AllowedActions     []contract.Action `json:"allowed_actions"`
	AllowedResources   []string          `json:"allowed_resources"`
	DestructiveEnabled bool              `json:"destructive_enabled"`
}

// connectorCreateRequest is the body of POST /api/cli/connectors. It never
// carries a secret value. A secret the CLI delivers (HAI-262) has no
// credential_ref here: the catalog assigns the stored name.
type connectorCreateRequest struct {
	Name             string                    `json:"name"`
	Provider         contract.Provider         `json:"provider"`
	CredentialSource contract.CredentialSource `json:"credential_source"`
	CredentialRef    string                    `json:"credential_ref,omitempty"`
	AccountModel     contract.AccountModel     `json:"account_model,omitempty"`
	Scopes           []string                  `json:"scopes"`
	Resources        []string                  `json:"resources"`
	Policy           connectorPolicy           `json:"policy"`
	Capabilities     []contract.Capability     `json:"capabilities"`
	Config           map[string]any            `json:"config,omitempty"`
}

// contractMetadata is the connector the catalog would store for this request,
// so the CLI can run the catalog's own create check before sending.
func (c connectorCreateRequest) contractMetadata(id, orgID, workspaceID, createdBy string) contract.Metadata {
	return contract.Metadata{
		ID: id, TenantID: orgID, WorkspaceID: workspaceID, Name: c.Name, Provider: c.Provider,
		Status: contract.StatusPending, CredentialSource: c.CredentialSource, CredentialRef: c.CredentialRef,
		AccountModel: c.AccountModel, Scopes: c.Scopes, Resources: c.Resources, Capabilities: c.Capabilities,
		Policy:    contract.PolicyAttributes{AllowedActions: c.Policy.AllowedActions, AllowedResources: c.Policy.AllowedResources, DestructiveEnabled: c.Policy.DestructiveEnabled},
		CreatedBy: createdBy, Config: c.Config,
	}
}

// connector is the part of a catalog connector the CLI renders. Secret values
// are never part of it.
type connector struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Provider         string         `json:"provider"`
	Status           string         `json:"status"`
	CredentialSource string         `json:"credential_source"`
	AccountModel     string         `json:"account_model"`
	Config           map[string]any `json:"config"`
	Capabilities     []struct {
		Name string `json:"name"`
	} `json:"capabilities"`
}

func runConnectorsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "connectors requires a subcommand: create, list, get, reconnect, or delete")
		return 2
	}
	switch args[0] {
	case "create":
		return runConnectorsCreate(args[1:], stdout, stderr, stdin, client, store)
	case "list":
		return runConnectorsList(args[1:], stdout, stderr, client, store)
	case "get":
		return runConnectorsGet(args[1:], stdout, stderr, client, store)
	case "reconnect":
		return runConnectorsReconnect(args[1:], stdout, stderr, stdin, client, store)
	case "delete":
		return runConnectorsDelete(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown connectors command %q\n", args[0])
		return 2
	}
}

// parseConnectorFlags parses flags that may come before or after one optional
// positional connector ID.
func parseConnectorFlags(flags *flag.FlagSet, args []string) (string, error) {
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	rest := flags.Args()
	if id == "" && len(rest) > 0 {
		id, rest = rest[0], rest[1:]
		if err := flags.Parse(rest); err != nil {
			return "", err
		}
		rest = flags.Args()
	}
	if len(rest) != 0 {
		return "", fmt.Errorf("unexpected argument %q", rest[0])
	}
	return id, nil
}

func connectorWorkspaceFlag(flags *flag.FlagSet) *string {
	return flags.String("workspace", "", "workspace ID or name (or set KEI_WORKSPACE_ID)")
}

func requireConnectorWorkspace(command, workspace string, stderr io.Writer) (string, bool) {
	if workspace == "" {
		workspace = os.Getenv("KEI_WORKSPACE_ID")
	}
	if workspace == "" {
		fmt.Fprintf(stderr, "connectors %s requires --workspace WORKSPACE (or KEI_WORKSPACE_ID)\n", command)
		return "", false
	}
	return workspace, true
}

func requireConnectorID(command, id string, stderr io.Writer) bool {
	if id == "" {
		fmt.Fprintf(stderr, "connectors %s requires a connector ID\n", command)
		return false
	}
	if _, err := uuid.Parse(id); err != nil {
		fmt.Fprintln(stderr, "connector ID must be a UUID")
		return false
	}
	return true
}

// connectorSession is a logged-in CLI call scoped to one workspace.
type connectorSession struct {
	client      *http.Client
	baseURL     string
	token       string
	workspaceID string
}

func openConnectorSession(command, workspace string, stderr io.Writer, client *http.Client, store credentialStore) (*connectorSession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	workspaceID, err := resolveWorkspaceID(context.Background(), client, baseURL, token, workspace)
	if err != nil {
		fmt.Fprintf(stderr, "connectors %s: %v\n", command, err)
		return nil, false
	}
	return &connectorSession{client: client, baseURL: baseURL, token: token, workspaceID: workspaceID}, true
}

// do sends one request to /api/cli/connectors<path>.
func (s *connectorSession) do(method, path string, body any) ([]byte, error) {
	return s.doPath(method, "/api/v1/data-connectors"+path, body)
}

// doPath sends one workspace-scoped request and returns the response body, or
// an error carrying the console's message and HTTP status.
func (s *connectorSession) doPath(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := s.baseURL + path + "?" + url.Values{"workspace_id": {s.workspaceID}}.Encode()
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s (HTTP %d)", connectorErrorMessage(payload), resp.StatusCode)
	}
	return payload, nil
}

func connectorErrorMessage(payload []byte) string {
	var aip struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &aip) == nil && aip.Message != "" {
		if aip.Reason != "" {
			return aip.Reason + ": " + aip.Message
		}
		return aip.Message
	}
	var structured struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &structured) == nil && structured.Error.Message != "" {
		return structured.Error.Message
	}
	if message := strings.TrimSpace(string(payload)); message != "" {
		return message
	}
	return "request failed"
}

// connectorState is the connection state shown to an admin.
func connectorState(status string) string {
	switch status {
	case string(contract.StatusActive):
		return "connected"
	case string(contract.StatusFailed):
		return "needs reconnect"
	default:
		return status
	}
}

func runConnectorsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if id, err := parseConnectorFlags(flags, args); err != nil || id != "" {
		fmt.Fprintln(stderr, "connectors list accepts no positional arguments")
		return 2
	}
	ws, ok := requireConnectorWorkspace("list", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openConnectorSession("list", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, err := session.do(http.MethodGet, "", nil)
	if err != nil {
		fmt.Fprintf(stderr, "connectors list: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var connectors []connector
	if err := json.Unmarshal(payload, &connectors); err != nil {
		fmt.Fprintf(stderr, "connectors list: decode response: %v\n", err)
		return 1
	}
	if len(connectors) == 0 {
		fmt.Fprintln(stdout, "No connectors found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tNAME\tPROVIDER\tSTATE\tACCOUNT MODEL")
	for _, c := range connectors {
		model := c.AccountModel
		if model == "" {
			model = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Provider, connectorState(c.Status), model)
	}
	_ = table.Flush()
	return 0
}

func runConnectorsGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	id, err := parseConnectorFlags(flags, args)
	if err != nil {
		return 2
	}
	ws, ok := requireConnectorWorkspace("get", *workspace, stderr)
	if !ok || !requireConnectorID("get", id, stderr) {
		return 2
	}
	session, ok := openConnectorSession("get", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, err := session.do(http.MethodGet, "/"+url.PathEscape(id), nil)
	if err != nil {
		fmt.Fprintf(stderr, "connectors get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var c connector
	if err := json.Unmarshal(payload, &c); err != nil {
		fmt.Fprintf(stderr, "connectors get: decode response: %v\n", err)
		return 1
	}
	credential := c.CredentialSource
	if c.AccountModel != "" {
		credential += " (" + c.AccountModel + ")"
	}
	fmt.Fprintf(stdout, "ID:          %s\nName:        %s\nProvider:    %s\nState:       %s (%s)\nCredential:  %s\n", c.ID, c.Name, c.Provider, connectorState(c.Status), c.Status, credential)
	if len(c.Capabilities) > 0 {
		names := make([]string, len(c.Capabilities))
		for i, capability := range c.Capabilities {
			names[i] = capability.Name
		}
		fmt.Fprintf(stdout, "Capabilities: %s\n", strings.Join(names, ", "))
	}
	if c.CredentialSource == string(contract.CredentialSourceOpaqueRef) {
		if secrets, err := session.do(http.MethodGet, "/"+url.PathEscape(id)+"/secrets", nil); err == nil {
			var list struct {
				Secrets []secretStatus `json:"secrets"`
			}
			if json.Unmarshal(secrets, &list) == nil {
				for _, secret := range list.Secrets {
					fmt.Fprintf(stdout, "Secret:      %s\n", describeSecretStatus(secret))
				}
			}
		}
	}
	if len(c.Config) > 0 {
		fmt.Fprintln(stdout, "Config:")
		keys := make([]string, 0, len(c.Config))
		for key := range c.Config {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			fmt.Fprintf(stdout, "  %s: %v\n", key, c.Config[key])
		}
	}
	return 0
}

func runConnectorsReconnect(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors reconnect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	noBrowser := flags.Bool("no-browser", false, "print the consent URL without opening a browser")
	keepSecret := flags.Bool("keep-secret", false, "reactivate a shared-secret connector without replacing its secret")
	id, err := parseConnectorFlags(flags, args)
	if err != nil {
		return 2
	}
	ws, ok := requireConnectorWorkspace("reconnect", *workspace, stderr)
	if !ok || !requireConnectorID("reconnect", id, stderr) {
		return 2
	}
	session, ok := openConnectorSession("reconnect", ws, stderr, client, store)
	if !ok {
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors reconnect: %v\n", err)
		return 1
	}
	payload, err := session.do(http.MethodGet, "/"+url.PathEscape(id), nil)
	if err != nil {
		return fail(err)
	}
	var current connector
	if err := json.Unmarshal(payload, &current); err != nil {
		return fail(fmt.Errorf("decode connector: %w", err))
	}
	if current.CredentialSource == string(contract.CredentialSourceOAuth) && current.AccountModel == string(contract.AccountModelPerUser) {
		return fail(errors.New("users connect their own accounts through their chat harness"))
	}

	// A shared-secret connector reconnects with a replacement secret: a new
	// generation, sealed and delivered like the first (HAI-262).
	field, hasSecret := connectorSecretField(contract.Provider(current.Provider), contract.AccountModel(current.AccountModel))
	if current.CredentialSource == string(contract.CredentialSourceOpaqueRef) && hasSecret && !*keepSecret {
		secret, err := readSecretField(field, bufio.NewReader(stdin), stdin, stdout)
		if err != nil {
			return fail(err)
		}
		request, err := session.sealForWorkspace(field.Name, secret)
		if err != nil {
			return fail(err)
		}
		status, err := session.setSecret(id, request)
		if err != nil {
			return fail(fmt.Errorf("set secret %q: %w", field.Name, err))
		}
		fmt.Fprintf(stdout, "Secret %q sealed and queued for delivery (generation %d).\n", field.Name, status.Generation)
		if current.Status == string(contract.StatusPending) {
			fmt.Fprintf(stdout, "Connector %s becomes connected when the runtime stores the secret.\n", id)
			return 0
		}
	}

	payload, err = session.do(http.MethodPost, "/"+url.PathEscape(id)+":reconnect", nil)
	if err != nil {
		return fail(err)
	}
	var result struct {
		AuthURL string `json:"auth_url"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	if result.AuthURL != "" {
		fmt.Fprintf(stdout, "Connect the shared account for connector %s by approving access at:\n\n  %s\n\n", id, result.AuthURL)
		if !*noBrowser {
			_ = openConnectorConsentURL(result.AuthURL)
		}
		return 0
	}
	fmt.Fprintf(stdout, "Connector %s is %s (%s).\n", id, connectorState(result.Status), result.Status)
	return 0
}

func runConnectorsDelete(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	yes := flags.Bool("yes", false, "confirm deletion")
	id, err := parseConnectorFlags(flags, args)
	if err != nil {
		return 2
	}
	ws, ok := requireConnectorWorkspace("delete", *workspace, stderr)
	if !ok || !requireConnectorID("delete", id, stderr) {
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "connectors delete revokes the connector for every agent and user; pass --yes to confirm")
		return 2
	}
	session, ok := openConnectorSession("delete", ws, stderr, client, store)
	if !ok {
		return 1
	}
	if _, err := session.do(http.MethodDelete, "/"+url.PathEscape(id), nil); err != nil {
		fmt.Fprintf(stderr, "connectors delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Connector %s revoked; every invocation now denies.\n", id)
	return 0
}

// setupValues collects repeated --set name=value flags.
type setupValues map[string]string

func (s setupValues) String() string { return "" }

func (s setupValues) Set(value string) error {
	name, raw, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("--set takes name=value")
	}
	s[strings.TrimSpace(name)] = raw
	return nil
}

func runConnectorsCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	providerFlag := flags.String("provider", "", "provider: gmail, google_drive, linear, github, tito, or crm")
	name := flags.String("name", "", "connector name (letters, digits, and . _ : / -)")
	accountModelFlag := flags.String("account-model", "", "OAuth account model: per_user, shared, or domain_delegation (gmail, google_drive)")
	values := setupValues{}
	flags.Var(values, "set", "non-secret setup field as name=value (repeatable)")
	credentialRef := flags.String("credential-ref", "", "existing secret-manager reference for the provider's secret field")
	capabilitiesFlag := flags.String("capabilities", "", "comma-separated capabilities (default: the provider's read capabilities)")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if id, err := parseConnectorFlags(flags, args); err != nil || id != "" {
		fmt.Fprintln(stderr, "connectors create accepts no positional arguments")
		return 2
	}
	ws, ok := requireConnectorWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	provider := contract.Provider(*providerFlag)
	if !slices.Contains(offeredConnectorProviders, provider) {
		fmt.Fprintf(stderr, "connectors create: --provider must be one of %s\n", offeredProviderNames())
		return 2
	}
	if *name == "" {
		fmt.Fprintln(stderr, "connectors create requires --name")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}

	interactive := isTerminalReader(stdin)
	reader := bufio.NewReader(stdin)
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors create: %v\n", err)
		return 1
	}

	model, err := chooseAccountModel(provider, contract.AccountModel(*accountModelFlag), interactive, reader, stdout)
	if err != nil {
		return fail(err)
	}
	schema, _ := setup.SetupSchemaFor(provider)
	fields := schema.FieldsFor(model)
	config, err := collectSetupConfig(provider, fields, values, interactive, reader, stdout)
	if err != nil {
		return fail(err)
	}
	if err := setup.ValidateConfig(provider, model, config); err != nil {
		return fail(err)
	}

	// A secret is read (without echo on a terminal) and validated before any
	// request, then sealed to the workspace's runtime keys; plaintext never
	// leaves the CLI.
	secretField, hasSecret := connectorSecretField(provider, model)
	secret := ""
	if hasSecret && *credentialRef == "" {
		if secret, err = readSecretField(secretField, reader, stdin, stdout); err != nil {
			return fail(err)
		}
	}

	var capabilityNames []string
	if *capabilitiesFlag != "" {
		capabilityNames = splitList(*capabilitiesFlag)
	}
	request, err := buildConnectorCreateRequest(provider, model, *name, config, *credentialRef, capabilityNames)
	if err != nil {
		return fail(err)
	}

	workspaceID, err := resolveWorkspaceID(context.Background(), client, baseURL, token, ws)
	if err != nil {
		return fail(err)
	}
	session := &connectorSession{client: client, baseURL: baseURL, token: token, workspaceID: workspaceID}
	var sealed setSecretRequest
	if secret != "" {
		if sealed, err = session.sealForWorkspace(secretField.Name, secret); err != nil {
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
	var delivery secretStatus
	if secret != "" {
		if delivery, err = session.setSecret(created.ID, sealed); err != nil {
			return fail(fmt.Errorf("connector %s was created, but its secret %q was not set: %w. Retry with: kei connectors reconnect %s --workspace %s", created.ID, secretField.Name, err, created.ID, ws))
		}
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	fmt.Fprintf(stdout, "Created connector %s (%s, %s).\nConnector instance ID: %s\n", created.Name, created.Provider, connectorState(created.Status), created.ID)
	switch {
	case request.CredentialSource == contract.CredentialSourceOAuth && model == contract.AccountModelShared:
		fmt.Fprintf(stdout, "Next, connect the shared account: kei connectors reconnect %s --workspace %s\n", created.ID, ws)
	case request.CredentialSource == contract.CredentialSourceOAuth:
		fmt.Fprintln(stdout, "users connect their own accounts through their chat harness")
	case secret != "":
		fmt.Fprintf(stdout, "Secret %q sealed and queued for delivery (generation %d); the connector becomes connected when the runtime stores it.\n", secretField.Name, delivery.Generation)
	default:
		fmt.Fprintf(stdout, "Once the secret is stored at its reference, activate the connector: kei connectors reconnect %s --workspace %s --keep-secret\n", created.ID, ws)
	}
	return 0
}

func offeredProviderNames() string {
	names := make([]string, len(offeredConnectorProviders))
	for i, provider := range offeredConnectorProviders {
		names[i] = string(provider)
	}
	return strings.Join(names, ", ")
}

func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func chooseAccountModel(provider contract.Provider, requested contract.AccountModel, interactive bool, reader *bufio.Reader, stdout io.Writer) (contract.AccountModel, error) {
	allowed := contract.AccountModelsFor(provider)
	if len(allowed) == 0 {
		if requested != "" {
			return "", fmt.Errorf("provider %q has no account model; do not pass --account-model", provider)
		}
		return "", nil
	}
	if requested == "" {
		requested = allowed[0]
		if interactive {
			names := make([]string, len(allowed))
			for i, model := range allowed {
				names[i] = string(model)
			}
			requested = contract.AccountModel(promptLine(reader, stdout, "Account model ("+strings.Join(names, ", ")+")", string(allowed[0])))
		}
	}
	if !slices.Contains(allowed, requested) {
		return "", fmt.Errorf("account model %q is not allowed for provider %q", requested, provider)
	}
	return requested, nil
}

// collectSetupConfig builds the non-secret config from --set values, then
// prompts for what is missing: every field on a terminal, and otherwise only
// required fields without a default. Secret fields are never accepted here.
func collectSetupConfig(provider contract.Provider, fields []setup.SetupField, values setupValues, interactive bool, reader *bufio.Reader, stdout io.Writer) (map[string]any, error) {
	byName := map[string]setup.SetupField{}
	for _, field := range fields {
		byName[field.Name] = field
	}
	for name := range values {
		field, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("setup field %q is not defined for provider %q with this account model", name, provider)
		}
		if field.Secret {
			return nil, fmt.Errorf("secret field %q is never accepted as a flag; enter it at the prompt or on stdin, or pass --credential-ref", name)
		}
	}
	config := map[string]any{}
	for _, field := range fields {
		if field.Secret || field.Location != setup.SetupLocationConfig {
			continue
		}
		raw, set := values[field.Name]
		if !set && (interactive || (field.Required && field.Default == nil)) {
			raw = promptLine(reader, stdout, field.Label+" ("+field.Name+")", defaultString(field.Default))
			set = raw != ""
		}
		if !set {
			if field.Default != nil {
				config[field.Name] = field.Default
			}
			continue
		}
		value, err := parseSetupValue(field, raw)
		if err != nil {
			return nil, err
		}
		config[field.Name] = value
	}
	return config, nil
}

func defaultString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []string:
		return strings.Join(v, ",")
	default:
		return fmt.Sprint(v)
	}
}

func parseSetupValue(field setup.SetupField, raw string) (any, error) {
	switch field.Type {
	case setup.SetupFieldBool:
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("config field %q must be true or false", field.Name)
		}
		return value, nil
	case setup.SetupFieldStringList:
		return splitList(raw), nil
	default:
		return strings.TrimSpace(raw), nil
	}
}

func splitList(raw string) []string {
	var items []string
	for item := range strings.SplitSeq(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// validateSecretValue checks a secret against its setup field. Errors name the
// field and never include the value.
func validateSecretValue(field setup.SetupField, value string) error {
	invalid := fmt.Errorf("secret field %q is invalid", field.Name)
	if value == "" {
		return fmt.Errorf("secret field %q is required", field.Name)
	}
	if len(value) < field.MinLength || (field.MaxLength > 0 && len(value) > field.MaxLength) {
		return invalid
	}
	if field.Pattern != "" && !regexp.MustCompile(field.Pattern).MatchString(value) {
		return invalid
	}
	if field.Type == setup.SetupFieldServiceAccountJSON {
		var key struct {
			Type        string `json:"type"`
			ClientEmail string `json:"client_email"`
			PrivateKey  string `json:"private_key"`
		}
		if json.Unmarshal([]byte(value), &key) != nil || key.Type != "service_account" || key.ClientEmail == "" || key.PrivateKey == "" {
			return invalid
		}
	}
	return nil
}

// buildConnectorCreateRequest turns validated setup input into the create
// body and runs the catalog's own create check on it (contract.Metadata
// validation plus the setup schema), so an invalid connector never leaves the
// CLI.
func buildConnectorCreateRequest(provider contract.Provider, model contract.AccountModel, name string, config map[string]any, credentialRef string, capabilityNames []string) (connectorCreateRequest, error) {
	schema, ok := setup.SetupSchemaFor(provider)
	if !ok || !slices.Contains(offeredConnectorProviders, provider) {
		return connectorCreateRequest{}, fmt.Errorf("provider %q cannot be set up", provider)
	}
	source, err := credentialSourceFor(schema, model)
	if err != nil {
		return connectorCreateRequest{}, err
	}
	hasSecret := slices.ContainsFunc(schema.FieldsFor(model), func(f setup.SetupField) bool { return f.Secret })
	switch {
	case source == contract.CredentialSourceOAuth && credentialRef != "":
		return connectorCreateRequest{}, errors.New("--credential-ref applies only to a provider's secret field; OAuth connectors have none")
	case source == contract.CredentialSourceOAuth:
		// OAuth tokens are bridge-owned; the reference is unique per connector
		// and never names a secret-manager entry.
		credentialRef = "kei/oauth/" + string(provider) + "/" + uuid.NewString()
	case !hasSecret && credentialRef == "":
		return connectorCreateRequest{}, fmt.Errorf("provider %q needs --credential-ref", provider)
	}
	// An empty reference on a secret-backed connector means the CLI delivers
	// the secret and the catalog assigns the stored name.
	if credentialRef != "" {
		if err := contract.ValidateCredentialRef(credentialRef); err != nil {
			return connectorCreateRequest{}, err
		}
	}

	capabilities, err := connectorCapabilities(provider, config, capabilityNames)
	if err != nil {
		return connectorCreateRequest{}, err
	}
	scopes := make([]string, len(capabilities))
	var actions []contract.Action
	for i, capability := range capabilities {
		scopes[i] = capability.Name
		if !slices.Contains(actions, capability.Action) {
			actions = append(actions, capability.Action)
		}
	}
	if len(config) == 0 {
		config = nil
	}
	request := connectorCreateRequest{
		Name: name, Provider: provider, CredentialSource: source, CredentialRef: credentialRef,
		AccountModel: model, Scopes: scopes, Resources: []string{}, Capabilities: capabilities, Config: config,
		Policy: connectorPolicy{AllowedActions: actions, AllowedResources: []string{}},
	}
	m := request.contractMetadata(uuid.NewString(), "org", "workspace", "cli")
	if m.CredentialRef == "" {
		m.CredentialRef = "assigned-by-the-catalog"
	}
	if model == contract.AccountModelShared {
		m.Subject = contract.SharedSubject(m.ID)
	}
	if err := setup.ValidateMetadata(m); err != nil {
		return connectorCreateRequest{}, err
	}
	return request, nil
}

func credentialSourceFor(schema setup.SetupSchema, model contract.AccountModel) (contract.CredentialSource, error) {
	for _, auth := range schema.Auth {
		if (model == "" && len(auth.AccountModels) == 0) || slices.Contains(auth.AccountModels, model) {
			return auth.CredentialSource, nil
		}
	}
	return "", fmt.Errorf("account model %q is not allowed for provider %q", model, schema.Provider)
}

// connectorCapabilities returns the named capabilities, or by default the
// provider's read capabilities. A crm connector's defaults are limited to the
// resources its config allows, because the Worker rejects an assertion that
// declares a capability for a resource it does not serve.
func connectorCapabilities(provider contract.Provider, config map[string]any, names []string) ([]contract.Capability, error) {
	defined := contract.CapabilitiesFor(provider)
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
		if capability.Action != contract.ActionRead {
			continue
		}
		if provider == contract.ProviderCRM && !crmResourceAllowed(config, capability.Name) {
			continue
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities, nil
}

func crmResourceAllowed(config map[string]any, capability string) bool {
	family, _, _ := strings.Cut(capability, ".")
	resources, _ := config["allowed_resources"].([]string)
	for _, resource := range resources {
		if strings.TrimSuffix(resource, "/*") == family+"s" {
			return true
		}
	}
	return false
}

// readSecretField reads a secret without echo on a terminal, or as one line of
// stdin, and validates it against its setup field.
func readSecretField(field setup.SetupField, reader *bufio.Reader, stdin io.Reader, stdout io.Writer) (string, error) {
	secret, err := promptSecret(reader, stdin, stdout, field.Label+": ")
	if err != nil {
		return "", fmt.Errorf("read secret field %q: %w", field.Name, err)
	}
	if err := validateSecretValue(field, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func describeSecretStatus(s secretStatus) string {
	if s.Set && s.DeliveredAt != nil {
		return fmt.Sprintf("%s set (generation %d, delivered %s)", s.Field, s.Generation, *s.DeliveredAt)
	}
	if s.Generation > 0 {
		return fmt.Sprintf("%s pending delivery (generation %d)", s.Field, s.Generation)
	}
	return s.Field + " not set"
}
