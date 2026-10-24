package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
)

type servicePrincipalBinding struct {
	ID                 string `json:"id"`
	ServicePrincipalID string `json:"service_principal_id"`
	InstallationID     string `json:"installation_id"`
	AgentID            string `json:"agent_id"`
}

func servicePrincipalBindingsPath(orgID, workspaceID string) string {
	return "/api/v1/organizations/" + url.PathEscape(orgID) + "/workspaces/" + url.PathEscape(workspaceID) + "/service-principal-bindings"
}

func servicePrincipalGroupMembersPath(orgID, workspaceID, groupID string) string {
	return "/api/v1/organizations/" + url.PathEscape(orgID) + "/workspaces/" + url.PathEscape(workspaceID) + "/groups/" + url.PathEscape(groupID) + "/service-principals"
}

// The identity API represents a principal's resource name as
// organizations/{org}/servicePrincipals/{short-name} and supplies its UUID.
func resolveServicePrincipalID(session *apiSession, name string) (string, error) {
	_, payload, err := session.do(http.MethodGet, servicePrincipalsPath(session.orgID, ""), nil, nil)
	if err != nil {
		return "", err
	}
	var page struct {
		Principals []servicePrincipal `json:"servicePrincipals"`
	}
	if err := json.Unmarshal(payload, &page); err != nil {
		return "", fmt.Errorf("decode service principals: %w", err)
	}
	for _, p := range page.Principals {
		if p.shortName() == name || p.Name == name {
			if _, err := uuid.Parse(p.ID); err != nil {
				return "", fmt.Errorf("service principal %q response has no valid id", name)
			}
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("service principal %q not found", name)
}

func runServicePrincipalBind(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "bind", "Bind a service principal to an installation and agent", stdout, stderr).WithArgs("NAME")
	installation := lc.fs.String("installation", "", "runtime installation UUID (required)")
	agent := lc.fs.String("agent", "", "agent UUID (required)")
	workspace := workspaceFlag(lc.fs)
	yes := yesFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "service-principals bind requires a service principal name")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "installation", "agent", "workspace") {
		return 2
	}
	if !isUUID(*installation) || !isUUID(*agent) {
		fmt.Fprintln(stderr, "service-principals bind: --installation and --agent must be UUIDs")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Bind service principal "+pos[0]+" to the installation and agent?") {
		return 2
	}
	session, ok := openAPISession("service-principals bind", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("service-principals bind", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	principalID, err := resolveServicePrincipalID(session, pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "service-principals bind: %v\n", err)
		return 1
	}
	_, payload, err := session.do(http.MethodPost, servicePrincipalBindingsPath(session.orgID, workspaceID), nil, map[string]string{
		"service_principal_id": principalID, "installation_id": *installation, "agent_id": *agent,
	})
	if err != nil {
		fmt.Fprintf(stderr, "service-principals bind: %v\n", err)
		return 1
	}
	var binding servicePrincipalBinding
	if err := json.Unmarshal(payload, &binding); err != nil || binding.ID == "" {
		fmt.Fprintln(stderr, "service-principals bind: decode response: missing binding id")
		return 1
	}
	fmt.Fprintf(stdout, "Bound service principal %s.\nBinding ID: %s\n", pos[0], binding.ID)
	return 0
}

func runServicePrincipalUnbind(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "unbind", "Remove a service principal binding", stdout, stderr).WithArgs("[NAME|BINDING_ID]")
	installation := lc.fs.String("installation", "", "runtime installation UUID")
	agent := lc.fs.String("agent", "", "agent UUID")
	workspace := workspaceFlag(lc.fs)
	bindingID := lc.fs.String("binding-id", "", "binding UUID")
	yes := yesFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	var pos []string
	argIndex := 0
	for argIndex < len(args) && !strings.HasPrefix(args[argIndex], "-") {
		pos = append(pos, args[argIndex])
		argIndex++
	}
	if err := lc.fs.Parse(args[argIndex:]); err != nil {
		return 2
	}
	pos = append(pos, lc.fs.Args()...)
	if len(pos) > 1 {
		fmt.Fprintln(stderr, "service-principals unbind accepts at most one principal name or binding ID")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "workspace") {
		return 2
	}
	byID := *bindingID != ""
	if len(pos) == 1 {
		if byID {
			fmt.Fprintln(stderr, "service-principals unbind: use either a positional name or --binding-id, not both")
			return 2
		}
		if isUUID(pos[0]) && *installation == "" && *agent == "" {
			*bindingID = pos[0]
			byID = true
		}
	}
	if byID {
		if len(pos) != 0 || !isUUID(*bindingID) {
			fmt.Fprintln(stderr, "service-principals unbind: --binding-id must be a UUID and cannot be combined with a name")
			return 2
		}
	} else if len(pos) != 1 || !requireFlags(lc.fs, stderr, "installation", "agent") || !isUUID(*installation) || !isUUID(*agent) {
		fmt.Fprintln(stderr, "service-principals unbind requires a binding ID or NAME with --installation and --agent UUIDs")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Remove this service principal binding?") {
		return 2
	}
	session, ok := openAPISession("service-principals unbind", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("service-principals unbind", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	if !byID {
		principalID, err := resolveServicePrincipalID(session, pos[0])
		if err != nil {
			fmt.Fprintf(stderr, "service-principals unbind: %v\n", err)
			return 1
		}
		bindings, err := listServicePrincipalBindings(session, workspaceID)
		if err != nil {
			fmt.Fprintf(stderr, "service-principals unbind: %v\n", err)
			return 1
		}
		for _, b := range bindings {
			if b.ServicePrincipalID == principalID && b.InstallationID == *installation && b.AgentID == *agent {
				*bindingID = b.ID
				break
			}
		}
		if *bindingID == "" {
			fmt.Fprintln(stderr, "service-principals unbind: matching binding not found")
			return 1
		}
	}
	path := servicePrincipalBindingsPath(session.orgID, workspaceID) + "/" + url.PathEscape(*bindingID)
	if _, _, err := session.do(http.MethodDelete, path, nil, nil); err != nil {
		fmt.Fprintf(stderr, "service-principals unbind: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Service principal binding %s removed.\n", *bindingID)
	return 0
}

func listServicePrincipalBindings(session *apiSession, workspaceID string) ([]servicePrincipalBinding, error) {
	_, payload, err := session.do(http.MethodGet, servicePrincipalBindingsPath(session.orgID, workspaceID), nil, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Bindings []servicePrincipalBinding `json:"service_principal_bindings"`
	}
	if err := json.Unmarshal(payload, &page); err != nil {
		return nil, fmt.Errorf("decode bindings: %w", err)
	}
	return page.Bindings, nil
}

func runServicePrincipalBindingsCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	group := commandGroup{name: "service-principals bindings", commands: []groupCommand{{name: "list", summary: "List service principal bindings"}}}
	sub, rest, code, handled := group.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	if sub != "list" {
		panic("unexpected subcommand: " + sub)
	}
	lc := newCommandFlags("service-principals", "bindings list", "List service principal bindings", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, rest)
	if !ok {
		return rc
	}
	if !requireFlags(lc.fs, stderr, "workspace") {
		return 2
	}
	session, ok := openAPISession("service-principals bindings list", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("service-principals bindings list", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	bindings, err := listServicePrincipalBindings(session, workspaceID)
	if err != nil {
		fmt.Fprintf(stderr, "service-principals bindings list: %v\n", err)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bindings)
		return 0
	}
	if len(bindings) == 0 {
		fmt.Fprintln(stdout, "No service principal bindings found.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BINDING ID\tSERVICE PRINCIPAL ID\tINSTALLATION ID\tAGENT ID")
	for _, b := range bindings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, b.ServicePrincipalID, b.InstallationID, b.AgentID)
	}
	_ = tw.Flush()
	return 0
}

func resolveServicePrincipalByName(session *apiSession, name string) (servicePrincipal, error) {
	_, payload, err := session.do(http.MethodGet, servicePrincipalsPath(session.orgID, ""), nil, nil)
	if err != nil {
		return servicePrincipal{}, err
	}
	var page struct {
		Principals []servicePrincipal `json:"servicePrincipals"`
	}
	if err := json.Unmarshal(payload, &page); err != nil {
		return servicePrincipal{}, err
	}
	for _, p := range page.Principals {
		if strings.EqualFold(p.shortName(), name) || p.Name == name {
			return p, nil
		}
	}
	return servicePrincipal{}, fmt.Errorf("service principal %q not found", name)
}

func runServicePrincipalGroupMembership(args []string, add bool, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	verb := "remove"
	if add {
		verb = "add"
	}
	lc := newCommandFlags("groups", "members "+verb, "Manage a service principal group member", stdout, stderr).WithArgs("GROUP")
	workspace := workspaceFlag(lc.fs)
	principal := lc.fs.String("service-principal", "", "service principal name or UUID (required)")
	yes := yesFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintf(stderr, "groups members %s requires a group name or ID and --service-principal\n", verb)
		return 2
	}
	if !requireFlags(lc.fs, stderr, "service-principal", "workspace") {
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, strings.Title(verb)+" service principal in group?") {
		return 2
	}
	session, ok := openAPISession("groups members "+verb, stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("members "+verb, *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	groupID, _, err := resolveGroupByNameOrID(session, workspaceID, pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "groups members %s: %v\n", verb, err)
		return 1
	}
	principalID := *principal
	if !isUUID(principalID) {
		principalID, err = resolveServicePrincipalID(session, principalID)
		if err != nil {
			fmt.Fprintf(stderr, "groups members %s: %v\n", verb, err)
			return 1
		}
	}
	path := servicePrincipalGroupMembersPath(session.orgID, workspaceID, groupID)
	method := http.MethodPost
	body := any(map[string]string{"service_principal_id": principalID})
	if !add {
		method = http.MethodDelete
		path += "/" + url.PathEscape(principalID)
		body = nil
	}
	if _, _, err := session.do(method, path, nil, body); err != nil {
		fmt.Fprintf(stderr, "groups members %s: %v\n", verb, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s service principal %s %s group %s.\n", strings.Title(verb), principalID, map[bool]string{true: "to", false: "from"}[add], groupID)
	return 0
}
