package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"
)

type accessGrant struct {
	ID            string  `json:"id"`
	OrgID         string  `json:"org_id"`
	WorkspaceID   string  `json:"workspace_id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	TargetType    string  `json:"target_type"`
	TargetID      string  `json:"target_id"`
	GrantedBy     *string `json:"granted_by,omitempty"`
	ApprovedBy    *string `json:"approved_by,omitempty"`
	Reason        *string `json:"reason,omitempty"`
	ExpireTime    string  `json:"expire_time"`
	CreatedAt     string  `json:"created_at"`
	RevokedAt     *string `json:"revoked_at,omitempty"`
	RevokedBy     *string `json:"revoked_by,omitempty"`
}

// principal returns the display form "type:id".
func (g accessGrant) principal() string {
	return g.PrincipalType + ":" + g.PrincipalID
}

// target returns the display form "type:id".
func (g accessGrant) target() string {
	return g.TargetType + ":" + g.TargetID
}

type accessGrantSession struct {
	client      *http.Client
	baseURL     string
	token       string
	orgID       string
	workspaceID string
}

func openAccessGrantSession(command string, sel workspaceSelection, stderr io.Writer, client *http.Client, store credentialStore) (*accessGrantSession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "access grant %s: %v; run kei login again\n", command, err)
		return nil, false
	}
	var workspaceID string
	if sel.pickDefault {
		workspaceID, err = resolveWorkspaceSelection(context.Background(), client, baseURL, token, sel)
	} else {
		workspaceID, err = resolveScopedWorkspace(context.Background(), client, baseURL, token, sel)
	}
	if err != nil {
		fmt.Fprintf(stderr, "access grant %s: %v\n", command, err)
		return nil, false
	}
	return &accessGrantSession{client: client, baseURL: baseURL, token: token, orgID: orgID, workspaceID: workspaceID}, true
}

// orgPath returns the org-scoped base path for access-grant requests.
func (s *accessGrantSession) orgPath() string {
	return fmt.Sprintf("/api/v1/organizations/%s/workspaces/%s/access-grants",
		url.PathEscape(s.orgID), url.PathEscape(s.workspaceID))
}

func (s *accessGrantSession) do(method, path string, body any) ([]byte, error) {
	endpoint := s.baseURL + s.orgPath() + path
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
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
		return nil, withCLIUpgradeHint(s.client, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, withCLIUpgradeHint(s.client, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, withCLIUpgradeHint(s.client, fmt.Errorf("%s (HTTP %d)", apiErrorMessage(payload), resp.StatusCode))
	}
	return payload, nil
}

var accessGrantGroup = commandGroup{
	name: "access grant",
	commands: []groupCommand{
		{name: "create", summary: "Create a time-bound access grant"},
		{name: "list", summary: "List access grants"},
		{name: "get", summary: "Get an access grant by ID"},
		{name: "revoke", summary: "Revoke an access grant"},
	},
}

func runAccessGrantCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := accessGrantGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "create":
		return runAccessGrantCreate(rest, stdout, stderr, client, store)
	case "list":
		return runAccessGrantList(rest, stdout, stderr, client, store)
	case "get":
		return runAccessGrantGet(rest, stdout, stderr, client, store)
	case "revoke":
		return runAccessGrantRevoke(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func accessGrantWorkspaceFlag(flags *flag.FlagSet) *string {
	return flags.String("workspace", "", "workspace name or ID (or set KEI_WORKSPACE)")
}

func requireAccessGrantWorkspace(command, workspace string, stderr io.Writer) (workspaceSelection, bool) {
	sel, err := selectWorkspace(workspace, keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "access grant %s: %v\n", command, err)
		return workspaceSelection{}, false
	}
	if sel.fromIDEnv {
		fmt.Fprintln(stderr, deprecationNote)
	}
	return sel, true
}

// --- Create ---

func runAccessGrantCreate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("access grant create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := accessGrantWorkspaceFlag(flags)
	principal := flags.String("principal", "", "grant principal, e.g. user:<id> or agent:<id>")
	target := flags.String("target", "", "grant target, e.g. shell:git or connector:linear")
	ttl := flags.String("ttl", "", "time-to-live, e.g. 4h (expire_time = now + ttl)")
	reason := flags.String("reason", "", "reason for the grant (optional)")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "access grant create accepts no positional arguments")
		return 2
	}
	if *principal == "" || *target == "" || *ttl == "" {
		fmt.Fprintln(stderr, "access grant create requires --principal, --target, and --ttl")
		return 2
	}
	dur, err := time.ParseDuration(*ttl)
	if err != nil {
		fmt.Fprintf(stderr, "access grant create: --ttl must be a valid duration (e.g. 4h): %v\n", err)
		return 2
	}
	expireTime := time.Now().UTC().Add(dur).Format(time.RFC3339)
	ws, ok := requireAccessGrantWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openAccessGrantSession("create", ws, stderr, client, store)
	if !ok {
		return 1
	}
	body := map[string]any{
		"principal":   *principal,
		"target":      *target,
		"expire_time": expireTime,
	}
	if *reason != "" {
		body["reason"] = *reason
	}
	payload, err := session.do(http.MethodPost, "", body)
	if err != nil {
		fmt.Fprintf(stderr, "access grant create: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var created accessGrant
	if err := json.Unmarshal(payload, &created); err != nil {
		fmt.Fprintf(stderr, "access grant create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created access grant %s (%s → %s, expires %s).\nGrant ID: %s\n",
		created.principal(), created.principal(), created.target(), created.ExpireTime, created.ID)
	return 0
}

// --- List ---

func runAccessGrantList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("access grant list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := accessGrantWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "access grant list accepts no positional arguments")
		return 2
	}
	ws, ok := requireAccessGrantWorkspace("list", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openAccessGrantSession("list", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, err := session.do(http.MethodGet, "", nil)
	if err != nil {
		fmt.Fprintf(stderr, "access grant list: %v\n", err)
		return 1
	}
	var allGrants []accessGrant
	if err := json.Unmarshal(payload, &allGrants); err != nil {
		fmt.Fprintf(stderr, "access grant list: decode response: %v\n", err)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(allGrants)
		return 0
	}
	if len(allGrants) == 0 {
		fmt.Fprintln(stdout, "No access grants found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tPRINCIPAL\tTARGET\tREASON\tEXPIRES")
	for _, g := range allGrants {
		r := ""
		if g.Reason != nil {
			r = *g.Reason
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", g.ID, g.principal(), g.target(), r, g.ExpireTime)
	}
	_ = table.Flush()
	return 0
}

// --- Get ---

func runAccessGrantGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("access grant get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := accessGrantWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if id == "" && len(rest) > 0 {
		id, rest = rest[0], rest[1:]
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		rest = flags.Args()
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "access grant get: unexpected argument %q\n", rest[0])
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "access grant get requires a grant ID")
		return 2
	}
	ws, ok := requireAccessGrantWorkspace("get", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openAccessGrantSession("get", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, err := session.do(http.MethodGet, "/"+url.PathEscape(id), nil)
	if err != nil {
		fmt.Fprintf(stderr, "access grant get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var g accessGrant
	if err := json.Unmarshal(payload, &g); err != nil {
		fmt.Fprintf(stderr, "access grant get: decode response: %v\n", err)
		return 1
	}
	r := ""
	if g.Reason != nil {
		r = *g.Reason
	}
	fmt.Fprintf(stdout, "ID:          %s\nPrincipal:   %s\nTarget:      %s\nExpires:     %s\n", g.ID, g.principal(), g.target(), g.ExpireTime)
	if r != "" {
		fmt.Fprintf(stdout, "Reason:      %s\n", r)
	}
	fmt.Fprintf(stdout, "Created:     %s\n", g.CreatedAt)
	if g.RevokedAt != nil {
		fmt.Fprintf(stdout, "Revoked:     %s\n", *g.RevokedAt)
	}
	return 0
}

// --- Revoke ---

func runAccessGrantRevoke(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("access grant revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := accessGrantWorkspaceFlag(flags)
	yes := flags.Bool("yes", false, "confirm revocation")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if id == "" && len(rest) > 0 {
		id, rest = rest[0], rest[1:]
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		rest = flags.Args()
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "access grant revoke: unexpected argument %q\n", rest[0])
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "access grant revoke requires a grant ID")
		return 2
	}
	ws, ok := requireAccessGrantWorkspace("revoke", *workspace, stderr)
	if !ok {
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "access grant revoke revokes the grant immediately; pass --yes to confirm")
		return 2
	}
	session, ok := openAccessGrantSession("revoke", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, err := session.do(http.MethodPost, "/"+url.PathEscape(id)+":revoke", nil)
	if err != nil {
		fmt.Fprintf(stderr, "access grant revoke: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var g accessGrant
	if err := json.Unmarshal(payload, &g); err != nil {
		fmt.Fprintf(stderr, "access grant revoke: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Access grant %s revoked.\n", g.ID)
	return 0
}
