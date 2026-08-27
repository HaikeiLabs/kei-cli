package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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

func runBotAgentsCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "bot agents requires list, add, or remove")
		return 2
	}
	switch args[0] {
	case "list":
		return runBotAgentsList(args[1:], stdout, stderr, client, store)
	case "add":
		return runBotAgentsAdd(args[1:], stdout, stderr, client, store)
	case "remove":
		return runBotAgentsRemove(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown bot agents command %q\n", args[0])
		return 2
	}
}

func parseBotAgentFlags(args []string, stderr io.Writer, command string) (string, string, string, bool, bool) {
	flags := flag.NewFlagSet("bot agents "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	installationID := flags.String("installation", "", "Kei installation ID")
	agentID := flags.String("agent", "", "Kei agent ID")
	makeDefault := flags.Bool("default", false, "make this the default agent")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return "", "", "", false, false
	}
	if _, err := uuid.Parse(*installationID); err != nil {
		fmt.Fprintln(stderr, "--installation must be a UUID")
		return "", "", "", false, false
	}
	if *agentID != "" {
		if _, err := uuid.Parse(*agentID); err != nil {
			fmt.Fprintln(stderr, "--agent must be a UUID")
			return "", "", "", false, false
		}
	}
	return *apiURL, *installationID, *agentID, *makeDefault, true
}

func loadCLIWebToken(apiURL string, store credentialStore, stderr io.Writer) (string, string, bool) {
	baseURL, err := normalizedKeiWebURL(apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents: %v\n", err)
		return "", "", false
	}
	token, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "bot agents: not logged in; run kei login first")
		return "", "", false
	}
	return baseURL, token, true
}

func runBotAgentsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	apiURL, installationID, _, _, ok := parseBotAgentFlags(args, stderr, "list")
	if !ok {
		fmt.Fprintln(stderr, "bot agents list requires --installation ID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(apiURL, store, stderr)
	if !ok {
		return 1
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/agents", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents list: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "bot agents list returned %d\n", response.StatusCode)
		return 1
	}
	_, _ = io.Copy(stdout, response.Body)
	return 0
}

func runBotAgentsAdd(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	apiURL, installationID, agentID, makeDefault, ok := parseBotAgentFlags(args, stderr, "add")
	if !ok || agentID == "" {
		fmt.Fprintln(stderr, "bot agents add requires --installation ID and --agent ID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(apiURL, store, stderr)
	if !ok {
		return 1
	}
	body, _ := json.Marshal(map[string]any{"agent_id": agentID, "default": makeDefault})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/agents", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doBotAgentMutation(req, stdout, stderr, client)
}

func runBotAgentsRemove(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	apiURL, installationID, agentID, _, ok := parseBotAgentFlags(args, stderr, "remove")
	if !ok || agentID == "" {
		fmt.Fprintln(stderr, "bot agents remove requires --installation ID and --agent ID")
		return 2
	}
	baseURL, token, ok := loadCLIWebToken(apiURL, store, stderr)
	if !ok {
		return 1
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+"/api/cli/runtime-installations/"+url.PathEscape(installationID)+"/agents/"+url.PathEscape(agentID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doBotAgentMutation(req, stdout, stderr, client)
}

func doBotAgentMutation(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "bot agents request: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "bot agents request returned %d\n", response.StatusCode)
		return 1
	}
	if response.StatusCode == http.StatusOK {
		_, _ = io.Copy(stdout, response.Body)
	}
	return 0
}
