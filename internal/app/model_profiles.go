package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

type modelProfile struct {
	ProfileID          string         `json:"profile_id"`
	OrgID              string         `json:"org_id"`
	DisplayName        string         `json:"display_name"`
	Endpoint           string         `json:"endpoint"`
	DefaultModel       string         `json:"default_model"`
	AuthType           string         `json:"auth_type"`
	Status             string         `json:"status"`
	Version            int            `json:"version"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
	AgentID            string         `json:"agent_id,omitempty"`
	IsWorkspaceDefault bool           `json:"is_workspace_default,omitempty"`
	Deployment         map[string]any `json:"deployment,omitempty"`
}

type createModelProfileRequest struct {
	DisplayName  string `json:"display_name"`
	Endpoint     string `json:"endpoint"`
	AuthType     string `json:"auth_type"`
	DefaultModel string `json:"default_model,omitempty"`
}

type updateModelProfileRequest struct {
	DisplayName  string `json:"display_name,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	AuthType     string `json:"auth_type,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
}

type testModelProfileRequest struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model,omitempty"`
}

type testModelProfileResponse struct {
	Status  string `json:"status"`
	Model   string `json:"model"`
	Latency string `json:"latency,omitempty"`
	Error   string `json:"error,omitempty"`
}

func runModelProfilesCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "model-profiles requires a subcommand: list, get, create, update, delete, test, or set-default")
		return 2
	}
	switch args[0] {
	case "list":
		return runModelProfilesList(args[1:], stdout, stderr, client, store)
	case "get":
		return runModelProfilesGet(args[1:], stdout, stderr, client, store)
	case "create":
		return runModelProfilesCreate(args[1:], stdout, stderr, client, store)
	case "update":
		return runModelProfilesUpdate(args[1:], stdout, stderr, client, store)
	case "delete":
		return runModelProfilesDelete(args[1:], stdout, stderr, client, store)
	case "test":
		return runModelProfilesTest(args[1:], stdout, stderr, client, store)
	case "set-default":
		return runModelProfilesSetDefault(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown model-profiles command %q\n", args[0])
		return 2
	}
}

func loadCLIWebTokenAndBaseURL(store credentialStore, stderr io.Writer) (string, string, bool) {
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

func runModelProfilesList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles list takes no positional arguments")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/cli/model-profiles", nil)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles list: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doModelProfilesRequest(req, stdout, stderr, client)
}

func runModelProfilesGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "model-profiles get requires a profile ID")
		return 2
	}
	profileID := flags.Arg(0)
	if _, err := uuid.Parse(profileID); err != nil {
		fmt.Fprintln(stderr, "profile ID must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/cli/model-profiles/"+url.PathEscape(profileID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles get: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doModelProfilesRequest(req, stdout, stderr, client)
}

func runModelProfilesCreate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	displayName := flags.String("display-name", "", "model profile display name")
	endpoint := flags.String("endpoint", "", "model API endpoint URL")
	authType := flags.String("auth-type", "", "authentication type (e.g. bearer, basic)")
	defaultModel := flags.String("default-model", "", "default model identifier")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles create takes no positional arguments")
		return 2
	}
	if *displayName == "" || *endpoint == "" || *authType == "" {
		fmt.Fprintln(stderr, "model-profiles create requires --display-name, --endpoint, and --auth-type")
		return 2
	}
	body, err := json.Marshal(createModelProfileRequest{
		DisplayName:  *displayName,
		Endpoint:     *endpoint,
		AuthType:     *authType,
		DefaultModel: *defaultModel,
	})
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles create: %v\n", err)
		return 1
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/cli/model-profiles", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles create: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doModelProfilesRequest(req, stdout, stderr, client)
}

func runModelProfilesUpdate(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	displayName := flags.String("display-name", "", "model profile display name")
	endpoint := flags.String("endpoint", "", "model API endpoint URL")
	authType := flags.String("auth-type", "", "authentication type")
	defaultModel := flags.String("default-model", "", "default model identifier")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "model-profiles update requires a profile ID")
		return 2
	}
	profileID := flags.Arg(0)
	if _, err := uuid.Parse(profileID); err != nil {
		fmt.Fprintln(stderr, "profile ID must be a UUID")
		return 2
	}
	body, err := json.Marshal(updateModelProfileRequest{
		DisplayName:  *displayName,
		Endpoint:     *endpoint,
		AuthType:     *authType,
		DefaultModel: *defaultModel,
	})
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
		return 1
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, baseURL+"/api/cli/model-profiles/"+url.PathEscape(profileID), bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doModelProfilesRequest(req, stdout, stderr, client)
}

func runModelProfilesDelete(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("yes", false, "confirm deletion")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "model-profiles delete requires a profile ID")
		return 2
	}
	profileID := flags.Arg(0)
	if _, err := uuid.Parse(profileID); err != nil {
		fmt.Fprintln(stderr, "profile ID must be a UUID")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "model-profiles delete requires --yes to confirm")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+"/api/cli/model-profiles/"+url.PathEscape(profileID), nil)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles delete: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doModelProfilesDeleteRequest(req, stdout, stderr, client)
}

func runModelProfilesTest(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles test", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "model API endpoint URL to test")
	model := flags.String("model", "", "model identifier to test with")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles test takes no positional arguments")
		return 2
	}
	if *endpoint == "" {
		fmt.Fprintln(stderr, "model-profiles test requires --endpoint")
		return 2
	}
	body, err := json.Marshal(testModelProfileRequest{
		Endpoint: *endpoint,
		Model:    *model,
	})
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles test: %v\n", err)
		return 1
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/cli/model-profiles/test", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles test: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doModelProfilesTestRequest(req, stdout, stderr, client)
}

func runModelProfilesSetDefault(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles set-default", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "model-profiles set-default requires a profile ID")
		return 2
	}
	profileID := flags.Arg(0)
	if _, err := uuid.Parse(profileID); err != nil {
		fmt.Fprintln(stderr, "profile ID must be a UUID")
		return 2
	}
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/cli/model-profiles/"+url.PathEscape(profileID)+"/default", nil)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles set-default: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doModelProfilesRequest(req, stdout, stderr, client)
}

func doModelProfilesRequest(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles request: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return 0
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		fmt.Fprintf(stderr, "model-profiles request returned %d: %s\n", response.StatusCode, bytes.TrimSpace(body))
		return 1
	}
	_, _ = io.Copy(stdout, response.Body)
	return 0
}

func doModelProfilesDeleteRequest(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles request: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusOK {
		fmt.Fprintln(stdout, "Model profile deleted.")
		return 0
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	fmt.Fprintf(stderr, "model-profiles request returned %d: %s\n", response.StatusCode, bytes.TrimSpace(body))
	return 1
}

func doModelProfilesTestRequest(req *http.Request, stdout, stderr io.Writer, client *http.Client) int {
	var testResponse testModelProfileResponse
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles test: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		fmt.Fprintf(stderr, "model-profiles test returned %d: %s\n", response.StatusCode, bytes.TrimSpace(body))
		return 1
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&testResponse); err != nil {
		fmt.Fprintf(stderr, "model-profiles test: decode response: %v\n", err)
		return 1
	}
	if testResponse.Error != "" {
		fmt.Fprintf(stderr, "model-profiles test failed: %s\n", testResponse.Error)
		return 1
	}
	if testResponse.Status == "" {
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(testResponse)
	return 0
}

// Ensure unused import errors don't happen.
var _ = errors.New
