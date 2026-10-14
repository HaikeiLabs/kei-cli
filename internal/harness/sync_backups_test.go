package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/providers"
)

// syncBackupCases are the file harnesses, each with a native config that the
// first sync of driftPolicies changes.
var syncBackupCases = []struct {
	name     string
	h        Harness
	fixtures map[string]string // fixture -> path relative to home
}{
	{"claude_code", claudeCode{}, map[string]string{"claude.settings.input.json": ".claude/settings.json"}},
	{"codex", codex{}, map[string]string{"codex.rules.input": ".codex/rules/kei.rules", "codex.hooks.legacy.input.json": ".codex/hooks.json"}},
	{"opencode", openCode{}, map[string]string{"opencode.input.json": "xdg/opencode/opencode.json"}},
}

func syncBackupEnv(t *testing.T, fixtures map[string]string) (Env, string) {
	t.Helper()
	home := t.TempDir()
	env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
	env.RunCmd = func(context.Context, string, ...string) (string, error) { return "2.1.200 (Claude Code)", nil }
	for fixture, rel := range fixtures {
		copyFixture(t, fixture, filepath.Join(home, filepath.FromSlash(rel)))
	}
	return env, home
}

func applySync(t *testing.T, h Harness, env Env, b Bundle, dryRun bool) (string, error) {
	t.Helper()
	r, err := h.Render(b)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = h.Apply(context.Background(), r, ApplyOpts{Env: env, DryRun: dryRun, Out: &out, Now: time.Now()})
	return out.String(), err
}

// secondPolicies changes every file harness's native permissions after a
// sync of driftPolicies.
const secondPolicies = `{"id":"p1","src_pattern":"*","dst_pattern":"shell:git log","effect":"permit","enabled":true},` +
	`{"id":"p2","src_pattern":"*","dst_pattern":"shell:curl","effect":"deny","enabled":true}`

// keiBackupName is the exact name of a Kei backup of a file.
var keiBackupName = regexp.MustCompile(`\.kei-backup-\d{8}T\d{6}\.\d{9}Z$`)

// keiBackupsOf returns the Kei backups beside file, keyed by path, with
// their bytes.
func keiBackupsOf(t *testing.T, file string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(file + ".kei-backup-*")
	if err != nil {
		t.Fatal(err)
	}
	backups := map[string][]byte{}
	for _, path := range paths {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || !keiBackupName.MatchString(path) {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		backups[path] = b
	}
	return backups
}

// onlyBackup returns the bytes of file's single Kei backup.
func onlyBackup(t *testing.T, file string) []byte {
	t.Helper()
	backups := keiBackupsOf(t, file)
	if len(backups) != 1 {
		t.Fatalf("%s has %d Kei backups, want 1: %v", file, len(backups), backups)
	}
	for _, b := range backups {
		return b
	}
	return nil
}

// HAI-582: a sync keeps one backup of each file it changed, holding the file
// from before the sync. The next successful sync replaces it, so each
// managed file has at most one Kei backup: the state before the most recent
// successful sync that changed it.
func TestSyncKeepsOneBackupPerFile(t *testing.T) {
	for _, tc := range syncBackupCases {
		t.Run(tc.name, func(t *testing.T) {
			env, home := syncBackupEnv(t, tc.fixtures)
			original := snapshotFiles(t, home)
			out, err := applySync(t, tc.h, env, driftBundle(2, driftPolicies), false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Kei-managed entries updated") {
				t.Fatalf("sync 1 changed nothing:\n%s", out)
			}
			synced1 := snapshotFiles(t, home)
			for path, before := range original {
				if bytes.Equal(synced1[path], before) {
					continue
				}
				if got := onlyBackup(t, path); !bytes.Equal(got, before) {
					t.Errorf("sync 1 backup of %s:\ngot:\n%s\nwant:\n%s", path, got, before)
				}
			}

			out, err = applySync(t, tc.h, env, driftBundle(3, secondPolicies), false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Kei-managed entries updated") {
				t.Fatalf("sync 2 changed nothing:\n%s", out)
			}
			synced2 := snapshotFiles(t, home)
			changed := 0
			for path := range original {
				if strings.Contains(path, ".kei-backup-") {
					continue
				}
				backup := onlyBackup(t, path)
				if !bytes.Equal(synced2[path], synced1[path]) {
					changed++
					if !bytes.Equal(backup, synced1[path]) {
						t.Errorf("sync 2 backup of %s holds:\n%s\nwant sync 1's output:\n%s", path, backup, synced1[path])
					}
				}
			}
			if changed == 0 {
				t.Fatal("sync 2 changed no native config")
			}
			for path := range synced2 {
				if strings.Contains(filepath.Base(path), ".kei-tmp-") {
					t.Fatalf("sync left a temp file %s", path)
				}
				if strings.HasPrefix(path, ledgerDir(env)) || strings.Contains(path, ".kei-backup-") {
					continue
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm()&^0o600 != 0 {
					t.Errorf("%s mode = %v, want 0600 or stricter", path, info.Mode().Perm())
				}
			}
		})
	}
}

// A native config written stricter than 0600 keeps its mode.
func TestSyncKeepsStricterMode(t *testing.T) {
	env, home := syncBackupEnv(t, map[string]string{"claude.settings.input.json": ".claude/settings.json"})
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := applySync(t, claudeCode{}, env, driftBundle(2, driftPolicies), false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode = %v, want 0400", info.Mode().Perm())
	}
}

// A sync whose write fails part-way restores every file it already wrote to
// the bytes the previous sync left, removes its own fresh backups, and
// leaves the previous sync's backup untouched.
func TestSyncWriteFailureRestoresOriginalAndKeepsPriorBackup(t *testing.T) {
	env, home := syncBackupEnv(t, map[string]string{"codex.rules.input": ".codex/rules/kei.rules", "codex.hooks.legacy.input.json": ".codex/hooks.json"})
	if _, err := applySync(t, codex{}, env, driftBundle(2, driftPolicies), false); err != nil {
		t.Fatal(err)
	}
	before := snapshotFiles(t, home)
	rules := filepath.Join(home, ".codex", "rules", "kei.rules")
	prior := keiBackupsOf(t, rules)
	if len(prior) != 1 {
		t.Fatalf("sync 1 kept %d backups of kei.rules, want 1", len(prior))
	}
	// The kei.rules write fails once; the rollback's own writes succeed.
	t.Cleanup(func() { renameFile = os.Rename })
	failed := false
	renameFile = func(from, to string) error {
		if filepath.Base(to) == "kei.rules" && !failed {
			failed = true
			return errors.New("disk full")
		}
		return os.Rename(from, to)
	}
	if _, err := applySync(t, codex{}, env, driftBundle(3, secondPolicies), false); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("sync err = %v, want the write failure", err)
	}
	after := snapshotFiles(t, home)
	for path, body := range before {
		if !bytes.Equal(after[path], body) {
			t.Errorf("%s not as sync 1 left it:\nwant:\n%s\ngot:\n%s", path, body, after[path])
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("failed sync left %s", path)
		}
	}
}

// staleBackupFixture lays out two backups earlier kei-cli versions kept
// beside config, older than any sync in the test, plus look-alikes kei-cli
// never wrote. It returns the Kei backups and the look-alikes.
func staleBackupFixture(t *testing.T, config string) (kei, lookAlikes []string) {
	t.Helper()
	dir := filepath.Dir(config)
	base := filepath.Base(config)
	stamp := "20260901T120000.123456789Z"
	write := func(path string) string {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	kei = append(kei,
		write(filepath.Join(dir, base+".kei-backup-"+stamp)),
		write(filepath.Join(dir, base+".kei-backup-20260902T080000.000000000Z")),
	)
	lookAlikes = append(lookAlikes,
		write(filepath.Join(dir, base+".kei-backup-old")),
		write(filepath.Join(dir, base+".kei-backup-20260901T120000Z")),
		write(filepath.Join(dir, base+".kei-backup-"+stamp+".txt")),
		write(filepath.Join(dir, "other.json.kei-backup-"+stamp)),
		write(filepath.Join(dir, "my"+base+".kei-backup-"+stamp)),
	)
	// A folder with a backup's name is not a file kei-cli wrote.
	folder := filepath.Join(dir, base+".kei-backup-20260903T000000.000000000Z")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return kei, append(lookAlikes, folder)
}

func assertRemoved(t *testing.T, out string, removed, kept []string) {
	t.Helper()
	for _, path := range removed {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("earlier Kei backup %s kept: %v", path, err)
		}
		if !strings.Contains(out, "Removed earlier Kei backup "+path+"\n") {
			t.Errorf("output does not report removing %s:\n%s", path, out)
		}
	}
	for _, path := range kept {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s removed: %v", path, err)
		}
		if strings.Contains(out, path+"\n") {
			t.Errorf("output names %s:\n%s", path, out)
		}
	}
}

// A sync that changes a file removes every earlier Kei backup of it and
// keeps only its own; files that only look like a Kei backup are kept.
func TestSyncReplacesEarlierKeiBackupsKeepsLookAlikes(t *testing.T) {
	env, home := syncBackupEnv(t, map[string]string{"claude.settings.input.json": ".claude/settings.json"})
	config := filepath.Join(home, ".claude", "settings.json")
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	kei, lookAlikes := staleBackupFixture(t, config)
	out, err := applySync(t, claudeCode{}, env, driftBundle(2, driftPolicies), false)
	if err != nil {
		t.Fatal(err)
	}
	assertRemoved(t, out, kei, lookAlikes)
	if got := onlyBackup(t, config); !bytes.Equal(got, original) {
		t.Fatalf("kept backup holds:\n%s\nwant the config from before the sync:\n%s", got, original)
	}
}

// A sync that changes nothing keeps the newest Kei backup, the state
// before the last sync that changed the file, and removes the older ones.
func TestSyncWithoutChangesKeepsNewestBackup(t *testing.T) {
	env, home := syncBackupEnv(t, map[string]string{"claude.settings.input.json": ".claude/settings.json"})
	config := filepath.Join(home, ".claude", "settings.json")
	b := driftBundle(2, driftPolicies)
	if _, err := applySync(t, claudeCode{}, env, b, false); err != nil {
		t.Fatal(err)
	}
	newest := keiBackupsOf(t, config)
	kei, lookAlikes := staleBackupFixture(t, config)
	out, err := applySync(t, claudeCode{}, env, b, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Kei-managed entries updated") {
		t.Fatalf("re-sync changed the config:\n%s", out)
	}
	assertRemoved(t, out, kei, lookAlikes)
	if got := keiBackupsOf(t, config); len(got) != 1 || len(newest) != 1 {
		t.Fatalf("backups = %v, want only %v", got, newest)
	}
	for path, body := range newest {
		if !bytes.Equal(keiBackupsOf(t, config)[path], body) {
			t.Fatalf("newest backup %s changed", path)
		}
	}
}

// --dry-run lists the earlier Kei backups a sync would remove and deletes
// nothing.
func TestSyncDryRunListsEarlierBackupsWithoutDeleting(t *testing.T) {
	env, home := syncBackupEnv(t, map[string]string{"claude.settings.input.json": ".claude/settings.json"})
	kei, lookAlikes := staleBackupFixture(t, filepath.Join(home, ".claude", "settings.json"))
	before := snapshotFiles(t, home)
	out, err := applySync(t, claudeCode{}, env, driftBundle(2, driftPolicies), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range kei {
		if !strings.Contains(out, "Would remove earlier Kei backup "+path+"\n") {
			t.Errorf("dry run does not list %s:\n%s", path, out)
		}
	}
	for _, path := range lookAlikes {
		if strings.Contains(out, path+"\n") {
			t.Errorf("dry run lists look-alike %s:\n%s", path, out)
		}
	}
	after := snapshotFiles(t, home)
	if len(after) != len(before) {
		t.Fatalf("dry run changed the file set: %d -> %d", len(before), len(after))
	}
	for path, body := range before {
		if !bytes.Equal(after[path], body) {
			t.Errorf("dry run changed %s", path)
		}
	}
}

// skillBackupDirs returns the Kei backups of skill under kind's backup
// folder, keyed by path, with their SKILL.md.
func skillBackupDirs(t *testing.T, env Env, kind, skill string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(ledgerDir(env), "skill-backups", kind, skill+"-2*"))
	if err != nil {
		t.Fatal(err)
	}
	backups := map[string]string{}
	for _, path := range paths {
		b, _ := os.ReadFile(filepath.Join(path, "SKILL.md"))
		backups[path] = string(b)
	}
	return backups
}

// updateDemo installs demo at version from a new snapshot.
func updateDemo(t *testing.T, env Env, b Bundle, version, commit string, dryRun bool) string {
	t.Helper()
	src := orgSkills(t)
	demo := providers.SkillsRepoSkill{Name: "demo", Version: version, Path: "skills/demo"}
	src.listing.Commit = commit
	src.listing.Skills[0] = demo
	src.archives["demo"] = skillArchive(t, demo, map[string]string{"SKILL.md": skillMD("demo", version)})
	out, _, err := installSkills(t, env, src, b, "claude_code", dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// HAI-582: each skill keeps one Kei backup, the copy from before the most
// recent successful update.
func TestInstallSkillsKeepsOneBackupPerSkill(t *testing.T) {
	env, _ := skillsEnv(t)
	b := skillsBundle(t, 1, defaultSkillPolicies()...)
	if _, _, err := installSkills(t, env, orgSkills(t), b, "claude_code", false); err != nil {
		t.Fatal(err)
	}
	updateDemo(t, env, b, "1.1.0", "beef", false)
	if got := skillBackupDirs(t, env, "claude_code", "demo"); len(got) != 1 {
		t.Fatalf("after update 1 demo backups = %v", got)
	}
	updateDemo(t, env, b, "1.2.0", "cafe", false)
	got := skillBackupDirs(t, env, "claude_code", "demo")
	if len(got) != 1 {
		t.Fatalf("after update 2 demo backups = %v", got)
	}
	for path, body := range got {
		if !strings.Contains(body, "version: 1.1.0") {
			t.Fatalf("%s holds:\n%s\nwant the 1.1.0 copy", path, body)
		}
	}
}

// An update removes the earlier Kei backups of that skill and keeps its own;
// a skill that was not changed keeps only its newest backup, and anything
// kei-cli did not name is kept. The dry run lists the same and deletes
// nothing.
func TestInstallSkillsReplacesEarlierSkillBackups(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry run", false: "sync"}[dryRun], func(t *testing.T) {
			env, _ := skillsEnv(t)
			b := skillsBundle(t, 1, defaultSkillPolicies()...)
			if _, _, err := installSkills(t, env, orgSkills(t), b, "claude_code", false); err != nil {
				t.Fatal(err)
			}
			backups := filepath.Join(ledgerDir(env), "skill-backups")
			mk := func(rel string) string {
				path := filepath.Join(backups, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			removed := []string{
				filepath.Dir(mk("claude_code/demo-20260901T120000.123456789Z/SKILL.md")),
				filepath.Dir(filepath.Dir(mk("claude_code/demo-20260901T120000.123456789Z-2/scripts/run.sh"))),
				filepath.Dir(mk("claude_code/lint-20260901T120000.000000000Z/SKILL.md")),
			}
			kept := []string{
				filepath.Dir(mk("claude_code/lint-20260902T120000.000000000Z/SKILL.md")),
				mk("claude_code/notes.txt"),
				filepath.Dir(mk("claude_code/demo-latest/SKILL.md")),
				filepath.Dir(mk("codex/demo-20260901T120000.123456789Z/SKILL.md")),
			}
			before := snapshotFiles(t, backups)
			out := updateDemo(t, env, b, "1.1.0", "beef", dryRun)
			if dryRun {
				for _, path := range removed {
					if !strings.Contains(out, "Would remove earlier Kei backup "+path+"\n") {
						t.Errorf("dry run does not list %s:\n%s", path, out)
					}
				}
				for _, path := range kept {
					if strings.Contains(out, path+"\n") {
						t.Errorf("dry run lists %s:\n%s", path, out)
					}
				}
				after := snapshotFiles(t, backups)
				if len(after) != len(before) {
					t.Fatalf("dry run changed the backups: %d -> %d", len(before), len(after))
				}
				return
			}
			assertRemoved(t, out, removed, kept)
			if got := skillBackupDirs(t, env, "claude_code", "demo"); len(got) != 1 {
				t.Fatalf("demo backups = %v", got)
			}
		})
	}
}
