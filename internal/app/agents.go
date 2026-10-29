package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

type runtimeInstallationAgent struct {
	InstallationID string `json:"installation_id"`
	AgentID        string `json:"agent_id"`
	IsDefault      bool   `json:"is_default"`
}

var botAgentsGroup = commandGroup{
	name: "bot agents",
	commands: []groupCommand{
		{name: "list", summary: "List bot agents"},
		{name: "add", summary: "Add an agent to a bot"},
		{name: "remove", summary: "Remove an agent from a bot"},
	},
}

func runBotAgentsCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := botAgentsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runBotAgentsList(rest, stdout, stderr, client, store)
	case "add":
		return runBotAgentsAdd(rest, stdout, stderr, client, store)
	case "remove":
		return runBotAgentsRemove(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func loadCLIWebToken(store credentialStore, stderr io.Writer) (string, string, bool) {
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return "", "", false
	}
	token, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "not logged in; run kei login first")
		return "", "", false
	}
	return baseURL, token, true
}

func runBotAgentsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("bot", "agents list", "List bot agents", stdout, stderr)
	installationID := lc.fs.String("installation", "", "Kei installation ID")
	lc.fs.String("agent", "", "Kei agent ID") // accepted but unused by list; validated when passed
	lc.fs.Bool("default", false, "make this the default agent")
	if rc, ok := parseLeaf(lc, args); !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "bot agents list accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "installation") {
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot agents list: --installation must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents list: %v\n", err)
		return 1
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations/"+url.PathEscape(*installationID)+"/agents", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		fmt.Fprintf(stderr, "bot agents list: %v\n", err)
		return 1
	}
	if statusCode != http.StatusOK {
		fmt.Fprintf(stderr, "bot agents list returned %d\n", statusCode)
		return 1
	}
	_, _ = stdout.Write(body)
	return 0
}

func runBotAgentsAdd(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("bot", "agents add", "Add an agent to a bot", stdout, stderr)
	installationID := lc.fs.String("installation", "", "Kei installation ID")
	agentID := lc.fs.String("agent", "", "Kei agent ID")
	makeDefault := lc.fs.Bool("default", false, "make this the default agent")
	if rc, ok := parseLeaf(lc, args); !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "bot agents add accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "installation", "agent") {
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot agents add: --installation must be a UUID")
		return 2
	}
	if _, err := uuid.Parse(*agentID); err != nil {
		fmt.Fprintln(stderr, "bot agents add: --agent must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents add: %v\n", err)
		return 1
	}
	body, _ := json.Marshal(map[string]any{"agent_id": agentID, "default": makeDefault})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations/"+url.PathEscape(*installationID)+"/agents", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doBotAgentMutation(req, stdout, stderr, client)
}

func runBotAgentsRemove(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("bot", "agents remove", "Remove an agent from a bot", stdout, stderr)
	installationID := lc.fs.String("installation", "", "Kei installation ID")
	agentID := lc.fs.String("agent", "", "Kei agent ID")
	lc.fs.Bool("default", false, "make this the default agent")
	if rc, ok := parseLeaf(lc, args); !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "bot agents remove accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "installation", "agent") {
		return 2
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "bot agents remove: --installation must be a UUID")
		return 2
	}
	if _, err := uuid.Parse(*agentID); err != nil {
		fmt.Fprintln(stderr, "bot agents remove: --agent must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents remove: %v\n", err)
		return 1
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID)+"/runtime-installations/"+url.PathEscape(*installationID)+"/agents/"+url.PathEscape(*agentID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doBotAgentMutation(req, stdout, stderr, client)
}

func doBotAgentMutation(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	statusCode, body, err := doRequest(client, req, 4<<10) // TODO(HAI-362): explicit oversize error, AIP pagination, request_id on creates
	if err != nil {
		fmt.Fprintf(stderr, "bot agents request: %v\n", err)
		return 1
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "bot agents request returned %d\n", statusCode)
		return 1
	}
	if statusCode == http.StatusOK {
		_, _ = stdout.Write(body)
	}
	return 0
}
