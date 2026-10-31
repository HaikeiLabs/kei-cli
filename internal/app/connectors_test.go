package app

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
)

const testWorkspaceID = "22222222-2222-2222-2222-222222222222"
const testConnectorID = "33333333-3333-3333-3333-333333333333"

// fakeConsole records what the CLI sent to the console's /api/v1/data-connectors
// routes and answers with canned responses.
type fakeConsole struct {
	t        *testing.T
	mu       sync.Mutex
	requests []consoleRequest
	respond  func(w http.ResponseWriter, r consoleRequest)
}

type consoleRequest struct {
	Method  string
	Path    string
	Query   string
	Auth    string
	Headers map[string]string
	Body    map[string]any
	Raw     string
}

func newFakeConsole(t *testing.T, respond func(w http.ResponseWriter, r consoleRequest)) (*fakeConsole, *httptest.Server, *memoryCredentialStore) {
	t.Helper()
	fake := &fakeConsole{t: t, respond: respond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer the access-aware workspace list so --workspace NAME resolves
		// to testWorkspaceID. Not recorded: tests count console traffic.
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/organizations/") && strings.HasSuffix(r.URL.Path, "/workspaces") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"` + testWorkspaceID + `","name":"Main"}]`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations/org-1" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"org-1","name":"Acme Corp"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		req := consoleRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
			Raw:    string(raw),
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req.Body); err != nil {
				t.Fatalf("CLI sent a non-JSON body: %v", err)
			}
		}
		fake.mu.Lock()
		fake.requests = append(fake.requests, req)
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fake.respond(w, req)
	}))
	t.Cleanup(server.Close)
	t.Setenv("KEI_WEB_URL", server.URL)
	t.Setenv("KEI_WORKSPACE", "")
	t.Setenv("KEI_WORKSPACE_ID", "")
	return fake, server, &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
}

func (f *fakeConsole) only(t *testing.T) consoleRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatalf("console requests = %d, want 1: %#v", len(f.requests), f.requests)
	}
	return f.requests[0]
}

func (f *fakeConsole) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func runConnectors(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	t.Helper()
	if len(args) > 0 && args[0] == "create" {
		args = append(args, "--yes")
	}
	var stdout, stderr bytes.Buffer
	code := runConnectorsCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
}

// runConnectorsWithoutDefaultAccess keeps older create tests focused on their
// specific behavior; default-access behavior is covered by dedicated tests.
func runConnectorsWithoutDefaultAccess(t *testing.T, server *httptest.Server, store credentialStore, stdin string, args ...string) (int, string, string) {
	if len(args) > 0 && args[0] == "create" {
		hasAccessFlag := false
		for _, arg := range args {
			if arg == "--allow-group" || arg == "--no-default-access" {
				hasAccessFlag = true
				break
			}
		}
		if !hasAccessFlag {
			args = append(args, "--no-default-access")
		}
	}
	return runConnectors(t, server, store, stdin, args...)
}

func failOnRequest(t *testing.T) func(http.ResponseWriter, consoleRequest) {
	return func(w http.ResponseWriter, r consoleRequest) {
		t.Errorf("unexpected console request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func createdResponse(w http.ResponseWriter, r consoleRequest) {
	w.WriteHeader(http.StatusCreated)
	body := map[string]any{"id": testConnectorID, "status": "pending"}
	for key, value := range r.Body {
		body[key] = value
	}
	_ = json.NewEncoder(w).Encode(body)
}

func TestConnectorsOfferedProvidersMatchTicketAndHaveSetupSchemas(t *testing.T) {
	want := []string{"crm", "github", "gmail", "google_drive", "http_api", "linear", "skills_repo", "tito"}
	var got []string
	for _, provider := range offeredConnectorProviders {
		got = append(got, string(provider))
		if _, ok := setup.SetupSchemaFor(provider); !ok {
			t.Errorf("offered provider %q has no v0.3.0 setup schema", provider)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("offered providers = %v, want %v (no s3 or notion)", got, want)
	}
}

// Every create body the CLI can build for an offered provider and account
// model must pass the same contract checks the catalog runs on create.
func TestConnectorsCreateRequestsPassTheContractCatalogCheck(t *testing.T) {
	for _, provider := range offeredConnectorProviders {
		schema, _ := setup.SetupSchemaFor(provider)
		models := contract.AccountModelsFor(provider)
		if len(models) == 0 {
			models = []contract.AccountModel{""}
		}
		for _, model := range models {
			t.Run(string(provider)+"/"+string(model), func(t *testing.T) {
				config := map[string]any{}
				credentialRef := ""
				for _, field := range schema.FieldsFor(model) {
					if field.Default != nil {
						config[field.Name] = field.Default
					}
					switch {
					case field.Secret:
						credentialRef = "kei/test/" + string(provider)
					case field.Name == "account_slug":
						config[field.Name] = "acme"
					case field.Name == "base_url":
						config[field.Name] = "https://crm.example.com"
					case field.Name == "assertion_audience":
						config[field.Name] = "kei-crm"
					case field.Name == "impersonate_email":
						config[field.Name] = "events@example.com"
					case field.Name == "repo_url":
						config[field.Name] = "https://github.com/HaikeiLabs/skills"
					}
				}
				var registration *contract.HTTPAPI
				if provider == contract.ProviderHTTPAPI {
					registration = testHTTPAPIRegistration()
				}
				request, err := buildConnectorCreateRequest(provider, model, "events", config, registration, credentialRef, nil)
				if err != nil {
					t.Fatalf("build: %v", err)
				}
				m := request.contractMetadata("44444444-4444-4444-4444-444444444444", "org-1", testWorkspaceID, "user-1")
				if model == contract.AccountModelShared {
					m.Subject = contract.SharedSubject(m.ID) // set by the catalog on create
				}
				if err := setup.ValidateMetadata(m); err != nil {
					t.Fatalf("catalog check rejects the CLI's create body: %v", err)
				}
			})
		}
	}
}

func TestConnectorsListRendersStateAndIDs(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[
		{"id":"` + testConnectorID + `","name":"events","provider":"tito","status":"active","credential_source":"opaque_ref"},
		{"id":"55555555-5555-5555-5555-555555555555","name":"mail","provider":"gmail","status":"failed","credential_source":"oauth","account_model":"shared"},
		{"id":"66666666-6666-6666-6666-666666666666","name":"old","provider":"linear","status":"revoked","credential_source":"oauth","account_model":"per_user"}
	]`))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/v1/data-connectors" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Auth != "Bearer "+store.token {
		t.Fatalf("Authorization = %q, want %q", req.Auth, "Bearer "+store.token)
	}
	for _, want := range []string{testConnectorID, "connected", "needs reconnect", "revoked", "shared", "per_user"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorsWithoutWorkspaceAutoPicksDefault(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusTeapot)
	})
	for _, args := range [][]string{{"list"}, {"get", testConnectorID}, {"delete", testConnectorID, "--yes"}, {"reconnect", testConnectorID}, {"connect", testConnectorID}, {"create", "--provider", "linear", "--name", "x"}} {
		code, _, _ := runConnectors(t, server, store, "", args...)
		if code != 1 {
			t.Errorf("%v: exit = %d, want 1 (console contacted)", args, code)
		}
	}
	if fake.count() == 0 {
		t.Fatal("expected at least one console request (workspace was auto-picked)")
	}
}

func TestConnectorsResolveWorkspaceByName(t *testing.T) {
	fake, server, _ := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`[]`))
	})
	store := &memoryCredentialStore{server: server.URL, token: workspaceTestToken()}
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 1 || fake.requests[0].Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("requests = %#v", fake.requests)
	}
	if !strings.Contains(stdout, "No connectors") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsGetShowsInstanceIDStateConfigAndSecretStatus(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","name":"events","provider":"tito","status":"pending","credential_source":"opaque_ref","config":{"account_slug":"acme"}}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "get", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); strings.Join(got, " ") != "GET /api/v1/data-connectors/"+testConnectorID+" GET /api/v1/data-connectors/"+testConnectorID+"/secrets" {
		t.Fatalf("requests = %v", got)
	}
	for _, want := range []string{testConnectorID, "tito", "pending", "account_slug", "acme", "api_token", "generation 2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorsGetRequiresUUID(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, store, "", "get", "not-a-uuid", "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "UUID") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorsCreatePerUserOAuth(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/data-connectors" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Body["provider"] != "gmail" || req.Body["credential_source"] != "oauth" || req.Body["account_model"] != "per_user" {
		t.Fatalf("body = %v", req.Body)
	}
	if ref, _ := req.Body["credential_ref"].(string); ref == "" || contract.ValidateCredentialRef(ref) != nil {
		t.Fatalf("credential_ref = %q, want a valid unique OAuth reference", ref)
	}
	if _, present := req.Body["subject"]; present {
		t.Fatal("a per_user connector must not declare a subject")
	}
	if config, _ := req.Body["config"].(map[string]any); config["include_body"] != false {
		t.Fatalf("config = %v, want the schema default include_body=false", req.Body["config"])
	}
	if !strings.Contains(stdout, testConnectorID) || !strings.Contains(stdout, "kei connectors connect "+testConnectorID) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateSharedOAuthPointsAtReconnect(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "google_drive", "--name", "shared-drive", "--account-model", "shared", "--set", "drive_id=0AbCd")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Body["account_model"] != "shared" || req.Body["credential_source"] != "oauth" {
		t.Fatalf("body = %v", req.Body)
	}
	if config, _ := req.Body["config"].(map[string]any); config["drive_id"] != "0AbCd" {
		t.Fatalf("config = %v", req.Body["config"])
	}
	if !strings.Contains(stdout, "kei connectors reconnect "+testConnectorID) {
		t.Fatalf("stdout does not point at reconnect: %q", stdout)
	}
}

func TestConnectorsCreateSharedSecretWithCredentialRef(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme", "--credential-ref", "kei/prod/tito/api-token")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Body["credential_source"] != "opaque_ref" || req.Body["credential_ref"] != "kei/prod/tito/api-token" {
		t.Fatalf("body = %v", req.Body)
	}
	if _, present := req.Body["account_model"]; present {
		t.Fatalf("tito has no account model: %v", req.Body)
	}
	if config, _ := req.Body["config"].(map[string]any); len(config) != 1 || config["account_slug"] != "acme" {
		t.Fatalf("config = %v, want only the non-secret account_slug", req.Body["config"])
	}
	caps, _ := req.Body["capabilities"].([]any)
	if len(caps) == 0 {
		t.Fatal("no capabilities declared")
	}
}

func TestConnectorsCreateSkillsRepoPerUserWithDefaults(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "skills_repo", "--name", "org-skills", "--set", "repo_url=https://github.com/HaikeiLabs/skills")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/data-connectors" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Body["provider"] != "skills_repo" || req.Body["credential_source"] != "oauth" || req.Body["account_model"] != "per_user" {
		t.Fatalf("body = %v", req.Body)
	}
	if ref, _ := req.Body["credential_ref"].(string); !strings.HasPrefix(ref, "kei/oauth/skills_repo/") {
		t.Fatalf("credential_ref = %q, want a unique OAuth reference", ref)
	}
	config, _ := req.Body["config"].(map[string]any)
	if len(config) != 3 || config["repo_url"] != "https://github.com/HaikeiLabs/skills" || config["ref"] != "main" || config["subdirectory"] != "skills/" {
		t.Fatalf("config = %v, want repo_url plus the schema defaults ref=main subdirectory=skills/", req.Body["config"])
	}
	caps, _ := req.Body["capabilities"].([]any)
	var names []string
	for _, c := range caps {
		capability, _ := c.(map[string]any)
		names = append(names, fmt.Sprint(capability["name"], ":", capability["action"]))
	}
	if strings.Join(names, ",") != "skills.list:read,skills.fetch:read" {
		t.Fatalf("capabilities = %v", names)
	}
	if scopes, _ := req.Body["scopes"].([]any); len(scopes) != 2 {
		t.Fatalf("scopes = %v", req.Body["scopes"])
	}
	if !strings.Contains(stdout, testConnectorID) || !strings.Contains(stdout, "kei connectors connect "+testConnectorID) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateSkillsRepoExplicitRefAndSubdirectory(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "skills_repo", "--name", "org-skills",
		"--account-model", "per_user", "--set", "repo_url=https://github.com/HaikeiLabs/skills", "--set", "ref=v1.2.0", "--set", "subdirectory=/")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	config, _ := fake.only(t).Body["config"].(map[string]any)
	if config["ref"] != "v1.2.0" || config["subdirectory"] != "/" {
		t.Fatalf("config = %v", config)
	}
}

func TestConnectorsCreateSkillsRepoSharedTokenByCredentialRef(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "skills_repo", "--name", "org-skills",
		"--set", "repo_url=https://github.com/HaikeiLabs/skills", "--credential-ref", "kei/prod/skills/github-token")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Body["credential_source"] != "opaque_ref" || req.Body["credential_ref"] != "kei/prod/skills/github-token" {
		t.Fatalf("body = %v", req.Body)
	}
	if _, present := req.Body["account_model"]; present {
		t.Fatalf("the opaque_ref path has no account model: %v", req.Body)
	}
	if config, _ := req.Body["config"].(map[string]any); len(config) != 3 || config["token"] != nil {
		t.Fatalf("config = %v, want only the non-secret fields", req.Body["config"])
	}
	if !strings.Contains(stdout, "kei connectors reconnect "+testConnectorID) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateSkillsRepoRejectsLocallyBeforeSending(t *testing.T) {
	for name, args := range map[string][]string{
		"http repo_url":       {"--set", "repo_url=http://github.com/HaikeiLabs/skills"},
		"non-GitHub repo_url": {"--set", "repo_url=https://gitlab.com/HaikeiLabs/skills"},
		"repo_url with path":  {"--set", "repo_url=https://github.com/HaikeiLabs/skills/tree/main"},
		"bad ref":             {"--set", "repo_url=https://github.com/HaikeiLabs/skills", "--set", "ref=-main"},
		"token as a flag":     {"--set", "repo_url=https://github.com/HaikeiLabs/skills", "--set", "token=ghp_not-a-real-token-0123456789"},
		"shared account":      {"--set", "repo_url=https://github.com/HaikeiLabs/skills", "--account-model", "shared"},
		"oauth with ref":      {"--set", "repo_url=https://github.com/HaikeiLabs/skills", "--account-model", "per_user", "--credential-ref", "kei/prod/skills/github-token"},
	} {
		t.Run(name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, failOnRequest(t))
			store.token = workspaceTestToken()
			base := []string{"create", "--workspace", "Main", "--provider", "skills_repo", "--name", "org-skills"}
			code, stdout, stderr := runConnectors(t, server, store, "", append(base, args...)...)
			if code == 0 || fake.count() != 0 {
				t.Fatalf("exit = %d requests = %d stderr=%q", code, fake.count(), stderr)
			}
			if strings.Contains(stdout+stderr, "ghp_not-a-real-token") {
				t.Fatal("output echoed a secret value")
			}
		})
	}
}

func TestConnectorsListAndGetRenderSkillsRepo(t *testing.T) {
	const skillsJSON = `{"id":"` + testConnectorID + `","name":"org-skills","provider":"skills_repo","status":"active","credential_source":"oauth","account_model":"per_user",` +
		`"capabilities":[{"name":"skills.list"},{"name":"skills.fetch"}],"config":{"repo_url":"https://github.com/HaikeiLabs/skills","ref":"main","subdirectory":"skills/"}}`
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/v1/data-connectors" {
			_, _ = w.Write([]byte("[" + skillsJSON + "]"))
			return
		}
		_, _ = w.Write([]byte(skillsJSON))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("list exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{testConnectorID, "org-skills", "skills_repo", "connected", "per_user"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, stderr = runConnectors(t, server, store, "", "get", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("get exit = %d stderr=%s", code, stderr)
	}
	for _, want := range []string{"skills_repo", "oauth (per_user)", "skills.list, skills.fetch", "repo_url: https://github.com/HaikeiLabs/skills", "ref: main", "subdirectory: skills/"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorsCreateSealsSecretAndSetsItWithoutSendingPlaintext(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	create := rt.requests[1]
	if create.Body["credential_source"] != "opaque_ref" {
		t.Fatalf("create body = %v", create.Body)
	}
	if _, present := create.Body["credential_ref"]; present {
		t.Fatal("a delivered secret's credential_ref is assigned by the catalog, not the CLI")
	}
	set := rt.requests[2]
	if set.Body["field"] != "api_token" || set.Body["credential_store_installation_id"] != testStoreID {
		t.Fatalf("setSecret body = %v", set.Body)
	}
	if rt.opened != secret {
		t.Fatalf("the runtime key opened %q, want the entered secret", rt.opened)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
	if !strings.Contains(stdout, testConnectorID) || !strings.Contains(stdout, "generation 1") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateWithoutRecipientsCreatesNothing(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	rt.noRecipients = true
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(stderr, "Nothing was created") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if got := rt.paths(); len(got) != 1 {
		t.Fatalf("requests = %v, want only the recipients lookup", got)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestConnectorsCreateReportsSetSecretFailureWithRecovery(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	rt.failSetSecret = true
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(stderr, testConnectorID) || !strings.Contains(stderr, "kei connectors reconnect "+testConnectorID) {
		t.Fatalf("exit = %d stderr=%q, want the created id and the reconnect retry", code, stderr)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestSealConnectorSecretOpensOnlyForItsRecipient(t *testing.T) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipient := credentialRecipient{RuntimeInstallationID: testRuntimeID, KeyID: "k1", PublicKey: base64.RawStdEncoding.EncodeToString(private.PublicKey().Bytes())}
	first, err := sealConnectorSecret("s3cr3t-value", recipient)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := sealConnectorSecret("s3cr3t-value", recipient)
	if first == second {
		t.Fatal("two seals of one secret are identical; the ephemeral key or nonce is reused")
	}
	if got, err := openKMP1ForTest(first, testRuntimeID, "k1", private); err != nil || got != "s3cr3t-value" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := openKMP1ForTest(first, testRuntimeID, "other-key", private); err == nil {
		t.Fatal("the envelope opened for another key id; the AAD does not bind the recipient")
	}
	if _, err := sealConnectorSecret("x", credentialRecipient{RuntimeInstallationID: testRuntimeID, KeyID: "k1", PublicKey: "not-a-key"}); err == nil {
		t.Fatal("sealed to an invalid public key")
	}
}

func TestConnectorsCreateInvalidSecretNamesFieldWithoutEchoingIt(t *testing.T) {
	const secret = "short"
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 1 || !strings.Contains(stderr, `"api_token"`) {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout+stderr, secret+"\n") || strings.Contains(stderr, ": "+secret) || fake.count() != 0 {
		t.Fatalf("invalid secret echoed or sent: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestConnectorsCreateRejectsServiceAccountKeyWithoutPrivateKey(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	key := `{"type":"service_account","client_email":"svc@example.iam.gserviceaccount.com"}`
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, key+"\n", "create", "--workspace", "Main", "--provider", "gmail", "--name", "mail", "--account-model", "domain_delegation", "--set", "impersonate_email=events@example.com")
	if code != 1 || !strings.Contains(stderr, `"service_account_key"`) || strings.Contains(stderr, "svc@example") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorsCreateDomainDelegationWithCredentialRef(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "mail", "--account-model", "domain_delegation", "--set", "impersonate_email=events@example.com", "--credential-ref", "kei/prod/google/sa-key")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Body["credential_source"] != "opaque_ref" || req.Body["account_model"] != "domain_delegation" {
		t.Fatalf("body = %v", req.Body)
	}
}

func TestConnectorsCreateRejectsLocallyBeforeSending(t *testing.T) {
	cases := map[string][]string{
		"s3 is not a connector":       {"--provider", "s3", "--name", "x"},
		"http_api without base url":   {"--provider", "http_api", "--name", "crm", "--credential-ref", "kei/crm"},
		"base url on another":         {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--credential-ref", "kei/t", "--base-url", "https://api.example.com"},
		"methods on another":          {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--credential-ref", "kei/t", "--methods", "GET"},
		"http_api base url not https": {"--provider", "http_api", "--name", "crm", "--base-url", "http://api.example.com", "--credential-ref", "kei/crm"},
		"http_api private host":       {"--provider", "http_api", "--name", "crm", "--base-url", "https://10.0.0.1", "--credential-ref", "kei/crm"},
		"http_api base url with key":  {"--provider", "http_api", "--name", "crm", "--base-url", "https://user:pw@api.example.com", "--credential-ref", "kei/crm"},
		"http_api method not allowed": {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--methods", "GET,DELETE", "--credential-ref", "kei/crm"},
		"http_api no methods":         {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--methods", "", "--credential-ref", "kei/crm"},
		"http_api relative path":      {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--paths", "api/*", "--credential-ref", "kei/crm"},
		"http_api bad auth header":    {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--auth-header", "X Key", "--credential-ref", "kei/crm"},
		"http_api block via --set":    {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--set", "http_api=x", "--credential-ref", "kei/crm"},
		"http_api kei:// ref":         {"--provider", "http_api", "--name", "crm", "--base-url", "https://api.example.com", "--credential-ref", "kei://connectors/x/api_key"},
		"crm base_url must be https":  {"--provider", "crm", "--name", "crm", "--set", "base_url=http://crm.example.com", "--set", "assertion_audience=kei-crm", "--credential-ref", "kei/crm"},
		"unknown field":               {"--provider", "tito", "--name", "x", "--set", "bogus=1", "--set", "account_slug=acme", "--credential-ref", "kei/t"},
		"secret field via --set":      {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--set", "api_token=abc"},
		"account model not allowed":   {"--provider", "linear", "--name", "x", "--account-model", "domain_delegation"},
		"account model on tito":       {"--provider", "tito", "--name", "x", "--account-model", "shared", "--set", "account_slug=acme", "--credential-ref", "kei/t"},
		"missing required config":     {"--provider", "tito", "--name", "x", "--credential-ref", "kei/t"},
		"name must be an identifier":  {"--provider", "linear", "--name", "has spaces"},
		"ref that is a URL":           {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--credential-ref", "https://vault.example.com/x"},
		"credential-ref on oauth":     {"--provider", "linear", "--name", "x", "--credential-ref", "kei/t"},
		"capability not in contract":  {"--provider", "linear", "--name", "x", "--capabilities", "issue.delete"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, failOnRequest(t))
			full := append([]string{"create", "--workspace", "Main"}, args...)
			code, _, stderr := runConnectors(t, server, store, "", full...)
			if code == 0 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want a local rejection", code, stderr)
			}
			if fake.count() != 0 {
				t.Fatal("an invalid create reached the console")
			}
		})
	}
}

func TestConnectorsCreateCRMDefaultsToCapabilitiesItsResourcesAllow(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "crm", "--name", "crm", "--set", "base_url=https://crm.example.com", "--set", "assertion_audience=kei-crm", "--credential-ref", "kei/prod/crm/api-key")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	caps, _ := fake.only(t).Body["capabilities"].([]any)
	var names []string
	for _, c := range caps {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "lead.read" {
		t.Fatalf("crm capabilities = %v, want only lead.read (investor reads need the investors resource)", names)
	}
}

func TestConnectorsCreatePromptsForMissingRequiredConfig(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "acme\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--credential-ref", "kei/prod/tito/api-token")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Tito account slug") {
		t.Fatalf("no prompt rendered from the schema label: %q", stdout)
	}
	if config, _ := fake.only(t).Body["config"].(map[string]any); config["account_slug"] != "acme" {
		t.Fatalf("config = %v", config)
	}
}

func TestConnectorsCreateSurfacesCatalogError(t *testing.T) {
	_, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"reason":"provider_not_supported","message":"provider must be one of gmail"}`))
	})
	store.token = workspaceTestToken()
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "linear", "--name", "x")
	if code != 1 || !strings.Contains(stderr, "provider must be one of gmail") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsReconnectPrintsConsentURL(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"gmail","status":"failed","credential_source":"oauth","account_model":"shared"}`)
	rt.authURL = "https://accounts.example.test/o/oauth2/auth?state=s"
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); strings.Join(got, "|") != "GET /api/v1/data-connectors/"+testConnectorID+"|POST /api/v1/data-connectors/"+testConnectorID+":reconnect" {
		t.Fatalf("requests = %v", got)
	}
	if !strings.Contains(stdout, rt.authURL) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsReconnectRotatesSecretThenReactivates(t *testing.T) {
	const secret = "tito-rotated-secret-value-9876543210"
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"failed","credential_source":"opaque_ref"}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/v1/data-connectors/" + testConnectorID,
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
		"POST /api/v1/data-connectors/" + testConnectorID + ":reconnect",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if rt.opened != secret {
		t.Fatalf("the runtime key opened %q, want the rotated secret", rt.opened)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
	if !strings.Contains(stdout, "connected") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsReconnectPendingSecretConnectorWaitsForDelivery(t *testing.T) {
	const secret = "tito-first-secret-value-0123456789"
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"pending","credential_source":"opaque_ref"}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, path := range rt.paths() {
		if strings.HasSuffix(path, ":reconnect") {
			t.Fatal("a pending connector is activated by the delivery ack, not by reconnect")
		}
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestConnectorsReconnectKeepSecretOnlyReactivates(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"failed","credential_source":"opaque_ref"}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--keep-secret")
	if code != 0 || !strings.Contains(stdout, "connected") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
	if got := rt.paths(); len(got) != 2 || !strings.HasSuffix(got[1], ":reconnect") {
		t.Fatalf("requests = %v", got)
	}
}

func TestConnectorsConnectFlow(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","capabilities":[{"name":"issue.read","required_oauth_scopes":["read"]}]}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			if got, want := r.Body["capabilities"], []any{"issue.read"}; !reflect.DeepEqual(got, want) {
				t.Errorf("connect capabilities = %#v, want %#v", got, want)
			}
			_, _ = w.Write([]byte(`{"auth_url":"https://auth.example.test/oauth?state=s","provider":"linear"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, ":connect") {
			_, _ = w.Write([]byte(`{"status":"connected","scope":"issue:read team:read","account_email":"user@example.com"}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	store.token = workspaceTestToken()

	code, stdout, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Connect your account") {
		t.Fatalf("stdout missing connect prompt:\n%s", stdout)
	}
	if !strings.Contains(stdout, "https://auth.example.test/oauth?state=s") {
		t.Fatalf("stdout missing auth URL:\n%s", stdout)
	}
	if !strings.Contains(stdout, "connected") || !strings.Contains(stdout, "issue:read team:read") || !strings.Contains(stdout, "user@example.com") {
		t.Fatalf("stdout = %q", stdout)
	}
	if fake.count() != 3 {
		t.Fatalf("requests = %d, want 3 (connector lookup, POST then GET): %#v", fake.count(), fake.requests)
	}
}

func TestConnectorsConnectWithoutEnabledCapabilitiesKeepsExistingFlow(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","capabilities":[]}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			if r.Body != nil {
				t.Errorf("connect body = %#v, want nil for no enabled capabilities", r.Body)
			}
			_, _ = w.Write([]byte(`{"auth_url":"https://auth.example.test/oauth?state=s","provider":"linear"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, ":connect") {
			_, _ = w.Write([]byte(`{"status":"connected"}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	})
	store.token = workspaceTestToken()

	code, stdout, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 0 || !strings.Contains(stdout, "Connect your account") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if fake.count() != 3 {
		t.Fatalf("requests = %d, want connector lookup, connect start, and poll", fake.count())
	}
}

func TestConnectorsConnectSendsEnabledCapabilitiesAndSkipsConsentWhenGranted(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","provider":"linear","status":"active","credential_source":"oauth","account_model":"per_user","capabilities":[{"name":"issue.read","required_oauth_scopes":["read"]}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect"):
			if got, want := r.Body["capabilities"], []any{"issue.read"}; !reflect.DeepEqual(got, want) {
				t.Errorf("connect capabilities = %#v, want %#v", got, want)
			}
			_, _ = w.Write([]byte(`{"status":"connected","scope":"read"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	store.token = workspaceTestToken()

	code, stdout, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main")
	if code != 0 || !strings.Contains(stdout, "already has the required OAuth scopes") || strings.Contains(stdout, "Connect your account") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("requests = %#v, want connector lookup and connect start", fake.requests)
	}
}

func TestConnectorsConnectPollsUntilConnected(t *testing.T) {
	pollCount := 0
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","capabilities":[{"name":"issue.read"}]}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			_, _ = w.Write([]byte(`{"auth_url":"https://auth.example.test/oauth?state=s","provider":"linear"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, ":connect") {
			pollCount++
			if pollCount < 3 {
				_, _ = w.Write([]byte(`{"status":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"connected","scope":"issue:read","account_email":"user@example.com"}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	store.token = workspaceTestToken()

	origSleep := connectPollSleep
	connectPollSleep = func(d time.Duration) {}
	defer func() { connectPollSleep = origSleep }()

	code, stdout, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if pollCount != 3 {
		t.Fatalf("poll count = %d, want 3", pollCount)
	}
	if fake.count() != 5 {
		t.Fatalf("total requests = %d, want 5 (connector lookup + 1 POST + 3 GETs)", fake.count())
	}
	if !strings.Contains(stdout, "connected") || !strings.Contains(stdout, "issue:read") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsConnect403(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `"}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"reason":"permission_denied","message":"you don't have access to this connector"}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	store.token = workspaceTestToken()

	code, _, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	for _, want := range []string{"403", "ask an admin", "kei policy create", "--group <group>", "--allow connector:" + testConnectorID, "--workspace Main"} {
		if code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("exit=%d stderr=%q missing %q", code, stderr, want)
		}
	}
	if code != 1 {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("requests = %d, want 2", fake.count())
	}
}

func TestConnectorsCreateDefaultsToAdminsAllowPolicy(t *testing.T) {
	const adminGroupID = "77777777-7777-7777-7777-777777777777"
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`[{"id":"` + adminGroupID + `","name":"admins"}]`))
		case r.Method == http.MethodPost && r.Path == "/api/v1/data-connectors":
			createdResponse(w, r)
		case r.Method == http.MethodPost && r.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `"}`))
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","name":"team-mail","status":"active"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	code, stdout, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var policy *consoleRequest
	for i := range fake.requests {
		if fake.requests[i].Method == http.MethodPost && fake.requests[i].Path == typedPoliciesPath() {
			policy = &fake.requests[i]
		}
	}
	if policy == nil {
		t.Fatal("default allow policy was not created")
	}
	assertNoLegacyPolicyFields(t, policy.Body, "default allow policy")
	selector, _ := policy.Body["selector_v2"].(map[string]any)
	principal, _ := policy.Body["principal"].(map[string]any)
	target, _ := policy.Body["target"].(map[string]any)
	if principal["kind"] != "group" || principal["value"] != "admins" || target["kind"] != "capability" || target["value"] != fmt.Sprint(selector["capability"]) || policy.Body["action"] != "permit" {
		t.Fatalf("default policy = %#v", policy)
	}
	if selector["connector_id"] != testConnectorID || selector["source"] != "gmail" {
		t.Fatalf("default selector = %#v", selector)
	}
	if !strings.Contains(fmt.Sprint(policy.Body["name"]), "admins") || !strings.Contains(stdout, "Allowed group admins to use team-mail") || !strings.Contains(stdout, "policy ") {
		t.Fatalf("stdout=%q policy=%#v", stdout, policy.Body)
	}
}

func TestConnectorsCreateMissingAdminsFailsBeforeCreate(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/groups" {
			_, _ = w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555","name":"Engineering"}]`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	})
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail")
	if code != 1 || !strings.Contains(stderr, "admins") || !strings.Contains(stderr, "--allow-group NAME") || !strings.Contains(stderr, "--no-default-access") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, req := range fake.requests {
		if req.Method == http.MethodPost && req.Path == "/api/v1/data-connectors" {
			t.Fatal("connector was created without an admins group")
		}
	}
}

func TestConnectorsCreateNoDefaultAccessSkipsPolicyAndWarns(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodPost && r.Path == "/api/v1/data-connectors" {
			createdResponse(w, r)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	})
	code, stdout, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail", "--no-default-access")
	if code != 0 || !strings.Contains(stdout, "nobody can connect") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, req := range fake.requests {
		if req.Method == http.MethodPost && req.Path == typedPoliciesPath() {
			t.Fatal("unexpected allow policy")
		}
	}
}

func TestConnectorsConnect400(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `"}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"this connector is not a per-user OAuth connector"}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	store.token = workspaceTestToken()

	code, _, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 1 || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if fake.count() != 2 {
		t.Fatalf("requests = %d, want 2", fake.count())
	}
}

func TestConnectorsConnectTimeout(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","capabilities":[{"name":"issue.read"}]}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, ":connect") {
			_, _ = w.Write([]byte(`{"auth_url":"https://auth.example.test/oauth?state=s","provider":"linear"}`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, ":connect") {
			_, _ = w.Write([]byte(`{"status":"pending"}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	store.token = workspaceTestToken()

	origSleep := connectPollSleep
	connectPollSleep = func(d time.Duration) {}
	defer func() { connectPollSleep = origSleep }()
	origTimeout := connectPollMaxDuration
	connectPollMaxDuration = time.Millisecond
	defer func() { connectPollMaxDuration = origTimeout }()

	code, _, stderr := runConnectors(t, server, store, "", "connect", testConnectorID, "--workspace", "Main", "--no-browser")
	if code != 1 || !strings.Contains(stderr, "timed out") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if fake.count() < 2 {
		t.Fatalf("requests = %d, want at least 2 (POST then at least 1 GET)", fake.count())
	}
}

func TestConnectorsReconnectPerUserExplains(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","provider":"linear","status":"active","credential_source":"oauth","account_model":"per_user"}`))
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"reason":"failed_precondition","message":"each user connects their own account for this connector from the console"}`))
	})
	store.token = workspaceTestToken()
	code, _, stderr := runConnectors(t, server, store, "", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "kei connectors connect "+testConnectorID) {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if got := fake.only(t); got.Method != http.MethodGet || got.Path != "/api/v1/data-connectors/"+testConnectorID {
		t.Fatalf("per-user reconnect made a connect request: %#v", got)
	}
}

func TestConnectorsDeleteRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, store, "", "delete", testConnectorID, "--workspace", "Main")
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsDeleteRevokes(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","status":"revoked"}`))
	})
	store.token = workspaceTestToken()
	code, stdout, stderr := runConnectors(t, server, store, "", "delete", testConnectorID, "--workspace", "Main", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if req := fake.only(t); req.Method != http.MethodDelete || req.Path != "/api/v1/data-connectors/"+testConnectorID {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(stdout, "revoked") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsNotLoggedIn(t *testing.T) {
	_, server, _ := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, &memoryCredentialStore{}, "", "list", "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "kei login") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsUsageListsSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei connectors create --workspace") || !strings.Contains(stdout.String(), "| test NAME") {
		t.Fatalf("usage missing connectors:\n%s", stdout.String())
	}
}

const testStoreID = "77777777-7777-7777-7777-777777777777"
const testRuntimeID = "88888888-8888-8888-8888-888888888888"

// secretConsole is a fake console routed by path. It holds the private half of
// a runtime credential-sync key, so a test can prove the CLI sealed the secret
// to that key: it opens each sealed payload exactly as kei-proxy credential
// sync does.
type secretConsole struct {
	t             *testing.T
	server        *httptest.Server
	store         *memoryCredentialStore
	mu            sync.Mutex
	requests      []consoleRequest
	private       *ecdh.PrivateKey
	connector     string
	authURL       string
	opened        string
	noRecipients  bool
	failSetSecret bool
	// templates answers GET /api/v1/connector-templates/{id} by id.
	templates map[string]string
	// openedFields is each setSecret's opened secret by field.
	openedFields map[string]string
}

func newSecretConsole(t *testing.T, connectorJSON string) *secretConsole {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rt := &secretConsole{t: t, private: private, connector: connectorJSON}
	_, server, store := newFakeConsole(t, rt.respond)
	store.token = workspaceTestToken() // sealForWorkspace needs a parseable JWT for orgID
	rt.server, rt.store = server, store
	return rt
}

func (rt *secretConsole) respond(w http.ResponseWriter, r consoleRequest) {
	rt.mu.Lock()
	rt.requests = append(rt.requests, r)
	rt.mu.Unlock()
	if r.Query != "workspace_id="+testWorkspaceID {
		rt.t.Errorf("%s %s query = %q, want the workspace scope", r.Method, r.Path, r.Query)
	}
	base := "/api/v1/data-connectors/" + testConnectorID
	switch {
	case r.Method == http.MethodGet && r.Path == "/api/v1/organizations/org-1/credential-store/recipients":
		recipients := `[{"runtime_installation_id":"` + testRuntimeID + `","key_id":"k1","public_key":"` + base64.RawStdEncoding.EncodeToString(rt.private.PublicKey().Bytes()) + `"}]`
		if rt.noRecipients {
			recipients = `[]`
		}
		_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","secret_backend":"aws-secrets-manager","recipients":` + recipients + `}`))
	case r.Method == http.MethodPost && r.Path == "/api/v1/data-connectors":
		createdResponse(w, r)
	case r.Method == http.MethodPost && r.Path == base+":setSecret":
		if rt.failSetSecret {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"invalid_argument","message":"credential delivery recipient is not an active runtime for the connector workspace"}`))
			return
		}
		recipients, _ := r.Body["recipients"].([]any)
		if len(recipients) != 1 {
			rt.t.Fatalf("recipients = %v", r.Body["recipients"])
		}
		recipient := recipients[0].(map[string]any)
		opened, err := openKMP1ForTest(recipient["sealed_payload"].(string), recipient["runtime_installation_id"].(string), recipient["key_id"].(string), rt.private)
		if err != nil {
			rt.t.Fatalf("the runtime key cannot open the sealed payload: %v", err)
		}
		rt.opened = opened
		if rt.openedFields == nil {
			rt.openedFields = map[string]string{}
		}
		rt.openedFields[r.Body["field"].(string)] = opened
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"field":"` + r.Body["field"].(string) + `","set":false,"generation":1,"delivered_at":null}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/api/v1/connector-templates/"):
		body, ok := rt.templates[strings.TrimPrefix(r.Path, "/api/v1/connector-templates/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"connector template not found"}}`))
			return
		}
		_, _ = w.Write([]byte(body))
	case r.Method == http.MethodGet && r.Path == base:
		_, _ = w.Write([]byte(rt.connector))
	case r.Method == http.MethodGet && r.Path == base+"/secrets":
		_, _ = w.Write([]byte(`{"secrets":[{"field":"api_token","set":true,"generation":2,"delivered_at":"2026-09-29T15:00:00Z"}]}`))
	case r.Method == http.MethodPost && r.Path == base+":reconnect":
		if rt.authURL != "" {
			_, _ = w.Write([]byte(`{"connector_id":"` + testConnectorID + `","auth_url":"` + rt.authURL + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"connector_id":"` + testConnectorID + `","status":"active"}`))
	default:
		rt.t.Errorf("unexpected console request %s %s", r.Method, r.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func (rt *secretConsole) paths() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]string, len(rt.requests))
	for i, r := range rt.requests {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

// assertNoPlaintext fails when the secret appears, raw or in a common
// encoding, in any request line, header, or body, or in the CLI's output.
func assertNoPlaintext(t *testing.T, secret string, requests []consoleRequest, outputs ...string) {
	t.Helper()
	forms := []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
		base64.URLEncoding.EncodeToString([]byte(secret)),
		hex.EncodeToString([]byte(secret)),
		url.QueryEscape(secret),
	}
	for _, r := range requests {
		haystack := r.Method + " " + r.Path + "?" + r.Query + "\n" + r.Auth + "\n" + r.Raw
		for _, form := range forms {
			if strings.Contains(haystack, form) {
				t.Fatalf("plaintext secret sent in %s %s", r.Method, r.Path)
			}
		}
	}
	for _, output := range outputs {
		for _, form := range forms {
			if strings.Contains(output, form) {
				t.Fatal("plaintext secret written to the CLI output")
			}
		}
	}
}

// openKMP1ForTest mirrors kei-proxy credential sync's decryptDeliveryPayload
// (kei-connector-runtime credential.go): the envelope a runtime opens.
func openKMP1ForTest(sealed, runtimeInstallationID, keyID string, private *ecdh.PrivateKey) (string, error) {
	payload, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		payload, err = base64.StdEncoding.DecodeString(sealed)
	}
	if err != nil || len(payload) < 4+32+12+16 || string(payload[:4]) != "KMP1" {
		return "", errors.New("invalid envelope")
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(payload[4:36])
	if err != nil {
		return "", err
	}
	shared, err := private.ECDH(ephemeral)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(append([]byte("kei-model-profile-envelope-v1\x00"), shared...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceEnd := 36 + gcm.NonceSize()
	plaintext, err := gcm.Open(nil, payload[36:nonceEnd], payload[nonceEnd:], []byte(runtimeInstallationID+"\x00"+keyID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func TestConnectorsCreateReadsPromptedConfigThenSecretFromOneStdin(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "acme\n"+secret+"\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if config, _ := rt.requests[1].Body["config"].(map[string]any); config["account_slug"] != "acme" {
		t.Fatalf("config = %v", rt.requests[1].Body["config"])
	}
	if rt.opened != secret {
		t.Fatalf("the runtime key opened %q, want the secret read after the prompted config", rt.opened)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestConnectorsCreateAdvertisesGroupAllowlist(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runConnectorsCommand([]string{"create", "--help"}, &stdout, &stderr, strings.NewReader(""), nil, nil)
	if code != 0 {
		t.Fatalf("create --help exit = %d, stderr: %s", code, stderr.String())
	}
	for _, flag := range []string{"allow-group", "no-default-access", "capabilities"} {
		if !strings.Contains(stdout.String(), flag) {
			t.Errorf("create help missing %s: %s", flag, stdout.String())
		}
	}
}

func TestConnectorsCreateAllowGroupsCreatesGroupCapabilityPolicies(t *testing.T) {
	const groupID = "55555555-5555-5555-5555-555555555555"
	const groupID2 = "66666666-6666-6666-6666-666666666666"
	const adminGroupID = "77777777-7777-7777-7777-777777777777"
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`[{"id":"` + groupID + `","name":"Engineering"},{"id":"` + groupID2 + `","name":"Operations"},{"id":"` + adminGroupID + `","name":"admins"}]`))
		case r.Method == http.MethodPost && r.Path == "/api/v1/data-connectors":
			createdResponse(w, r)
		case r.Method == http.MethodPost && r.Path == typedPoliciesPath():
			_, _ = w.Write([]byte(`{"id":"` + testPolicyID + `"}`))
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","name":"team-mail","status":"active"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail", "--allow-group", "Engineering", "--allow-group", "Operations", "--capabilities", "message.search,message.get")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	var policies []consoleRequest
	for _, req := range fake.requests {
		if req.Method == http.MethodPost && req.Path == typedPoliciesPath() {
			policies = append(policies, req)
		}
	}
	if len(policies) != 4 {
		t.Fatalf("policy creates = %d, want one per group and capability: %#v", len(policies), policies)
	}
	for i, group := range []string{"Engineering", "Engineering", "Operations", "Operations"} {
		body := policies[i].Body
		selector, _ := body["selector_v2"].(map[string]any)
		capability := []string{"message.search", "message.get", "message.search", "message.get"}[i]
		assertNoLegacyPolicyFields(t, body, "group capability policy")
		principal, _ := body["principal"].(map[string]any)
		target, _ := body["target"].(map[string]any)
		if principal["kind"] != "group" || principal["value"] != group || target["kind"] != "capability" || target["value"] != capability || body["action"] != "permit" {
			t.Errorf("policy body = %#v", body)
		}
		if selector["connector_id"] != testConnectorID || selector["source"] != "gmail" || selector["capability"] != capability {
			t.Errorf("policy selector = %#v", selector)
		}
		if !strings.Contains(fmt.Sprint(body["name"]), group) {
			t.Errorf("policy name missing group %q: %#v", group, body)
		}
		if _, ok := body["agent_id"]; ok {
			t.Errorf("group policy has agent_id: %#v", body)
		}
	}
	for _, req := range fake.requests {
		if req.Path == "/api/v1/data-connectors" && req.Method == http.MethodPost {
			caps := req.Body["capabilities"].([]any)
			if len(caps) != 2 {
				t.Fatalf("connector capabilities = %#v", caps)
			}
		}
	}
}

func TestConnectorsCreateAllowGroupConfirmationDeclinedCreatesNothing(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/groups" {
			_, _ = w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555","name":"Engineering"}]`))
			return
		}
		t.Errorf("unexpected create request %s %s", r.Method, r.Path)
	})
	var stdout, stderr bytes.Buffer
	code := runConnectorsCommand([]string{"create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail", "--allow-group", "Engineering"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store)
	created := false
	for _, req := range fake.requests {
		if req.Method == http.MethodPost {
			created = true
		}
	}
	if code != 2 || created {
		t.Fatalf("exit=%d requests=%d stderr=%q", code, fake.count(), stderr.String())
	}
}

func TestConnectorsCreateUnknownAllowGroupCreatesNoConnector(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet && r.Path == "/api/v1/groups" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	})
	code, _, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "gmail", "--name", "team-mail", "--allow-group", "Missing")
	if code != 1 || fake.count() != 1 || !strings.Contains(stderr, `group "Missing" was not found`) {
		t.Fatalf("exit=%d requests=%d stderr=%q", code, fake.count(), stderr)
	}
}

func TestConnectorsTestUsesPoliciesTestAndPrintsDecision(t *testing.T) {
	const userID = "alice"
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors":
			_, _ = w.Write([]byte(`[{"id":"` + testConnectorID + `","name":"team-mail","capabilities":[{"name":"gmail.messages.list"}]}]`))
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","name":"team-mail","capabilities":[{"name":"message.search"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/policies:test"):
			_, _ = w.Write([]byte(`{"decision":"permit","decided_by":"policy-123","outranked":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	store.token = testUserToken(userID)
	code, stdout, stderr := runConnectors(t, server, store, "", "test", "team-mail", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Decision: permit") || !strings.Contains(stdout, "policy-123") {
		t.Fatalf("test output = %q", stdout)
	}
	var req *consoleRequest
	for i := range fake.requests {
		if strings.HasSuffix(fake.requests[i].Path, "/policies:test") {
			req = &fake.requests[i]
			break
		}
	}
	if req == nil || req.Method != http.MethodPost || req.Body["as"] != "user:"+userID || req.Body["target"] != "connector:"+testConnectorID {
		t.Fatalf("policies:test request = %#v", req)
	}
}

func TestConnectorsTestPrintsServerReasonOnDeny(t *testing.T) {
	const userID = "bob"
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		switch {
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors":
			_, _ = w.Write([]byte(`[{"id":"` + testConnectorID + `","name":"linear-bugs","capabilities":[{"name":"issues.list"}]}]`))
		case r.Method == http.MethodGet && r.Path == "/api/v1/data-connectors/"+testConnectorID:
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","name":"linear-bugs","capabilities":[{"name":"issues.list"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/policies:test"):
			_, _ = w.Write([]byte(`{"decision":"deny","reason_code":"no_match","outranked":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	store.token = testUserToken(userID)
	code, stdout, stderr := runConnectors(t, server, store, "", "test", "linear-bugs", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Decision: deny") {
		t.Fatalf("test output = %q", stdout)
	}
	if !strings.Contains(stdout, "Reason: no_match") {
		t.Fatalf("test output = %q, want the server reason code", stdout)
	}
	if strings.Contains(stdout, "no matching policy") {
		t.Fatalf("test output = %q, must not print the fixed fallback", stdout)
	}
	var req *consoleRequest
	for i := range fake.requests {
		if strings.HasSuffix(fake.requests[i].Path, "/policies:test") {
			req = &fake.requests[i]
			break
		}
	}
	if req == nil || req.Body["target"] != "connector:"+testConnectorID {
		t.Fatalf("policies:test request = %#v", req)
	}
}

func testUserToken(sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pld := base64.RawURLEncoding.EncodeToString([]byte(`{"org_id":"org-1","sub":"` + sub + `"}`))
	return hdr + "." + pld + "."
}
