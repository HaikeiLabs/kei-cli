package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
)

// connectorKindMCP is the kind of a connector template whose connectors are
// remote MCP servers (kei ADR-036 §3.1).
const connectorKindMCP = "mcp"

// connectorTemplate is the part of a catalog connector template the CLI reads
// to choose the create path. The CLI holds no template list of its own: the
// templates and their kinds come from GET /api/v1/connector-templates/{id}.
type connectorTemplate struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	DefaultURL *string `json:"default_url"`
}

// mcpConnectorCreateRequest is the body of POST /api/v1/data-connectors for
// an mcp-kind template (ADR-036 §3.4). Its users connect their own accounts,
// so it carries no credential, capability or policy fields.
type mcpConnectorCreateRequest struct {
	Name           string  `json:"name"`
	Provider       string  `json:"provider"`
	DisplayName    string  `json:"display_name"`
	Slug           *string `json:"slug,omitempty"`
	MCPURL         *string `json:"mcp_url,omitempty"`
	ExpectedIssuer *string `json:"expected_issuer,omitempty"`
}

// templateCreateFlags are the connectors create flags of the template path.
type templateCreateFlags struct {
	template, kind, label, slug, mcpURL, expectedIssuer *string
}

func registerTemplateCreateFlags(flags *flag.FlagSet) templateCreateFlags {
	return templateCreateFlags{
		template:       flags.String("template", "", "connector template ID, from the catalog's connector templates (an mcp template creates an MCP connector)"),
		kind:           flags.String("kind", "", "with --template: the template's expected kind, for example mcp"),
		label:          flags.String("label", "", "with --template: the connector's label, shown to users"),
		slug:           flags.String("slug", "", "with --template: the workspace-unique slug (default: derived from --label; it cannot be changed later)"),
		mcpURL:         flags.String("mcp-url", "", "with an mcp template: the server's https MCP URL (required when the template has none)"),
		expectedIssuer: flags.String("expected-issuer", "", "with an mcp template: the OAuth authorization server issuer the server must use"),
	}
}

// nativeOnlyCreateFlags are the create flags of the provider path; the
// template path rejects them.
var nativeOnlyCreateFlags = []string{"provider", "account-model", "set", "credential-ref", "base-url", "methods", "paths"}

// templateOnlyCreateFlags are the create flags of the template path; the
// provider path rejects them.
var templateOnlyCreateFlags = []string{"kind", "label", "slug", "mcp-url", "expected-issuer"}

// checkCreateFlagPaths rejects a create that mixes the provider path and the
// template path, before any request. It reports false after printing why.
func checkCreateFlagPaths(flags *flag.FlagSet, stderr io.Writer) bool {
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["template"] {
		for _, name := range nativeOnlyCreateFlags {
			if set[name] {
				fmt.Fprintf(stderr, "connectors create: --%s cannot be used with --template\n", name)
				return false
			}
		}
		return true
	}
	for _, name := range templateOnlyCreateFlags {
		if set[name] {
			fmt.Fprintf(stderr, "connectors create: --%s requires --template\n", name)
			return false
		}
	}
	return true
}

// optionalFlag returns a pointer to value, or nil when it is empty.
func optionalFlag(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// runConnectorsCreateFromTemplate creates a connector from a catalog template
// (HAI-489). The template's kind, read from the catalog, picks the path; only
// mcp templates are created here.
func runConnectorsCreateFromTemplate(f templateCreateFlags, name string, ws workspaceSelection, capabilities []string, allowGroups []string, defaultAccess, noDefaultAccess, yes bool, stdin io.Reader, jsonOutput bool, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if *f.label == "" {
		fmt.Fprintln(stderr, "connectors create --template requires --label")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors create: %v\n", err)
		return 1
	}
	session, ok := openConnectorSession("create", ws, stderr, client, store)
	if !ok {
		return 1
	}
	template, err := session.connectorTemplate(*f.template)
	if err != nil {
		return fail(err)
	}
	if *f.kind != "" && *f.kind != template.Kind {
		return fail(fmt.Errorf("template %q is kind %s, not %s", template.ID, template.Kind, *f.kind))
	}
	if template.Kind != connectorKindMCP {
		return fail(fmt.Errorf("template %q is kind %s; create it with --provider %s", template.ID, template.Kind, template.ID))
	}
	if template.DefaultURL == nil && *f.mcpURL == "" {
		return fail(fmt.Errorf("template %q has no MCP URL; pass --mcp-url", template.ID))
	}
	if name == "" {
		name = *f.label
	}
	request := mcpConnectorCreateRequest{
		Name: name, Provider: template.ID, DisplayName: *f.label,
		Slug: optionalFlag(*f.slug), MCPURL: optionalFlag(*f.mcpURL), ExpectedIssuer: optionalFlag(*f.expectedIssuer),
	}
	orgName, err := getOrganizationName(context.Background(), client, session.baseURL, session.token, session.orgID)
	if err != nil {
		return fail(err)
	}
	workspaceName := workspaceNameByID(context.Background(), client, session.baseURL, session.token, session.orgID, session.workspaceID)
	_, err = resolveConnectorGroupIDs(session, allowGroups)
	if err != nil {
		if defaultAccess && strings.Contains(err.Error(), `group "admins" was not found`) {
			return fail(fmt.Errorf("workspace group %q was not found; pass --allow-group NAME or --no-default-access", "admins"))
		}
		return fail(err)
	}
	fmt.Fprintf(stdout, "Organization: %s\nWorkspace: %s\n", orgName, workspaceName)
	confirmText := "Create connector " + name + "? This may grant the selected groups access."
	if defaultAccess {
		confirmText = "Create connector " + name + "? This will allow the admins group to use it."
	}
	if !confirmWrite(stdin, stderr, yes, confirmText) {
		return 2
	}
	payload, err := session.do(http.MethodPost, "", request)
	if err != nil {
		return fail(err)
	}
	var created connector
	if err := json.Unmarshal(payload, &created); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	capabilityNames := make([]string, 0, len(created.Capabilities))
	for _, capability := range created.Capabilities {
		capabilityNames = append(capabilityNames, capability.Name)
	}
	source := created.Provider
	if source == "" {
		source = template.ID
	}
	policyNames, err := createConnectorGroupAllows(session, allowGroups, created.ID, name, source, capabilityNames)
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
	// A catalog that predates expected_issuer (HAI-563) accepts the create
	// and drops the field, leaving a connector that trusts any authorization
	// server. Fail closed: remove it rather than leave it unpinned.
	if request.ExpectedIssuer != nil && created.ExpectedIssuer != *request.ExpectedIssuer {
		reason := fmt.Errorf("the catalog does not support expected_issuer and created connector %s without it", created.ID)
		if _, err := session.do(http.MethodDelete, "/"+url.PathEscape(created.ID), nil); err != nil {
			return fail(fmt.Errorf("%w; deleting it failed (%v). Delete it with: kei connectors delete %s --workspace %s --yes", reason, err, created.ID, ws.value))
		}
		return fail(fmt.Errorf("%w, so it was deleted. Upgrade the catalog, or create the connector without --expected-issuer", reason))
	}
	if jsonOutput {
		_, _ = stdout.Write(payload)
		if noDefaultAccess {
			fmt.Fprintln(stderr, "Warning: nobody can connect until access is granted.")
		}
		return 0
	}
	fmt.Fprintf(stdout, "Created MCP connector %s (slug %s, template %s, %s).\nConnector instance ID: %s\nMCP URL: %s\n", created.DisplayName, created.Slug, template.ID, connectorState(created.Status), created.ID, created.MCPURL)
	for _, policy := range policyNames {
		fmt.Fprintf(stdout, "Allowed group %s to use %s (policy %s).\n", policy.GroupName, name, policy.Name)
	}
	if noDefaultAccess {
		fmt.Fprintln(stdout, "Warning: nobody can connect until access is granted.")
	}
	if created.ExpectedIssuer != "" {
		fmt.Fprintf(stdout, "Issuer: %s\n", created.ExpectedIssuer)
	}
	printPerUserMCPNextSteps(stdout, created.ID, ws.value)
	return 0
}

// connectorTemplate reads one template from the catalog.
func (s *connectorSession) connectorTemplate(id string) (connectorTemplate, error) {
	payload, err := s.doPath(http.MethodGet, "/api/v1/connector-templates/"+url.PathEscape(id), nil)
	var status *connectorStatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
		return connectorTemplate{}, fmt.Errorf("unknown connector template %q", id)
	}
	if err != nil {
		return connectorTemplate{}, err
	}
	var template connectorTemplate
	if err := json.Unmarshal(payload, &template); err != nil {
		return connectorTemplate{}, fmt.Errorf("decode connector template: %w", err)
	}
	if template.ID == "" || template.Kind == "" {
		return connectorTemplate{}, errors.New("connector template response has no id or kind")
	}
	return template, nil
}

// orDash returns value, or "-" when it is empty.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
