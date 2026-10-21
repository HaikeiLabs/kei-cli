package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
	"github.com/google/uuid"
	"github.com/pkg/browser"
	"golang.org/x/term"
)

// offeredConnectorProviders are the providers Kei has built connectors for,
// in the setup schema's display order (HAI-222). s3 is not a connector, and
// notion is not offered yet. http_api is the generic connector a named API
// such as crm is registered as (HAI-384). skills_repo is the workspace's agent
// skill source (HAI-431). The console refuses the same set.
var offeredConnectorProviders = []contract.Provider{
	contract.ProviderGmail,
	contract.ProviderGoogle,
	contract.ProviderLinear,
	contract.ProviderGitHub,
	contract.ProviderTito,
	contract.ProviderSkillsRepo,
	contract.ProviderCRM,
	contract.ProviderHTTPAPI,
}

// openConnectorConsentURL opens a shared connector's OAuth consent page.
var openConnectorConsentURL = browser.OpenURL

// connectPollSleep is the sleep between connection-status polls, overridden in tests.
var connectPollSleep = time.Sleep

// connectPollInterval is the delay between connection-status polls, overridden
// in tests.
var connectPollInterval = 2 * time.Second

// connectPollMaxDuration is the total time to wait for a user to complete the
// OAuth flow in the browser, overridden in tests.
var connectPollMaxDuration = 10 * time.Minute

type connectorPolicy struct {
	AllowedActions     []contract.Action `json:"allowed_actions"`
	AllowedResources   []string          `json:"allowed_resources"`
	DestructiveEnabled bool              `json:"destructive_enabled"`
}

// connectorCreateRequest is the body of POST /api/v1/data-connectors. It never
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
	HTTPAPI          *contract.HTTPAPI         `json:"http_api,omitempty"`
}

// contractMetadata is the connector the catalog would store for this request,
// so the CLI can run the catalog's own create check before sending.
func (c connectorCreateRequest) contractMetadata(id, orgID, workspaceID, createdBy string) contract.Metadata {
	return contract.Metadata{
		ID: id, TenantID: orgID, WorkspaceID: workspaceID, Name: c.Name, Provider: c.Provider,
		Status: contract.StatusPending, CredentialSource: c.CredentialSource, CredentialRef: c.CredentialRef,
		AccountModel: c.AccountModel, Scopes: c.Scopes, Resources: c.Resources, Capabilities: c.Capabilities,
		Policy:    contract.PolicyAttributes{AllowedActions: c.Policy.AllowedActions, AllowedResources: c.Policy.AllowedResources},
		CreatedBy: createdBy, Config: c.Config, HTTPAPI: c.HTTPAPI,
	}
}

// connector is the part of a catalog connector the CLI renders. Secret values
// are never part of it.
type connector struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	DisplayName      string            `json:"display_name"`
	Slug             string            `json:"slug"`
	Kind             string            `json:"kind"`
	MCPURL           string            `json:"mcp_url"`
	ExpectedIssuer   string            `json:"expected_issuer"`
	Provider         string            `json:"provider"`
	Status           string            `json:"status"`
	CredentialSource string            `json:"credential_source"`
	CredentialRef    string            `json:"credential_ref"`
	AccountModel     string            `json:"account_model"`
	Config           map[string]any    `json:"config"`
	HTTPAPI          *contract.HTTPAPI `json:"http_api"`
	Capabilities     []struct {
		Name string `json:"name"`
	} `json:"capabilities"`
}

var connectorsGroup = commandGroup{
	name: "connectors",
	commands: []groupCommand{
		{name: "create", summary: "Create a data connector"},
		{name: "test", summary: "Test a connector"},
		{name: "list", summary: "List connectors"},
		{name: "get", summary: "Get a connector by ID"},
		{name: "connect", summary: "Connect a connector (OAuth)"},
		{name: "reconnect", summary: "Reconnect a connector"},
		{name: "delete", summary: "Delete a connector"},
		{name: "manifests", summary: "Manage connector tool manifests"},
		{name: "templates", summary: "List connector templates"},
	},
	aliases: map[string]string{"tools": "manifests"},
}

func runConnectorsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := connectorsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "create":
		return runConnectorsCreate(rest, stdout, stderr, stdin, client, store)
	case "test":
		return runConnectorsTest(rest, stdout, stderr, client, store)
	case "list":
		return runConnectorsList(rest, stdout, stderr, client, store)
	case "get":
		return runConnectorsGet(rest, stdout, stderr, client, store)
	case "connect":
		return runConnectorsConnect(rest, stdout, stderr, client, store)
	case "reconnect":
		return runConnectorsReconnect(rest, stdout, stderr, stdin, client, store)
	case "delete":
		return runConnectorsDelete(rest, stdout, stderr, client, store)
	case "manifests", "tools":
		return runConnectorsManifests(rest, stdout, stderr, stdin, client, store)
	case "templates":
		return runConnectorsTemplates(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
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
	return flags.String("workspace", "", "workspace name or ID (or set KEI_WORKSPACE)")
}

func requireConnectorWorkspace(command, workspace string, stderr io.Writer) (workspaceSelection, bool) {
	sel, err := selectWorkspace(workspace, keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "connectors %s: %v\n", command, err)
		return workspaceSelection{}, false
	}
	if sel.fromIDEnv {
		fmt.Fprintln(stderr, deprecationNote)
	}
	return sel, true
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
	orgID       string
	workspaceID string
}

func openConnectorSession(command string, sel workspaceSelection, stderr io.Writer, client *http.Client, store credentialStore) (*connectorSession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "connectors %s: %v; run kei login again\n", command, err)
		return nil, false
	}
	var workspaceID string
	if sel.pickDefault {
		workspaceID, err = resolveWorkspaceSelection(context.Background(), client, baseURL, token, sel)
	} else {
		workspaceID, err = resolveScopedWorkspace(context.Background(), client, baseURL, token, sel)
	}
	if err != nil {
		fmt.Fprintf(stderr, "connectors %s: %v\n", command, err)
		return nil, false
	}
	return &connectorSession{client: client, baseURL: baseURL, token: token, orgID: orgID, workspaceID: workspaceID}, true
}

// do sends one request to /api/v1/data-connectors<path>.
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
	if err := checkHTMLFallthrough(resp, payload); err != nil {
		return nil, withCLIUpgradeHint(s.client, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		statusErr := &connectorStatusError{StatusCode: resp.StatusCode, message: connectorErrorMessage(payload)}
		return nil, withCLIUpgradeHint(s.client, statusErr)
	}
	return payload, nil
}

// connectorStatusError is a non-2xx answer from the console: the catalog's
// message and the HTTP status.
type connectorStatusError struct {
	StatusCode int
	message    string
}

func (e *connectorStatusError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.message, e.StatusCode)
}

func connectorErrorMessage(payload []byte) string {
	return apiErrorMessage(payload)
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
	withManifests := flags.Bool("manifests", false, "also show each mcp connector's tool-manifest status (one request per mcp connector)")
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
	header := "ID\tNAME\tSLUG\tKIND\tPROVIDER\tSTATE\tACCOUNT MODEL\tMCP URL"
	if *withManifests {
		header += "\tMANIFEST"
	}
	fmt.Fprintln(table, header)
	for _, c := range connectors {
		label := c.DisplayName
		if label == "" {
			label = c.Name
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s", c.ID, label, orDash(c.Slug), orDash(c.Kind), c.Provider, connectorState(c.Status), orDash(c.AccountModel), orDash(c.MCPURL))
		if *withManifests {
			status := "-"
			if c.Kind == connectorKindMCP {
				status = "unavailable"
				if _, manifests, err := session.toolManifests(c.ID); err == nil {
					status = manifestStatusColumn(manifests)
				}
			}
			row += "\t" + status
		}
		fmt.Fprintln(table, row)
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
	fmt.Fprintf(stdout, "ID:          %s\nName:        %s\n", c.ID, c.Name)
	if c.DisplayName != "" {
		fmt.Fprintf(stdout, "Label:       %s\n", c.DisplayName)
	}
	if c.Slug != "" {
		fmt.Fprintf(stdout, "Slug:        %s\n", c.Slug)
	}
	if c.Kind != "" {
		fmt.Fprintf(stdout, "Kind:        %s\n", c.Kind)
	}
	fmt.Fprintf(stdout, "Provider:    %s\nState:       %s (%s)\nCredential:  %s\n", c.Provider, connectorState(c.Status), c.Status, credential)
	if c.MCPURL != "" {
		fmt.Fprintf(stdout, "MCP URL:     %s\n", c.MCPURL)
	}
	if c.ExpectedIssuer != "" {
		fmt.Fprintf(stdout, "Issuer:      %s\n", c.ExpectedIssuer)
	}
	if c.Kind == connectorKindMCP {
		status := "unavailable"
		if _, manifests, err := session.toolManifests(c.ID); err == nil {
			status = manifestStatus(manifests)
		}
		fmt.Fprintf(stdout, "Manifest:    %s\n", status)
	}
	if len(c.Capabilities) > 0 {
		names := make([]string, len(c.Capabilities))
		for i, capability := range c.Capabilities {
			names[i] = capability.Name
		}
		fmt.Fprintf(stdout, "Capabilities: %s\n", strings.Join(names, ", "))
	}
	if c.CredentialSource == string(contract.CredentialSourceOpaqueRef) && c.CredentialRef != "" {
		fmt.Fprintf(stdout, "Secret ref:  %s\n", c.CredentialRef)
	}
	if c.HTTPAPI != nil {
		printHTTPAPI(stdout, c.HTTPAPI)
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

func runConnectorsConnect(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	noBrowser := flags.Bool("no-browser", false, "print the auth URL without opening a browser")
	id, err := parseConnectorFlags(flags, args)
	if err != nil {
		return 2
	}
	ws, ok := requireConnectorWorkspace("connect", *workspace, stderr)
	if !ok || !requireConnectorID("connect", id, stderr) {
		return 2
	}
	session, ok := openConnectorSession("connect", ws, stderr, client, store)
	if !ok {
		return 1
	}
	fail := func(err error) int {
		var status *connectorStatusError
		if errors.As(err, &status) && status.StatusCode == http.StatusForbidden {
			err = fmt.Errorf("%w; ask an admin to allow your group, e.g. kei policies create --workspace %s --src-pattern group:<group> --dst-pattern connector:%s --effect permit", err, ws.value, id)
		}
		fmt.Fprintf(stderr, "connectors connect: %v\n", err)
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
	capabilities := make([]string, 0, len(current.Capabilities))
	for _, capability := range current.Capabilities {
		capabilities = append(capabilities, capability.Name)
	}
	var connectBody any
	if len(capabilities) > 0 {
		connectBody = struct {
			Capabilities []string `json:"capabilities"`
		}{Capabilities: capabilities}
	}
	payload, err = session.do(http.MethodPost, "/"+url.PathEscape(id)+":connect", connectBody)
	if err != nil {
		return fail(err)
	}
	var startResult struct {
		AuthURL  string `json:"auth_url"`
		Provider string `json:"provider"`
		Status   string `json:"status"`
		Scope    string `json:"scope"`
	}
	if err := json.Unmarshal(payload, &startResult); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}

	if startResult.AuthURL == "" && startResult.Status == "connected" {
		fmt.Fprintf(stdout, "Connector %s already has the required OAuth scopes", id)
		if startResult.Scope != "" {
			fmt.Fprintf(stdout, ": %s", startResult.Scope)
		}
		fmt.Fprintln(stdout, ".")
		return 0
	}
	if startResult.AuthURL == "" {
		return fail(errors.New("connect response did not include an auth URL or connected status"))
	}
	fmt.Fprintf(stdout, "Connect your account for connector %s by approving access at:\n\n  %s\n\n", id, startResult.AuthURL)
	if !*noBrowser {
		_ = openConnectorConsentURL(startResult.AuthURL)
	}

	deadline := time.Now().Add(connectPollMaxDuration)
	pollPath := "/" + url.PathEscape(id) + ":connect"
	for {
		payload, err := session.do(http.MethodGet, pollPath, nil)
		if err != nil {
			return fail(err)
		}
		var pollResult struct {
			Status       string `json:"status"`
			Scope        string `json:"scope,omitempty"`
			AccountEmail string `json:"account_email,omitempty"`
		}
		if err := json.Unmarshal(payload, &pollResult); err != nil {
			return fail(fmt.Errorf("decode poll response: %w", err))
		}

		if pollResult.Status == "connected" {
			parts := []string{fmt.Sprintf("Status: %s", pollResult.Status)}
			if pollResult.Scope != "" {
				parts = append(parts, fmt.Sprintf("Scopes: %s", pollResult.Scope))
			}
			if pollResult.AccountEmail != "" {
				parts = append(parts, fmt.Sprintf("Account: %s", pollResult.AccountEmail))
			}
			fmt.Fprintln(stdout, strings.Join(parts, " | "))
			return 0
		}

		if time.Now().After(deadline) {
			return fail(errors.New("timed out waiting for the user to authorize the connection"))
		}
		_, _ = fmt.Fprintf(stdout, "Status: %s\n", pollResult.Status)
		connectPollSleep(connectPollInterval)
	}
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
		return fail(fmt.Errorf("this is a per-user connector; connect your own account with: kei connectors connect %s --workspace %s", id, ws.value))
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

func runConnectorsTest(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors test", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	id, err := parseConnectorFlags(flags, args)
	if err != nil || id == "" {
		fmt.Fprintln(stderr, "connectors test requires a connector name or ID")
		return 2
	}
	ws, ok := requireConnectorWorkspace("test", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openConnectorSession("test", ws, stderr, client, store)
	if !ok {
		return 1
	}
	resolved, err := resolveConnectorID(session, id)
	if err != nil {
		fmt.Fprintf(stderr, "connectors test: %v\n", err)
		return 1
	}
	connectorBody, err := session.do(http.MethodGet, "/"+url.PathEscape(resolved), nil)
	if err != nil {
		fmt.Fprintln(stdout, "Decision: deny\nReason: connector lookup unavailable")
		return 0
	}
	var target connector
	if err := json.Unmarshal(connectorBody, &target); err != nil || len(target.Capabilities) == 0 {
		fmt.Fprintln(stdout, "Decision: no_match\nReason: connector has no testable capability")
		return 0
	}
	principal, err := userPrincipalFromCLIToken(session.token)
	if err != nil {
		fmt.Fprintln(stdout, "Decision: deny\nReason: calling user is unavailable in the login token")
		return 0
	}
	orgID, err := organizationIDFromCLIToken(session.token)
	if err != nil {
		fmt.Fprintln(stdout, "Decision: deny\nReason: organization is unavailable in the login token")
		return 0
	}
	testRequest := map[string]string{"as": principal, "target": "connector:" + target.Name}
	api := &apiSession{client: session.client, baseURL: session.baseURL, token: session.token, orgID: orgID}
	var result struct {
		Decision  string `json:"decision"`
		DecidedBy string `json:"decided_by"`
	}
	_, body, err := api.do(http.MethodPost, "/api/v1/organizations/"+url.PathEscape(orgID)+"/workspaces/"+url.PathEscape(session.workspaceID)+"/policies:test", nil, testRequest)
	if err != nil {
		fmt.Fprintln(stdout, "Decision: deny\nReason: smoke test unavailable")
		return 0
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(stdout, "Smoke test unavailable: deny (invalid test response)\n")
		return 0
	}
	reason := "no matching policy"
	if result.DecidedBy != "" {
		reason = "decided by policy " + result.DecidedBy
	}
	fmt.Fprintf(stdout, "Capability: %s\nDecision: %s\nReason: %s\n", target.Capabilities[0].Name, result.Decision, reason)
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

type repeatedStrings []string

func (s *repeatedStrings) String() string { return strings.Join(*s, ",") }
func (s *repeatedStrings) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func runConnectorsCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	providerFlag := flags.String("provider", "", "provider: gmail, google_drive, linear, github, tito, skills_repo, crm, or http_api (or --template for a catalog template such as an MCP server)")
	name := flags.String("name", "", "connector name (letters, digits, and . _ : / -)")
	accountModelFlag := flags.String("account-model", "", "OAuth account model: per_user, shared, or domain_delegation (gmail, google_drive); skills_repo takes per_user, or --credential-ref instead; cannot be combined with --template")
	values := setupValues{}
	flags.Var(values, "set", "non-secret setup field as name=value (repeatable)")
	credentialRef := flags.String("credential-ref", "", "existing secret-manager reference for the provider's secret field")
	capabilitiesFlag := flags.String("capabilities", "", "comma-separated capabilities (default: the provider's read capabilities)")
	var allowGroups repeatedStrings
	flags.Var(&allowGroups, "allow-group", "group name to allow (repeatable; defaults to admins)")
	noDefaultAccess := flags.Bool("no-default-access", false, "skip the default admins allow policy")
	yes := flags.Bool("yes", false, "confirm without prompting")
	httpAPI := registerHTTPAPIFlags(flags)
	fromTemplate := registerTemplateCreateFlags(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if id, err := parseConnectorFlags(flags, args); err != nil || id != "" {
		fmt.Fprintln(stderr, "connectors create accepts no positional arguments")
		return 2
	}
	ws, ok := requireConnectorWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	if !checkCreateFlagPaths(flags, stderr) {
		return 2
	}
	if *noDefaultAccess && len(allowGroups) > 0 {
		fmt.Fprintln(stderr, "connectors create: --allow-group cannot be combined with --no-default-access")
		return 2
	}
	defaultAccess := len(allowGroups) == 0 && !*noDefaultAccess
	if defaultAccess {
		allowGroups = []string{"admins"}
	}
	if *fromTemplate.template != "" {
		return runConnectorsCreateFromTemplate(fromTemplate, *name, ws, splitList(*capabilitiesFlag), allowGroups, defaultAccess, *noDefaultAccess, *yes, stdin, *jsonOutput, stdout, stderr, client, store)
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
	if !httpAPI.check(provider, stderr) {
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

	model, err := chooseAccountModel(provider, contract.AccountModel(*accountModelFlag), *credentialRef != "", interactive, reader, stdout)
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
	var registration *contract.HTTPAPI
	if provider == contract.ProviderHTTPAPI {
		if registration, err = httpAPI.registration(); err != nil {
			return fail(err)
		}
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
	request, err := buildConnectorCreateRequest(provider, model, *name, config, registration, *credentialRef, capabilityNames)
	if err != nil {
		return fail(err)
	}

	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		return fail(fmt.Errorf("%w; run kei login again", err))
	}
	var workspaceID string
	if ws.pickDefault {
		workspaceID, err = resolveWorkspaceSelection(context.Background(), client, baseURL, token, ws)
	} else {
		workspaceID, err = resolveScopedWorkspace(context.Background(), client, baseURL, token, ws)
	}
	if err != nil {
		return fail(err)
	}
	session := &connectorSession{client: client, baseURL: baseURL, token: token, orgID: orgID, workspaceID: workspaceID}
	_, err = resolveConnectorGroupIDs(session, allowGroups)
	if err != nil {
		if defaultAccess && strings.Contains(err.Error(), `group "admins" was not found`) {
			return fail(fmt.Errorf("workspace group %q was not found; pass --allow-group NAME or --no-default-access", "admins"))
		}
		return fail(err)
	}
	orgName, err := getOrganizationName(context.Background(), client, baseURL, token, orgID)
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not fetch organization name: %v\n", err)
		orgName = orgID
	}
	workspaceName := workspaceNameByID(context.Background(), client, baseURL, token, orgID, workspaceID)
	fmt.Fprintf(stdout, "Organization: %s\nWorkspace: %s\n", orgName, workspaceName)
	confirmText := "Create connector " + *name + "? This may grant the selected groups access."
	if defaultAccess {
		confirmText = "Create connector " + *name + "? This will allow the admins group to use it."
	}
	if !confirmWrite(stdin, stderr, *yes, confirmText) {
		return 2
	}
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
	capabilityNames = make([]string, 0, len(request.Capabilities))
	for _, capability := range request.Capabilities {
		capabilityNames = append(capabilityNames, capability.Name)
	}
	policyNames, err := createConnectorGroupAllows(session, allowGroups, created.ID, *name, string(request.Provider), capabilityNames)
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
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		if *noDefaultAccess {
			fmt.Fprintln(stderr, "Warning: nobody can connect until access is granted.")
		}
		return 0
	}
	fmt.Fprintf(stdout, "Created connector %s (%s, %s).\nConnector instance ID: %s\n", created.Name, created.Provider, connectorState(created.Status), created.ID)
	for _, policy := range policyNames {
		fmt.Fprintf(stdout, "Allowed group %s to use %s (policy %s).\n", policy.GroupName, *name, policy.Name)
	}
	if *noDefaultAccess {
		fmt.Fprintln(stdout, "Warning: nobody can connect until access is granted.")
	}
	switch {
	case request.CredentialSource == contract.CredentialSourceOAuth && model == contract.AccountModelShared:
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

func workspaceNameByID(ctx context.Context, client *http.Client, baseURL, token, orgID, workspaceID string) string {
	workspaces, err := listWorkspaces(ctx, client, baseURL, token)
	if err == nil {
		for _, ws := range workspaces {
			if ws.ID == workspaceID {
				return ws.Name
			}
		}
	}
	return workspaceID
}

func resolveConnectorID(s *connectorSession, name string) (string, error) {
	if _, err := uuid.Parse(name); err == nil {
		return name, nil
	}
	body, err := s.do(http.MethodGet, "", nil)
	if err != nil {
		return "", err
	}
	var list []connector
	if err := json.Unmarshal(body, &list); err != nil {
		return "", err
	}
	for _, item := range list {
		if item.Name == name {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("connector %q not found", name)
}

func userPrincipalFromCLIToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid login token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid login token")
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || strings.TrimSpace(claims.Subject) == "" {
		return "", errors.New("user claim missing")
	}
	return "user:" + claims.Subject, nil
}

func resolveConnectorGroupIDs(connectorSession *connectorSession, groupNames []string) (map[string]string, error) {
	resolved := make(map[string]string, len(groupNames))
	if len(groupNames) == 0 {
		return resolved, nil
	}
	api := &apiSession{client: connectorSession.client, baseURL: connectorSession.baseURL, token: connectorSession.token, orgID: connectorSession.orgID}
	_, body, err := api.do(http.MethodGet, "/api/v1/groups", groupsQuery(api, connectorSession.workspaceID), nil)
	if err != nil {
		return nil, err
	}
	var groups []catalogGroup
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, err
	}
	for _, group := range groups {
		for _, name := range groupNames {
			if group.Name == name {
				resolved[name] = group.ID
			}
		}
	}
	for _, name := range groupNames {
		if resolved[name] == "" {
			return nil, fmt.Errorf("group %q was not found in this workspace", name)
		}
	}
	return resolved, nil
}

type connectorAllowPolicy struct {
	GroupName string
	Name      string
}

func createConnectorGroupAllows(connectorSession *connectorSession, groupNames []string, connectorID, connectorName, source string, capabilityNames []string) ([]connectorAllowPolicy, error) {
	policies := &policySession{client: connectorSession.client, baseURL: connectorSession.baseURL, token: connectorSession.token, workspaceID: connectorSession.workspaceID}
	created := make([]connectorAllowPolicy, 0, len(groupNames)*max(1, len(capabilityNames)))
	for _, groupName := range groupNames {
		targets := capabilityNames
		if len(targets) == 0 {
			targets = []string{""}
		}
		for _, capability := range targets {
			policyName := "Allow " + groupName + " " + connectorName
			dstPattern := "connector:" + connectorID
			selector := &createPolicySelectorV2{ConnectorID: connectorID}
			if capability != "" {
				policyName += " " + capability
				dstPattern = "capability:" + capability
				selector.Source = source
				selector.Capability = capability
			}
			request := createPolicyRequest{Name: policyName, SrcPattern: "group:" + groupName, DstPattern: dstPattern, Effect: "permit", Action: "permit", Priority: 100, Enabled: true, SelectorV2: selector}
			if _, err := policies.do(http.MethodPost, "", request, nil); err != nil {
				return nil, err
			}
			created = append(created, connectorAllowPolicy{GroupName: groupName, Name: policyName})
		}
	}
	return created, nil
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

// chooseAccountModel picks the account model, or none. A provider that offers
// both OAuth and a shared-secret token without an account model (skills_repo)
// takes the shared-secret path when --credential-ref is given and no account
// model is requested.
func chooseAccountModel(provider contract.Provider, requested contract.AccountModel, byCredentialRef, interactive bool, reader *bufio.Reader, stdout io.Writer) (contract.AccountModel, error) {
	allowed := contract.AccountModelsFor(provider)
	if len(allowed) == 0 {
		if requested != "" {
			return "", fmt.Errorf("provider %q has no account model; do not pass --account-model", provider)
		}
		return "", nil
	}
	if requested == "" && byCredentialRef && offersSharedSecretWithoutAccountModel(provider) {
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

func offersSharedSecretWithoutAccountModel(provider contract.Provider) bool {
	schema, ok := setup.SetupSchemaFor(provider)
	return ok && slices.ContainsFunc(schema.Auth, func(auth setup.SetupAuth) bool {
		return auth.CredentialSource == contract.CredentialSourceOpaqueRef && len(auth.AccountModels) == 0
	})
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
		if field.Location != setup.SetupLocationConfig {
			return nil, fmt.Errorf("setup field %q is not set with --set; use its own flags", name)
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
func buildConnectorCreateRequest(provider contract.Provider, model contract.AccountModel, name string, config map[string]any, httpAPI *contract.HTTPAPI, credentialRef string, capabilityNames []string) (connectorCreateRequest, error) {
	schema, ok := setup.SetupSchemaFor(provider)
	if !ok || !slices.Contains(offeredConnectorProviders, provider) {
		return connectorCreateRequest{}, fmt.Errorf("provider %q cannot be set up", provider)
	}
	if (provider == contract.ProviderHTTPAPI) != (httpAPI != nil) {
		return connectorCreateRequest{}, errors.New("an http_api registration is required for, and only valid on, provider http_api")
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

	capabilities, err := connectorCapabilities(provider, config, httpAPI, capabilityNames)
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
		Policy: connectorPolicy{AllowedActions: actions, AllowedResources: []string{}}, HTTPAPI: httpAPI,
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
// declares a capability for a resource it does not serve. An http_api
// connector's defaults are the contract's capabilities for its allowed
// methods, reads and writes alike: the methods are the admin's explicit choice.
func connectorCapabilities(provider contract.Provider, config map[string]any, httpAPI *contract.HTTPAPI, names []string) ([]contract.Capability, error) {
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
		switch {
		case provider == contract.ProviderHTTPAPI:
			if !httpAPIMethodAllowed(httpAPI, capability.Name) {
				continue
			}
		case capability.Action != contract.ActionRead:
			continue
		case provider == contract.ProviderCRM && !crmResourceAllowed(config, capability.Name):
			continue
		}
		capabilities = append(capabilities, capability)
	}
	if provider == contract.ProviderHTTPAPI {
		for _, method := range httpAPI.AllowedMethods {
			name := "http." + strings.ToLower(string(method))
			if !slices.ContainsFunc(capabilities, func(c contract.Capability) bool { return c.Name == name }) {
				return nil, fmt.Errorf("HTTP method %s has no capability in this kei's connector contract; upgrade kei", method)
			}
		}
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
