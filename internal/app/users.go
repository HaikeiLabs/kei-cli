package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
)

// users is the identity service's users/{user} API (identity-v1), reached on
// the public AIP host (api.haikeilabs.com). The organization is read from the
// CLI token; the identity service enforces the org scope and role.

// identityUser is the users/{user} resource (identity-v1 §3.1).
type identityUser struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	CreateTime  string `json:"create_time"`
	UpdateTime  string `json:"update_time"`
}

// userID extracts the Kei user id from the users/{id} resource name.
func (u identityUser) userID() string {
	return strings.TrimPrefix(u.Name, "users/")
}

type createUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

// updateUserRequest is the PATCH body: only the fields named in update_mask
// change.
type updateUserRequest struct {
	Email       *string `json:"email,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	UpdateMask  string  `json:"update_mask,omitempty"`
}

type resolveUserRequest struct {
	Email string `json:"email"`
}

// identityLoginIdentity is users/{user}/identities/{identity} (identity-v1
// §3.2), an SSO/login identity.
type identityLoginIdentity struct {
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Subject    string `json:"subject"`
	CreateTime string `json:"create_time"`
}

// identityRemoteIdentity is users/{user}/remoteIdentities/{link} (identity-v1
// §3.3), a chat-platform link.
type identityRemoteIdentity struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Status      string `json:"status"`
	CreateTime  string `json:"create_time"`
	UpdateTime  string `json:"update_time"`
}

var usersGroup = commandGroup{
	name: "users",
	commands: []groupCommand{
		{name: "list", summary: "List users"},
		{name: "get", summary: "Get a user by ID"},
		{name: "create", summary: "Create a user"},
		{name: "update", summary: "Update a user"},
		{name: "resolve", summary: "Resolve a user identity"},
		{name: "identities", summary: "List user identities"},
		{name: "remote-identities", summary: "List remote identities"},
	},
}

func runUsersCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := usersGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runUsersList(rest, stdout, stderr, client, store)
	case "get":
		return runUsersGet(rest, stdout, stderr, client, store)
	case "create":
		return runUsersCreate(rest, stdout, stderr, stdin, client, store)
	case "update":
		return runUsersUpdate(rest, stdout, stderr, stdin, client, store)
	case "resolve":
		return runUsersResolve(rest, stdout, stderr, client, store)
	case "identities":
		return runUsersIdentities(rest, stdout, stderr, client, store)
	case "remote-identities":
		return runUsersRemoteIdentities(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func runUsersList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "list", "List users", stdout, stderr)
	jsonOutput := jsonFlag(lc.fs)
	pageSize := lc.fs.Int("page-size", 50, "page size (max 200)")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "users list accepts no positional arguments")
		return 2
	}
	session, ok := openAPISession("users list", stderr, client, store)
	if !ok {
		return 1
	}
	var all []identityUser
	pageToken := ""
	for {
		query := url.Values{"page_size": {strconv.Itoa(*pageSize)}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		_, payload, err := session.do(http.MethodGet, "/api/v1/users", query, nil)
		if err != nil {
			fmt.Fprintf(stderr, "users list: %v\n", err)
			return 1
		}
		var page struct {
			Users         []identityUser `json:"users"`
			NextPageToken string         `json:"next_page_token"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "users list: decode response: %v\n", err)
			return 1
		}
		all = append(all, page.Users...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No users found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tEMAIL\tDISPLAY NAME\tCREATED")
	for _, u := range all {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", u.userID(), u.Email, u.DisplayName, u.CreateTime)
	}
	_ = table.Flush()
	return 0
}

func runUsersGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "get", "Get a user by ID", stdout, stderr).WithArgs("USER_ID")
	jsonOutput := jsonFlag(lc.fs)
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "users get requires a user ID")
		return 2
	}
	id := pos[0]
	session, ok := openAPISession("users get", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, "/api/v1/users/"+url.PathEscape(id), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "users get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var u identityUser
	if err := json.Unmarshal(payload, &u); err != nil {
		fmt.Fprintf(stderr, "users get: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ID:        %s\nEmail:     %s\nDisplay:   %s\nCreated:   %s\nUpdated:   %s\n", u.userID(), u.Email, u.DisplayName, u.CreateTime, u.UpdateTime)
	return 0
}

func runUsersCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "create", "Create a user", stdout, stderr)
	email := lc.fs.String("email", "", "user email (required)")
	name := lc.fs.String("name", "", "display name")
	jsonOutput := jsonFlag(lc.fs)
	yes := yesFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "users create requires --email EMAIL")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "email") {
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Create a new Kei user?") {
		return 2
	}
	session, ok := openAPISession("users create", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPost, "/api/v1/users", nil, createUserRequest{Email: *email, DisplayName: *name})
	if err != nil {
		fmt.Fprintf(stderr, "users create: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var u identityUser
	if err := json.Unmarshal(payload, &u); err != nil {
		fmt.Fprintf(stderr, "users create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created user %s (%s).\nUser ID: %s\n", u.Email, u.DisplayName, u.userID())
	return 0
}

func runUsersUpdate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "update", "Update a user", stdout, stderr).WithArgs("USER_ID")
	email := lc.fs.String("email", "", "new email")
	name := lc.fs.String("name", "", "new display name")
	jsonOutput := jsonFlag(lc.fs)
	yes := yesFlag(lc.fs)
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "users update requires a user ID")
		return 2
	}
	id := pos[0]
	set := make(map[string]bool)
	lc.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["email"] && !set["name"] {
		fmt.Fprintln(stderr, "users update requires --email and/or --name")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Update the Kei user?") {
		return 2
	}
	session, ok := openAPISession("users update", stderr, client, store)
	if !ok {
		return 1
	}
	var mask []string
	body := updateUserRequest{}
	if set["email"] {
		body.Email = email
		mask = append(mask, "email")
	}
	if set["name"] {
		body.DisplayName = name
		mask = append(mask, "display_name")
	}
	body.UpdateMask = strings.Join(mask, ",")
	_, payload, err := session.do(http.MethodPatch, "/api/v1/users/"+url.PathEscape(id), nil, body)
	if err != nil {
		fmt.Fprintf(stderr, "users update: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var u identityUser
	if err := json.Unmarshal(payload, &u); err != nil {
		fmt.Fprintf(stderr, "users update: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated user %s (%s).\n", u.userID(), u.Email)
	return 0
}

func runUsersResolve(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "resolve", "Resolve a user identity", stdout, stderr)
	email := lc.fs.String("email", "", "user email to resolve")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "users resolve requires --email EMAIL")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "email") {
		return 2
	}
	session, ok := openAPISession("users resolve", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPost, "/api/v1/users:resolve", nil, resolveUserRequest{Email: *email})
	if err != nil {
		fmt.Fprintf(stderr, "users resolve: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var u identityUser
	if err := json.Unmarshal(payload, &u); err != nil {
		fmt.Fprintf(stderr, "users resolve: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "User ID: %s\nEmail:     %s\nDisplay:   %s\n", u.userID(), u.Email, u.DisplayName)
	return 0
}

func runUsersIdentities(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "users identities supports only: list <user>")
		return 2
	}
	return runUsersIdentitiesList(args[1:], stdout, stderr, client, store)
}

func runUsersIdentitiesList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "identities list", "List user identities", stdout, stderr).WithArgs("USER_ID")
	jsonOutput := jsonFlag(lc.fs)
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "users identities list requires a user ID")
		return 2
	}
	userID := pos[0]
	session, ok := openAPISession("users identities list", stderr, client, store)
	if !ok {
		return 1
	}
	var all []identityLoginIdentity
	pageToken := ""
	for {
		query := url.Values{"page_size": {"200"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		_, payload, err := session.do(http.MethodGet, "/api/v1/users/"+url.PathEscape(userID)+"/identities", query, nil)
		if err != nil {
			fmt.Fprintf(stderr, "users identities list: %v\n", err)
			return 1
		}
		var page struct {
			Identities    []identityLoginIdentity `json:"identities"`
			NextPageToken string                  `json:"next_page_token"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "users identities list: decode response: %v\n", err)
			return 1
		}
		all = append(all, page.Identities...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No identities found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "PROVIDER\tSUBJECT\tCREATED")
	for _, i := range all {
		fmt.Fprintf(table, "%s\t%s\t%s\n", i.Provider, i.Subject, i.CreateTime)
	}
	_ = table.Flush()
	return 0
}

func runUsersRemoteIdentities(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "users remote-identities supports only: list <user>")
		return 2
	}
	return runUsersRemoteIdentitiesList(args[1:], stdout, stderr, client, store)
}

func runUsersRemoteIdentitiesList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("users", "remote-identities list", "List remote identities", stdout, stderr).WithArgs("USER_ID")
	jsonOutput := jsonFlag(lc.fs)
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "users remote-identities list requires a user ID")
		return 2
	}
	userID := pos[0]
	session, ok := openAPISession("users remote-identities list", stderr, client, store)
	if !ok {
		return 1
	}
	var all []identityRemoteIdentity
	pageToken := ""
	for {
		query := url.Values{"page_size": {"200"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		_, payload, err := session.do(http.MethodGet, "/api/v1/users/"+url.PathEscape(userID)+"/remoteIdentities", query, nil)
		if err != nil {
			fmt.Fprintf(stderr, "users remote-identities list: %v\n", err)
			return 1
		}
		var page struct {
			RemoteIdentities []identityRemoteIdentity `json:"remoteIdentities"`
			NextPageToken    string                   `json:"next_page_token"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			fmt.Fprintf(stderr, "users remote-identities list: decode response: %v\n", err)
			return 1
		}
		all = append(all, page.RemoteIdentities...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "No remote identities found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "SOURCE\tEXTERNAL ID\tDISPLAY NAME\tSTATUS\tCREATED")
	for _, r := range all {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", r.Source, r.ExternalID, r.DisplayName, r.Status, r.CreateTime)
	}
	_ = table.Flush()
	return 0
}
