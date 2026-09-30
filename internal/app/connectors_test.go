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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/setup"
)

const testWorkspaceID = "22222222-2222-2222-2222-222222222222"
const testConnectorID = "33333333-3333-3333-3333-333333333333"

// fakeConsole records what the CLI sent to the console's /api/cli/connectors
// routes and answers with canned responses.
type fakeConsole struct {
	t        *testing.T
	mu       sync.Mutex
	requests []consoleRequest
	respond  func(w http.ResponseWriter, r consoleRequest)
}

type consoleRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   map[string]any
	Raw    string
}

func newFakeConsole(t *testing.T, respond func(w http.ResponseWriter, r consoleRequest)) (*fakeConsole, *httptest.Server, *memoryCredentialStore) {
	t.Helper()
	fake := &fakeConsole{t: t, respond: respond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := consoleRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Raw: string(raw)}
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
	t.Setenv("KEI_WORKSPACE_ID", "")
	return fake, server, &memoryCredentialStore{server: server.URL, token: "cli-session-token"}
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
	var stdout, stderr bytes.Buffer
	code := runConnectorsCommand(args, &stdout, &stderr, strings.NewReader(stdin), server.Client(), store)
	return code, stdout.String(), stderr.String()
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
	want := []string{"crm", "github", "gmail", "google_drive", "linear", "tito"}
	var got []string
	for _, provider := range offeredConnectorProviders {
		got = append(got, string(provider))
		if _, ok := setup.SetupSchemaFor(provider); !ok {
			t.Errorf("offered provider %q has no v0.3.0 setup schema", provider)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("offered providers = %v, want %v (no s3, http_api, or notion)", got, want)
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
					}
				}
				request, err := buildConnectorCreateRequest(provider, model, "events", config, credentialRef, nil)
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
		_, _ = w.Write([]byte(`{"connectors":[
			{"id":"` + testConnectorID + `","name":"events","provider":"tito","status":"active","credential_source":"opaque_ref"},
			{"id":"55555555-5555-5555-5555-555555555555","name":"mail","provider":"gmail","status":"failed","credential_source":"oauth","account_model":"shared"},
			{"id":"66666666-6666-6666-6666-666666666666","name":"old","provider":"linear","status":"revoked","credential_source":"oauth","account_model":"per_user"}
		],"next_page_token":""}`))
	})
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/api/cli/connectors" || req.Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if req.Auth != "Bearer cli-session-token" {
		t.Fatalf("Authorization = %q", req.Auth)
	}
	for _, want := range []string{testConnectorID, "connected", "needs reconnect", "revoked", "shared", "per_user"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestConnectorsRequireWorkspace(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	for _, args := range [][]string{{"list"}, {"get", testConnectorID}, {"delete", testConnectorID, "--yes"}, {"reconnect", testConnectorID}, {"create", "--provider", "linear", "--name", "x"}} {
		code, _, stderr := runConnectors(t, server, store, "", args...)
		if code != 2 || !strings.Contains(stderr, "--workspace") {
			t.Errorf("%v: exit = %d stderr=%q, want 2 naming --workspace", args, code, stderr)
		}
	}
	if fake.count() != 0 {
		t.Fatal("a command without a workspace called the console")
	}
}

func TestConnectorsResolveWorkspaceByName(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Path == "/api/cli/workspaces" {
			_, _ = w.Write([]byte(`{"workspaces":[{"id":"` + testWorkspaceID + `","name":"Main"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"connectors":[],"next_page_token":""}`))
	})
	code, stdout, stderr := runConnectors(t, server, store, "", "list", "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if fake.count() != 2 || fake.requests[1].Query != "workspace_id="+testWorkspaceID {
		t.Fatalf("requests = %#v", fake.requests)
	}
	if !strings.Contains(stdout, "No connectors") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsGetShowsInstanceIDStateConfigAndSecretStatus(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","name":"events","provider":"tito","status":"pending","credential_source":"opaque_ref","config":{"account_slug":"acme"}}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "get", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); strings.Join(got, " ") != "GET /api/cli/connectors/"+testConnectorID+" GET /api/cli/connectors/"+testConnectorID+"/secrets" {
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
	code, _, stderr := runConnectors(t, server, store, "", "get", "not-a-uuid", "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "UUID") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorsCreatePerUserOAuth(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	code, stdout, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "gmail", "--name", "team-mail")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	req := fake.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/cli/connectors" || req.Query != "workspace_id="+testWorkspaceID {
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
	if !strings.Contains(stdout, testConnectorID) || !strings.Contains(stdout, "users connect their own accounts through their chat harness") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateSharedOAuthPointsAtReconnect(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	code, stdout, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "google_drive", "--name", "shared-drive", "--account-model", "shared", "--set", "drive_id=0AbCd")
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
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--set", "account_slug=acme", "--credential-ref", "kei/prod/tito/api-token")
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

func TestConnectorsCreateSealsSecretAndSetsItWithoutSendingPlaintext(t *testing.T) {
	const secret = "tito-live-secret-value-0123456789"
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/cli/credential-store/recipients",
		"POST /api/cli/connectors",
		"POST /api/cli/connectors/" + testConnectorID + ":setSecret",
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
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
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
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
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
	code, stdout, stderr := runConnectors(t, server, store, secret+"\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
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
	code, _, stderr := runConnectors(t, server, store, key+"\n", "create", "--workspace", testWorkspaceID, "--provider", "gmail", "--name", "mail", "--account-model", "domain_delegation", "--set", "impersonate_email=events@example.com")
	if code != 1 || !strings.Contains(stderr, `"service_account_key"`) || strings.Contains(stderr, "svc@example") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q requests=%d", code, stderr, fake.count())
	}
}

func TestConnectorsCreateDomainDelegationWithCredentialRef(t *testing.T) {
	fake, server, store := newFakeConsole(t, createdResponse)
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "gmail", "--name", "mail", "--account-model", "domain_delegation", "--set", "impersonate_email=events@example.com", "--credential-ref", "kei/prod/google/sa-key")
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
		"s3 is not a connector":      {"--provider", "s3", "--name", "x"},
		"http_api not offered":       {"--provider", "http_api", "--name", "x"},
		"crm base_url must be https": {"--provider", "crm", "--name", "crm", "--set", "base_url=http://crm.example.com", "--set", "assertion_audience=kei-crm", "--credential-ref", "kei/crm"},
		"unknown field":              {"--provider", "tito", "--name", "x", "--set", "bogus=1", "--set", "account_slug=acme", "--credential-ref", "kei/t"},
		"secret field via --set":     {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--set", "api_token=abc"},
		"account model not allowed":  {"--provider", "linear", "--name", "x", "--account-model", "domain_delegation"},
		"account model on tito":      {"--provider", "tito", "--name", "x", "--account-model", "shared", "--set", "account_slug=acme", "--credential-ref", "kei/t"},
		"missing required config":    {"--provider", "tito", "--name", "x", "--credential-ref", "kei/t"},
		"name must be an identifier": {"--provider", "linear", "--name", "has spaces"},
		"ref that is a URL":          {"--provider", "tito", "--name", "x", "--set", "account_slug=acme", "--credential-ref", "https://vault.example.com/x"},
		"credential-ref on oauth":    {"--provider", "linear", "--name", "x", "--credential-ref", "kei/t"},
		"capability not in contract": {"--provider", "linear", "--name", "x", "--capabilities", "issue.delete"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, failOnRequest(t))
			full := append([]string{"create", "--workspace", testWorkspaceID}, args...)
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
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "crm", "--name", "crm", "--set", "base_url=https://crm.example.com", "--set", "assertion_audience=kei-crm", "--credential-ref", "kei/prod/crm/api-key")
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
	code, stdout, stderr := runConnectors(t, server, store, "acme\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events", "--credential-ref", "kei/prod/tito/api-token")
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
		_, _ = w.Write([]byte(`{"error":{"reason":"provider_not_supported","message":"provider must be one of gmail"}}`))
	})
	code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", testWorkspaceID, "--provider", "linear", "--name", "x")
	if code != 1 || !strings.Contains(stderr, "provider must be one of gmail") || !strings.Contains(stderr, "400") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsReconnectPrintsConsentURL(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"gmail","status":"failed","credential_source":"oauth","account_model":"shared"}`)
	rt.authURL = "https://accounts.example.test/o/oauth2/auth?state=s"
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", testWorkspaceID, "--no-browser")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); strings.Join(got, "|") != "GET /api/cli/connectors/"+testConnectorID+"|POST /api/cli/connectors/"+testConnectorID+":reconnect" {
		t.Fatalf("requests = %v", got)
	}
	if !strings.Contains(stdout, rt.authURL) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsReconnectRotatesSecretThenReactivates(t *testing.T) {
	const secret = "tito-rotated-secret-value-9876543210"
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"failed","credential_source":"opaque_ref"}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", testWorkspaceID)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	want := []string{
		"GET /api/cli/connectors/" + testConnectorID,
		"GET /api/cli/credential-store/recipients",
		"POST /api/cli/connectors/" + testConnectorID + ":setSecret",
		"POST /api/cli/connectors/" + testConnectorID + ":reconnect",
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
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", testWorkspaceID)
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
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", testWorkspaceID, "--keep-secret")
	if code != 0 || !strings.Contains(stdout, "connected") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
	if got := rt.paths(); len(got) != 2 || !strings.HasSuffix(got[1], ":reconnect") {
		t.Fatalf("requests = %v", got)
	}
}

func TestConnectorsReconnectPerUserExplains(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","provider":"linear","status":"active","credential_source":"oauth","account_model":"per_user"}`))
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"reason":"failed_precondition","message":"each user connects their own account for this connector from the console"}}`))
	})
	code, _, stderr := runConnectors(t, server, store, "", "reconnect", testConnectorID, "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "users connect their own accounts through their chat harness") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
	if got := fake.only(t); got.Method != http.MethodGet || got.Path != "/api/cli/connectors/"+testConnectorID {
		t.Fatalf("per-user reconnect made a connect request: %#v", got)
	}
}

func TestConnectorsDeleteRequiresYes(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, store, "", "delete", testConnectorID, "--workspace", testWorkspaceID)
	if code != 2 || !strings.Contains(stderr, "--yes") || fake.count() != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsDeleteRevokes(t *testing.T) {
	fake, server, store := newFakeConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"id":"` + testConnectorID + `","status":"revoked"}`))
	})
	code, stdout, stderr := runConnectors(t, server, store, "", "delete", testConnectorID, "--workspace", testWorkspaceID, "--yes")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if req := fake.only(t); req.Method != http.MethodDelete || req.Path != "/api/cli/connectors/"+testConnectorID {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if !strings.Contains(stdout, "revoked") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsNotLoggedIn(t *testing.T) {
	_, server, _ := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, &memoryCredentialStore{}, "", "list", "--workspace", testWorkspaceID)
	if code != 1 || !strings.Contains(stderr, "kei login") {
		t.Fatalf("exit = %d stderr=%q", code, stderr)
	}
}

func TestConnectorsUsageListsSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	PrintUsage(&stdout)
	if !strings.Contains(stdout.String(), "kei connectors create|list|get|reconnect|delete") {
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
}

func newSecretConsole(t *testing.T, connectorJSON string) *secretConsole {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rt := &secretConsole{t: t, private: private, connector: connectorJSON}
	_, server, store := newFakeConsole(t, rt.respond)
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
	base := "/api/cli/connectors/" + testConnectorID
	switch {
	case r.Method == http.MethodGet && r.Path == "/api/cli/credential-store/recipients":
		recipients := `[{"runtime_installation_id":"` + testRuntimeID + `","key_id":"k1","public_key":"` + base64.RawStdEncoding.EncodeToString(rt.private.PublicKey().Bytes()) + `"}]`
		if rt.noRecipients {
			recipients = `[]`
		}
		_, _ = w.Write([]byte(`{"credential_store_installation_id":"` + testStoreID + `","secret_backend":"aws-secrets-manager","recipients":` + recipients + `}`))
	case r.Method == http.MethodPost && r.Path == "/api/cli/connectors":
		createdResponse(w, r)
	case r.Method == http.MethodPost && r.Path == base+":setSecret":
		if rt.failSetSecret {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"reason":"invalid_argument","message":"credential delivery recipient is not an active runtime for the connector workspace"}}`))
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
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"field":"` + r.Body["field"].(string) + `","set":false,"generation":1,"delivered_at":null}`))
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
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "acme\n"+secret+"\n", "create", "--workspace", testWorkspaceID, "--provider", "tito", "--name", "events")
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
