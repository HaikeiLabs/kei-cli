package app

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/google/uuid"
)

// toolManifest is a connector's tool manifest as the catalog serves it
// (kei ADR-036 §5.4): the canonical tools the runtime observed, and whether
// an admin approved them.
type toolManifest struct {
	ID           string         `json:"id"`
	ManifestHash string         `json:"manifest_hash"`
	Status       string         `json:"status"`
	SubmittedAt  string         `json:"submitted_at"`
	ApprovedBy   string         `json:"approved_by"`
	ApprovedAt   string         `json:"approved_at"`
	Tools        []manifestTool `json:"tools"`
}

// manifestTool is one canonical tool: name, description and inputSchema.
type manifestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

const (
	manifestStatusApproved  = "approved"
	manifestStatusCandidate = "candidate"
)

var connectorsManifestsGroup = commandGroup{
	name: "connectors manifests",
	commands: []groupCommand{
		{name: "list", summary: "List connector tool manifests"},
		{name: "show", summary: "Show a connector tool manifest"},
		{name: "approve", summary: "Approve a connector tool manifest"},
	},
}

// runConnectorsManifests serves kei connectors manifests (alias tools):
// list and show a connector's tool manifests, and approve one.
func runConnectorsManifests(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := connectorsManifestsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runConnectorsManifestsList(rest, stdout, stderr, client, store)
	case "show":
		return runConnectorsManifestsShow(rest, stdout, stderr, client, store)
	case "approve":
		return runConnectorsManifestsApprove(rest, stdout, stderr, stdin, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

// parseManifestArgs parses flags around a connector ID and, when allowed, an
// optional manifest ID.
func parseManifestArgs(flags *flag.FlagSet, args []string, allowManifest bool) (connectorID, manifestID string, err error) {
	var positional []string
	for len(args) > 0 {
		if !strings.HasPrefix(args[0], "-") {
			positional, args = append(positional, args[0]), args[1:]
			continue
		}
		if err := flags.Parse(args); err != nil {
			return "", "", err
		}
		args = flags.Args()
	}
	max := 1
	if allowManifest {
		max = 2
	}
	if len(positional) == 0 || len(positional) > max {
		if allowManifest {
			return "", "", fmt.Errorf("takes a connector ID and an optional manifest ID")
		}
		return "", "", fmt.Errorf("takes one connector ID")
	}
	connectorID = positional[0]
	if _, err := uuid.Parse(connectorID); err != nil {
		return "", "", fmt.Errorf("connector ID must be a UUID")
	}
	if len(positional) == 2 {
		manifestID = positional[1]
		if _, err := uuid.Parse(manifestID); err != nil {
			return "", "", fmt.Errorf("manifest ID must be a UUID")
		}
	}
	return connectorID, manifestID, nil
}

func runConnectorsManifestsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors manifests list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	connectorID, _, err := parseManifestArgs(flags, args, false)
	if err != nil {
		fmt.Fprintf(stderr, "connectors manifests list: %v\n", err)
		return 2
	}
	ws, ok := requireConnectorWorkspace("manifests list", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openConnectorSession("manifests list", ws, stderr, client, store)
	if !ok {
		return 1
	}
	payload, manifests, err := session.toolManifests(connectorID)
	if err != nil {
		fmt.Fprintf(stderr, "connectors manifests list: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	if len(manifests) == 0 {
		fmt.Fprintln(stdout, "No tool manifests yet. The runtime submits one when it first lists the server's tools.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tSTATUS\tTOOLS\tSUBMITTED\tAPPROVED\tHASH")
	for _, m := range manifests {
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n", m.ID, m.Status, len(m.Tools), orDash(m.SubmittedAt), orDash(m.ApprovedAt), m.ManifestHash)
	}
	_ = table.Flush()
	return 0
}

func runConnectorsManifestsShow(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors manifests show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	jsonOutput := flags.Bool("json", false, "output as JSON")
	connectorID, manifestID, err := parseManifestArgs(flags, args, true)
	if err != nil {
		fmt.Fprintf(stderr, "connectors manifests show: %v\n", err)
		return 2
	}
	ws, ok := requireConnectorWorkspace("manifests show", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openConnectorSession("manifests show", ws, stderr, client, store)
	if !ok {
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors manifests show: %v\n", err)
		return 1
	}
	var payload []byte
	var shown toolManifest
	var all []toolManifest
	if manifestID != "" {
		if payload, err = session.do(http.MethodGet, "/"+url.PathEscape(connectorID)+"/tool-manifests/"+url.PathEscape(manifestID), nil); err != nil {
			return fail(err)
		}
		if err := json.Unmarshal(payload, &shown); err != nil {
			return fail(fmt.Errorf("decode response: %w", err))
		}
	} else {
		if _, all, err = session.toolManifests(connectorID); err != nil {
			return fail(err)
		}
		pick, ok := defaultShownManifest(all)
		if !ok {
			fmt.Fprintln(stdout, "No tool manifests yet. The runtime submits one when it first lists the server's tools.")
			return 0
		}
		shown = pick
		if payload, err = json.Marshal(shown); err != nil {
			return fail(err)
		}
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	printToolManifest(stdout, shown)
	if shown.Status != manifestStatusCandidate {
		return 0
	}
	if all == nil {
		if _, all, err = session.toolManifests(connectorID); err != nil {
			return fail(err)
		}
	}
	printManifestChanges(stdout, shown, all)
	return 0
}

// printManifestChanges prints a candidate's changes since the connector's
// approved manifest, if there is one.
func printManifestChanges(w io.Writer, candidate toolManifest, all []toolManifest) {
	approved, ok := findApprovedManifest(all)
	if !ok {
		fmt.Fprintln(w, "No manifest is approved yet: every tool above is new.")
		return
	}
	fmt.Fprintf(w, "Changes since the approved manifest %s:\n", approved.ID)
	changes := manifestChanges(approved, candidate)
	if len(changes) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, line := range changes {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

// runConnectorsManifestsApprove approves one candidate manifest with the
// catalog's :approve custom method (ADR-036 §5.4 step 3). It shows the
// manifest and its changes first and needs confirmation (--yes, or y at a
// terminal). The console requires an organization administrator.
func runConnectorsManifestsApprove(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("connectors manifests approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := connectorWorkspaceFlag(flags)
	yes := flags.Bool("yes", false, "approve without prompting")
	connectorID, manifestID, err := parseManifestArgs(flags, args, true)
	if err == nil && manifestID == "" {
		err = fmt.Errorf("takes a connector ID and the ID of the manifest to approve")
	}
	if err != nil {
		fmt.Fprintf(stderr, "connectors manifests approve: %v\n", err)
		return 2
	}
	ws, ok := requireConnectorWorkspace("manifests approve", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openConnectorSession("manifests approve", ws, stderr, client, store)
	if !ok {
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "connectors manifests approve: %v\n", err)
		return 1
	}
	memberPath := "/" + url.PathEscape(connectorID) + "/tool-manifests/" + url.PathEscape(manifestID)
	payload, err := session.do(http.MethodGet, memberPath, nil)
	if err != nil {
		return fail(err)
	}
	var candidate toolManifest
	if err := json.Unmarshal(payload, &candidate); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	if candidate.Status == manifestStatusApproved {
		fmt.Fprintf(stdout, "Manifest %s is already approved.\n", candidate.ID)
		return 0
	}
	_, all, err := session.toolManifests(connectorID)
	if err != nil {
		return fail(err)
	}
	printToolManifest(stdout, candidate)
	printManifestChanges(stdout, candidate, all)
	if !confirmWrite(stdin, stderr, *yes, fmt.Sprintf("Approve manifest %s? Its %d tools become callable, and it replaces the current approval. Pass --yes to confirm without prompting.", candidate.ID, len(candidate.Tools))) {
		return 2
	}
	payload, err = session.do(http.MethodPost, memberPath+":approve", nil)
	if err != nil {
		return fail(err)
	}
	var approved toolManifest
	if err := json.Unmarshal(payload, &approved); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	fmt.Fprintf(stdout, "Approved manifest %s (%d tools).\n", approved.ID, len(approved.Tools))
	return 0
}

// toolManifests reads a connector's manifest collection.
func (s *connectorSession) toolManifests(connectorID string) ([]byte, []toolManifest, error) {
	payload, err := s.do(http.MethodGet, "/"+url.PathEscape(connectorID)+"/tool-manifests", nil)
	if err != nil {
		return nil, nil, err
	}
	var manifests []toolManifest
	if err := json.Unmarshal(payload, &manifests); err != nil {
		return nil, nil, fmt.Errorf("decode tool manifests: %w", err)
	}
	return payload, manifests, nil
}

// defaultShownManifest is what show displays without a manifest ID: the
// newest candidate awaiting review, otherwise the approved manifest. The
// catalog lists manifests newest first, so the newest candidate is the first.
func defaultShownManifest(manifests []toolManifest) (toolManifest, bool) {
	for _, m := range manifests {
		if m.Status == manifestStatusCandidate {
			return m, true
		}
	}
	return findApprovedManifest(manifests)
}

// findApprovedManifest returns the connector's approved manifest; the catalog
// allows at most one.
func findApprovedManifest(manifests []toolManifest) (toolManifest, bool) {
	for _, m := range manifests {
		if m.Status == manifestStatusApproved {
			return m, true
		}
	}
	return toolManifest{}, false
}

// manifestStatus summarizes a connector's manifests for get and list.
func manifestStatus(manifests []toolManifest) string {
	parts := []string{}
	if approved, ok := findApprovedManifest(manifests); ok {
		parts = append(parts, fmt.Sprintf("approved (%d tools)", len(approved.Tools)))
	} else {
		parts = append(parts, "none approved")
	}
	if candidate, ok := defaultShownManifest(manifests); ok && candidate.Status == manifestStatusCandidate {
		parts = append(parts, "candidate "+candidate.ID+" pending review")
	}
	return strings.Join(parts, "; ")
}

// manifestStatusColumn is list --manifests' short form of manifestStatus.
func manifestStatusColumn(manifests []toolManifest) string {
	_, approved := findApprovedManifest(manifests)
	candidate, ok := defaultShownManifest(manifests)
	pending := ok && candidate.Status == manifestStatusCandidate
	switch {
	case approved && pending:
		return "approved; candidate pending review"
	case approved:
		return "approved"
	case pending:
		return "candidate pending review"
	default:
		return "none"
	}
}

func printToolManifest(w io.Writer, m toolManifest) {
	fmt.Fprintf(w, "Manifest:    %s\nStatus:      %s\nHash:        %s\nSubmitted:   %s\n", m.ID, m.Status, m.ManifestHash, orDash(m.SubmittedAt))
	if m.ApprovedAt != "" {
		fmt.Fprintf(w, "Approved:    %s by %s\n", m.ApprovedAt, orDash(m.ApprovedBy))
	}
	fmt.Fprintf(w, "Tools (%d):\n", len(m.Tools))
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, tool := range sortedTools(m.Tools) {
		fmt.Fprintf(table, "  %s\t%s\n", terminalSafe(tool.Name), terminalSafe(tool.Description))
	}
	_ = table.Flush()
}

func sortedTools(tools []manifestTool) []manifestTool {
	out := append([]manifestTool(nil), tools...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// manifestChanges lists the tools a candidate adds (+), removes (-) and
// changes (~, naming what changed) relative to the approved manifest.
func manifestChanges(approved, candidate toolManifest) []string {
	before := map[string]manifestTool{}
	for _, tool := range approved.Tools {
		before[tool.Name] = tool
	}
	after := map[string]manifestTool{}
	for _, tool := range candidate.Tools {
		after[tool.Name] = tool
	}
	var lines []string
	for _, tool := range sortedTools(candidate.Tools) {
		old, existed := before[tool.Name]
		if !existed {
			lines = append(lines, "+ "+terminalSafe(tool.Name))
			continue
		}
		var what []string
		if old.Description != tool.Description {
			what = append(what, "description")
		}
		if !sameJSON(old.InputSchema, tool.InputSchema) {
			what = append(what, "inputSchema")
		}
		if len(what) > 0 {
			lines = append(lines, "~ "+terminalSafe(tool.Name)+" ("+strings.Join(what, ", ")+")")
		}
	}
	for _, tool := range sortedTools(approved.Tools) {
		if _, kept := after[tool.Name]; !kept {
			lines = append(lines, "- "+terminalSafe(tool.Name))
		}
	}
	return lines
}

// sameJSON compares two JSON values ignoring member order and whitespace.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// terminalSafe makes upstream text (tool names and descriptions come from
// the MCP server) safe to print: control characters, including escape
// sequences' ESC and line breaks, become spaces.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
}
