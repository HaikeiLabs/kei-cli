package harness

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNoHookSkipsAuditHookInstallation(t *testing.T) {
	policies := `{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","effect":"permit","enabled":true}`

	cases := []struct {
		name     string
		h        Harness
		fixtures map[string]string
		// hookRel is the relative path of a standalone hook file; empty
		// means the hook is merged into one of the fixture files (claude).
		hookRel string
		// hookSig is a unique string that appears in the hook output.
		hookSig string
		// permSig is a unique string that appears in the permission output
		// (used to verify permissions are still synced with NoHook).
		permSig string
	}{
		{
			name:     "claude_code",
			h:        claudeCode{},
			fixtures: map[string]string{"claude.settings.input.json": ".claude/settings.json"},
			hookSig:  "kei-proxy hook claude",
			permSig:  "Bash(git status:*)",
		},
		{
			name:     "codex",
			h:        codex{},
			fixtures: map[string]string{"codex.rules.input": ".codex/rules/kei.rules"},
			hookRel:  ".codex/hooks.json",
			hookSig:  "kei-proxy hook codex",
			permSig:  "allow",
		},
		{
			name:     "opencode",
			h:        openCode{},
			fixtures: map[string]string{"opencode.input.json": "xdg/opencode/opencode.json"},
			hookRel:  "xdg/opencode/plugins/kei-audit.js",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" default installs hook", func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
			env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
			for fixture, rel := range tc.fixtures {
				copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
			}

			b := driftBundle(1, policies)
			r, err := tc.h.Render(b)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err = tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, Out: &out, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Kei-managed entries updated") {
				t.Fatalf("sync should report changes: %s", out.String())
			}
			if tc.hookRel != "" {
				abs := filepath.Join(home, filepath.FromSlash(tc.hookRel))
				if _, err := os.Stat(abs); err != nil {
					t.Fatalf("hook file %s should exist: %v", tc.hookRel, err)
				}
			}
			if tc.hookSig != "" {
				var checkPath string
				if tc.hookRel != "" {
					checkPath = filepath.Join(home, filepath.FromSlash(tc.hookRel))
				} else {
					for _, rel := range tc.fixtures {
						checkPath = filepath.Join(home, filepath.FromSlash(rel))
						break
					}
				}
				b, err := os.ReadFile(checkPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(b), tc.hookSig) {
					t.Fatalf("%s should contain %q", checkPath, tc.hookSig)
				}
			}
		})

		t.Run(tc.name+" no-hook skips hook", func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
			env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
			for fixture, rel := range tc.fixtures {
				copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
			}

			b := driftBundle(1, policies)
			r, err := tc.h.Render(b)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err = tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, NoHook: true, Out: &out, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Kei-managed entries updated") {
				t.Fatalf("sync should still report permission changes: %s", out.String())
			}
			// Hook file must not exist
			if tc.hookRel != "" {
				abs := filepath.Join(home, filepath.FromSlash(tc.hookRel))
				if _, err := os.Stat(abs); !os.IsNotExist(err) {
					t.Fatalf("hook file %s should NOT exist: %v", tc.hookRel, err)
				}
			}
			// Hook content must not appear in any fixture file (claude
			// merges into settings.json)
			if tc.hookRel == "" && tc.hookSig != "" {
				for _, rel := range tc.fixtures {
					abs := filepath.Join(home, filepath.FromSlash(rel))
					b, err := os.ReadFile(abs)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(b), tc.hookSig) {
						t.Fatalf("%s should NOT contain %q with NoHook", abs, tc.hookSig)
					}
				}
			}
			// Permissions must still be present
			if tc.permSig != "" {
				// Check the fixture files that should carry permissions
				for _, rel := range tc.fixtures {
					abs := filepath.Join(home, filepath.FromSlash(rel))
					b, err := os.ReadFile(abs)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(b), tc.permSig) {
						t.Fatalf("%s should still contain permissions (%q) with NoHook", abs, tc.permSig)
					}
				}
			}
		})

		t.Run(tc.name+" dry-run no-hook reports but does not write", func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
			env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
			for fixture, rel := range tc.fixtures {
				copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
			}

			pre := snapshotFiles(t, home)
			b := driftBundle(1, policies)
			r, err := tc.h.Render(b)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err = tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, NoHook: true, DryRun: true, Out: &out, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			post := snapshotFiles(t, home)
			for path, before := range pre {
				if !bytes.Equal(post[path], before) {
					t.Errorf("dry-run should not modify %s", path)
				}
			}
			if len(post) != len(pre) {
				t.Errorf("dry-run should not create files: %d before, %d after", len(pre), len(post))
			}
		})

		t.Run(tc.name+" no-hook re-sync is idempotent", func(t *testing.T) {
			home := t.TempDir()
			env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
			env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
			for fixture, rel := range tc.fixtures {
				copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
			}

			b := driftBundle(1, policies)
			r, err := tc.h.Render(b)
			if err != nil {
				t.Fatal(err)
			}
			// First sync with NoHook
			var out bytes.Buffer
			_, err = tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, NoHook: true, Out: &out, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Kei-managed entries updated") {
				t.Fatalf("first sync should report changes: %s", out.String())
			}
			synced := snapshotFiles(t, home)
			backups := len(backupFiles(t, home))

			// Second sync with NoHook should be a no-op
			var out2 bytes.Buffer
			_, err = tc.h.Apply(context.Background(), r, ApplyOpts{Env: env, NoHook: true, Out: &out2, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out2.String(), "Kei-managed entries updated") {
				t.Fatalf("re-sync should not report changes:\n%s", out2.String())
			}
			if got := backupFiles(t, home); len(got) != backups {
				t.Fatalf("re-sync wrote backups: %d -> %d: %v", backups, len(got), got)
			}
			after := snapshotFiles(t, home)
			for path, before := range synced {
				if !bytes.Equal(after[path], before) {
					t.Errorf("re-sync rewrote %s", path)
				}
			}
			if len(after) != len(synced) {
				t.Errorf("re-sync changed file count: %d -> %d", len(synced), len(after))
			}
		})
	}
}
