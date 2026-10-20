package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func runGroupsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "groups requires a subcommand: list, create or members")
		return 2
	}
	switch args[0] {
	case "list":
		return runGroupsList(args[1:], stdout, stderr, client, store)
	case "create":
		return runGroupsCreate(args[1:], stdout, stderr, stdin, client, store)
	case "members":
		return runGroupsMembersCommand(args[1:], stdout, stderr, stdin, client, store)
	default:
		fmt.Fprintf(stderr, "unknown groups command %q\n", args[0])
		return 2
	}
}

// runGroupsList lists the groups of one workspace, so a script can find a
// group's ID by name. --workspace is required, like groups create.
func runGroupsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("groups list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace name or ID (required)")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "groups list accepts no positional arguments")
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
	flags := flag.NewFlagSet("groups create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "group name (required)")
	description := flags.String("description", "", "group description")
	workspace := flags.String("workspace", "", "workspace name or ID (required)")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	yes := flags.Bool("yes", false, "confirm without prompting")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *name == "" {
		fmt.Fprintln(stderr, "groups create requires --name NAME and --workspace WORKSPACE")
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

func runGroupsMembersCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "groups members requires a subcommand: list, add, or remove")
		return 2
	}
	switch args[0] {
	case "list":
		return runGroupsMembersList(args[1:], stdout, stderr, client, store)
	case "add":
		return runGroupsMembersAdd(args[1:], stdout, stderr, stdin, client, store)
	case "remove":
		return runGroupsMembersRemove(args[1:], stdout, stderr, stdin, client, store)
	default:
		fmt.Fprintf(stderr, "unknown groups members command %q\n", args[0])
		return 2
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
	flags := flag.NewFlagSet("groups members list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	workspace := flags.String("workspace", "", "workspace name or ID (default: the organization's default workspace)")
	pos, err := parsePositionals(flags, args, 1)
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
	flags := flag.NewFlagSet("groups members add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace name or ID (default: the organization's default workspace)")
	yes := flags.Bool("yes", false, "confirm without prompting")
	pos, err := parsePositionals(flags, args, 2)
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
	flags := flag.NewFlagSet("groups members remove", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace name or ID (default: the organization's default workspace)")
	yes := flags.Bool("yes", false, "confirm without prompting")
	pos, err := parsePositionals(flags, args, 2)
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
