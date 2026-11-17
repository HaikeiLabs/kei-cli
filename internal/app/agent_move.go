package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

type agentMoveOptions struct {
	groups        map[string]string
	installations map[string]string
}

type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

var agentsGroup = commandGroup{
	name:     "agents",
	commands: []groupCommand{{name: "move", summary: "Move an agent to another workspace"}},
}

func runAgentsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := agentsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "move":
		return runAgentMove(rest, stdout, stderr, stdin, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func runAgentMove(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("agents", "move", "Move an agent to another workspace", stdout, stderr).WithArgs("AGENT")
	toWorkspace := lc.fs.String("to-workspace", "", "destination workspace name or ID (required)")
	var groupFlags, installationFlags repeatedFlag
	lc.fs.Var(&groupFlags, "group", "group action NAME=move|copy|leave (repeatable)")
	lc.fs.Var(&installationFlags, "installation", "installation action ID=move_all|detach (repeatable)")
	dryRun := lc.fs.Bool("dry-run", false, "show the move plan without writing")
	yes := yesFlag(lc.fs)
	jsonOutput := jsonFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "agents move requires an agent ID or name")
		return 2
	}
	if strings.TrimSpace(*toWorkspace) == "" {
		fmt.Fprintln(stderr, "agents move requires --to-workspace")
		return 2
	}
	options, err := parseAgentMoveOptions(groupFlags, installationFlags)
	if err != nil {
		fmt.Fprintf(stderr, "agents move: %v\n", err)
		return 2
	}
	session, ok := openAPISession("agents move", stderr, client, store)
	if !ok {
		return 1
	}
	destination, err := resolveScopedWorkspace(
		context.Background(), session.client, session.baseURL, session.token, workspaceSelection{value: *toWorkspace},
	)
	if err != nil {
		fmt.Fprintf(stderr, "agents move: resolve destination workspace: %v\n", err)
		return 1
	}
	agentID, err := resolveAgentID(session, pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "agents move: resolve agent: %v\n", err)
		return 1
	}

	path := "/api/v1/agents/" + url.PathEscape(agentID) + ":move"
	request := map[string]any{
		"destination_workspace": destination,
		"groups":                options.groups,
		"installations":         options.installations,
		"validate_only":         true,
	}
	_, plan, err := session.do(http.MethodPost, path, nil, request)
	if err != nil {
		fmt.Fprintf(stderr, "agents move: preview: %v\n", err)
		return 1
	}
	blocked, err := movePlanHasBlockers(plan)
	if err != nil {
		fmt.Fprintf(stderr, "agents move: decode plan: %v\n", err)
		return 1
	}
	if *dryRun || !*jsonOutput {
		if *jsonOutput {
			_, _ = stdout.Write(plan)
			if len(plan) == 0 || plan[len(plan)-1] != '\n' {
				fmt.Fprintln(stdout)
			}
		} else {
			printAgentMovePlan(stdout, plan)
		}
	}
	if blocked {
		if *jsonOutput && !*dryRun {
			_, _ = stdout.Write(plan)
			if len(plan) == 0 || plan[len(plan)-1] != '\n' {
				fmt.Fprintln(stdout)
			}
		}
		printAgentMoveBlockers(stderr, plan)
		return 1
	}
	if *dryRun {
		return 0
	}
	if !confirmWrite(stdin, stderr, *yes, fmt.Sprintf("Move agent %s to workspace %s?", pos[0], destination)) {
		return 2
	}
	request["validate_only"] = false
	_, result, err := session.do(http.MethodPost, path, nil, request)
	if err != nil {
		fmt.Fprintf(stderr, "agents move: apply: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(result)
		if len(result) == 0 || result[len(result)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
	} else {
		fmt.Fprintf(stdout, "Moved agent %s to workspace %s.\n", pos[0], destination)
		printAgentMoveNextSteps(stderr, result)
	}
	return 0
}

func printAgentMoveNextSteps(w io.Writer, payload []byte) {
	var result struct {
		Installations []struct {
			ID     string `json:"id"`
			Name   string `json:"display_name"`
			Action string `json:"action"`
		} `json:"installations"`
	}
	if json.Unmarshal(payload, &result) != nil {
		return
	}
	var rescaled []struct {
		ID   string
		Name string
	}
	for _, installation := range result.Installations {
		if installation.Action == "move_all" {
			rescaled = append(rescaled, struct {
				ID   string
				Name string
			}{ID: installation.ID, Name: installation.Name})
		}
	}
	if len(rescaled) == 0 {
		return
	}
	fmt.Fprintln(w, "Next steps:")
	for _, installation := range rescaled {
		label := installation.ID
		if installation.Name != "" {
			label += " (" + installation.Name + ")"
		}
		fmt.Fprintf(w, "  - Installation %s: run `kei-proxy policy sync --force`.\n", label)
	}
	fmt.Fprintln(w, "The running kei-proxy re-resolves its identity on the next authorization.")
}

func parseAgentMoveOptions(groups, installations []string) (agentMoveOptions, error) {
	options := agentMoveOptions{groups: map[string]string{}, installations: map[string]string{}}
	for _, item := range groups {
		name, action, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return options, fmt.Errorf("invalid --group %q; expected NAME=move|copy|leave", item)
		}
		switch action {
		case "move", "copy", "leave":
		default:
			return options, fmt.Errorf("invalid --group action %q; expected move, copy, or leave", action)
		}
		if _, exists := options.groups[name]; exists {
			return options, fmt.Errorf("--group %q was specified more than once", name)
		}
		options.groups[name] = action
	}
	for _, item := range installations {
		id, action, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(id) == "" {
			return options, fmt.Errorf("invalid --installation %q; expected ID=move_all|detach", item)
		}
		if _, err := uuid.Parse(id); err != nil {
			return options, fmt.Errorf("--installation ID %q must be a UUID", id)
		}
		switch action {
		case "move_all", "detach":
		default:
			return options, fmt.Errorf("invalid --installation action %q; expected move_all or detach", action)
		}
		if _, exists := options.installations[id]; exists {
			return options, fmt.Errorf("--installation %q was specified more than once", id)
		}
		options.installations[id] = action
	}
	return options, nil
}

func resolveAgentID(session *apiSession, value string) (string, error) {
	if _, err := uuid.Parse(value); err == nil {
		return value, nil
	}
	query := url.Values{"org_id": {session.orgID}}
	_, payload, err := session.do(http.MethodGet, "/api/v1/agents", query, nil)
	if err != nil {
		return "", err
	}
	var agents []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(payload, &agents); err != nil {
		return "", fmt.Errorf("decode agent list: %w", err)
	}
	var found string
	for _, agent := range agents {
		if agent.Name == value {
			if found != "" {
				return "", fmt.Errorf("multiple agents match name %q; use an agent ID", value)
			}
			found = agent.ID
		}
	}
	if found == "" {
		return "", fmt.Errorf("agent %q not found", value)
	}
	return found, nil
}

func movePlanHasBlockers(payload []byte) (bool, error) {
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(payload, &plan); err != nil {
		return false, err
	}
	for _, key := range []string{"blockers", "blocker"} {
		if raw, ok := plan[key]; ok {
			var blockers []json.RawMessage
			if err := json.Unmarshal(raw, &blockers); err == nil {
				return len(blockers) > 0, nil
			}
		}
	}
	return false, nil
}

func printAgentMovePlan(w io.Writer, payload []byte) {
	var plan any
	if json.Unmarshal(payload, &plan) != nil {
		fmt.Fprintln(w, string(payload))
		return
	}
	formatted, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		fmt.Fprintln(w, string(payload))
		return
	}
	fmt.Fprintf(w, "Move plan:\n%s\n", formatted)
}

func printAgentMoveBlockers(w io.Writer, payload []byte) {
	var plan map[string]json.RawMessage
	if json.Unmarshal(payload, &plan) != nil {
		return
	}
	raw, ok := plan["blockers"]
	if !ok {
		raw = plan["blocker"]
	}
	var blockers []map[string]any
	if json.Unmarshal(raw, &blockers) != nil {
		return
	}
	fmt.Fprintln(w, "agents move: blockers prevent the move; no changes were written:")
	for _, blocker := range blockers {
		reason, _ := blocker["reason"].(string)
		if reason == "" {
			reason = "unspecified blocker"
		}
		line := "  - " + reason
		resource, _ := blocker["resource"].(string)
		if resource == "" {
			resource, _ = blocker["name"].(string)
		}
		if resource != "" {
			line += " (" + resource + ")"
		}
		fmt.Fprintln(w, line+agentMoveBlockerResolution(reason))
	}
}

func agentMoveBlockerResolution(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "installation") && strings.Contains(r, "shared"):
		return "; resolve with --installation ID=move_all or --installation ID=detach"
	case strings.Contains(r, "group") && (strings.Contains(r, "member") || strings.Contains(r, "agent")):
		return "; resolve with --group NAME=move or --group NAME=leave"
	case strings.Contains(r, "group") && strings.Contains(r, "collision"):
		return "; resolve the destination name collision before retrying"
	case strings.Contains(r, "policy") && strings.Contains(r, "collision"):
		return "; resolve the destination policy collision before retrying"
	case strings.Contains(r, "model_profile") && strings.Contains(r, "destination"):
		return "; create an equivalent model profile in the destination workspace before retrying"
	case strings.Contains(r, "connector") || strings.Contains(r, "mcp") || strings.Contains(r, "tool"):
		return "; add a matching connector/tool in the destination workspace before retrying"
	case strings.Contains(r, "nested"):
		return "; resolve the nested group dependency before retrying"
	default:
		return "; review the plan details and adjust --group or --installation options"
	}
}
