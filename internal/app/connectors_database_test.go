package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeCatalogProviders is the catalog's GET /api/v1/connector-providers
// answer: the providers the CLI already knows plus the database providers it
// learns only from the catalog (HAI-493).
const fakeCatalogProviders = `{"connector_providers":[
{"id":"gmail","display_name":"Gmail","capabilities":["mail.read"],"account_models":["per_user","shared","domain_delegation"],"access_kinds":["read"]},
{"id":"google_drive","display_name":"Google Drive","capabilities":["file.read"],"account_models":["per_user","shared","domain_delegation"],"access_kinds":["read"]},
{"id":"linear","display_name":"Linear","capabilities":["issue.read"],"account_models":["per_user","shared"],"access_kinds":["read"]},
{"id":"github","display_name":"GitHub","capabilities":["repo.read"],"account_models":["per_user","shared"],"access_kinds":["read"]},
{"id":"tito","display_name":"Tito","capabilities":["event.read"],"access_kinds":["read"]},
{"id":"skills_repo","display_name":"Skills repository","capabilities":["skill.read"],"account_models":["per_user"],"access_kinds":["read"]},
{"id":"http_api","display_name":"HTTP API","capabilities":["http.get"],"access_kinds":["read"]},
{"id":"postgres","display_name":"PostgreSQL","capabilities":["query.run"],"account_models":["shared"],"access_kinds":["read"]},
{"id":"mysql","display_name":"MySQL","capabilities":["query.run"],"account_models":["shared"],"access_kinds":["read"]},
{"id":"mssql","display_name":"Microsoft SQL Server","capabilities":["query.run"],"account_models":["shared"],"access_kinds":["read"]},
{"id":"snowflake","display_name":"Snowflake","capabilities":["query.run"],"account_models":["shared"],"access_kinds":["read"]}
]}`

// fakeCatalogProviderDetail is GET /api/v1/connector-providers/{provider} for
// a database provider: one shared account over opaque_ref and one secret DSN.
func fakeCatalogProviderDetail(provider, display, label string) string {
	return fakeCatalogSecretProviderDetail(provider, display, "dsn", label, 8, 4096)
}

// fakeCatalogSecretProviderDetail is a catalog database provider whose one
// secret credential field is named field (Snowflake's is key_pair).
func fakeCatalogSecretProviderDetail(provider, display, field, label string, minLength, maxLength int) string {
	return `{"connector_provider":{"id":"` + provider + `","display_name":"` + display + `",
"capabilities":[{"name":"query.run","action":"read","description":"Run a read-only SQL query"}],
"account_models":["shared"],
"setup_schema":{"schema":"kei.connector.setup/v1","provider":"` + provider + `",
"auth":[{"credential_source":"opaque_ref","account_models":["shared"],"default_account_model":"shared"}],
"fields":[{"name":"` + field + `","label":"` + label + `","type":"string","location":"credential","required":true,"secret":true,"min_length":` + strconv.Itoa(minLength) + `,"max_length":` + strconv.Itoa(maxLength) + `}]}}}`
}

var fakeCatalogDetails = map[string]string{
	"postgres": fakeCatalogProviderDetail("postgres", "PostgreSQL", "Connection string (DSN) for a read-only role (Postgres or Supabase)"),
	"mysql":    fakeCatalogProviderDetail("mysql", "MySQL", "Connection string (DSN) for a read-only MySQL user"),
	"mssql":    fakeCatalogProviderDetail("mssql", "Microsoft SQL Server", "Connection string (DSN) for a read-only SQL Server login"),
	"snowflake": fakeCatalogSecretProviderDetail("snowflake", "Snowflake", "key_pair",
		"Key pair JSON (account, user, role, warehouse, private_key as unencrypted PEM) of a read-only service user", 64, 16384),
}

// serveFakeCatalogProviders answers the catalog's connector-providers routes.
// It reports whether it handled the request.
func serveFakeCatalogProviders(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/v1/connector-providers") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/v1/connector-providers" {
		_, _ = w.Write([]byte(fakeCatalogProviders))
		return true
	}
	body, ok := fakeCatalogDetails[strings.TrimPrefix(r.URL.Path, "/api/v1/connector-providers/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"reason":"not_found","message":"connector provider not found"}`))
		return true
	}
	_, _ = w.Write([]byte(body))
	return true
}

var databaseDSNs = map[string]string{
	"postgres": "postgres://reader:pg-Secret-pw-0123@db.example.com:5432/app?sslmode=require",
	"mysql":    "reader:my-Secret-pw-0123@tcp(db.example.com:3306)/app?tls=true",
	"mssql":    "sqlserver://reader:ms-Secret-pw-0123@db.example.com:1433?database=app",
}

func assertDatabaseConnectorCreated(t *testing.T, rt *secretConsole, provider, dsn, stdout, stderr string) {
	t.Helper()
	want := []string{
		"GET /api/v1/organizations/org-1/credential-store/recipients",
		"POST /api/v1/data-connectors",
		"POST /api/v1/data-connectors/" + testConnectorID + ":setSecret",
	}
	if got := rt.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	create := rt.requests[1].Body
	if create["provider"] != provider || create["credential_source"] != "opaque_ref" || create["account_model"] != "shared" {
		t.Fatalf("create body = %v", create)
	}
	if _, present := create["credential_ref"]; present {
		t.Fatal("a delivered DSN's credential_ref is assigned by the catalog, not the CLI")
	}
	if _, present := create["config"]; present {
		t.Fatalf("a database connector has no config: %v", create["config"])
	}
	capabilities, _ := create["capabilities"].([]any)
	if len(capabilities) != 1 || capabilities[0].(map[string]any)["name"] != "query.run" {
		t.Fatalf("capabilities = %v, want query.run from the catalog", create["capabilities"])
	}
	if scopes, _ := create["scopes"].([]any); len(scopes) != 1 || scopes[0] != "query.run" {
		t.Fatalf("scopes = %v", create["scopes"])
	}
	if set := rt.requests[2].Body; set["field"] != "dsn" {
		t.Fatalf("setSecret body = %v, want field dsn", set)
	}
	if rt.opened != dsn {
		t.Fatal("the runtime key did not open the entered DSN")
	}
	assertNoPlaintext(t, dsn, rt.requests, stdout, stderr)
	if !strings.Contains(stdout, "Created connector warehouse ("+provider+", ") || !strings.Contains(stdout, `Secret "dsn" sealed and queued for delivery (generation 1)`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsCreateDatabaseProvidersFromCatalog(t *testing.T) {
	for _, provider := range []string{"postgres", "mysql", "mssql"} {
		t.Run(provider, func(t *testing.T) {
			dsn := databaseDSNs[provider]
			rt := newSecretConsole(t, "")
			code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, dsn+"\n", "create", "--workspace", "Main", "--provider", provider, "--name", "warehouse")
			if code != 0 {
				t.Fatalf("exit = %d stderr=%s", code, stderr)
			}
			assertDatabaseConnectorCreated(t, rt, provider, dsn, stdout, stderr)
		})
	}
}

func TestConnectorsCreateDatabaseAcceptsExplicitSharedAccountModel(t *testing.T) {
	dsn := databaseDSNs["postgres"]
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, dsn+"\n", "create", "--workspace", "Main", "--provider", "postgres", "--name", "warehouse", "--account-model", "shared")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	assertDatabaseConnectorCreated(t, rt, "postgres", dsn, stdout, stderr)
}

func TestConnectorsCreateDatabaseReadsDSNFromSecretFile(t *testing.T) {
	dsn := databaseDSNs["mssql"]
	path := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(path, []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newSecretConsole(t, "")
	code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "", "create", "--workspace", "Main", "--provider", "mssql", "--name", "warehouse", "--secret-file", path)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	assertDatabaseConnectorCreated(t, rt, "mssql", dsn, stdout, stderr)
}

func TestConnectorsCreateDatabaseRejectsPerUser(t *testing.T) {
	fake, server, store := newFakeConsole(t, failOnRequest(t))
	code, _, stderr := runConnectors(t, server, store, databaseDSNs["postgres"]+"\n", "create", "--workspace", "Main", "--provider", "postgres", "--name", "warehouse", "--account-model", "per_user")
	if code == 0 {
		t.Fatal("per_user database connector was accepted")
	}
	for _, want := range []string{`provider "postgres"`, "shared account model", "per_user"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	}
	if fake.count() != 0 {
		t.Fatal("a per_user database connector reached the console")
	}
}

func TestConnectorsCreateDatabaseNeverEchoesDSN(t *testing.T) {
	cases := map[string]struct {
		stdin string
		args  []string
	}{
		"dsn as a --set flag": {stdin: "", args: []string{"--set", "dsn=" + databaseDSNs["postgres"]}},
		"dsn too short":       {stdin: "pw=Zq9\n", args: nil},
		"unknown --set field": {stdin: databaseDSNs["postgres"] + "\n", args: []string{"--set", "host=db.example.com"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, failOnRequest(t))
			args := append([]string{"create", "--workspace", "Main", "--provider", "postgres", "--name", "warehouse"}, tc.args...)
			code, stdout, stderr := runConnectors(t, server, store, tc.stdin, args...)
			if code == 0 || stderr == "" {
				t.Fatalf("exit = %d stderr=%q, want a local rejection", code, stderr)
			}
			for _, secret := range []string{databaseDSNs["postgres"], "pw=Zq9", "pg-Secret-pw-0123"} {
				assertNoPlaintext(t, secret, nil, stdout, stderr)
			}
			if fake.count() != 0 {
				t.Fatal("an invalid database connector reached the console")
			}
		})
	}
}

func TestConnectorsCreateUnknownProviderStillRejected(t *testing.T) {
	for _, provider := range []string{"oracle", "s3", "crm"} {
		t.Run(provider, func(t *testing.T) {
			fake, server, store := newFakeConsole(t, failOnRequest(t))
			code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--provider", provider, "--name", "x")
			if code != 2 {
				t.Fatalf("exit = %d stderr=%q, want 2", code, stderr)
			}
			want := "connectors create: --provider must be one of gmail, google_drive, linear, github, tito, skills_repo, http_api, postgres, mysql, mssql, snowflake\n"
			if stderr != want {
				t.Fatalf("stderr = %q, want %q", stderr, want)
			}
			if fake.count() != 0 {
				t.Fatal("an unknown provider reached the console's connector routes")
			}
		})
	}
}

func TestConnectorsCreateHelpNamesDatabaseProvidersAndSecretFile(t *testing.T) {
	_, server, store := newFakeConsole(t, failOnRequest(t))
	_, stdout, stderr := runConnectors(t, server, store, "", "create", "--help")
	help := stdout + stderr
	for _, want := range []string{"postgres, mysql, mssql", "--secret-file", "shared"} {
		if !strings.Contains(help, want) {
			t.Fatalf("create --help does not mention %q:\n%s", want, help)
		}
	}
}

// TestConnectorsCreateExistingProvidersOutputUnchanged pins the exact output
// of the hard-coded providers so reading the catalog changes none of it.
func TestConnectorsCreateExistingProvidersOutputUnchanged(t *testing.T) {
	t.Run("tito delivered secret", func(t *testing.T) {
		rt := newSecretConsole(t, "")
		code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, rt.server, rt.store, "tito-live-secret-value-0123456789\n", "create", "--workspace", "Main", "--provider", "tito", "--name", "events", "--set", "account_slug=acme")
		if code != 0 {
			t.Fatalf("exit = %d stderr=%s", code, stderr)
		}
		wantOut := "Tito API token:: Organization: Acme Corp\nWorkspace: Main\nCreated connector events (tito, pending).\nConnector instance ID: " + testConnectorID + "\nWarning: nobody can connect until access is granted.\nSecret \"api_token\" sealed and queued for delivery (generation 1); the connector becomes connected when the runtime stores it.\n"
		if stdout != wantOut || stderr != "" {
			t.Fatalf("stdout = %q\nstderr = %q", stdout, stderr)
		}
	})
	t.Run("linear per-user oauth", func(t *testing.T) {
		_, server, store := newFakeConsole(t, createdResponse)
		code, stdout, stderr := runConnectorsWithoutDefaultAccess(t, server, store, "", "create", "--workspace", "Main", "--provider", "linear", "--name", "issues")
		if code != 0 {
			t.Fatalf("exit = %d stderr=%s", code, stderr)
		}
		wantOut := "Organization: Acme Corp\nWorkspace: Main\nCreated connector issues (linear, pending).\nConnector instance ID: " + testConnectorID + "\nWarning: nobody can connect until access is granted.\nNext, connect your account: kei connectors connect " + testConnectorID + " --workspace Main\n"
		if stdout != wantOut || stderr != "" {
			t.Fatalf("stdout = %q\nstderr = %q", stdout, stderr)
		}
	})
	t.Run("unknown provider before login is still a usage error", func(t *testing.T) {
		_, server, store := newFakeConsole(t, failOnRequest(t))
		code, _, stderr := runConnectors(t, server, store, "", "create", "--workspace", "Main", "--name", "x")
		want := "connectors create: --provider must be one of gmail, google_drive, linear, github, tito, skills_repo, http_api\n"
		if code != 2 || stderr != want {
			t.Fatalf("exit = %d stderr = %q, want %q", code, stderr, want)
		}
	})
}
