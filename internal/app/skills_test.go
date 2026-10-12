package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
	"github.com/HaikeiLabs/kei-connector-contracts/providers"
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

func skillArchiveFixture(t *testing.T, skill providers.SkillsRepoSkill, body string) providers.SkillsRepoArchive {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: skill.Name + "/SKILL.md", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	return providers.SkillsRepoArchive{Skill: skill, Commit: "c0ffee", Format: providers.SkillsRepoArchiveFormat, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: buf.Len(), Archive: buf.Bytes()}
}

// fakeKeiProxy writes a kei-proxy executable that answers connector invoke
// for skills.list and skills.fetch the way the runtime does (one JSON object
// with decision and result), logs its argv and whether the runtime token
// reached it through the environment, and denies anything else.
func fakeKeiProxy(t *testing.T, deny bool) (proxyPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "invocations.log")
	demo := providers.SkillsRepoSkill{Name: "demo", Version: "1.0.0", Path: "skills/demo"}
	secret := providers.SkillsRepoSkill{Name: "secret", Version: "1.0.0", Path: "skills/secret"}
	write := func(name string, value any) string {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	allow := func(capability string, result any) map[string]any {
		return map[string]any{"decision": "allow", "connector_id": skillsConnectorID, "provider": "skills_repo", "capability": capability, "result": result}
	}
	list := write("list.json", allow("skills.list", providers.SkillsRepoListing{Ref: "main", Commit: "c0ffee", Skills: []providers.SkillsRepoSkill{demo, secret}}))
	fetch := write("demo.json", allow("skills.fetch", skillArchiveFixture(t, demo, "---\nname: demo\n---\n# demo from the org\n")))
	denied := write("deny.json", map[string]any{"decision": "deny", "reason_code": "connector_not_bound", "reason": "agent is not bound to the connector"})
	code := "0"
	if deny {
		list, fetch, code = denied, denied, "1"
	}
	script := "#!/bin/sh\n" +
		"printf 'argv: %s\\n' \"$*\" >> '" + logPath + "'\n" +
		"if [ \"$KEI_RUNTIME_TOKEN\" = '" + skillsTestToken + "' ]; then echo 'token: env' >> '" + logPath + "'; else cat '" + denied + "'; exit 1; fi\n" +
		"case \"$*\" in\n" +
		"  'connector invoke --connector " + skillsConnectorID + " --capability skills.list --action read --resource skills') cat '" + list + "'; exit " + code + " ;;\n" +
		"  'connector invoke --connector " + skillsConnectorID + " --capability skills.fetch --action read --resource skills/demo') cat '" + fetch + "'; exit " + code + " ;;\n" +
		"  *) cat '" + denied + "'; exit 1 ;;\n" +
		"esac\n"
	proxyPath = filepath.Join(dir, "kei-proxy")
	if err := os.WriteFile(proxyPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return proxyPath, logPath
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

func TestSkillsInstallFetchesThroughKeiProxy(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	proxy, log := fakeKeiProxy(t, false)
	config := writeSkillsConfig(t, server.URL, proxy)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"install", "--config", config, "--harness", "claude_code", "--dry-run"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("dry run exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "install demo 1.0.0") || strings.Contains(stdout.String(), "secret") {
		t.Fatalf("dry-run plan:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the skill dir: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runSkillsCommand([]string{"install", "--config", config, "--harness", "claude_code"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("install exit=%d stderr=%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "demo from the org") {
		t.Fatalf("demo not installed: %q %v", body, err)
	}
	invocations, _ := os.ReadFile(log)
	for _, want := range []string{
		"argv: connector invoke --connector " + skillsConnectorID + " --capability skills.list --action read --resource skills",
		"argv: connector invoke --connector " + skillsConnectorID + " --capability skills.fetch --action read --resource skills/demo",
		"token: env",
	} {
		if !strings.Contains(string(invocations), want) {
			t.Fatalf("kei-proxy invocations missing %q:\n%s", want, invocations)
		}
	}
	if strings.Contains(string(invocations), skillsTestToken) || strings.Contains(stdout.String()+stderr.String(), skillsTestToken) {
		t.Fatal("runtime token leaked into argv or output")
	}
	if strings.Contains(string(invocations), "skills/secret") {
		t.Fatalf("denied skill fetched:\n%s", invocations)
	}
}

func TestSkillsInstallFailsClosedOnKeiProxyDeny(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	proxy, _ := fakeKeiProxy(t, true)
	config := writeSkillsConfig(t, server.URL, proxy)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"install", "--config", config, "--harness", "claude_code"}, &stdout, &stderr, server.Client()); code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "connector_not_bound") {
		t.Fatalf("stderr does not name the deny: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill dir written after a deny: %v", err)
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

func TestHarnessSyncInstallsSkillsAfterRender(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	proxy, _ := fakeKeiProxy(t, false)
	config := runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: skillsTestToken, ProxyPath: proxy}

	var stdout, stderr bytes.Buffer
	if code := syncHarnessBundle(t.Context(), config, "claude_code", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now); code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("sync did not install demo: %v\n%s", err, stdout.String())
	}
	// The permission render still ran, and the skill copy shares its ledger.
	// (A kei-proxy deny warns rather than failing the sync.)
	ledger, err := os.ReadFile(testLedgerPath(home, "claude_code"))
	if err != nil || !strings.Contains(string(ledger), `"harness_id": "claude_code"`) || !strings.Contains(string(ledger), filepath.Join(home, ".claude", "skills", "demo")) {
		t.Fatalf("ledger = %s, err = %v", ledger, err)
	}
}

func TestHarnessSyncWarnsWhenSkillsDenied(t *testing.T) {
	home := skillsHome(t)
	server := skillsBundleServer(t)
	proxy, _ := fakeKeiProxy(t, true)
	config := runtimeConfig{ControlPlaneURL: server.URL, RuntimeToken: skillsTestToken, ProxyPath: proxy}

	var stdout, stderr bytes.Buffer
	if code := syncHarnessBundle(t.Context(), config, "claude_code", false, &stdout, &stderr, server.Client(), harness.Default, harness.OSEnv(), time.Now); code != 0 {
		t.Fatalf("sync exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "skills not installed") || !strings.Contains(stderr.String(), "connector_not_bound") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill dir written after a deny: %v", err)
	}
}
