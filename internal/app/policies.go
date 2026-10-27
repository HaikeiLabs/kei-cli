package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
	"github.com/google/uuid"
)

type policy struct {
	ID               string  `json:"id"`
	OrgID            string  `json:"org_id"`
	WorkspaceID      string  `json:"workspace_id"`
	AgentID          *string `json:"agent_id,omitempty"`
	Name             string  `json:"name"`
	Description      string  `json:"description,omitempty"`
	SrcPattern       string  `json:"src_pattern"`
	DstPattern       string  `json:"dst_pattern"`
	Effect           string  `json:"effect"`
	Enabled          bool    `json:"enabled"`
	ApprovalRequired bool    `json:"approval_required"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type createPolicyRequest struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	SrcPattern  string                  `json:"src_pattern"`
	DstPattern  string                  `json:"dst_pattern"`
	Effect      string                  `json:"effect"`
	Action      string                  `json:"action,omitempty"`
	Enabled     bool                    `json:"enabled"`
	SelectorV2  *createPolicySelectorV2 `json:"selector_v2,omitempty"`
	// AgentID scopes the policy to one agent; omitted, it applies to every
	// agent in the workspace.
	AgentID *string `json:"agent_id,omitempty"`
}

type createPolicySelectorV2 struct {
	Source      string `json:"source,omitempty"`
	ConnectorID string `json:"connector_id,omitempty"`
	Capability  string `json:"capability,omitempty"`
}

type updatePolicyRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	SrcPattern  *string `json:"src_pattern,omitempty"`
	DstPattern  *string `json:"dst_pattern,omitempty"`
	Effect      *string `json:"effect,omitempty"`
	Action      *string `json:"action,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	// AgentID is the JSON for agent_id: a quoted UUID, or null to clear the
	// agent. Nil leaves it out of the body.
	AgentID json.RawMessage `json:"agent_id,omitempty"`
}

type migrationReportResponse struct {
	StandingPrincipals []struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		SrcPattern    string   `json:"src_pattern"`
		DstPattern    string   `json:"dst_pattern"`
		Action        string   `json:"action"`
		ProposedGroup string   `json:"proposed_group"`
		Members       []string `json:"members"`
		Conversion    string   `json:"conversion"`
	} `json:"standing_principals"`
	WildcardRoles []struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		SrcPattern         string `json:"src_pattern"`
		DstPattern         string `json:"dst_pattern"`
		Action             string `json:"action"`
		ProposedSource     string `json:"proposed_source"`
		NeedsOwnerDecision bool   `json:"needs_owner_decision"`
		Note               string `json:"note,omitempty"`
	} `json:"wildcard_roles"`
	HarnessSources []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		SrcPattern  string `json:"src_pattern"`
		DstPattern  string `json:"dst_pattern"`
		Action      string `json:"action"`
		HarnessKind string `json:"harness_kind"`
		Condition   string `json:"condition"`
	} `json:"harness_sources"`
	DenyWinsImpact []struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		SrcPattern   string   `json:"src_pattern"`
		DstPattern   string   `json:"dst_pattern"`
		Action       string   `json:"action"`
		Priority     int      `json:"priority"`
		OldDecision  string   `json:"old_decision"`
		NewDecision  string   `json:"new_decision"`
		OldOutranked []string `json:"old_outranked"`
	} `json:"deny_wins_impact"`
	RejectedRows []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		SrcPattern   string `json:"src_pattern"`
		DstPattern   string `json:"dst_pattern"`
		Action       string `json:"action"`
		RejectReason string `json:"reject_reason"`
	} `json:"rejected_rows"`
	Markdown string `json:"markdown"`
}

type policySession struct {
	client      *http.Client
	baseURL     string
	token       string
	workspaceID string
}

func openPolicySession(command string, sel workspaceSelection, stderr io.Writer, client *http.Client, store credentialStore) (*policySession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	var workspaceID string
	var err error
	if sel.pickDefault {
		workspaceID, err = resolveWorkspaceSelection(context.Background(), client, baseURL, token, sel)
	} else {
		workspaceID, err = resolveScopedWorkspace(context.Background(), client, baseURL, token, sel)
	}
	if err != nil {
		fmt.Fprintf(stderr, "policies %s: %v\n", command, err)
		return nil, false
	}
	return &policySession{client: client, baseURL: baseURL, token: token, workspaceID: workspaceID}, true
}

func (s *policySession) do(method, path string, body any, extraQuery url.Values) ([]byte, error) {
	params := url.Values{"workspace_id": {s.workspaceID}}
	for k, vs := range extraQuery {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	endpoint := s.baseURL + "/api/v1/policies" + path + "?" + params.Encode()
	return s.request(method, endpoint, body)
}

func (s *policySession) request(method, endpoint string, body any) ([]byte, error) {
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
		return nil, withCLIUpgradeHint(s.client, fmt.Errorf("%s (HTTP %d)", policyErrorMessage(payload), resp.StatusCode))
	}
	return payload, nil
}

func policyErrorMessage(payload []byte) string {
	return apiErrorMessage(payload)
}

var policyGroup = commandGroup{
	name: "policy",
	commands: []groupCommand{
		{name: "list", summary: "List policies"},
		{name: "get", summary: "Get a policy by ID"},
		{name: "create", summary: "Create a policy"},
		{name: "delete", summary: "Delete a policy"},
		{name: "test", summary: "Test a policy"},
		{name: "migration-report", summary: "Show ADR-038 policy migration report (dry run)"},
	},
}

func runPolicyCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := policyGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runPoliciesList("policy", rest, stdout, stderr, client, store)
	case "get":
		return runPoliciesGet("policy", rest, stdout, stderr, client, store)
	case "create":
		return runPolicyCreate(rest, stdout, stderr, client, store)
	case "delete":
		return runPoliciesDelete("policy", rest, stdout, stderr, client, store)
	case "test":
		return runPolicyTest(rest, stdout, stderr, client, store)
	case "migration-report":
		return runPolicyMigrationReport(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

var policiesGroup = commandGroup{
	name: "policies",
	commands: []groupCommand{
		{name: "list", summary: "List policies"},
		{name: "get", summary: "Get a policy by ID"},
		{name: "create", summary: "Create a policy"},
		{name: "update", summary: "Update a policy"},
		{name: "delete", summary: "Delete a policy"},
		{name: "import", summary: "Import policies from a file"},
	},
}

func runPoliciesCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	fmt.Fprintln(stderr, "Deprecated: use `kei policy` instead of `kei policies`.")
	sub, rest, code, handled := policiesGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runPoliciesList("policies", rest, stdout, stderr, client, store)
	case "get":
		return runPoliciesGet("policies", rest, stdout, stderr, client, store)
	case "create":
		return runPoliciesCreate(rest, stdout, stderr, client, store)
	case "update":
		return runPoliciesUpdate(rest, stdout, stderr, client, store)
	case "delete":
		return runPoliciesDelete("policies", rest, stdout, stderr, client, store)
	case "import":
		return runPoliciesImport(rest, stdout, stderr, stdin, client, store, harness.Default)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func requirePoliciesWorkspace(command, workspace string, stderr io.Writer) (workspaceSelection, bool) {
	sel, err := selectWorkspace(workspace, keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "policies %s: %v\n", command, err)
		return workspaceSelection{}, false
	}
	if sel.fromIDEnv {
		fmt.Fprintln(stderr, deprecationNote)
	}
	return sel, true
}

func parsePolicyFlags(flags *flag.FlagSet, args []string) (string, error) {
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

func requirePolicyID(command, id string, stderr io.Writer) bool {
	if id == "" {
		fmt.Fprintf(stderr, "policies %s requires a policy ID or name\n", command)
		return false
	}
	return true
}

// resolvePolicyID returns the resolved UUID for id. If id is already a valid
// UUID it is returned as-is. Otherwise id is treated as a policy name and all
// policies in the workspace are listed to find a matching name.
func resolvePolicyID(session *policySession, id string) (string, error) {
	if _, err := uuid.Parse(id); err == nil {
		return id, nil
	}
	var allPolicies []policy
	pageToken := ""
	for {
		extraQuery := url.Values{"page_size": {"200"}}
		if pageToken != "" {
			extraQuery.Set("page_token", pageToken)
		}
		payload, err := session.do(http.MethodGet, "", nil, extraQuery)
		if err != nil {
			return "", err
		}
		page, err := decodePoliciesPage(payload)
		if err != nil {
			return "", err
		}
		allPolicies = append(allPolicies, page.Policies...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	var matches []policy
	for _, p := range allPolicies {
		if p.Name == id {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no policy found with name %q", id)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple policies found with name %q; use ID instead", id)
	}
	return matches[0].ID, nil
}

type policiesPage struct {
	Policies      []policy `json:"policies"`
	NextPageToken string   `json:"next_page_token"`
}

// decodePoliciesPage decodes a list-policies response that may be either the
// standard AIP-wrapped object or a legacy bare JSON array.
func decodePoliciesPage(payload []byte) (policiesPage, error) {
	var page policiesPage
	if err := json.Unmarshal(payload, &page); err == nil {
		return page, nil
	}
	var policies []policy
	if err := json.Unmarshal(payload, &policies); err != nil {
		return policiesPage{}, err
	}
	return policiesPage{Policies: policies}, nil
}

func runPoliciesList(group string, args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags(group, "list", "List policies", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	pageSize := lc.fs.Int("page-size", 100, "page size")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "policies list accepts no positional arguments")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("list", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openPolicySession("list", ws, stderr, client, store)
	if !ok {
		return 1
	}
	var allPolicies []policy
	pageToken := ""
	for {
		extraQuery := url.Values{"page_size": {strconv.Itoa(*pageSize)}}
		if pageToken != "" {
			extraQuery.Set("page_token", pageToken)
		}
		payload, err := session.do(http.MethodGet, "", nil, extraQuery)
		if err != nil {
			fmt.Fprintf(stderr, "policies list: %v\n", err)
			return 1
		}
		page, err := decodePoliciesPage(payload)
		if err != nil {
			fmt.Fprintf(stderr, "policies list: decode response: %v\n", err)
			return 1
		}
		allPolicies = append(allPolicies, page.Policies...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(allPolicies)
		return 0
	}
	if len(allPolicies) == 0 {
		fmt.Fprintln(stdout, "No policies found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tNAME\tSOURCE\tTARGET\tEFFECT\tENABLED")
	for _, p := range allPolicies {
		enabled := "no"
		if p.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.SrcPattern, p.DstPattern, p.Effect, enabled)
	}
	_ = table.Flush()
	return 0
}

func runPoliciesGet(group string, args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags(group, "get", "Get a policy by ID", stdout, stderr).WithArgs("ID")
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
		fmt.Fprintln(stderr, "policies get requires a policy ID")
		return 2
	}
	id := pos[0]
	ws, ok := requirePoliciesWorkspace("get", *workspace, stderr)
	if !ok || !requirePolicyID("get", id, stderr) {
		return 2
	}
	session, ok := openPolicySession("get", ws, stderr, client, store)
	if !ok {
		return 1
	}
	resolvedID, err := resolvePolicyID(session, id)
	if err != nil {
		fmt.Fprintf(stderr, "policies get: %v\n", err)
		return 1
	}
	payload, err := session.do(http.MethodGet, "/"+url.PathEscape(resolvedID), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "policies get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var p policy
	if err := json.Unmarshal(payload, &p); err != nil {
		fmt.Fprintf(stderr, "policies get: decode response: %v\n", err)
		return 1
	}
	enabled := "no"
	if p.Enabled {
		enabled = "yes"
	}
	fmt.Fprintf(stdout, "ID:          %s\nName:        %s\nSource:      %s\nTarget:      %s\nEffect:      %s\nEnabled:     %s\n", p.ID, p.Name, p.SrcPattern, p.DstPattern, p.Effect, enabled)
	if p.Description != "" {
		fmt.Fprintf(stdout, "Description: %s\n", p.Description)
	}
	if p.AgentID != nil {
		fmt.Fprintf(stdout, "Agent:       %s\n", *p.AgentID)
	}
	fmt.Fprintf(stdout, "Created:     %s\nUpdated:     %s\n", p.CreatedAt, p.UpdatedAt)
	return 0
}

func runPoliciesCreate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("policies", "create", "Create a policy", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	name := lc.fs.String("name", "", "policy name")
	srcPattern := lc.fs.String("src-pattern", "", "source pattern (e.g. harness:claude_code)")
	dstPattern := lc.fs.String("dst-pattern", "", "target pattern (e.g. shell:git, skill:read, path:/workspace/.env)")
	effect := lc.fs.String("effect", "", "effect: permit or deny")
	lc.fs.Int("priority", 0, "deprecated and ignored: deny always wins")
	description := lc.fs.String("description", "", "policy description")
	agentID := lc.fs.String("agent-id", "", "agent ID to scope the policy to (default: every agent in the workspace)")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	createSet := make(map[string]bool)
	lc.fs.Visit(func(f *flag.Flag) { createSet[f.Name] = true })
	warnPriorityIgnored(createSet, stderr)
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "policies create accepts no positional arguments")
		return 2
	}
	if *name == "" || *srcPattern == "" || *dstPattern == "" || *effect == "" {
		fmt.Fprintln(stderr, "policies create requires --name, --src-pattern, --dst-pattern, and --effect")
		return 2
	}
	if *effect != "permit" && *effect != "deny" {
		fmt.Fprintln(stderr, "policies create: --effect must be permit or deny")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	var agent *string
	if *agentID != "" {
		if _, err := uuid.Parse(*agentID); err != nil {
			fmt.Fprintln(stderr, "policies create: --agent-id must be a UUID")
			return 2
		}
		agent = agentID
	}
	session, ok := openPolicySession("create", ws, stderr, client, store)
	if !ok {
		return 1
	}
	request := createPolicyRequest{
		Name:        *name,
		Description: *description,
		SrcPattern:  *srcPattern,
		DstPattern:  *dstPattern,
		Effect:      *effect,
		Action:      *effect,
		Enabled:     true,
		AgentID:     agent,
	}
	payload, err := session.do(http.MethodPost, "", request, nil)
	if err != nil {
		fmt.Fprintf(stderr, "policies create: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var created policy
	if err := json.Unmarshal(payload, &created); err != nil {
		fmt.Fprintf(stderr, "policies create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created policy %s (%s → %s, %s).\nPolicy ID: %s\n", created.Name, created.SrcPattern, created.DstPattern, created.Effect, created.ID)
	return 0
}

// standingRuleSource validates --group for a standing rule and returns the
// policy src_pattern. Standing rules apply to groups and autogroups; per-user
// sources belong to access grants instead.
func standingRuleSource(group string) (string, error) {
	g := strings.TrimSpace(group)
	if g == "" {
		return "", fmt.Errorf("--group is required")
	}
	for _, prefix := range []string{"user:", "email:", "agent:"} {
		if strings.HasPrefix(g, prefix) {
			return "", fmt.Errorf("standing rules apply only to groups or autogroups; %s sources need `kei access grant`", prefix)
		}
	}
	if strings.HasPrefix(g, "autogroup:") {
		return g, nil
	}
	return "group:" + strings.TrimPrefix(g, "group:"), nil
}

func runPolicyCreate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("policy", "create", "Create a standing rule", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	group := lc.fs.String("group", "", "group or autogroup the rule applies to")
	allow := lc.fs.String("allow", "", "target to permit")
	deny := lc.fs.String("deny", "", "target to deny")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "policy create accepts no positional arguments")
		return 2
	}
	src, err := standingRuleSource(*group)
	if err != nil {
		fmt.Fprintf(stderr, "policy create: %v\n", err)
		return 2
	}
	var target, effect string
	switch {
	case *allow != "" && *deny != "":
		fmt.Fprintln(stderr, "policy create: pass exactly one of --allow or --deny")
		return 2
	case *deny != "":
		target, effect = *deny, "deny"
	case *allow != "":
		target, effect = *allow, "permit"
	default:
		fmt.Fprintln(stderr, "policy create requires --allow or --deny")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("create", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openPolicySession("create", ws, stderr, client, store)
	if !ok {
		return 1
	}
	request := createPolicyRequest{
		Name:       target + " / " + src,
		SrcPattern: src,
		DstPattern: target,
		Effect:     effect,
		Enabled:    true,
	}
	payload, err := session.do(http.MethodPost, "", request, nil)
	if err != nil {
		fmt.Fprintf(stderr, "policy create: %v\n", err)
		return 1
	}
	var created policy
	if err := json.Unmarshal(payload, &created); err != nil {
		fmt.Fprintf(stderr, "policy create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created policy %s (%s → %s, %s).\nPolicy ID: %s\n", created.Name, created.SrcPattern, created.DstPattern, created.Effect, created.ID)
	return 0
}

func runPolicyTest(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("policy", "test", "Test a policy", stdout, stderr).WithArgs("TARGET")
	workspace := workspaceFlag(lc.fs)
	as := lc.fs.String("as", "", "subject to evaluate (e.g. group:Engineering)")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if *as == "" || lc.fs.NArg() != 1 {
		fmt.Fprintln(stderr, `policy test requires --as <who> and one target, e.g. kei policy test --as group:Engineering "shell:git"`)
		return 2
	}
	target := lc.fs.Arg(0)
	ws, ok := requirePoliciesWorkspace("test", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openPolicySession("test", ws, stderr, client, store)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(session.token)
	if err != nil {
		fmt.Fprintf(stderr, "policy test: determine organization from login: %v\n", err)
		return 1
	}
	endpoint := session.baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) +
		"/workspaces/" + url.PathEscape(session.workspaceID) + "/policies:test"
	payload, err := session.request(http.MethodPost, endpoint, map[string]string{"as": *as, "target": target})
	if err != nil {
		fmt.Fprintf(stderr, "policy test: %v\n", err)
		return 1
	}
	var result struct {
		Decision   string  `json:"decision"`
		DecidedBy  string  `json:"decided_by"`
		ReasonCode *string `json:"reason_code"`
		Hint       *string `json:"hint"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		fmt.Fprintf(stderr, "policy test: decode response: %v\n", err)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	fmt.Fprintf(stdout, "Decision: %s\n", result.Decision)
	if result.DecidedBy == "" {
		fmt.Fprintln(stdout, "Deciding rule: (none)")
	} else {
		fmt.Fprintf(stdout, "Deciding rule: %s\n", result.DecidedBy)
	}
	if result.ReasonCode != nil && *result.ReasonCode != "" {
		fmt.Fprintf(stdout, "Reason code: %s\n", *result.ReasonCode)
	}
	if result.Hint != nil && *result.Hint != "" {
		fmt.Fprintln(stdout, "Hint:")
		fmt.Fprintln(stdout, *result.Hint)
	}
	return 0
}

func runPolicyMigrationReport(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("policy", "migration-report", "Show ADR-038 policy migration report (dry run)", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "policy migration-report accepts no positional arguments")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("migration-report", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openPolicySession("migration-report", ws, stderr, client, store)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(session.token)
	if err != nil {
		fmt.Fprintf(stderr, "policy migration-report: determine organization from login: %v\n", err)
		return 1
	}
	endpoint := session.baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) +
		"/workspaces/" + url.PathEscape(session.workspaceID) + "/policies:migrationReport"
	payload, err := session.request(http.MethodPost, endpoint, nil)
	if err != nil {
		fmt.Fprintf(stderr, "policy migration-report: %v\n", err)
		return 1
	}

	var resp migrationReportResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		fmt.Fprintf(stderr, "policy migration-report: decode response: %v\n", err)
		return 1
	}

	if *jsonOutput {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, payload, "", "  "); err != nil {
			_, _ = stdout.Write(payload)
			return 0
		}
		_, _ = pretty.WriteTo(stdout)
		return 0
	}

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(table, "%s\t%d\n", "Standing principals to autogroups:", len(resp.StandingPrincipals))
	fmt.Fprintf(table, "%s\t%d\n", "Wildcard/role to autogroups:", len(resp.WildcardRoles))
	fmt.Fprintf(table, "%s\t%d\n", "Harness scoping to conditions:", len(resp.HarnessSources))
	fmt.Fprintf(table, "%s\t%d\n", "Deny-wins outcome changes:", len(resp.DenyWinsImpact))
	fmt.Fprintf(table, "%s\t%d\n", "Rows that would be rejected:", len(resp.RejectedRows))
	_ = table.Flush()

	total := len(resp.StandingPrincipals) + len(resp.WildcardRoles) + len(resp.HarnessSources) + len(resp.DenyWinsImpact) + len(resp.RejectedRows)
	fmt.Fprintf(stdout, "\nTotal rows in report: %d\n", total)
	fmt.Fprintln(stdout, "This is a dry run. Nothing has been migrated or written.")
	return 0
}

func runPoliciesUpdate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("policies", "update", "Update a policy", stdout, stderr).WithArgs("ID")
	workspace := workspaceFlag(lc.fs)
	name := lc.fs.String("name", "", "policy name")
	srcPattern := lc.fs.String("src-pattern", "", "source pattern")
	dstPattern := lc.fs.String("dst-pattern", "", "target pattern")
	effect := lc.fs.String("effect", "", "effect: permit or deny")
	lc.fs.Int("priority", 0, "deprecated and ignored: deny always wins")
	enabled := lc.fs.Bool("enabled", false, "enabled")
	description := lc.fs.String("description", "", "policy description")
	agentID := lc.fs.String("agent-id", "", "agent ID to scope the policy to; an empty value clears it")
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
		fmt.Fprintln(stderr, "policies update requires a policy ID")
		return 2
	}
	id := pos[0]
	ws, ok := requirePoliciesWorkspace("update", *workspace, stderr)
	if !ok || !requirePolicyID("update", id, stderr) {
		return 2
	}
	if *effect != "" && *effect != "permit" && *effect != "deny" {
		fmt.Fprintln(stderr, "policies update: --effect must be permit or deny")
		return 2
	}
	set := make(map[string]bool)
	lc.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	warnPriorityIgnored(set, stderr)
	request := updatePolicyRequest{}
	if set["name"] {
		request.Name = name
	}
	if set["src-pattern"] {
		request.SrcPattern = srcPattern
	}
	if set["dst-pattern"] {
		request.DstPattern = dstPattern
	}
	if set["effect"] {
		request.Effect = effect
		request.Action = effect
	}
	if set["enabled"] {
		request.Enabled = enabled
	}
	if set["description"] {
		request.Description = description
	}
	if set["agent-id"] {
		request.AgentID = json.RawMessage("null")
		if *agentID != "" {
			if _, err := uuid.Parse(*agentID); err != nil {
				fmt.Fprintln(stderr, "policies update: --agent-id must be a UUID (or empty to clear it)")
				return 2
			}
			request.AgentID, _ = json.Marshal(*agentID)
		}
	}
	if request.Name == nil && request.Description == nil && request.SrcPattern == nil && request.DstPattern == nil && request.Effect == nil && request.Enabled == nil && request.AgentID == nil {
		fmt.Fprintln(stderr, "policies update requires at least one field to update: --name, --src-pattern, --dst-pattern, --effect, --enabled, --description, or --agent-id")
		return 2
	}
	session, ok := openPolicySession("update", ws, stderr, client, store)
	if !ok {
		return 1
	}
	resolvedID, err := resolvePolicyID(session, id)
	if err != nil {
		fmt.Fprintf(stderr, "policies update: %v\n", err)
		return 1
	}
	var maskFields []string
	if set["name"] {
		maskFields = append(maskFields, "name")
	}
	if set["description"] {
		maskFields = append(maskFields, "description")
	}
	if set["src-pattern"] {
		maskFields = append(maskFields, "src_pattern")
	}
	if set["dst-pattern"] {
		maskFields = append(maskFields, "dst_pattern")
	}
	if set["effect"] {
		maskFields = append(maskFields, "effect")
	}
	if set["enabled"] {
		maskFields = append(maskFields, "enabled")
	}
	if set["agent-id"] {
		maskFields = append(maskFields, "agent_id")
	}
	extraQuery := url.Values{}
	if len(maskFields) > 0 {
		extraQuery.Set("update_mask", strings.Join(maskFields, ","))
	}
	payload, err := session.do(http.MethodPatch, "/"+url.PathEscape(resolvedID), request, extraQuery)
	if err != nil {
		fmt.Fprintf(stderr, "policies update: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var updated policy
	if err := json.Unmarshal(payload, &updated); err != nil {
		fmt.Fprintf(stderr, "policies update: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated policy %s (%s).\n", updated.ID, updated.Name)
	return 0
}

func runPoliciesDelete(group string, args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags(group, "delete", "Delete a policy", stdout, stderr).WithArgs("ID")
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
		fmt.Fprintln(stderr, "policies delete requires a policy ID")
		return 2
	}
	id := pos[0]
	ws, ok := requirePoliciesWorkspace("delete", *workspace, stderr)
	if !ok || !requirePolicyID("delete", id, stderr) {
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "policies delete removes the policy for all agents; pass --yes to confirm")
		return 2
	}
	session, ok := openPolicySession("delete", ws, stderr, client, store)
	if !ok {
		return 1
	}
	resolvedID, err := resolvePolicyID(session, id)
	if err != nil {
		fmt.Fprintf(stderr, "policies delete: %v\n", err)
		return 1
	}
	if _, err := session.do(http.MethodDelete, "/"+url.PathEscape(resolvedID), nil, nil); err != nil {
		fmt.Fprintf(stderr, "policies delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Policy %s deleted.\n", resolvedID)
	return 0
}

type importedPolicy struct {
	Name       string `json:"name"`
	SrcPattern string `json:"src_pattern"`
	DstPattern string `json:"dst_pattern"`
	Effect     string `json:"effect"`
}

func runPoliciesImport(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore, registry *harness.Registry) int {
	lc := newCommandFlags("policies", "import", "Import policies from a harness", stdout, stderr)
	workspace := workspaceFlag(lc.fs)
	importNames := registry.ImportNames()
	from := lc.fs.String("from", "", "harness to import from: "+harness.JoinOr(importNames))
	file := lc.fs.String("file", "", "override the source file or directory")
	src := lc.fs.String("src", "", "source pattern: harness:<kind> (only that harness) or harness:* (every harness)")
	out := lc.fs.String("out", "", "write the proposed policy-set JSON to this file")
	apply := lc.fs.Bool("apply", false, "create the imported policies (default: dry run)")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "policies import accepts no positional arguments")
		return 2
	}
	if *from == "" {
		fmt.Fprintf(stderr, "policies import requires --from %s\n", strings.Join(importNames, "|"))
		return 2
	}
	source, ok := registry.Importer(*from)
	if !ok {
		fmt.Fprintf(stderr, "policies import: --from must be %s\n", harness.JoinOr(importNames))
		return 2
	}
	if *src == "" {
		fmt.Fprintln(stderr, "policies import requires --src harness:<kind> (only that harness) or harness:* (every harness)")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("import", *workspace, stderr)
	if !ok {
		return 2
	}
	srcPattern := *src
	policies, err := importPolicies(source, *file, srcPattern, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "policies import: %v\n", err)
		return 1
	}
	if len(policies) == 0 {
		fmt.Fprintln(stdout, "No policies found to import.")
		return 0
	}
	reader := bufio.NewReader(stdin)
	printImportScope(stdout, srcPattern)
	if *out != "" {
		encoded, err := json.MarshalIndent(policies, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "policies import: encode JSON: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*out, encoded, 0o644); err != nil {
			fmt.Fprintf(stderr, "policies import: write %s: %v\n", *out, err)
			return 1
		}
		fmt.Fprintf(stdout, "Wrote %d policies to %s\n", len(policies), *out)
	}
	fmt.Fprintf(stdout, "Importing %d policies from %s:\n\n", len(policies), *from)
	for _, p := range policies {
		fmt.Fprintf(stdout, "  %-20s %-20s %-40s %s\n", p.Name, p.SrcPattern, p.DstPattern, p.Effect)
	}
	fmt.Fprintln(stdout)
	if !*apply {
		fmt.Fprintln(stdout, "Dry run. Pass --apply to create these policies.")
		return 0
	}
	fmt.Fprintf(stdout, "Create %d policies? [y/N] ", len(policies))
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	if answer != "y" && answer != "yes" {
		fmt.Fprintln(stdout, "Import aborted; no policies were created.")
		return 0
	}
	session, ok := openPolicySession("import", ws, stderr, client, store)
	if !ok {
		return 1
	}
	created := 0
	for _, p := range policies {
		request := createPolicyRequest{
			Name:       p.Name,
			SrcPattern: p.SrcPattern,
			DstPattern: p.DstPattern,
			Effect:     p.Effect,
			Action:     p.Effect,
			Enabled:    true,
		}
		if _, err := session.do(http.MethodPost, "", request, nil); err != nil {
			fmt.Fprintf(stderr, "policies import: failed to create %s: %v\n", p.Name, err)
			fmt.Fprintf(stdout, "Created %d policies.\n", created)
			return 1
		}
		created++
	}
	fmt.Fprintf(stdout, "Created %d policies.\n", created)
	return 0
}

// printImportScope states which harnesses the imported policies will apply to.
func printImportScope(stdout io.Writer, srcPattern string) {
	switch {
	case srcPattern == "harness:*" || srcPattern == "*":
		fmt.Fprintf(stdout, "Scope: src %s (shared with every harness)\n", srcPattern)
	case strings.HasPrefix(srcPattern, "harness:"):
		fmt.Fprintf(stdout, "Scope: src %s (only that harness; pass --src harness:* to share these rules with every harness)\n", srcPattern)
	default:
		fmt.Fprintf(stdout, "Scope: src %s\n", srcPattern)
	}
}

// importPolicies reads h's native rules and maps each onto a policy with src
// srcPattern, reporting the entries that have no Kei equivalent.
func importPolicies(h harness.Harness, fileOverride, srcPattern string, stderr io.Writer) ([]importedPolicy, error) {
	rules, err := h.ImportRules(harness.OSEnv(), fileOverride)
	if err != nil {
		return nil, err
	}
	var policies []importedPolicy
	for _, rule := range rules {
		if rule.SkipReason != "" {
			fmt.Fprintf(stderr, "policies import: skipping %q (%s)\n", rule.Native, rule.SkipReason)
			continue
		}
		policies = append(policies, importedPolicy{Name: rule.Name, SrcPattern: srcPattern, DstPattern: rule.Dst, Effect: rule.Effect})
	}
	return policies, nil
}

// warnPriorityIgnored tells a caller that still passes --priority that the
// catalog no longer uses it (ADR-038 D-1: deny wins, no priority).
func warnPriorityIgnored(set map[string]bool, stderr io.Writer) {
	if set["priority"] {
		fmt.Fprintln(stderr, "warning: --priority is deprecated and ignored: a matching deny always wins")
	}
}
