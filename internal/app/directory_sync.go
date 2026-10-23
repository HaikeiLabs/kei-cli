package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
)

// Directory sync is the organization singleton
// /api/v1/organizations/{org}/directory-sync in kei-policy-catalog, reached
// through the console proxy with the CLI bearer. It holds the customer's Entra
// settings that kei-oidc-bridge syncs app-role assignments from; the bridge
// writes one run record per sync to …/directory-sync/runs. The organization
// comes from the CLI token. Reads and writes require an organization admin.

type directorySync struct {
	OrganizationID string            `json:"organization_id"`
	Provider       string            `json:"provider"`
	TenantID       string            `json:"tenant_id"`
	ClientID       string            `json:"client_id"`
	AppID          string            `json:"app_id"`
	GroupID        string            `json:"group_id"`
	Enabled        bool              `json:"enabled"`
	DryRun         bool              `json:"dry_run"`
	CreateUsers    bool              `json:"create_users"`
	Revoke         bool              `json:"revoke"`
	Version        int               `json:"version"`
	CreateTime     string            `json:"create_time"`
	UpdateTime     string            `json:"update_time"`
	LastRun        *directorySyncRun `json:"last_run"`
}

type directorySyncRun struct {
	ID               string                `json:"id"`
	StartTime        string                `json:"start_time"`
	EndTime          string                `json:"end_time"`
	DryRun           bool                  `json:"dry_run"`
	Result           string                `json:"result"`
	Error            string                `json:"error"`
	Stats            map[string]int64      `json:"stats"`
	Changes          []directorySyncChange `json:"changes"`
	ChangesTruncated bool                  `json:"changes_truncated"`
}

type directorySyncChange struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

// patchDirectorySyncRequest is the body of PATCH …/directory-sync: only the
// fields named in update_mask change. The first PATCH creates the resource.
type patchDirectorySyncRequest struct {
	TenantID    *string   `json:"tenant_id,omitempty"`
	ClientID    *string   `json:"client_id,omitempty"`
	AppID       *string   `json:"app_id,omitempty"`
	GroupID     *string   `json:"group_id,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
	DryRun      *bool     `json:"dry_run,omitempty"`
	CreateUsers *bool     `json:"create_users,omitempty"`
	Revoke      *bool     `json:"revoke,omitempty"`
	UpdateMask  fieldMask `json:"update_mask"`
}

// directorySyncKeyStats are the run counts shown in summaries and tables;
// report shows every stat.
var directorySyncKeyStats = []string{"assignments", "groups_created", "members_added", "memberships_revoked", "users_created", "would_create", "member_errors"}

var directorySyncGroup = commandGroup{
	name: "directory-sync",
	commands: []groupCommand{
		{name: "get", summary: "Get the directory sync configuration"},
		{name: "set", summary: "Configure directory sync"},
		{name: "enable", summary: "Enable directory sync"},
		{name: "disable", summary: "Disable directory sync"},
		{name: "report", summary: "Get the latest sync report"},
	},
}

func runDirectorySyncCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := directorySyncGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "get":
		return runDirectorySyncGet(rest, stdout, stderr, client, store)
	case "set":
		return runDirectorySyncSet(rest, stdout, stderr, stdin, client, store)
	case "enable":
		return runDirectorySyncSetEnabled(true, rest, stdout, stderr, stdin, client, store)
	case "disable":
		return runDirectorySyncSetEnabled(false, rest, stdout, stderr, stdin, client, store)
	case "report":
		return runDirectorySyncReport(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func runDirectorySyncGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("directory-sync", "get", "Get the directory sync configuration", stdout, stderr)
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "directory-sync get takes no positional arguments")
		return 2
	}
	session, ok := openOrgSession("directory-sync get", workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	status, payload, err := session.do(http.MethodGet, session.orgPath("/directory-sync"), nil, nil)
	if status == http.StatusNotFound {
		fmt.Fprintln(stderr, "No directory sync configured.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "directory-sync get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	return printDirectorySync("get", payload, stdout, stderr)
}

// runDirectorySyncSet changes (or, the first time, creates) the directory
// sync with PATCH; update_mask names exactly the flags given.
func runDirectorySyncSet(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("directory-sync", "set", "Configure directory sync", stdout, stderr)
	tenantID := lc.fs.String("tenant-id", "", "the customer's Entra tenant ID (GUID)")
	clientID := lc.fs.String("client-id", "", "client ID of the customer's sync app registration (GUID)")
	appID := lc.fs.String("app-id", "", "client ID of the app whose app roles are synced (GUID)")
	groupID := lc.fs.String("group-id", "", "Entra security group ID to sync (GUID; mutually exclusive with --app-id)")
	dryRun := lc.fs.Bool("dry-run", true, "report what would change without writing (true or false)")
	createUsers := lc.fs.Bool("create-users", false, "create Kei users for assigned Entra users (true or false)")
	revoke := lc.fs.Bool("revoke", false, "remove memberships no longer assigned in Entra (true or false)")
	yes := yesFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "directory-sync set takes no positional arguments")
		return 2
	}
	if *groupID != "" {
		if _, err := uuid.Parse(*groupID); err != nil {
			fmt.Fprintln(stderr, "directory-sync set: --group-id must be a GUID")
			return 2
		}
		appIDSet := false
		lc.fs.Visit(func(f *flag.Flag) {
			appIDSet = appIDSet || f.Name == "app-id"
		})
		if appIDSet {
			fmt.Fprintln(stderr, "directory-sync set: --group-id and --app-id are mutually exclusive")
			return 2
		}
	}
	var req patchDirectorySyncRequest
	var changes []string
	lc.fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "tenant-id":
			req.TenantID = tenantID
		case "client-id":
			req.ClientID = clientID
		case "app-id":
			req.AppID = appID
		case "group-id":
			req.GroupID = groupID
		case "dry-run":
			req.DryRun = dryRun
		case "create-users":
			req.CreateUsers = createUsers
		case "revoke":
			req.Revoke = revoke
		default:
			return
		}
		path := strings.ReplaceAll(f.Name, "-", "_")
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, path)
		changes = append(changes, path+"="+f.Value.String())
	})
	if len(req.UpdateMask.Paths) == 0 {
		fmt.Fprintln(stderr, "directory-sync set requires at least one of --tenant-id, --client-id, --app-id, --group-id, --dry-run, --create-users, or --revoke")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Set directory sync "+strings.Join(changes, ", ")+"? Pass --yes to confirm without prompting.") {
		return 2
	}
	session, ok := openOrgSession("directory-sync set", workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPatch, session.orgPath("/directory-sync"), nil, req)
	if err != nil {
		fmt.Fprintf(stderr, "directory-sync set: %v\n", err)
		return 1
	}
	return printDirectorySync("set", payload, stdout, stderr)
}

func runDirectorySyncSetEnabled(enabled bool, args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	command := "disable"
	if enabled {
		command = "enable"
	}
	lc := newCommandFlags("directory-sync", command, "Enable or disable directory sync", stdout, stderr)
	yes := yesFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintf(stderr, "directory-sync %s takes no positional arguments\n", command)
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, strings.ToUpper(command[:1])+command[1:]+" directory sync for the organization? Pass --yes to confirm without prompting.") {
		return 2
	}
	session, ok := openOrgSession("directory-sync "+command, workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	req := patchDirectorySyncRequest{Enabled: &enabled, UpdateMask: fieldMask{Paths: []string{"enabled"}}}
	_, payload, err := session.do(http.MethodPatch, session.orgPath("/directory-sync"), nil, req)
	if err != nil {
		fmt.Fprintf(stderr, "directory-sync %s: %v\n", command, err)
		return 1
	}
	var ds directorySync
	if err := json.Unmarshal(payload, &ds); err != nil {
		fmt.Fprintf(stderr, "directory-sync %s: decode response: %v\n", command, err)
		return 1
	}
	if !enabled {
		fmt.Fprintln(stdout, "Directory sync disabled. The bridge stops syncing this organization at its next interval.")
		return 0
	}
	fmt.Fprintln(stdout, "Directory sync enabled. The bridge syncs this organization at its next interval.")
	if ds.DryRun {
		fmt.Fprintln(stdout, "It runs in dry run (the default): nothing is written; each run records what it would change.")
		fmt.Fprintln(stdout, "Read the newest run with `kei directory-sync report`, then turn dry run off with `kei directory-sync set --dry-run=false`.")
	} else {
		fmt.Fprintln(stdout, "Dry run is off: runs write changes. Read what each run did with `kei directory-sync report`.")
	}
	return 0
}

func runDirectorySyncReport(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("directory-sync", "report", "Get the latest sync report", stdout, stderr)
	limit := lc.fs.Int("limit", 1, "number of runs to show, newest first; 1 shows the run in full")
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "directory-sync report takes no positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "directory-sync report: --limit must be at least 1")
		return 2
	}
	session, ok := openOrgSession("directory-sync report", workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	query := url.Values{"page_size": {strconv.Itoa(*limit)}}
	_, payload, err := session.do(http.MethodGet, session.orgPath("/directory-sync/runs"), query, nil)
	if err != nil {
		fmt.Fprintf(stderr, "directory-sync report: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	runs, err := decodeDirectorySyncRuns(payload)
	if err != nil {
		fmt.Fprintf(stderr, "directory-sync report: decode response: %v\n", err)
		return 1
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, "No runs yet.")
		return 0
	}
	if *limit == 1 {
		printDirectorySyncRun(stdout, runs[0])
		return 0
	}
	if len(runs) > *limit {
		runs = runs[:*limit]
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "START_TIME\tRESULT\tDRY_RUN\tGROUPS_CREATED\tMEMBERS_ADDED\tREVOKED\tUSERS_CREATED\tWOULD_CREATE\tERROR")
	for _, run := range runs {
		fmt.Fprintf(table, "%s\t%s\t%t\t%d\t%d\t%d\t%d\t%d\t%s\n", run.StartTime, run.Result, run.DryRun,
			run.Stats["groups_created"], run.Stats["members_added"], run.Stats["memberships_revoked"],
			run.Stats["users_created"], run.Stats["would_create"], run.Error)
	}
	_ = table.Flush()
	return 0
}

// decodeDirectorySyncRuns reads the runs list page. The catalog's paginated
// lists put the page in items; runs and a bare array are accepted too.
func decodeDirectorySyncRuns(payload []byte) ([]directorySyncRun, error) {
	trimmed := strings.TrimSpace(string(payload))
	if strings.HasPrefix(trimmed, "[") {
		var runs []directorySyncRun
		err := json.Unmarshal(payload, &runs)
		return runs, err
	}
	var page struct {
		Items []directorySyncRun `json:"items"`
		Runs  []directorySyncRun `json:"runs"`
	}
	if err := json.Unmarshal(payload, &page); err != nil {
		return nil, err
	}
	if len(page.Items) > 0 {
		return page.Items, nil
	}
	return page.Runs, nil
}

func printDirectorySync(command string, payload []byte, stdout, stderr io.Writer) int {
	var ds directorySync
	if err := json.Unmarshal(payload, &ds); err != nil {
		fmt.Fprintf(stderr, "directory-sync %s: decode response: %v\n", command, err)
		return 1
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(table, "provider:\t%s\n", ds.Provider)
	fmt.Fprintf(table, "tenant_id:\t%s\n", ds.TenantID)
	fmt.Fprintf(table, "client_id:\t%s\n", ds.ClientID)
	fmt.Fprintf(table, "app_id:\t%s\n", ds.AppID)
	fmt.Fprintf(table, "group_id:\t%s\n", ds.GroupID)
	fmt.Fprintf(table, "enabled:\t%t\n", ds.Enabled)
	fmt.Fprintf(table, "dry_run:\t%t\n", ds.DryRun)
	fmt.Fprintf(table, "create_users:\t%t\n", ds.CreateUsers)
	fmt.Fprintf(table, "revoke:\t%t\n", ds.Revoke)
	fmt.Fprintf(table, "version:\t%d\n", ds.Version)
	fmt.Fprintf(table, "update_time:\t%s\n", ds.UpdateTime)
	_ = table.Flush()
	if ds.LastRun == nil {
		fmt.Fprintln(stdout, "last_run: none yet")
		return 0
	}
	run := ds.LastRun
	fmt.Fprintln(stdout, "last_run:")
	table = tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(table, "  start_time:\t%s\n", run.StartTime)
	fmt.Fprintf(table, "  result:\t%s\n", run.Result)
	fmt.Fprintf(table, "  dry_run:\t%t\n", run.DryRun)
	if run.Error != "" {
		fmt.Fprintf(table, "  error:\t%s\n", run.Error)
	}
	if stats := keyStatsLine(run.Stats); stats != "" {
		fmt.Fprintf(table, "  stats:\t%s\n", stats)
	}
	_ = table.Flush()
	fmt.Fprintln(stdout, "Run `kei directory-sync report` for the full run.")
	return 0
}

// keyStatsLine is "name=n" for each key stat the run reported, in a fixed
// order.
func keyStatsLine(stats map[string]int64) string {
	var parts []string
	for _, key := range directorySyncKeyStats {
		if value, ok := stats[key]; ok {
			parts = append(parts, key+"="+strconv.FormatInt(value, 10))
		}
	}
	return strings.Join(parts, " ")
}

func printDirectorySyncRun(stdout io.Writer, run directorySyncRun) {
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(table, "start_time:\t%s\n", run.StartTime)
	fmt.Fprintf(table, "end_time:\t%s\n", run.EndTime)
	fmt.Fprintf(table, "result:\t%s\n", run.Result)
	fmt.Fprintf(table, "dry_run:\t%t\n", run.DryRun)
	if run.Error != "" {
		fmt.Fprintf(table, "error:\t%s\n", run.Error)
	}
	_ = table.Flush()
	if len(run.Stats) > 0 {
		fmt.Fprintln(stdout, "stats:")
		keys := make([]string, 0, len(run.Stats))
		for key := range run.Stats {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		table = tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, key := range keys {
			fmt.Fprintf(table, "  %s:\t%d\n", key, run.Stats[key])
		}
		_ = table.Flush()
	}
	heading := "changes:"
	if run.DryRun {
		heading = "changes (dry run; not applied):"
	}
	if len(run.Changes) == 0 {
		fmt.Fprintln(stdout, heading+" none")
	} else {
		fmt.Fprintln(stdout, heading)
		for _, change := range run.Changes {
			fmt.Fprintf(stdout, "  %s → %s\n", change.Action, change.Target)
		}
	}
	if run.ChangesTruncated {
		fmt.Fprintf(stdout, "Only the first %d changes are listed; the run recorded more.\n", len(run.Changes))
	}
}
