package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/term"
)

// Model profiles are AIP resources in kei-policy-catalog, reached through the
// console's /api/v1 proxy with the CLI bearer:
//
//	organizations/{org}/model-profiles[/{profile_id}]                          org-level profiles
//	organizations/{org}/model-profiles:setDefault                              org default
//	organizations/{org}/workspaces/{ws}/model-profiles[/{profile_id}]          workspace profiles
//	organizations/{org}/workspaces/{ws}/model-profiles/{profile_id}/readiness  provider readiness
//	organizations/{org}/workspaces/{ws}/agents/{agent_id}/model-profiles       agent assignment
//	organizations/{org}/credential-store/recipients                            sealing keys
//
// The organization comes from the CLI token; --workspace takes a name or ID.

type modelProfile struct {
	ProfileID                     string   `json:"profile_id"`
	OrgID                         string   `json:"org_id"`
	AgentID                       string   `json:"agent_id,omitempty"`
	AssignedAgentID               string   `json:"assigned_agent_id,omitempty"`
	AssignedAgentIDs              []string `json:"assigned_agent_ids,omitempty"`
	IsWorkspaceDefault            bool     `json:"is_workspace_default"`
	CredentialStoreInstallationID string   `json:"credential_store_installation_id"`
	DisplayName                   string   `json:"display_name"`
	Endpoint                      string   `json:"endpoint"`
	DefaultModel                  string   `json:"default_model"`
	AuthType                      string   `json:"auth_type"`
	Status                        string   `json:"status"`
	Version                       int      `json:"version"`
	Generation                    int      `json:"generation"`
	GenerationStatus              string   `json:"generation_status"`
	CreatedAt                     string   `json:"created_at"`
	UpdatedAt                     string   `json:"updated_at"`
}

// createModelProfileRequest is the catalog's create body. API keys travel only
// as KMP1 envelopes sealed to runtime credential-sync keys.
type createModelProfileRequest struct {
	DisplayName                   string            `json:"display_name"`
	Endpoint                      string            `json:"endpoint"`
	DefaultModel                  string            `json:"default_model"`
	AuthType                      string            `json:"auth_type"`
	AgentID                       string            `json:"agent_id,omitempty"`
	IsWorkspaceDefault            bool              `json:"is_workspace_default,omitempty"`
	CredentialStoreInstallationID string            `json:"credential_store_installation_id"`
	Recipients                    []sealedRecipient `json:"recipients,omitempty"`
}

// updateModelProfileRequest is the catalog's PUT body: only supplied fields
// change.
type updateModelProfileRequest struct {
	DisplayName        *string           `json:"display_name,omitempty"`
	Endpoint           *string           `json:"endpoint,omitempty"`
	DefaultModel       *string           `json:"default_model,omitempty"`
	AuthType           *string           `json:"auth_type,omitempty"`
	Recipients         []sealedRecipient `json:"recipients,omitempty"`
	IsWorkspaceDefault *bool             `json:"is_workspace_default,omitempty"`
}

// profileIDRequest is the body of POST model-profiles:setDefault and of PUT
// …/agents/{agent_id}/model-profiles.
type profileIDRequest struct {
	ProfileID string `json:"profile_id"`
}

var errModelProfileTestUnavailable = errors.New("model-profiles test is not available yet: the Kei API has no model-profile test endpoint. Use `kei model-profiles readiness PROFILE --workspace WORKSPACE` to see whether runtimes can reach the provider")

func runModelProfilesCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "model-profiles requires a subcommand: list, get, create, update, delete, set-default, assign, unassign, assignment, readiness, or test")
		return 2
	}
	switch args[0] {
	case "list":
		return runModelProfilesList(args[1:], stdout, stderr, client, store)
	case "get":
		return runModelProfilesGet(args[1:], stdout, stderr, client, store)
	case "create":
		return runModelProfilesCreate(args[1:], stdout, stderr, stdin, client, store)
	case "update":
		return runModelProfilesUpdate(args[1:], stdout, stderr, stdin, client, store)
	case "delete":
		return runModelProfilesDelete(args[1:], stdout, stderr, client, store)
	case "set-default":
		return runModelProfilesSetDefault(args[1:], stdout, stderr, client, store)
	case "assign":
		return runModelProfilesAssign(args[1:], stdout, stderr, client, store)
	case "unassign":
		return runModelProfilesUnassign(args[1:], stdout, stderr, client, store)
	case "assignment":
		return runModelProfilesAssignment(args[1:], stdout, stderr, client, store)
	case "readiness":
		return runModelProfilesReadiness(args[1:], stdout, stderr, client, store)
	case "test":
		return runModelProfilesTest(args[1:], stderr)
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

// orgSession is a logged-in CLI call against the organization bound to the
// CLI token, optionally scoped to one workspace.
type orgSession struct {
	client      *http.Client
	baseURL     string
	token       string
	orgID       string
	workspaceID string
}

// openOrgSession loads the CLI token, reads its organization, and resolves
// workspace (a name or ID) when one is given.
func openOrgSession(command, workspace string, stderr io.Writer, client *http.Client, store credentialStore) (*orgSession, bool) {
	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return nil, false
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v; run kei login again\n", command, err)
		return nil, false
	}
	session := &orgSession{client: client, baseURL: baseURL, token: token, orgID: orgID}
	if workspace != "" {
		workspaceID, err := resolveWorkspaceID(context.Background(), client, baseURL, token, workspace)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", command, err)
			return nil, false
		}
		session.workspaceID = workspaceID
	}
	return session, true
}

// orgPath is /api/v1/organizations/{org}<suffix>.
func (s *orgSession) orgPath(suffix string) string {
	return "/api/v1/organizations/" + url.PathEscape(s.orgID) + suffix
}

// profilesPath is the model-profiles collection for the session's scope: the
// workspace collection when a workspace is set, otherwise the org collection.
func (s *orgSession) profilesPath() string {
	if s.workspaceID != "" {
		return s.orgPath("/workspaces/" + url.PathEscape(s.workspaceID) + "/model-profiles")
	}
	return s.orgPath("/model-profiles")
}

func (s *orgSession) profilePath(profileID string) string {
	return s.profilesPath() + "/" + url.PathEscape(profileID)
}

func (s *orgSession) agentProfilePath(agentID string) string {
	return s.orgPath("/workspaces/" + url.PathEscape(s.workspaceID) + "/agents/" + url.PathEscape(agentID) + "/model-profiles")
}

// do sends one request and returns the status and body of a 2xx response, or
// an error carrying the server's message and status. HTML from the console's
// SPA fallthrough (a route the console does not proxy) is an error with
// status 0, since no API answered.
func (s *orgSession) do(method, path string, query url.Values, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := s.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	status, payload, err := doRequest(s.client, req, 4<<20)
	if err != nil {
		return 0, nil, err
	}
	if status < 200 || status > 299 {
		return status, nil, fmt.Errorf("%s (HTTP %d)", connectorErrorMessage(payload), status)
	}
	return status, payload, nil
}

// sealForProfile lists the credential-sync keys that may receive a key for a
// profile in scope and seals apiKey to each. It returns the credential store
// the profile belongs to. With apiKey empty it only resolves the store.
func (s *orgSession) sealForProfile(agentID, apiKey string) (string, []sealedRecipient, error) {
	query := url.Values{}
	if s.workspaceID != "" {
		query.Set("workspace_id", s.workspaceID)
		if agentID != "" {
			query.Set("agent_id", agentID)
		}
	}
	_, payload, err := s.do(http.MethodGet, s.orgPath("/credential-store/recipients"), query, nil)
	if err != nil {
		return "", nil, fmt.Errorf("list credential delivery recipients: %w", err)
	}
	var recipients credentialRecipients
	if err := json.Unmarshal(payload, &recipients); err != nil {
		return "", nil, fmt.Errorf("decode credential delivery recipients: %w", err)
	}
	if recipients.CredentialStoreInstallationID == "" {
		return "", nil, errors.New("no active credential store; configure one with kei credential-store put")
	}
	if apiKey == "" {
		return recipients.CredentialStoreInstallationID, nil, nil
	}
	if len(recipients.Recipients) == 0 {
		return "", nil, errors.New("no runtime in this scope can receive the API key; set up a runtime with a credential-sync key first")
	}
	sealed := make([]sealedRecipient, 0, len(recipients.Recipients))
	for _, recipient := range recipients.Recipients {
		payload, err := sealConnectorSecret(apiKey, recipient)
		if err != nil {
			return "", nil, fmt.Errorf("seal API key for runtime %s: %w", recipient.RuntimeInstallationID, err)
		}
		sealed = append(sealed, sealedRecipient{RuntimeInstallationID: recipient.RuntimeInstallationID, KeyID: recipient.KeyID, SealedPayload: payload})
	}
	return recipients.CredentialStoreInstallationID, sealed, nil
}

// resolveProfileID returns id when it is a UUID; otherwise it lists the
// session's scope and matches exactly one non-revoked profile by display name.
func (s *orgSession) resolveProfileID(id string) (string, error) {
	if _, err := uuid.Parse(id); err == nil {
		return id, nil
	}
	_, payload, err := s.do(http.MethodGet, s.profilesPath(), nil, nil)
	if err != nil {
		return "", fmt.Errorf("resolve model profile: %w", err)
	}
	var profiles []modelProfile
	if err := json.Unmarshal(payload, &profiles); err != nil {
		return "", fmt.Errorf("decode model profiles: %w", err)
	}
	var matches []modelProfile
	for _, profile := range profiles {
		if profile.DisplayName == id && profile.Status != "revoked" {
			matches = append(matches, profile)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no model profile found with name %q", id)
	case 1:
		return matches[0].ProfileID, nil
	default:
		return "", fmt.Errorf("multiple model profiles found with name %q; use ID instead", id)
	}
}

// readAPIKey reads a provider API key without echo from a terminal, or as the
// first line of piped stdin. The key is never a flag, and never printed.
func readAPIKey(stdin io.Reader, stderr io.Writer) (string, error) {
	if stdin == nil {
		return "", errors.New("no API key on stdin")
	}
	var key string
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(stderr, "API key: ")
		raw, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read API key: %w", err)
		}
		key = string(raw)
	} else {
		line, err := bufio.NewReader(io.LimitReader(stdin, 64<<10)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read API key: %w", err)
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("no API key on stdin; pipe it in or enter it at the prompt")
	}
	return key, nil
}

func profileWorkspaceFlag(flags *flag.FlagSet) *string {
	return flags.String("workspace", "", "workspace ID or name; omit for organization-level profiles")
}

func requireProfileWorkspace(command, workspace string, stderr io.Writer) (string, bool) {
	if workspace == "" {
		workspace = os.Getenv("KEI_WORKSPACE_ID")
	}
	if workspace == "" {
		fmt.Fprintf(stderr, "model-profiles %s requires --workspace WORKSPACE (or KEI_WORKSPACE_ID)\n", command)
		return "", false
	}
	return workspace, true
}

func requireAgentID(command, agentID string, stderr io.Writer) bool {
	if agentID == "" {
		fmt.Fprintf(stderr, "model-profiles %s requires --agent AGENT_ID\n", command)
		return false
	}
	if _, err := uuid.Parse(agentID); err != nil {
		fmt.Fprintln(stderr, "agent ID must be a UUID")
		return false
	}
	return true
}

func requireProfileArg(command, id string, stderr io.Writer) bool {
	if id == "" {
		fmt.Fprintf(stderr, "model-profiles %s requires a profile ID or name\n", command)
		return false
	}
	return true
}

func validProfileAuthType(authType string) bool {
	return authType == "none" || authType == "api_key"
}

func writeProfileResult(command string, stdout, stderr io.Writer, payload []byte, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles %s: %v\n", command, err)
		return 1
	}
	_, _ = stdout.Write(payload)
	return 0
}

func runModelProfilesList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles list takes no positional arguments")
		return 2
	}
	session, ok := openOrgSession("model-profiles list", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, session.profilesPath(), nil, nil)
	return writeProfileResult("list", stdout, stderr, payload, err)
}

func runModelProfilesGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles get: %v\n", err)
		return 2
	}
	if !requireProfileArg("get", id, stderr) {
		return 2
	}
	session, ok := openOrgSession("model-profiles get", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles get: %v\n", err)
		return 1
	}
	_, payload, err := session.do(http.MethodGet, session.profilePath(profileID), nil, nil)
	return writeProfileResult("get", stdout, stderr, payload, err)
}

func runModelProfilesCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	displayName := flags.String("display-name", "", "model profile display name")
	endpoint := flags.String("endpoint", "", "model API endpoint URL (https)")
	authType := flags.String("auth-type", "", "none or api_key (api_key reads the key from stdin)")
	defaultModel := flags.String("default-model", "", "default model identifier")
	agentID := flags.String("agent", "", "assign the workspace profile to this agent ID")
	workspaceDefault := flags.Bool("workspace-default", false, "make this the workspace's default profile")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles create takes no positional arguments")
		return 2
	}
	if *displayName == "" || *endpoint == "" || *authType == "" || *defaultModel == "" {
		fmt.Fprintln(stderr, "model-profiles create requires --display-name, --endpoint, --auth-type, and --default-model")
		return 2
	}
	if !validProfileAuthType(*authType) {
		fmt.Fprintln(stderr, "--auth-type must be none or api_key")
		return 2
	}
	if (*agentID != "" || *workspaceDefault) && *workspace == "" {
		fmt.Fprintln(stderr, "--agent and --workspace-default require --workspace")
		return 2
	}
	if *agentID != "" {
		if _, err := uuid.Parse(*agentID); err != nil {
			fmt.Fprintln(stderr, "agent ID must be a UUID")
			return 2
		}
	}
	if *authType == "api_key" && *workspace != "" && *agentID == "" && !*workspaceDefault {
		fmt.Fprintln(stderr, "an api_key workspace profile needs --agent or --workspace-default so the key has a runtime to go to")
		return 2
	}
	apiKey := ""
	if *authType == "api_key" {
		var err error
		if apiKey, err = readAPIKey(stdin, stderr); err != nil {
			fmt.Fprintf(stderr, "model-profiles create: %v\n", err)
			return 2
		}
	}
	session, ok := openOrgSession("model-profiles create", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	storeID, recipients, err := session.sealForProfile(*agentID, apiKey)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles create: %v\n", err)
		return 1
	}
	_, payload, err := session.do(http.MethodPost, session.profilesPath(), nil, createModelProfileRequest{
		DisplayName:                   *displayName,
		Endpoint:                      *endpoint,
		DefaultModel:                  *defaultModel,
		AuthType:                      *authType,
		AgentID:                       *agentID,
		IsWorkspaceDefault:            *workspaceDefault,
		CredentialStoreInstallationID: storeID,
		Recipients:                    recipients,
	})
	return writeProfileResult("create", stdout, stderr, payload, err)
}

func runModelProfilesUpdate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	flags.String("display-name", "", "model profile display name")
	flags.String("endpoint", "", "model API endpoint URL (https)")
	authType := flags.String("auth-type", "", "none or api_key (switching to api_key reads the key from stdin)")
	flags.String("default-model", "", "default model identifier")
	rotateKey := flags.Bool("rotate-key", false, "read a replacement API key from stdin")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
		return 2
	}
	if !requireProfileArg("update", id, stderr) {
		return 2
	}
	var req updateModelProfileRequest
	flags.Visit(func(f *flag.Flag) {
		value := f.Value.String()
		switch f.Name {
		case "display-name":
			req.DisplayName = &value
		case "endpoint":
			req.Endpoint = &value
		case "auth-type":
			req.AuthType = &value
		case "default-model":
			req.DefaultModel = &value
		}
	})
	if req.AuthType != nil && !validProfileAuthType(*authType) {
		fmt.Fprintln(stderr, "--auth-type must be none or api_key")
		return 2
	}
	if *rotateKey && req.AuthType != nil && *authType == "none" {
		fmt.Fprintln(stderr, "--rotate-key cannot be combined with --auth-type none")
		return 2
	}
	needsKey := *rotateKey || (req.AuthType != nil && *authType == "api_key")
	if req.DisplayName == nil && req.Endpoint == nil && req.AuthType == nil && req.DefaultModel == nil && !needsKey {
		fmt.Fprintln(stderr, "model-profiles update requires at least one of --display-name, --endpoint, --auth-type, --default-model, or --rotate-key")
		return 2
	}
	apiKey := ""
	if needsKey {
		if apiKey, err = readAPIKey(stdin, stderr); err != nil {
			fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
			return 2
		}
	}
	session, ok := openOrgSession("model-profiles update", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
		return 1
	}
	if needsKey {
		// Seal only to the runtimes in the profile's current scope: its
		// assigned agent's runtimes, else the workspace's.
		agentID := ""
		if session.workspaceID != "" {
			_, payload, err := session.do(http.MethodGet, session.profilePath(profileID), nil, nil)
			if err != nil {
				fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
				return 1
			}
			var current modelProfile
			if err := json.Unmarshal(payload, &current); err != nil {
				fmt.Fprintf(stderr, "model-profiles update: decode model profile: %v\n", err)
				return 1
			}
			if !current.IsWorkspaceDefault {
				agentID = current.AssignedAgentID
			}
		}
		if _, req.Recipients, err = session.sealForProfile(agentID, apiKey); err != nil {
			fmt.Fprintf(stderr, "model-profiles update: %v\n", err)
			return 1
		}
	}
	_, payload, err := session.do(http.MethodPut, session.profilePath(profileID), nil, req)
	return writeProfileResult("update", stdout, stderr, payload, err)
}

func runModelProfilesDelete(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	yes := flags.Bool("yes", false, "confirm deletion")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles delete: %v\n", err)
		return 2
	}
	if !requireProfileArg("delete", id, stderr) {
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "model-profiles delete requires --yes to confirm")
		return 2
	}
	session, ok := openOrgSession("model-profiles delete", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles delete: %v\n", err)
		return 1
	}
	if _, _, err := session.do(http.MethodDelete, session.profilePath(profileID), nil, nil); err != nil {
		fmt.Fprintf(stderr, "model-profiles delete: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Model profile deleted.")
	return 0
}

// runModelProfilesSetDefault sets the organization default (POST
// model-profiles:setDefault) or, with --workspace, the workspace default.
func runModelProfilesSetDefault(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles set-default", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles set-default: %v\n", err)
		return 2
	}
	if !requireProfileArg("set-default", id, stderr) {
		return 2
	}
	session, ok := openOrgSession("model-profiles set-default", *workspace, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles set-default: %v\n", err)
		return 1
	}
	var payload []byte
	if session.workspaceID != "" {
		isDefault := true
		_, payload, err = session.do(http.MethodPut, session.profilePath(profileID), nil, updateModelProfileRequest{IsWorkspaceDefault: &isDefault})
	} else {
		_, payload, err = session.do(http.MethodPost, session.orgPath("/model-profiles:setDefault"), nil, profileIDRequest{ProfileID: profileID})
	}
	return writeProfileResult("set-default", stdout, stderr, payload, err)
}

func runModelProfilesAssign(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles assign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	agentID := flags.String("agent", "", "agent ID to assign the profile to")
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles assign: %v\n", err)
		return 2
	}
	if !requireProfileArg("assign", id, stderr) || !requireAgentID("assign", *agentID, stderr) {
		return 2
	}
	ws, ok := requireProfileWorkspace("assign", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("model-profiles assign", ws, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles assign: %v\n", err)
		return 1
	}
	_, payload, err := session.do(http.MethodPut, session.agentProfilePath(*agentID), nil, profileIDRequest{ProfileID: profileID})
	return writeProfileResult("assign", stdout, stderr, payload, err)
}

func runModelProfilesUnassign(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles unassign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	agentID := flags.String("agent", "", "agent ID whose profile assignment to remove")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles unassign takes no positional arguments")
		return 2
	}
	if !requireAgentID("unassign", *agentID, stderr) {
		return 2
	}
	ws, ok := requireProfileWorkspace("unassign", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("model-profiles unassign", ws, stderr, client, store)
	if !ok {
		return 1
	}
	if _, _, err := session.do(http.MethodDelete, session.agentProfilePath(*agentID), nil, nil); err != nil {
		fmt.Fprintf(stderr, "model-profiles unassign: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Model profile assignment removed.")
	return 0
}

func runModelProfilesAssignment(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles assignment", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	agentID := flags.String("agent", "", "agent ID whose profile assignment to show")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "model-profiles assignment takes no positional arguments")
		return 2
	}
	if !requireAgentID("assignment", *agentID, stderr) {
		return 2
	}
	ws, ok := requireProfileWorkspace("assignment", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("model-profiles assignment", ws, stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, session.agentProfilePath(*agentID), nil, nil)
	return writeProfileResult("assignment", stdout, stderr, payload, err)
}

func runModelProfilesReadiness(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("model-profiles readiness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := profileWorkspaceFlag(flags)
	id, err := parsePolicyFlags(flags, args)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles readiness: %v\n", err)
		return 2
	}
	if !requireProfileArg("readiness", id, stderr) {
		return 2
	}
	ws, ok := requireProfileWorkspace("readiness", *workspace, stderr)
	if !ok {
		return 2
	}
	session, ok := openOrgSession("model-profiles readiness", ws, stderr, client, store)
	if !ok {
		return 1
	}
	profileID, err := session.resolveProfileID(id)
	if err != nil {
		fmt.Fprintf(stderr, "model-profiles readiness: %v\n", err)
		return 1
	}
	_, payload, err := session.do(http.MethodGet, session.profilePath(profileID)+"/readiness", nil, nil)
	return writeProfileResult("readiness", stdout, stderr, payload, err)
}

// runModelProfilesTest fails without a request: the catalog has no test
// endpoint, and the console no longer serves /api/cli/model-profiles/test.
func runModelProfilesTest(_ []string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%v\n", errModelProfileTestUnavailable)
	return 1
}
