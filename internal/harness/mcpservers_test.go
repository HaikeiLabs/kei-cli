package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mcpCase is one harness whose MCP server config the sync renders into.
type mcpCase struct {
	harness Harness
	// config is the MCP config file under home.
	config string
	golden string
	// userEntries are user-written servers that must survive byte-for-byte.
	userEntries []string
}

var mcpCases = []mcpCase{
	{
		harness: claudeCode{}, config: ".claude.json", golden: "claude",
		userEntries: []string{
			`"my-local": {"command": "npx", "args": ["-y", "@me/server"]}`,
			`"github": {"type": "http", "url": "https://api.githubcopilot.com/mcp/"}`,
			"\"projects\": {\n    \"/work/app\": {\"allowedTools\": []}\n  }",
		},
	},
	{
		harness: codex{}, config: filepath.Join(".codex", "config.toml"), golden: "codex",
		userEntries: []string{
			"[mcp_servers.my-local]\ncommand = \"npx\"\nargs = [\"-y\", \"@me/server\"]\n",
			"[mcp_servers.github]\nurl = \"https://api.githubcopilot.com/mcp/\"\n",
			"[profiles.fast]\nmodel = \"gpt-5-mini\"\n",
		},
	},
	{
		harness: openCode{}, config: filepath.Join(".config", "opencode", "opencode.json"), golden: "opencode",
		userEntries: []string{
			`"my-local": {"type": "local", "command": ["npx", "-y", "@me/server"]}`,
			`"github": {"type": "remote", "url": "https://api.githubcopilot.com/mcp/"}`,
			`"permission": {"bash": {"git status": "allow"}}`,
		},
	},
}

// The secrets a sync can see: the runtime token in the environment and
// credential fields a catalog connector might carry. None may be written.
const (
	mcpRuntimeToken  = "kei_rt_do-not-write-me"
	mcpCredentialRef = "kei://credential-stores/vault/linear-oauth"
	mcpConnectorTok  = "lin_oauth_secret_token"
)

// mcpBundle is a policy bundle carrying the bound connectors as the catalog
// serves them, decoded the way the CLI decodes the fetched payload.
func mcpBundle(t *testing.T, version int64, connectors string) Bundle {
	t.Helper()
	payload := fmt.Sprintf(`{"schema":"kei.policy-bundle/v2","bundle_id":"b1","bundle_version":%d,"policy_revision":1,"not_after":"2099-01-01T00:00:00Z","policy_set":{"policies":[]},"connectors":%s}`, version, connectors)
	var b Bundle
	if err := json.Unmarshal([]byte(payload), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

const mcpConnectors = `[
	{"slug":"notion","kind":"mcp"},
	{"slug":"linear","kind":"mcp","credential_ref":"` + mcpCredentialRef + `","token":"` + mcpConnectorTok + `"},
	{"slug":"github","kind":"mcp"},
	{"slug":"crm","kind":"http_api"},
	{"slug":"Bad Slug","kind":"mcp"}
]`

func mcpEnv(home string) Env {
	return hermeticEnv(home, map[string]string{"KEI_RUNTIME_TOKEN": mcpRuntimeToken})
}

func syncMCP(t *testing.T, h Harness, env Env, b Bundle) string {
	t.Helper()
	r, err := h.Render(b)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := h.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// seedMCP writes the user's config and a ledger saying a previous sync
// managed "retired" (a remote entry) and "legacy-stdio" (a stdio entry).
func seedMCP(t *testing.T, c mcpCase, home string) string {
	t.Helper()
	input, err := os.ReadFile(filepath.Join("testdata", "harness", "mcp", c.golden+".input"+filepath.Ext(c.config)))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, c.config)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	kind := c.harness.Kind()
	ledger := syncLedger{HarnessID: kind, Kind: kind, Bundle: 1, Files: map[string]fileLedger{}, MCP: &mcpLedger{Path: path, Servers: []string{"retired", "legacy-stdio"}}}
	if err := writeLedger(ledgerPath(mcpEnv(home), kind), ledger); err != nil {
		t.Fatal(err)
	}
	return path
}

// HAI-489: a bound mcp connector renders as a remote server named after its
// slug at kei-proxy's /mcp/{slug} route; the user's servers and every other
// byte stay as they were; the entries a previous sync managed (remote or
// stdio) are removed.
func TestMCPServersRenderGolden(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			out := syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 2, mcpConnectors))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "harness", "mcp", c.golden+".golden"+filepath.Ext(c.config)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, golden) {
				t.Fatalf("rendered %s mismatch\ngot:\n%s\nwant:\n%s", c.config, got, golden)
			}
			if !strings.Contains(out, "github") || !strings.Contains(out, "Bad Slug") {
				t.Fatalf("sync output does not name the skipped connectors:\n%s", out)
			}
		})
	}
}

func TestMCPServersPreserveUserEntriesByteForByte(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 2, mcpConnectors))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range c.userEntries {
				if !bytes.Contains(got, []byte(entry)) {
					t.Errorf("user entry not preserved byte-for-byte: %q\nin:\n%s", entry, got)
				}
			}
		})
	}
}

// A connector unbound since the last sync loses its managed entry; the
// user's entries are left alone.
func TestMCPServersRemoveStaleManagedEntry(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			env := mcpEnv(home)
			syncMCP(t, c.harness, env, mcpBundle(t, 2, mcpConnectors))
			syncMCP(t, c.harness, env, mcpBundle(t, 3, `[{"slug":"notion","kind":"mcp"}]`))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(got, []byte("linear")) {
				t.Fatalf("stale managed entry linear still rendered:\n%s", got)
			}
			if !bytes.Contains(got, []byte("/mcp/notion")) {
				t.Fatalf("bound connector notion missing:\n%s", got)
			}
			for _, entry := range c.userEntries {
				if !bytes.Contains(got, []byte(entry)) {
					t.Errorf("user entry lost: %q", entry)
				}
			}
		})
	}
}

// The route key value (kmr_...) is never written into rendered configs: only
// the env-var name appears. Actual secrets are also never written.
func TestMCPServersWriteNoCredential(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 2, mcpConnectors))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{mcpRuntimeToken, mcpCredentialRef, mcpConnectorTok, "credential", "kmr_"} {
				if bytes.Contains(got, []byte(secret)) {
					t.Errorf("rendered %s contains %q:\n%s", c.config, secret, got)
				}
			}
		})
	}
}

// Kei never renders a stdio server: every managed entry is remote, and a
// stdio entry a previous sync managed is dropped.
func TestMCPServersNeverRenderStdio(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 2, mcpConnectors))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, stdio := range []string{"kei-proxy", "legacy-stdio", `"stdio"`, "KEI_MODE"} {
				if bytes.Contains(got, []byte(stdio)) {
					t.Errorf("rendered %s still holds stdio %q:\n%s", c.config, stdio, got)
				}
			}
		})
	}
}

// With no bound mcp connector and nothing managed, the sync leaves an
// existing config untouched and creates none.
func TestMCPServersNothingManagedLeavesConfigAlone(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, c.config)
			if c.harness.Kind() == "opencode" {
				// OpenCode creates its config whatever the policies; only
				// check the bytes of an existing one.
				input, err := os.ReadFile(filepath.Join("testdata", "harness", "mcp", "opencode.input.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, input, 0o600); err != nil {
					t.Fatal(err)
				}
				syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 1, `[]`))
				got, _ := os.ReadFile(path)
				if !bytes.Equal(got, input) {
					t.Fatalf("config changed:\n%s", got)
				}
				return
			}
			syncMCP(t, c.harness, mcpEnv(home), mcpBundle(t, 1, `[{"slug":"crm","kind":"http_api"}]`))
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("%s created with nothing to manage (err %v)", c.config, err)
			}
		})
	}
}

// The route base uses KEI_PROXY_MCP_LISTEN_ADDR (default 127.0.0.1:8086) and
// returns "" for non-loopback addresses.
func TestMCPProxyBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                 "http://127.0.0.1:8086",
		":9090":            "http://127.0.0.1:9090",
		"0.0.0.0:7000":     "http://127.0.0.1:7000",
		"127.0.0.1:7001":   "http://127.0.0.1:7001",
		"[::1]:7002":       "http://[::1]:7002",
		"not-an-addr":      "http://127.0.0.1:8086",
		"10.0.0.1:8086":    "",
		"192.168.1.1:8086": "",
		"203.0.113.1:8086": "",
	}
	for addr, want := range cases {
		env := hermeticEnv(t.TempDir(), map[string]string{"KEI_PROXY_MCP_LISTEN_ADDR": addr})
		if got := mcpProxyBaseURL(env); got != want {
			t.Errorf("KEI_PROXY_MCP_LISTEN_ADDR=%q: base = %q, want %q", addr, got, want)
		}
	}
}

// A sync of the same connectors leaves the config byte-identical.
func TestMCPServersResyncIsStable(t *testing.T) {
	for _, c := range mcpCases {
		t.Run(c.harness.Kind(), func(t *testing.T) {
			home := t.TempDir()
			path := seedMCP(t, c, home)
			env := mcpEnv(home)
			syncMCP(t, c.harness, env, mcpBundle(t, 2, mcpConnectors))
			first, _ := os.ReadFile(path)
			syncMCP(t, c.harness, env, mcpBundle(t, 3, mcpConnectors))
			second, _ := os.ReadFile(path)
			if !bytes.Equal(first, second) {
				t.Fatalf("resync changed the config\nfirst:\n%s\nsecond:\n%s", first, second)
			}
		})
	}
}
