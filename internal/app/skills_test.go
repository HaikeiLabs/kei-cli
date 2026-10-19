package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

const (
	skillsConnectorID = "11111111-2222-3333-4444-555555555555"
	skillsTestToken   = "kei_rt_skills_secret_do_not_print"
)

// skillsHome isolates HOME and XDG_CONFIG_HOME in temp dirs, so nothing
// touches the real ~/.claude, ~/.codex or ~/.config, and makes Claude Code
// the one detected harness.
func skillsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("OPENCODE_CONFIG", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

// skillsBundleServer serves whoami and a v2 bundle that binds a skills_repo
// connector and permits skill:demo and denies skill:secret.
func skillsBundleServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runtime/whoami":
			_, _ = w.Write([]byte(`{"id":"runtime-id","org_id":"org-id","platform":"cli"}`))
		case "/api/v1/runtime/policy-bundles/current":
			_, _ = w.Write([]byte(`{"schema":"kei.policy-bundle/v2","bundle_id":"bundle-1","bundle_version":3,"policy_revision":4,"audience":{"installation_id":"runtime-id","org_id":"org-id","workspace_id":"workspace-id"},"not_after":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","harness_match_semantics":"kei.harness-match/v1","harnesses":[],"policy_set":{"connectors":[{"id":"` + skillsConnectorID + `","provider":"skills_repo"}],"harness_policies":[{"policy_id":"p1","name":"deny secret","src_pattern":"*","dst_pattern":"skill:secret","action":"deny","scope":{}},{"policy_id":"p2","name":"permit demo","src_pattern":"*","dst_pattern":"skill:demo","action":"permit","scope":{}}]}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeSkillsConfig(t *testing.T, serverURL, proxyPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kei.yaml")
	config := "control_plane_url: \"" + serverURL + "\"\nruntime_token: \"" + skillsTestToken + "\"\nharness_url: \"http://127.0.0.1:9\"\nproxy_path: \"" + proxyPath + "\"\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// HAI-723 / ADR-039: the CLI no longer reads skills through kei-proxy
// connector invoke. When the bundle carries a skills_repo connector but no
// repo_url, the install prints a warning and writes nothing.
func TestSkillsInstallSkipsWhenBundleLacksRepoURL(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	config := writeSkillsConfig(t, server.URL, "")

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"install", "--config", config, "--harness", "claude_code"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "repo_url") {
		t.Fatalf("stderr does not name the missing repo_url: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill dir written: %v", err)
	}
}

func TestSkillsInstallRejectsBadUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand(nil, &stdout, &stderr, http.DefaultClient); code != 2 {
		t.Fatalf("no subcommand exit=%d", code)
	}
	if code := runSkillsCommand([]string{"install", "--harness", "nope"}, &stdout, &stderr, http.DefaultClient); code != 2 {
		t.Fatalf("bad --harness exit=%d", code)
	}
}

// HAI-723: harness sync still renders permissions; skills install is a
// no-op with a warning when the bundle lacks the repo_url.
func TestHarnessSyncWarnsWhenSkillsLackRepoURL(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	config := runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: skillsTestToken}

	var stdout, stderr bytes.Buffer
	if code := syncHarnessBundle(t.Context(), config, "claude_code", false, false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now); code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "repo_url") {
		t.Fatalf("stderr does not name the missing repo_url: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill dir written: %v", err)
	}
	// The permission render still ran and wrote the ledger.
	if _, err := os.ReadFile(testLedgerPath(home, "claude_code")); err != nil {
		t.Fatalf("ledger not written: %v", err)
	}
}
