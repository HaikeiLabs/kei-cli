package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	AuthMethod string  `json:"auth_method"`
	// CredentialFields name a secret_ref template's secrets (HAI-583): one
	// per field, each set with :setSecret. Names only, never a value.
	CredentialFields []templateCredentialField `json:"credential_fields"`
}

// templateCredentialField is one secret of a secret_ref template.
type templateCredentialField struct {
	Field  string `json:"field"`
	Header string `json:"header"`
}

// templateAuthSecretRef is the auth method of templates whose connectors send
// static secrets from the workspace's credential store (ADR-036 §6.4).
const templateAuthSecretRef = "secret_ref"

// sealedField is one credential field's secret, sealed to the workspace's
// runtimes and ready for :setSecret.
type sealedField struct {
	field   string
	request setSecretRequest
}

// readAndSealTemplateSecrets reads each credential field's secret (without
// echo on a terminal, else one stdin line per field) and seals it to the
// workspace's credential delivery recipients. It runs before anything is
// created or changed, so a missing secret or recipient changes nothing.
func readAndSealTemplateSecrets(session *connectorSession, template connectorTemplate, stdin io.Reader, stdout io.Writer) ([]sealedField, error) {
	if len(template.CredentialFields) == 0 {
		return nil, fmt.Errorf("template %q declares no credential fields", template.ID)
	}
	reader := bufio.NewReader(stdin)
	sealed := make([]sealedField, 0, len(template.CredentialFields))
	for _, f := range template.CredentialFields {
		secret, err := promptSecret(reader, stdin, stdout, f.Field+" (sent in "+f.Header+"): ")
		if err != nil {
			return nil, fmt.Errorf("read secret field %q: %w", f.Field, err)
		}
		if secret == "" {
			return nil, fmt.Errorf("secret field %q is empty", f.Field)
		}
		request, err := session.sealForWorkspace(f.Field, secret)
		if err != nil {
			return nil, err
		}
		sealed = append(sealed, sealedField{field: f.Field, request: request})
	}
	return sealed, nil
}

// setSealedSecrets sets each sealed field on connector id and reports the
// queued generation.
func setSealedSecrets(session *connectorSession, id string, sealed []sealedField, stdout io.Writer) error {
	for _, s := range sealed {
		status, err := session.setSecret(id, s.request)
		if err != nil {
			return fmt.Errorf("set secret %q: %w", s.field, err)
		}
		fmt.Fprintf(stdout, "Secret %q sealed and queued for delivery (generation %d).\n", s.field, status.Generation)
	}
	return nil
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
var nativeOnlyCreateFlags = []string{"provider", "account-model", "set", "credential-ref", "capabilities", "base-url", "methods", "paths"}

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
func runConnectorsCreateFromTemplate(f templateCreateFlags, name, workspace string, jsonOutput bool, stdin io.Reader, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if *f.label == "" {
		fmt.Fprintln(stderr, "connectors create --template requires --label")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors create: %v\n", err)
		return 1
	}
	session, ok := openConnectorSession("create", workspace, stderr, client, store)
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
	var sealed []sealedField
	if template.AuthMethod == templateAuthSecretRef {
		if *f.expectedIssuer != "" {
			return fail(fmt.Errorf("template %q uses static secrets; --expected-issuer is for OAuth templates", template.ID))
		}
		if sealed, err = readAndSealTemplateSecrets(session, template, stdin, stdout); err != nil {
			return fail(err)
		}
	}
	request := mcpConnectorCreateRequest{
		Name: name, Provider: template.ID, DisplayName: *f.label,
		Slug: optionalFlag(*f.slug), MCPURL: optionalFlag(*f.mcpURL), ExpectedIssuer: optionalFlag(*f.expectedIssuer),
	}
	payload, err := session.do(http.MethodPost, "", request)
	if err != nil {
		return fail(err)
	}
	var created connector
	if err := json.Unmarshal(payload, &created); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	// A catalog that predates expected_issuer (HAI-563) accepts the create
	// and drops the field, leaving a connector that trusts any authorization
	// server. Fail closed: remove it rather than leave it unpinned.
	if request.ExpectedIssuer != nil && created.ExpectedIssuer != *request.ExpectedIssuer {
		reason := fmt.Errorf("the catalog does not support expected_issuer and created connector %s without it", created.ID)
		if _, err := session.do(http.MethodDelete, "/"+url.PathEscape(created.ID), nil); err != nil {
			return fail(fmt.Errorf("%w; deleting it failed (%v). Delete it with: kei connectors delete %s --workspace %s --yes", reason, err, created.ID, workspace))
		}
		return fail(fmt.Errorf("%w, so it was deleted. Upgrade the catalog, or create the connector without --expected-issuer", reason))
	}
	if len(sealed) > 0 {
		out := stdout
		if jsonOutput {
			out = io.Discard
		}
		if err := setSealedSecrets(session, created.ID, sealed, out); err != nil {
			return fail(fmt.Errorf("connector %s was created, but %w. Retry with: kei connectors reconnect %s --workspace %s", created.ID, err, created.ID, workspace))
		}
	}
	if jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	fmt.Fprintf(stdout, "Created MCP connector %s (slug %s, template %s, %s).\nConnector instance ID: %s\nMCP URL: %s\n", created.DisplayName, created.Slug, template.ID, connectorState(created.Status), created.ID, created.MCPURL)
	if len(sealed) > 0 {
		fmt.Fprintln(stdout, "It uses one shared key from the workspace's credential store and becomes connected when the runtime stores it. Its tools are usable once an admin approves its tool manifest.")
		return 0
	}
	if created.ExpectedIssuer != "" {
		fmt.Fprintf(stdout, "Issuer: %s\n", created.ExpectedIssuer)
	}
	fmt.Fprintln(stdout, "Users connect their own accounts through their chat harness. Its tools are usable once an admin approves its tool manifest.")
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
