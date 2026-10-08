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
	"golang.org/x/term"
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
	Priority         int     `json:"priority"`
	Enabled          bool    `json:"enabled"`
	ApprovalRequired bool    `json:"approval_required"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// UnmarshalJSON supports both the AIP shape ("effect") and the legacy shape
// ("action") returned when the console does not forward X-Kei-API-Shape.
func (p *policy) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               string  `json:"id"`
		OrgID            string  `json:"org_id"`
		WorkspaceID      string  `json:"workspace_id"`
		AgentID          *string `json:"agent_id,omitempty"`
		Name             string  `json:"name"`
		Description      string  `json:"description,omitempty"`
		SrcPattern       string  `json:"src_pattern"`
		DstPattern       string  `json:"dst_pattern"`
		Effect           string  `json:"effect"`
		Action           string  `json:"action"`
		Priority         int     `json:"priority"`
		Enabled          bool    `json:"enabled"`
		ApprovalRequired bool    `json:"approval_required"`
		CreatedAt        string  `json:"created_at"`
		UpdatedAt        string  `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.ID = raw.ID
	p.OrgID = raw.OrgID
	p.WorkspaceID = raw.WorkspaceID
	p.AgentID = raw.AgentID
	p.Name = raw.Name
	p.Description = raw.Description
	p.SrcPattern = raw.SrcPattern
	p.DstPattern = raw.DstPattern
	p.Priority = raw.Priority
	p.Enabled = raw.Enabled
	p.ApprovalRequired = raw.ApprovalRequired
	p.CreatedAt = raw.CreatedAt
	p.UpdatedAt = raw.UpdatedAt
	if raw.Effect != "" {
		p.Effect = raw.Effect
	} else {
		p.Effect = raw.Action
	}
	return nil
}

type createPolicyRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	SrcPattern  string  `json:"src_pattern"`
	DstPattern  string  `json:"dst_pattern"`
	Effect      string  `json:"effect"`
	Action      string  `json:"action"`
	Priority    int     `json:"priority"`
	Enabled     bool    `json:"enabled"`
	AgentID     *string `json:"agent_id,omitempty"`
}

type updatePolicyRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	SrcPattern  *string `json:"src_pattern,omitempty"`
	DstPattern  *string `json:"dst_pattern,omitempty"`
	Effect      *string `json:"effect,omitempty"`
	Action      *string `json:"action,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

type policySession struct {
	client      *http.Client
	baseURL     string
	token       string
	workspaceID string
}

func openPolicySession(command, workspace string, stderr io.Writer, client *http.Client, store credentialStore) (*policySession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	workspaceID, err := resolveWorkspaceID(context.Background(), client, baseURL, token, workspace)
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
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := s.baseURL + "/api/v1/policies" + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("X-Kei-API-Shape", "aip")
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
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s (HTTP %d)", policyErrorMessage(payload), resp.StatusCode)
	}
	return payload, nil
}

func policyErrorMessage(payload []byte) string {
	var aip struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &aip) == nil && aip.Message != "" {
		if aip.Reason != "" {
			return aip.Reason + ": " + aip.Message
		}
		return aip.Message
	}
	var structured struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &structured) == nil && structured.Error.Message != "" {
		return structured.Error.Message
	}
	if message := strings.TrimSpace(string(payload)); message != "" {
		return message
	}
	return "request failed"
}

func runPoliciesCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "policies requires a subcommand: list, get, create, update, delete, or import")
		return 2
	}
	switch args[0] {
	case "list":
		return runPoliciesList(args[1:], stdout, stderr, client, store)
	case "get":
		return runPoliciesGet(args[1:], stdout, stderr, client, store)
	case "create":
		return runPoliciesCreate(args[1:], stdout, stderr, client, store)
	case "update":
		return runPoliciesUpdate(args[1:], stdout, stderr, client, store)
	case "delete":
		return runPoliciesDelete(args[1:], stdout, stderr, client, store)
	case "import":
		return runPoliciesImport(args[1:], stdout, stderr, stdin, client, store, harness.Default)
	default:
		fmt.Fprintf(stderr, "unknown policies command %q\n", args[0])
		return 2
	}
}

func policiesWorkspaceFlag(flags *flag.FlagSet) *string {
	return flags.String("workspace", "", "workspace ID or name (or set KEI_WORKSPACE_ID)")
}

func requirePoliciesWorkspace(command, workspace string, stderr io.Writer) (string, bool) {
	if workspace == "" {
		workspace = os.Getenv("KEI_WORKSPACE_ID")
	}
	if workspace == "" {
		fmt.Fprintf(stderr, "policies %s requires --workspace WORKSPACE (or KEI_WORKSPACE_ID)\n", command)
		return "", false
	}
	return workspace, true
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

func runPoliciesList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("policies list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	pageSize := flags.Int("page-size", 100, "page size")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
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
	fmt.Fprintln(table, "ID\tNAME\tSOURCE\tTARGET\tEFFECT\tPRIORITY\tENABLED")
	for _, p := range allPolicies {
		enabled := "no"
		if p.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", p.ID, p.Name, p.SrcPattern, p.DstPattern, p.Effect, p.Priority, enabled)
	}
	_ = table.Flush()
	return 0
}

func runPoliciesGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("policies get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "policies get requires a policy ID")
		return 2
	}
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
	fmt.Fprintf(stdout, "ID:          %s\nName:        %s\nSource:      %s\nTarget:      %s\nEffect:      %s\nPriority:    %d\nEnabled:     %s\n", p.ID, p.Name, p.SrcPattern, p.DstPattern, p.Effect, p.Priority, enabled)
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
	flags := flag.NewFlagSet("policies create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	name := flags.String("name", "", "policy name")
	srcPattern := flags.String("src-pattern", "", "source pattern (e.g. harness:claude)")
	dstPattern := flags.String("dst-pattern", "", "target pattern (e.g. shell:git, skill:read, path:*.env)")
	effect := flags.String("effect", "", "effect: permit or deny")
	priority := flags.Int("priority", 0, "priority (higher = more specific)")
	description := flags.String("description", "", "policy description")
	agentID := flags.String("agent-id", "", "optional agent ID")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
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
		Priority:    *priority,
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

func runPoliciesUpdate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("policies update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	name := flags.String("name", "", "policy name")
	srcPattern := flags.String("src-pattern", "", "source pattern")
	dstPattern := flags.String("dst-pattern", "", "target pattern")
	effect := flags.String("effect", "", "effect: permit or deny")
	priority := flags.Int("priority", 0, "priority")
	enabled := flags.Bool("enabled", false, "enabled")
	description := flags.String("description", "", "policy description")
	jsonOutput := flags.Bool("json", false, "output as JSON")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "policies update requires a policy ID")
		return 2
	}
	ws, ok := requirePoliciesWorkspace("update", *workspace, stderr)
	if !ok || !requirePolicyID("update", id, stderr) {
		return 2
	}
	if *effect != "" && *effect != "permit" && *effect != "deny" {
		fmt.Fprintln(stderr, "policies update: --effect must be permit or deny")
		return 2
	}
	set := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
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
	if set["priority"] {
		request.Priority = priority
	}
	if set["enabled"] {
		request.Enabled = enabled
	}
	if set["description"] {
		request.Description = description
	}
	if request.Name == nil && request.Description == nil && request.SrcPattern == nil && request.DstPattern == nil && request.Effect == nil && request.Priority == nil && request.Enabled == nil {
		fmt.Fprintln(stderr, "policies update requires at least one field to update: --name, --src-pattern, --dst-pattern, --effect, --priority, --enabled, or --description")
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
	if set["priority"] {
		maskFields = append(maskFields, "priority")
	}
	if set["enabled"] {
		maskFields = append(maskFields, "enabled")
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

func runPoliciesDelete(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("policies delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	yes := flags.Bool("yes", false, "confirm deletion")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "policies delete requires a policy ID")
		return 2
	}
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
	flags := flag.NewFlagSet("policies import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := policiesWorkspaceFlag(flags)
	importNames := registry.ImportNames()
	from := flags.String("from", "", "harness to import from: "+harness.JoinOr(importNames))
	file := flags.String("file", "", "override the source file or directory")
	src := flags.String("src", "", "source pattern (default: harness:<kind>; harness:* shares the rules with every harness)")
	out := flags.String("out", "", "write the proposed policy-set JSON to this file")
	apply := flags.Bool("apply", false, "create the imported policies (default: dry run)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
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
	ws, ok := requirePoliciesWorkspace("import", *workspace, stderr)
	if !ok {
		return 2
	}
	srcPattern := *src
	if srcPattern == "" {
		srcPattern = "harness:" + source.Kind()
	}
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
	if *src == "" && *apply && stdinIsInteractive(stdin) {
		fmt.Fprintf(stdout, "Imported rules default to src %s (only that harness). Apply to all harnesses? [y/N] ", srcPattern)
		line, _ := reader.ReadString('\n')
		if answer := strings.TrimSpace(strings.ToLower(line)); answer == "y" || answer == "yes" {
			srcPattern = "harness:*"
			for i := range policies {
				policies[i].SrcPattern = srcPattern
			}
		}
	}
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
			break
		}
		created++
	}
	fmt.Fprintf(stdout, "Created %d policies.\n", created)
	return 0
}

// stdinIsInteractive reports whether stdin is a terminal; tests replace it.
var stdinIsInteractive = func(stdin io.Reader) bool {
	file, ok := stdin.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
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
