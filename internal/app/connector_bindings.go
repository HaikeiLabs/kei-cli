package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
)

// connectorBinding is one agent↔connector binding row as the catalog's AIP
// connector-bindings handler returns it (internal/connectorbindings.Binding).
//
// Bindings are wiring only (ADR-038 §4, decision 5): they attach a connector
// to an agent so the wiring exists — they never gate a permission. Access is
// a group grant, not a binding.
type connectorBinding struct {
	ID           string   `json:"id"`
	OrgID        string   `json:"org_id"`
	WorkspaceID  string   `json:"workspace_id"`
	AgentID      string   `json:"agent_id"`
	ConnectorID  string   `json:"connector_id"`
	Capabilities []string `json:"capabilities"`
	CreatedBy    string   `json:"created_by,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// connectorBindingsPage is the AIP ListResponse envelope the catalog returns
// for connector-bindings list: {"items":[...],"nextPageToken":"..."}.
type connectorBindingsPage struct {
	Items         []connectorBinding `json:"items"`
	NextPageToken string             `json:"nextPageToken"`
}

func runConnectorBindingsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	group := commandGroup{
		name: "connector-bindings",
		commands: []groupCommand{
			{name: "list", summary: "List connector bindings"},
			{name: "get", summary: "Get a connector binding by ID"},
			{name: "create", summary: "Create a connector binding (wiring only, never a permission gate; ADR-038)"},
			{name: "delete", summary: "Delete a connector binding"},
		},
	}
	sub, rest, code, handled := group.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runConnectorBindingsList(rest, stdout, stderr, client, store)
	case "get":
		return runConnectorBindingsGet(rest, stdout, stderr, client, store)
	case "create":
		return runConnectorBindingsCreate(rest, stdout, stderr, client, store)
	case "delete":
		return runConnectorBindingsDelete(rest, stdout, stderr, stdin, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

// openConnectorBindingSession loads the CLI token and resolves the selected
// workspace against the AIP host.
func openConnectorBindingSession(command string, sel workspaceSelection, stderr io.Writer, client *http.Client, store credentialStore) (*apiSession, string, bool) {
	session, ok := openAPISession("connector-bindings "+command, stderr, client, store)
	if !ok {
		return nil, "", false
	}
	ws, err := resolveScopedWorkspace(context.Background(), session.client, session.baseURL, session.token, sel)
	if err != nil {
		fmt.Fprintf(stderr, "connector-bindings %s: %v\n", command, err)
		return nil, "", false
	}
	return session, ws, true
}

// connectorBindingsPath builds the AIP connector-bindings collection path for
// the session's organization and workspace.
func connectorBindingsPath(session *apiSession, workspaceID string) string {
	return "/api/v1/organizations/" + url.PathEscape(session.orgID) +
		"/workspaces/" + url.PathEscape(workspaceID) + "/connector-bindings"
}

// requireConnectorBindingsWorkspace resolves --workspace (name or ID), picking
// the organization's default workspace when it is empty.
func requireConnectorBindingsWorkspace(command, workspace string, stderr io.Writer) (workspaceSelection, bool) {
	sel, err := selectWorkspace(workspace, keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "connector-bindings %s: %v\n", command, err)
		return workspaceSelection{}, false
	}
	if sel.fromIDEnv {
		fmt.Fprintln(stderr, deprecationNote)
	}
	return sel, true
}

// --- List ---

func runConnectorBindingsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("connector-bindings", "list", "List connector bindings", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	agent := lc.fs.String("agent", "", "list only bindings for this agent UUID")
	pageSize := lc.fs.Int("page-size", 100, "page size")
	pageToken := lc.fs.String("page-token", "", "page token from a previous list response")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "connector-bindings list accepts no positional arguments")
		return 2
	}
	if *agent != "" {
		if _, err := uuid.Parse(*agent); err != nil {
			fmt.Fprintln(stderr, "connector-bindings list: --agent must be a UUID")
			return 2
		}
	}
	ws, ok := requireConnectorBindingsWorkspace("list", *workspace, stderr)
	if !ok {
		return 2
	}
	session, wsID, ok := openConnectorBindingSession("list", ws, stderr, client, store)
	if !ok {
		return 1
	}
	query := url.Values{"page_size": {strconv.Itoa(*pageSize)}}
	if *pageToken != "" {
		query.Set("page_token", *pageToken)
	}
	if *agent != "" {
		query.Set("agent_id", *agent)
	}
	var all []connectorBinding
	for {
		_, payload, err := session.do(http.MethodGet, connectorBindingsPath(session, wsID), query, nil)
		if err != nil {
			fmt.Fprintf(stderr, "connector-bindings list: %v\n", err)
			return 1
		}
		var page connectorBindingsPage
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "connector-bindings list: decode response: %v\n", err)
			return 1
		}
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			break
		}
		query.Set("page_token", page.NextPageToken)
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No connector bindings found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tAGENT\tCONNECTOR\tCAPABILITIES\tCREATED")
	for _, binding := range all {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", binding.ID, binding.AgentID, binding.ConnectorID,
			strings.Join(binding.Capabilities, ", "), binding.CreatedAt)
	}
	_ = table.Flush()
	return 0
}

// --- Get ---

func runConnectorBindingsGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("connector-bindings", "get", "Get a connector binding by ID", stdout, stderr).WithArgs("BINDING_ID")
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "connector-bindings get requires a binding ID")
		return 2
	}
	id := pos[0]
	ws, ok := requireConnectorBindingsWorkspace("get", *workspace, stderr)
	if !ok {
		return 2
	}
	session, wsID, ok := openConnectorBindingSession("get", ws, stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, connectorBindingsPath(session, wsID)+"/"+url.PathEscape(id), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "connector-bindings get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		fmt.Fprintln(stdout)
		return 0
	}
	var binding connectorBinding
	if err := json.Unmarshal(payload, &binding); err != nil {
		fmt.Fprintf(stderr, "connector-bindings get: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ID:         %s\nAgent:      %s\nConnector:  %s\n", binding.ID, binding.AgentID, binding.ConnectorID)
	if len(binding.Capabilities) > 0 {
		fmt.Fprintf(stdout, "Capabilities: %s\n", strings.Join(binding.Capabilities, ", "))
	}
	fmt.Fprintf(stdout, "Created:    %s\n", binding.CreatedAt)
	return 0
}

// --- Create ---

func runConnectorBindingsCreate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	// ADR-038 §4/decision 5: a binding is wiring, never a permission gate.
	lc := newCommandFlags("connector-bindings", "create", "Create a connector binding (wiring only, never a permission gate; ADR-038)", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	agent := lc.fs.String("agent", "", "agent UUID to bind")
	connector := lc.fs.String("connector", "", "connector UUID or name")
	capabilities := lc.fs.String("capabilities", "", "comma-separated capabilities (default: the connector's declared capabilities)")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "connector-bindings create accepts no positional arguments")
		return 2
	}
	if *agent == "" || *connector == "" {
		fmt.Fprintln(stderr, "connector-bindings create requires --agent and --connector")
		return 2
	}
	if _, err := uuid.Parse(*agent); err != nil {
		fmt.Fprintln(stderr, "connector-bindings create: --agent must be a UUID")
		return 2
	}
	ws, ok := requireConnectorBindingsWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	session, wsID, ok := openConnectorBindingSession("create", ws, stderr, client, store)
	if !ok {
		return 1
	}
	connectorID, ok := resolveConnectorBindingConnector(session, wsID, "create", *connector, stderr)
	if !ok {
		return 1
	}
	// The CLI caller is authenticated, so created_by is left to the server.
	body := map[string]any{
		"agent_id":     *agent,
		"connector_id": connectorID,
	}
	if caps := splitList(*capabilities); len(caps) > 0 {
		body["capabilities"] = caps
	}
	_, payload, err := session.do(http.MethodPost, connectorBindingsPath(session, wsID), nil, body)
	if err != nil {
		fmt.Fprintf(stderr, "connector-bindings create: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		fmt.Fprintln(stdout)
		return 0
	}
	var created connectorBinding
	if err := json.Unmarshal(payload, &created); err != nil {
		fmt.Fprintf(stderr, "connector-bindings create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created connector binding %s.\nAgent:      %s\nConnector:  %s\n", created.ID, created.AgentID, created.ConnectorID)
	if len(created.Capabilities) > 0 {
		fmt.Fprintf(stdout, "Capabilities: %s\n", strings.Join(created.Capabilities, ", "))
	}
	return 0
}

// resolveConnectorBindingConnector returns the connector UUID for idOrName: a
// UUID passes through; otherwise the workspace's connectors are listed and the
// first one whose name matches is used.
func resolveConnectorBindingConnector(session *apiSession, workspaceID, command, idOrName string, stderr io.Writer) (string, bool) {
	if _, err := uuid.Parse(idOrName); err == nil {
		return idOrName, true
	}
	_, payload, err := session.do(http.MethodGet, "/api/v1/data-connectors", url.Values{"workspace_id": {workspaceID}}, nil)
	if err != nil {
		fmt.Fprintf(stderr, "connector-bindings %s: %v\n", command, err)
		return "", false
	}
	var connectors []connector
	if err := json.Unmarshal(payload, &connectors); err != nil {
		fmt.Fprintf(stderr, "connector-bindings %s: decode connectors: %v\n", command, err)
		return "", false
	}
	for _, c := range connectors {
		if c.Name == idOrName {
			return c.ID, true
		}
	}
	fmt.Fprintf(stderr, "connector-bindings %s: connector %q not found\n", command, idOrName)
	return "", false
}

// --- Delete ---

func runConnectorBindingsDelete(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("connector-bindings", "delete", "Delete a connector binding", stdout, stderr).WithArgs("BINDING_ID")
	workspace := workspaceFlag(lc.fs)
	yes := yesFlag(lc.fs)
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "connector-bindings delete requires a binding ID")
		return 2
	}
	id := pos[0]
	ws, ok := requireConnectorBindingsWorkspace("delete", *workspace, stderr)
	if !ok {
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "connector-bindings delete deletes the binding immediately; pass --yes to confirm")
		return 2
	}
	session, wsID, ok := openConnectorBindingSession("delete", ws, stderr, client, store)
	if !ok {
		return 1
	}
	if _, _, err := session.do(http.MethodDelete, connectorBindingsPath(session, wsID)+"/"+url.PathEscape(id), nil, nil); err != nil {
		fmt.Fprintf(stderr, "connector-bindings delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Connector binding %s deleted.\n", id)
	return 0
}
