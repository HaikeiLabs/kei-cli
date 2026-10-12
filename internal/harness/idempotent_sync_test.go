package harness

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyFixture copies a testdata/harness fixture to dst, creating its parent.
func copyFixture(t *testing.T, fixture, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "harness", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// backupFiles returns every Kei backup under root.
func backupFiles(t *testing.T, root string) []string {
	t.Helper()
	var backups []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), ".kei-backup-") {
			backups = append(backups, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return backups
}

// snapshotFiles returns the bytes of every file under root.
func snapshotFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		files[path] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// HAI-508: a sync followed by a dry-run of the same bundle reports no
// change, and a second sync writes no file and no backup. The diff used to
// be taken against the config with the previous sync's permission entries
// already stripped, so every re-sync re-added them and wrote a backup.
func TestSyncThenDryRunReportsNoChanges(t *testing.T) {
	allowOnly := `{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","effect":"permit","enabled":true},` +
		`{"id":"p2","src_pattern":"*","dst_pattern":"shell:ls","effect":"permit","enabled":true}`
	cases := []struct {
		name     string
		h        Harness
		policies string
		fixtures map[string]string // fixture -> path relative to home
	}{
		{"claude_code", claudeCode{}, driftPolicies, map[string]string{"claude.settings.input.json": ".claude/settings.json"}},
		{"codex allow only", codex{}, allowOnly, map[string]string{"codex.rules.input": ".codex/rules/kei.rules", "codex.hooks.legacy.input.json": ".codex/hooks.json"}},
		{"codex with deny", codex{}, driftPolicies, map[string]string{"codex.rules.input": ".codex/rules/kei.rules", "codex.hooks.legacy.input.json": ".codex/hooks.json"}},
		{"opencode", openCode{}, driftPolicies, map[string]string{"opencode.input.json": "xdg/opencode/opencode.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
			env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
			for fixture, rel := range tc.fixtures {
				copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
			}
			b := driftBundle(2, tc.policies)
			r, err := tc.h.Render(b)
			if err != nil {
				t.Fatal(err)
			}
			apply := func(dryRun bool) string {
				var out bytes.Buffer
				if _, err := tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, DryRun: dryRun, Out: &out, Now: time.Now()}); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			if out := apply(false); !strings.Contains(out, "Kei-managed entries updated") {
				t.Fatalf("first sync changed nothing:\n%s", out)
			}
			synced := snapshotFiles(t, home)
			backups := len(backupFiles(t, home))

			if out := apply(true); strings.Contains(out, "Kei-managed entries updated") {
				t.Fatalf("dry-run after sync reports changes:\n%s", out)
			}
			if out := apply(false); strings.Contains(out, "Kei-managed entries updated") {
				t.Fatalf("re-sync reports changes:\n%s", out)
			}
			if got := backupFiles(t, home); len(got) != backups {
				t.Fatalf("re-sync wrote backups: %d -> %d: %v", backups, len(got), got)
			}
			after := snapshotFiles(t, home)
			for path, before := range synced {
				if !bytes.Equal(after[path], before) {
					t.Errorf("re-sync rewrote %s:\nbefore:\n%s\nafter:\n%s", path, before, after[path])
				}
			}
			if len(after) != len(synced) {
				t.Errorf("re-sync changed the file set: %d -> %d", len(synced), len(after))
			}
		})
	}
}
