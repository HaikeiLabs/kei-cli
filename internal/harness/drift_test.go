package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func driftBundle(version int64, policies string) Bundle {
	return Bundle{BundleVersion: version, PayloadDigest: "sha256:v" + string(rune('0'+version)), NotAfter: time.Now().Add(time.Hour), PolicySet: json.RawMessage(`{"policies":[` + policies + `]}`)}
}

const driftPolicies = `{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","effect":"permit","enabled":true},` +
	`{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","effect":"deny","enabled":true},` +
	`{"id":"p3","src_pattern":"*","dst_pattern":"skill:my-skill","effect":"permit","enabled":true}`

// syncWith renders b for h and applies it, as `kei harness sync` does.
func syncWith(t *testing.T, h Harness, env Env, b Bundle) (Rendered, Result) {
	t.Helper()
	r, err := h.Render(b)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := h.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return r, result
}

func checkDrift(t *testing.T, h Harness, env Env, r Rendered) Drift {
	t.Helper()
	d, err := CheckDrift(h, env, r)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func noteContaining(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// HAI-416: a long-running Claude Code session saves an "always allow" answer
// from the settings it loaded before the last sync, dropping every
// Kei-written permission while the hook stays. The check reports each
// dropped entry; the next sync restores them and keeps the session's answer.
func TestClaudeStaleWriterIsDetectedAndHealed(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	path := claudeSettingsPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["Bash(user:*)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := claudeCode{}
	if d := checkDrift(t, h, env, Rendered{}); d.Status != DriftNotSynced {
		t.Fatalf("before first sync status = %s", d.Status)
	}
	r, _ := syncWith(t, h, env, driftBundle(2, driftPolicies))
	managed := len(r.Allows) + len(r.Denies)
	if managed < 3 {
		t.Fatalf("rendered %d entries: %+v", managed, r)
	}
	if d := checkDrift(t, h, env, r); d.Status != DriftInSync {
		t.Fatalf("after sync drift = %+v", d)
	}

	// The stale writer: its in-memory copy predates the sync, so it rewrites
	// permissions without the Kei entries but keeps the rest of the file.
	synced, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(synced, &settings); err != nil {
		t.Fatal(err)
	}
	settings["permissions"] = map[string]any{"allow": []any{"Bash(user:*)", "Bash(npm test:*)"}}
	stale, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatal(err)
	}

	d := checkDrift(t, h, env, r)
	if d.Status != DriftDetected || len(d.Missing) != managed || len(d.Pending) != 0 || len(d.Stale) != 0 || len(d.AlteredFiles) != 0 {
		t.Fatalf("stale-writer drift = %+v", d)
	}
	for _, e := range d.Missing {
		if e.File != path || (e.Effect != "allow" && e.Effect != "deny") {
			t.Fatalf("missing entry = %+v", e)
		}
	}

	_, result := syncWith(t, h, env, driftBundle(2, driftPolicies))
	if !noteContaining(result.Notes, "re-applied 3 entries removed since last sync (likely an open Claude session saved stale settings") {
		t.Fatalf("notes = %q", result.Notes)
	}
	healed, _ := os.ReadFile(path)
	for _, want := range append(append([]string{"Bash(user:*)", "Bash(npm test:*)", "kei-proxy hook claude"}, r.Allows...), r.Denies...) {
		if !strings.Contains(string(healed), want) {
			t.Errorf("healed settings missing %q:\n%s", want, healed)
		}
	}
	if d := checkDrift(t, h, env, r); d.Status != DriftInSync {
		t.Fatalf("after heal drift = %+v", d)
	}
	// A sync with nothing to restore says nothing about it.
	if _, result := syncWith(t, h, env, driftBundle(2, driftPolicies)); noteContaining(result.Notes, "re-applied") {
		t.Fatalf("idempotent sync notes = %q", result.Notes)
	}
}

// A bundle change since the last sync is drift too: new entries are pending
// and dropped ones stale, but nothing is reported missing.
func TestDriftReportsPendingAndStaleAfterBundleChange(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := claudeCode{}
	syncWith(t, h, env, driftBundle(2, driftPolicies))
	next, err := h.Render(driftBundle(3, `{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","effect":"permit","enabled":true},{"id":"p4","src_pattern":"*","dst_pattern":"shell:make","effect":"permit","enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	d := checkDrift(t, h, env, next)
	if d.Status != DriftDetected || len(d.Missing) != 0 || d.SyncedBundleVersion != 2 {
		t.Fatalf("drift = %+v", d)
	}
	if len(d.Pending) != 1 || d.Pending[0].Entry != "Bash(make:*)" {
		t.Fatalf("pending = %+v", d.Pending)
	}
	if len(d.Stale) != 2 {
		t.Fatalf("stale = %+v", d.Stale)
	}
}

// Codex keeps its rules in the Kei-owned kei.rules; deleting it is both an
// altered file and missing entries, and sync rewrites it.
func TestCodexDeletedRulesFileIsDetectedAndHealed(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	h := codex{}
	r, _ := syncWith(t, h, env, driftBundle(2, driftPolicies))
	rules := filepath.Join(home, ".codex", "rules", "kei.rules")
	if d := checkDrift(t, h, env, r); d.Status != DriftInSync {
		t.Fatalf("after sync drift = %+v", d)
	}
	if err := os.Remove(rules); err != nil {
		t.Fatal(err)
	}
	d := checkDrift(t, h, env, r)
	if d.Status != DriftDetected || len(d.AlteredFiles) != 1 || d.AlteredFiles[0] != rules || len(d.Missing) != len(r.Allows)+len(r.Denies) {
		t.Fatalf("drift = %+v", d)
	}
	_, result := syncWith(t, h, env, driftBundle(2, driftPolicies))
	if !noteContaining(result.Notes, "Codex: re-applied 2 entries removed since last sync") {
		t.Fatalf("notes = %q", result.Notes)
	}
	if d := checkDrift(t, h, env, r); d.Status != DriftInSync {
		t.Fatalf("after heal drift = %+v", d)
	}
}

// OpenCode: an entry whose decision another writer changed is not in force.
func TestOpenCodeChangedDecisionIsMissing(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	h := openCode{}
	r, _ := syncWith(t, h, env, driftBundle(2, driftPolicies))
	path, _ := opencodeConfigPath(env)
	if d := checkDrift(t, h, env, r); d.Status != DriftInSync {
		t.Fatalf("after sync drift = %+v", d)
	}
	cfg, _ := os.ReadFile(path)
	changed := strings.Replace(string(cfg), `"git status": "allow"`, `"git status": "ask"`, 1)
	if changed == string(cfg) {
		t.Fatalf("fixture has no git status allow:\n%s", cfg)
	}
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	d := checkDrift(t, h, env, r)
	if d.Status != DriftDetected || len(d.Missing) != 1 || d.Missing[0] != (DriftEntry{File: path, Effect: "allow", Entry: "git status"}) {
		t.Fatalf("drift = %+v", d)
	}
}

func TestCustomHarnessDriftUnsupported(t *testing.T) {
	if d := checkDrift(t, custom{}, hermeticEnv(t.TempDir(), nil), Rendered{}); d.Status != DriftUnsupported || d.Drifted() {
		t.Fatalf("custom drift = %+v", d)
	}
}

func TestDriftReportTextNamesEntriesAndRemedy(t *testing.T) {
	report := NewDriftReport(driftBundle(3, ""), time.Now())
	report.Add(Drift{Kind: "claude_code", Status: DriftDetected, SyncedBundleVersion: 3, Missing: []DriftEntry{{File: "/h/.claude/settings.json", Effect: "deny", Entry: "Bash(rm:*)"}}})
	report.Add(Drift{Kind: "codex", Status: DriftInSync})
	if !report.Drift {
		t.Fatal("report.Drift = false")
	}
	var out bytes.Buffer
	report.WriteText(&out)
	for _, want := range []string{"claude_code: drift (last synced bundle 3, current 3)", "missing deny Bash(rm:*) in /h/.claude/settings.json", "codex: in sync with bundle 3", "Run kei harness sync"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text report missing %q:\n%s", want, out.String())
		}
	}
}
