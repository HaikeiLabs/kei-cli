package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rotating an existing database connector's secret (HAI-493, console#395
// parity): providers the CLI learns only from the catalog rotate through the
// same seal + :setSecret path as create, and get shows their secret delivery.

// databaseSecretFields is each catalog database provider's one secret field.
var databaseSecretFields = map[string]string{
	"postgres":  "dsn",
	"mysql":     "dsn",
	"mssql":     "dsn",
	"snowflake": "key_pair",
}

// snowflakeKeyPair is a synthetic Snowflake key_pair document: multi-line,
// so it can only arrive through --secret-file.
const snowflakeKeyPair = `{"account":"FAKE-ACCOUNT","user":"FAKE_READER","role":"FAKE_RO","warehouse":"FAKE_WH",
"private_key":"-----BEGIN PRIVATE KEY-----\nFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE\n-----END PRIVATE KEY-----"}`

func databaseConnectorJSON(provider, status string) string {
	return `{"id":"` + testConnectorID + `","name":"warehouse","provider":"` + provider + `","status":"` + status + `","credential_source":"opaque_ref","account_model":"shared","credential_ref":"kei://connectors/` + testConnectorID + `/` + databaseSecretFields[provider] + `","capabilities":[{"name":"query.run","action":"read"}]}`
}

func TestConnectorsReconnectRotatesDatabaseSecret(t *testing.T) {
	secrets := map[string]string{
		"postgres":  databaseDSNs["postgres"],
		"mysql":     databaseDSNs["mysql"],
		"mssql":     databaseDSNs["mssql"],
		"snowflake": strings.ReplaceAll(snowflakeKeyPair, "\n", " "),
	}
	for _, provider := range []string{"postgres", "mysql", "mssql", "snowflake"} {
		t.Run(provider, func(t *testing.T) {
			secret, field := secrets[provider], databaseSecretFields[provider]
			rt := newSecretConsole(t, databaseConnectorJSON(provider, "active"))
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
			if rt.openedFields[field] != secret {
				t.Fatalf("the runtime key did not open the rotated %s", field)
			}
			assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
			wantOut := "Secret \"" + field + "\" sealed and queued for delivery (generation 1).\nConnector " + testConnectorID + " is connected (active).\n"
			if !strings.HasSuffix(stdout, wantOut) {
				t.Fatalf("stdout = %q, want %q", stdout, wantOut)
			}
		})
	}
}

func TestConnectorsReconnectReadsDatabaseSecretFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key_pair.json")
	if err := os.WriteFile(path, []byte(snowflakeKeyPair+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newSecretConsole(t, databaseConnectorJSON("snowflake", "failed"))
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--secret-file", path)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if rt.openedFields["key_pair"] != snowflakeKeyPair {
		t.Fatal("the runtime key did not open the key pair read from --secret-file")
	}
	assertNoPlaintext(t, snowflakeKeyPair, rt.requests, stdout, stderr)
	if !strings.Contains(stdout, `Secret "key_pair" sealed and queued for delivery (generation 1).`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestConnectorsReconnectSecretFileForBuiltInProvider(t *testing.T) {
	const secret = "tito-rotated-secret-value-9876543210"
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"failed","credential_source":"opaque_ref"}`)
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--secret-file", path)
	if code != 0 || rt.opened != secret {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	assertNoPlaintext(t, secret, rt.requests, stdout, stderr)
}

func TestConnectorsReconnectPendingDatabaseConnectorWaitsForDelivery(t *testing.T) {
	dsn := databaseDSNs["postgres"]
	rt := newSecretConsole(t, databaseConnectorJSON("postgres", "pending"))
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, dsn+"\n", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	for _, path := range rt.paths() {
		if strings.HasSuffix(path, ":reconnect") {
			t.Fatal("a pending connector is activated by the delivery ack, not by reconnect")
		}
	}
	if rt.openedFields["dsn"] != dsn || !strings.Contains(stdout, "becomes connected when the runtime stores the secret") {
		t.Fatalf("stdout = %q", stdout)
	}
	assertNoPlaintext(t, dsn, rt.requests, stdout, stderr)
}

func TestConnectorsReconnectDatabaseKeepSecretOnlyReactivates(t *testing.T) {
	rt := newSecretConsole(t, databaseConnectorJSON("mysql", "failed"))
	code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--keep-secret")
	if code != 0 || !strings.Contains(stdout, "connected") {
		t.Fatalf("exit = %d stdout=%q stderr=%s", code, stdout, stderr)
	}
	if got := rt.paths(); len(got) != 2 || !strings.HasSuffix(got[1], ":reconnect") {
		t.Fatalf("requests = %v", got)
	}
}

func TestConnectorsReconnectDatabaseRejectsInvalidSecret(t *testing.T) {
	rt := newSecretConsole(t, databaseConnectorJSON("postgres", "active"))
	code, _, stderr := runConnectors(t, rt.server, rt.store, "short\n", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, `secret field "dsn"`) || strings.Contains(stderr, "short") {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); len(got) != 1 {
		t.Fatalf("an invalid secret must stop before sealing; requests = %v", got)
	}
}

func TestConnectorsReconnectUnknownCatalogProviderFailsClosed(t *testing.T) {
	rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"oracle","status":"failed","credential_source":"opaque_ref","account_model":"shared"}`)
	code, _, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main")
	if code != 1 || !strings.Contains(stderr, "--keep-secret") {
		t.Fatalf("exit = %d stderr=%s", code, stderr)
	}
	if got := rt.paths(); len(got) != 1 {
		t.Fatalf("nothing is reactivated when the secret field is unknown; requests = %v", got)
	}
}

func TestConnectorsReconnectSecretFileConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(path, []byte(databaseDSNs["postgres"]), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("keep-secret", func(t *testing.T) {
		rt := newSecretConsole(t, databaseConnectorJSON("postgres", "active"))
		code, _, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--keep-secret", "--secret-file", path)
		if code != 2 || !strings.Contains(stderr, "--secret-file") || len(rt.paths()) != 0 {
			t.Fatalf("exit = %d stderr=%s requests=%v", code, stderr, rt.paths())
		}
	})
	t.Run("oauth", func(t *testing.T) {
		rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"gmail","status":"failed","credential_source":"oauth","account_model":"shared"}`)
		code, _, stderr := runConnectors(t, rt.server, rt.store, "", "reconnect", testConnectorID, "--workspace", "Main", "--secret-file", path)
		if code != 1 || !strings.Contains(stderr, "--secret-file") {
			t.Fatalf("exit = %d stderr=%s", code, stderr)
		}
		if got := rt.paths(); len(got) != 1 {
			t.Fatalf("requests = %v", got)
		}
	})
}

func TestConnectorsGetShowsDatabaseSecretDelivery(t *testing.T) {
	head := "ID:          " + testConnectorID + "\nName:        warehouse\nProvider:    postgres\nState:       connected (active)\nCredential:  opaque_ref (shared)\nCapabilities: query.run\nSecret ref:  kei://connectors/" + testConnectorID + "/dsn\n"
	cases := []struct {
		name, secrets, want string
	}{
		{"delivered", `{"secrets":[{"field":"dsn","set":true,"generation":3,"delivered_at":"2026-11-11T15:00:00Z"}]}`, "Secret:      dsn set (generation 3, delivered 2026-11-11T15:00:00Z)\n"},
		{"pending", `{"secrets":[{"field":"dsn","set":false,"generation":2,"delivered_at":null}]}`, "Secret:      dsn pending delivery (generation 2)\n"},
		{"never set", `{"secrets":[]}`, "Secret:      dsn not set\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newSecretConsole(t, databaseConnectorJSON("postgres", "active"))
			rt.secrets = tc.secrets
			code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "get", testConnectorID, "--workspace", "Main")
			if code != 0 {
				t.Fatalf("exit = %d stderr=%s", code, stderr)
			}
			if stdout != head+tc.want {
				t.Fatalf("stdout =\n%s\nwant\n%s", stdout, head+tc.want)
			}
		})
	}
}

// Golden: built-in providers' get and reconnect output is byte-identical to
// the output before database rotation (HAI-493).
func TestConnectorsBuiltInOutputUnchangedGolden(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","name":"events","provider":"tito","status":"pending","credential_source":"opaque_ref","credential_ref":"kei://connectors/`+testConnectorID+`/api_token","config":{"account_slug":"acme"}}`)
		rt.secrets = `{"secrets":[]}`
		code, stdout, stderr := runConnectors(t, rt.server, rt.store, "", "get", testConnectorID, "--workspace", "Main")
		if code != 0 {
			t.Fatalf("exit = %d stderr=%s", code, stderr)
		}
		const golden = "ID:          " + testConnectorID + "\nName:        events\nProvider:    tito\nState:       pending (pending)\nCredential:  opaque_ref\nSecret ref:  kei://connectors/" + testConnectorID + "/api_token\nConfig:\n  account_slug: acme\n"
		if stdout != golden {
			t.Fatalf("stdout =\n%q\nwant\n%q", stdout, golden)
		}
	})
	t.Run("reconnect", func(t *testing.T) {
		const secret = "tito-rotated-secret-value-9876543210"
		rt := newSecretConsole(t, `{"id":"`+testConnectorID+`","provider":"tito","status":"failed","credential_source":"opaque_ref"}`)
		code, stdout, stderr := runConnectors(t, rt.server, rt.store, secret+"\n", "reconnect", testConnectorID, "--workspace", "Main")
		if code != 0 {
			t.Fatalf("exit = %d stderr=%s", code, stderr)
		}
		const golden = "Tito API token:: Secret \"api_token\" sealed and queued for delivery (generation 1).\nConnector " + testConnectorID + " is connected (active).\n"
		if stdout != golden {
			t.Fatalf("stdout =\n%q\nwant\n%q", stdout, golden)
		}
	})
}
