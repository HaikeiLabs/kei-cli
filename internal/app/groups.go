package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
)

// groups members is the catalog's group-membership API (kei-policy-catalog),
// reached on the public AIP host through the console's /api/v1 proxy. The
// catalog scopes groups by org_id (and, optionally, workspace_id, defaulting
// server-side to the organization's default workspace). The organization comes
// from the CLI token.

// catalogGroup is the catalog's group row (kei-policy-catalog pkg/database).
type catalogGroup struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	CreatedAt   string        `json:"created_at"`
	Users       []catalogUser `json:"users"`
}

type catalogUser struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

var groupsGroup = commandGroup{
	name: "groups",
	commands: []groupCommand{
		{name: "list", summary: "List groups"},
		{name: "create", summary: "Create a group"},
		{name: "delete", summary: "Delete a group"},
		{name: "members", summary: "Manage group members"},
	},
}

func runGroupsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := groupsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runGroupsList(rest, stdout, stderr, client, store)
	case "create":
		return runGroupsCreate(rest, stdout, stderr, stdin, client, store)
	case "delete":
		return runGroupsDelete(rest, stdout, stderr, stdin, client, store)
	case "members":
		return runGroupsMembersCommand(rest, stdout, stderr, stdin, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

// runGroupsList lists the groups of one workspace, so a script can find a
// group's ID by name. --workspace is required, like groups create.
func runGroupsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("groups", "list", "List groups", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "groups list accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "workspace") {
		return 2
	}
	session, ok := openAPISession("groups list", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("list", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, "/api/v1/groups", groupsQuery(session, workspaceID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "groups list: %v\n", err)
		return 1
	}
	var groups []catalogGroup
	if err := json.Unmarshal(payload, &groups); err != nil {
		fmt.Fprintf(stderr, "groups list: decode response: %v\n", err)
		return 1
	}
	if *jsonOutput {
		type groupSummary struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Members     int    `json:"members"`
		}
		summaries := make([]groupSummary, 0, len(groups))
		for _, g := range groups {
			summaries = append(summaries, groupSummary{ID: g.ID, Name: g.Name, Description: g.Description, Members: len(g.Users)})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summaries)
		return 0
	}
	if len(groups) == 0 {
		fmt.Fprintln(stdout, "No groups found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tNAME\tMEMBERS")
	for _, g := range groups {
		fmt.Fprintf(table, "%s\t%s\t%d\n", g.ID, g.Name, len(g.Users))
	}
	_ = table.Flush()
	return 0
}

// runGroupsCreate creates a group in one workspace. Unlike the members
// commands, --workspace is required: a create never lands in the
// organization's default workspace by omission.
func runGroupsCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("groups", "create", "Create a group", stdout, stderr)
	name := lc.fs.String("name", "", "group name (required)")
	description := lc.fs.String("description", "", "group description")
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	yes := yesFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "groups create accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "name", "workspace") {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(stderr, "--name must not be empty")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Create group "+*name+"?") {
		return 2
	}
	session, ok := openAPISession("groups create", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("create", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPost, "/api/v1/groups", groupsQuery(session, workspaceID), map[string]string{"name": *name, "description": *description})
	if err != nil {
		fmt.Fprintf(stderr, "groups create: %v\n", err)
		return 1
	}
	var created catalogGroup
	if err := json.Unmarshal(payload, &created); err != nil || created.ID == "" {
		fmt.Fprintln(stderr, "groups create: decode response: missing group id")
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	fmt.Fprintf(stdout, "Created group %s.\nGroup ID: %s\n", created.Name, created.ID)
	return 0
}

func runGroupsDelete(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("groups", "delete", "Delete a group", stdout, stderr).WithArgs("GROUP")
	workspace := workspaceFlag(lc.fs)
	yes := yesFlag(lc.fs)
	force := lc.fs.Bool("force", false, "delete even if policies reference the group")
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "groups delete requires a group name or ID and --workspace WORKSPACE")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "workspace") {
		return 2
	}
	group := pos[0]
	if !*yes {
		fmt.Fprintln(stderr, "groups delete deletes the group and all its memberships; pass --yes to confirm")
		return 2
	}
	session, ok := openAPISession("groups delete", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("delete", *workspace, session, stderr, true)
	if !ok {
		return 1
	}
	// Resolve the group name or ID to a group ID and group name.
	groupID, groupName, err := resolveGroupByNameOrID(session, workspaceID, group)
	if err != nil {
		fmt.Fprintf(stderr, "groups delete: %v\n", err)
		return 1
	}
	// Check for policies that reference this group, unless --force.
	if !*force && groupName != "" {
		refs, err := findPoliciesReferencingGroup(session, workspaceID, groupName)
		if err != nil {
			fmt.Fprintf(stderr, "groups delete: %v\n", err)
			return 1
		}
		if len(refs) > 0 {
			fmt.Fprintf(stderr, "groups delete: %d policy(s) reference group %q; use --force to delete anyway\n", len(refs), groupName)
			for _, p := range refs {
				fmt.Fprintf(stderr, "  %s (%s -> %s)\n", p.Name, p.SrcPattern, p.DstPattern)
			}
			return 1
		}
	}
	_, _, err = session.do(http.MethodDelete, "/api/v1/groups/"+url.PathEscape(groupID), groupsQuery(session, workspaceID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "groups delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Group %s deleted.\n", groupID)
	return 0
}

// resolveGroupByNameOrID resolves the given group argument to a group ID and
// name. If the argument is a valid UUID it is used as-is and the group's name
// is looked up; otherwise it is treated as a name and resolved to an ID.
func resolveGroupByNameOrID(session *apiSession, workspaceID, group string) (id, name string, err error) {
	if _, parseErr := uuid.Parse(group); parseErr == nil {
		// The argument is a UUID; look up the group name.
		id = group
		_, payload, lookupErr := session.do(http.MethodGet, "/api/v1/groups", groupsQuery(session, workspaceID), nil)
		if lookupErr != nil {
			return "", "", lookupErr
		}
		var groups []catalogGroup
		if decodeErr := json.Unmarshal(payload, &groups); decodeErr != nil {
			return "", "", decodeErr
		}
		for _, g := range groups {
			if g.ID == group {
				return id, g.Name, nil
			}
		}
		return id, "", nil // name empty means we couldn't find it
	}
	// Treat the argument as a name; resolve to an ID.
	_, payload, lookupErr := session.do(http.MethodGet, "/api/v1/groups", groupsQuery(session, workspaceID), nil)
	if lookupErr != nil {
		return "", "", lookupErr
	}
	var groups []catalogGroup
	if decodeErr := json.Unmarshal(payload, &groups); decodeErr != nil {
		return "", "", decodeErr
	}
	for _, g := range groups {
		if g.Name == group {
			return g.ID, g.Name, nil
		}
	}
	return "", "", fmt.Errorf("group %q not found", group)
}

// findPoliciesReferencingGroup lists policies in the workspace and returns
// those whose src_pattern matches "group:<name>".
func findPoliciesReferencingGroup(session *apiSession, workspaceID, groupName string) ([]policy, error) {
	query := url.Values{"workspace_id": {workspaceID}, "page_size": {"100"}}
	_, payload, err := session.do(http.MethodGet, "/api/v1/policies", query, nil)
	if err != nil {
		return nil, err
	}
	page, err := decodePoliciesPage(payload)
	if err != nil {
		return nil, err
	}
	want := "group:" + groupName
	var refs []policy
	for _, p := range page.Policies {
		if strings.HasPrefix(p.SrcPattern, want) {
			refs = append(refs, p)
		}
	}
	return refs, nil
}

var groupsMembersGroup = commandGroup{
	name: "groups members",
	commands: []groupCommand{
		{name: "list", summary: "List group members"},
		{name: "add", summary: "Add a member to a group"},
		{name: "remove", summary: "Remove a member from a group"},
	},
}

func runGroupsMembersCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := groupsMembersGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runGroupsMembersList(rest, stdout, stderr, client, store)
	case "add":
		return runGroupsMembersAdd(rest, stdout, stderr, stdin, client, store)
	case "remove":
		return runGroupsMembersRemove(rest, stdout, stderr, stdin, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

// resolveGroupsWorkspace resolves --workspace by name or ID against the AIP host.
func resolveGroupsWorkspace(sub, workspace string, session *apiSession, stderr io.Writer, required ...bool) (string, bool) {
	if workspace == "" && (len(required) == 0 || !required[0]) {
		return "", true
	}
	sel := workspaceSelection{value: workspace}
	if workspace == "" {
		sel.pickDefault = true
	}
	ws, err := resolveScopedWorkspace(context.Background(), session.client, session.baseURL, session.token, sel)
	if err != nil {
		fmt.Fprintf(stderr, "groups %s: %v\n", sub, err)
		return "", false
	}
	return ws, true
}

func groupsQuery(session *apiSession, workspaceID string) url.Values {
	query := url.Values{"org_id": {session.orgID}}
	if workspaceID != "" {
		query.Set("workspace_id", workspaceID)
	}
	return query
}

func runGroupsMembersList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("groups", "members list", "List group members", stdout, stderr).WithArgs("GROUP")
	jsonOutput := jsonFlag(lc.fs)
	workspace := workspaceFlagHelp(lc.fs, "workspace name or ID (default: the organization's default workspace)")
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "groups members list requires a group ID")
		return 2
	}
	groupID := pos[0]
	if _, err := uuid.Parse(groupID); err != nil {
		fmt.Fprintln(stderr, "groups members list: group ID must be a UUID")
		return 2
	}
	session, ok := openAPISession("groups members list", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("members list", *workspace, session, stderr)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, "/api/v1/groups", groupsQuery(session, workspaceID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "groups members list: %v\n", err)
		return 1
	}
	var groups []catalogGroup
	if err := json.Unmarshal(payload, &groups); err != nil {
		fmt.Fprintf(stderr, "groups members list: decode response: %v\n", err)
		return 1
	}
	var group *catalogGroup
	for i := range groups {
		if groups[i].ID == groupID {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		fmt.Fprintf(stderr, "groups members list: group %s not found\n", groupID)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(group.Users)
		return 0
	}
	if len(group.Users) == 0 {
		fmt.Fprintf(stdout, "No members in group %s.\n", group.Name)
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "USER ID\tEMAIL\tNAME")
	for _, u := range group.Users {
		fmt.Fprintf(table, "%s\t%s\t%s\n", u.Sub, u.Email, u.Name)
	}
	_ = table.Flush()
	return 0
}

func runGroupsMembersAdd(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	for _, arg := range args {
		if arg == "--service-principal" || strings.HasPrefix(arg, "--service-principal=") {
			return runServicePrincipalGroupMembership(args, true, stdout, stderr, stdin, client, store)
		}
	}
	lc := newCommandFlags("groups", "members add", "Add a member to a group", stdout, stderr).WithArgs("GROUP [USER]")
	lc.fs.String("service-principal", "", "service principal name or UUID instead of a user")
	workspace := workspaceFlagHelp(lc.fs, "workspace name or ID (default: the organization's default workspace)")
	yes := yesFlag(lc.fs)
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 2)
	if err != nil {
		fmt.Fprintln(stderr, "groups members add requires a group ID and a user ID")
		return 2
	}
	groupID := pos[0]
	user := pos[1]
	if _, err := uuid.Parse(groupID); err != nil {
		fmt.Fprintln(stderr, "groups members add: group ID must be a UUID")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Add the user to the group?") {
		return 2
	}
	session, ok := openAPISession("groups members add", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("members add", *workspace, session, stderr)
	if !ok {
		return 1
	}
	_, _, err = session.do(http.MethodPost, "/api/v1/groups/"+url.PathEscape(groupID)+"/users", groupsQuery(session, workspaceID), map[string]string{"user_id": user})
	if err != nil {
		fmt.Fprintf(stderr, "groups members add: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added user %s to group %s.\n", user, groupID)
	return 0
}

func runGroupsMembersRemove(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	for _, arg := range args {
		if arg == "--service-principal" || strings.HasPrefix(arg, "--service-principal=") {
			return runServicePrincipalGroupMembership(args, false, stdout, stderr, stdin, client, store)
		}
	}
	lc := newCommandFlags("groups", "members remove", "Remove a member from a group", stdout, stderr).WithArgs("GROUP [USER]")
	lc.fs.String("service-principal", "", "service principal name or UUID instead of a user")
	workspace := workspaceFlagHelp(lc.fs, "workspace name or ID (default: the organization's default workspace)")
	yes := yesFlag(lc.fs)
	// Intercept -h/--help before parsePositionals.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 2)
	if err != nil {
		fmt.Fprintln(stderr, "groups members remove requires a group ID and a user ID")
		return 2
	}
	groupID := pos[0]
	user := pos[1]
	if _, err := uuid.Parse(groupID); err != nil {
		fmt.Fprintln(stderr, "groups members remove: group ID must be a UUID")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Remove the user from the group?") {
		return 2
	}
	session, ok := openAPISession("groups members remove", stderr, client, store)
	if !ok {
		return 1
	}
	workspaceID, ok := resolveGroupsWorkspace("members remove", *workspace, session, stderr)
	if !ok {
		return 1
	}
	query := groupsQuery(session, workspaceID)
	query.Set("user_sub", user)
	_, _, err = session.do(http.MethodDelete, "/api/v1/groups/"+url.PathEscape(groupID)+"/users", query, nil)
	if err != nil {
		fmt.Fprintf(stderr, "groups members remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed user %s from group %s.\n", user, groupID)
	return 0
}
