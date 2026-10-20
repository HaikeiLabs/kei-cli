package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	policybundle "github.com/HaikeiLabs/kei-connector-contracts/bundle"
	"github.com/HaikeiLabs/kei-connector-contracts/providers"
)

var updateSkillsGolden = flag.Bool("update-skills", false, "rewrite testdata/skills/*.golden")

// fakeSkillSource is the skills_repo connector as kei-proxy returns it: a
// listing and one archive per skill. It records every fetch.
type fakeSkillSource struct {
	listing  providers.SkillsRepoListing
	archives map[string]providers.SkillsRepoArchive
	fetched  []string
}

func (f *fakeSkillSource) ListSkills(context.Context) (providers.SkillsRepoListing, error) {
	return f.listing, nil
}

func (f *fakeSkillSource) FetchSkill(_ context.Context, name string) (providers.SkillsRepoArchive, error) {
	f.fetched = append(f.fetched, name)
	archive, ok := f.archives[name]
	if !ok {
		return providers.SkillsRepoArchive{}, errors.New("skill not found")
	}
	return archive, nil
}

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

// rawSkillArchive builds a skills.fetch result whose archive holds entries
// exactly as given, digest and size included.
func rawSkillArchive(t *testing.T, skill providers.SkillsRepoSkill, entries []tarEntry) providers.SkillsRepoArchive {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Mode: mode, Typeflag: typeflag, Linkname: e.linkname, ModTime: time.Unix(0, 0)}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return providers.SkillsRepoArchive{
		Skill:   skill,
		Commit:  "c0ffee",
		Format:  providers.SkillsRepoArchiveFormat,
		Digest:  "sha256:" + hex.EncodeToString(sum[:]),
		Size:    buf.Len(),
		Archive: buf.Bytes(),
	}
}

// skillArchive is a well-formed archive of one skill folder.
func skillArchive(t *testing.T, skill providers.SkillsRepoSkill, files map[string]string) providers.SkillsRepoArchive {
	t.Helper()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]tarEntry, 0, len(paths))
	for _, path := range paths {
		mode := int64(0o644)
		if strings.HasSuffix(path, ".sh") {
			mode = 0o755
		}
		entries = append(entries, tarEntry{name: skill.Name + "/" + path, body: files[path], mode: mode})
	}
	return rawSkillArchive(t, skill, entries)
}

func skillMD(name, version string) string {
	return "---\nname: " + name + "\ndescription: " + name + " skill\nversion: " + version + "\n---\n# " + name + "\n"
}

// orgSkills is the org's skills repository: demo and lint for every
// harness, secret (denied by policy), unlisted (no permit) and codex-only.
func orgSkills(t *testing.T) *fakeSkillSource {
	t.Helper()
	skills := []providers.SkillsRepoSkill{
		{Name: "demo", Version: "1.0.0", Description: "demo skill", Path: "skills/demo"},
		{Name: "lint", Version: "2.1.0", Description: "lint skill", Path: "skills/lint"},
		{Name: "secret", Version: "1.0.0", Description: "secret skill", Path: "skills/secret"},
		{Name: "unlisted", Version: "1.0.0", Description: "unlisted skill", Path: "skills/unlisted"},
		{Name: "codex-only", Version: "0.1.0", Description: "codex skill", Harnesses: []string{"codex"}, Path: "skills/codex-only"},
	}
	src := &fakeSkillSource{listing: providers.SkillsRepoListing{Ref: "main", Commit: "c0ffee", Skills: skills}, archives: map[string]providers.SkillsRepoArchive{}}
	for _, skill := range skills {
		files := map[string]string{"SKILL.md": skillMD(skill.Name, skill.Version)}
		if skill.Name == "demo" {
			files["scripts/run.sh"] = "#!/bin/sh\necho demo\n"
		}
		src.archives[skill.Name] = skillArchive(t, skill, files)
	}
	return src
}

type skillPolicy struct{ dst, effect string }

// skillsBundle is a bundle with one skills_repo connector and the given
// policies, in order, each sourced from every harness.
func skillsBundle(t *testing.T, version int64, policies ...skillPolicy) Bundle {
	t.Helper()
	type policy struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		SrcPattern string `json:"src_pattern"`
		DstPattern string `json:"dst_pattern"`
		Effect     string `json:"effect"`
		Enabled    bool   `json:"enabled"`
	}
	set := struct {
		Policies   []policy         `json:"policies"`
		Connectors []map[string]any `json:"connectors"`
	}{Connectors: []map[string]any{{"id": "11111111-2222-3333-4444-555555555555", "provider": "skills_repo"}, {"id": "99999999-2222-3333-4444-555555555555", "provider": "github"}}}
	for i, p := range policies {
		set.Policies = append(set.Policies, policy{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("%s %s", p.effect, p.dst), SrcPattern: "*", DstPattern: p.dst, Effect: p.effect, Enabled: true})
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{Payload: policybundle.Payload{Schema: BundleSchemaV2, BundleVersion: version, NotAfter: time.Now().Add(time.Hour), PolicySet: raw}, PayloadDigest: fmt.Sprintf("sha256:%d", version)}
}

// defaultSkillPolicies permit demo, lint and codex-only and deny secret.
func defaultSkillPolicies() []skillPolicy {
	return []skillPolicy{{"skill:secret", "deny"}, {"skill:demo", "permit"}, {"skill:lint", "permit"}, {"skill:codex-only", "permit"}}
}

// skillsEnv is a fake home: HOME and XDG_CONFIG_HOME are temp dirs, so
// nothing reads or writes the real ~/.claude, ~/.codex or ~/.config.
func skillsEnv(t *testing.T) (Env, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	for _, dir := range []string{home, xdg} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": xdg}
	return Env{Home: home, Getenv: func(k string) string { return vars[k] }, Getwd: func() (string, error) { return home, nil }}, root
}

func installSkills(t *testing.T, env Env, src SkillSource, b Bundle, kind string, dryRun bool) (string, SkillsResult, error) {
	t.Helper()
	h, ok := Default.Get(kind)
	if !ok {
		t.Fatalf("no harness %s", kind)
	}
	var out bytes.Buffer
	result, err := InstallSkills(context.Background(), []SkillSource{src}, b, []Harness{h}, SkillsOpts{Env: env, DryRun: dryRun, Out: &out, Now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)})
	return out.String(), result, err
}

// tree lists every file under root (relative path, mode and content), the
// shape the golden files assert on.
func tree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			fmt.Fprintf(&b, "%s symlink\n", rel)
		default:
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.HasSuffix(rel, ".json") && strings.Contains(rel, "harness-sync") {
				fmt.Fprintf(&b, "== %s %v\n%s\n", rel, info.Mode().Perm(), body)
				return nil
			}
			fmt.Fprintf(&b, "== %s %v\n%s", rel, info.Mode().Perm(), body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "skills", name)
	if *updateSkillsGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update-skills)", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

var skillKinds = []string{"claude_code", "codex", "opencode"}

// TestInstallSkillsGolden installs the permitted org skills into each harness
// kind's skill dir: a dry-run plan first (writes nothing), then the install.
func TestInstallSkillsGolden(t *testing.T) {
	for _, kind := range skillKinds {
		t.Run(kind, func(t *testing.T) {
			env, root := skillsEnv(t)
			src := orgSkills(t)
			b := skillsBundle(t, 1, defaultSkillPolicies()...)

			plan, _, err := installSkills(t, env, src, b, kind, true)
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if got := tree(t, root); got != "" {
				t.Fatalf("dry run wrote files:\n%s", got)
			}
			out, result, err := installSkills(t, env, src, b, kind, false)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if len(result.Warnings) != 0 {
				t.Fatalf("warnings = %v", result.Warnings)
			}
			got := "-- dry run --\n" + plan + "-- install --\n" + out + "-- tree --\n" + tree(t, root)
			checkGolden(t, kind+".golden", strings.ReplaceAll(got, root, "$ROOT"))
		})
	}
}

// TestInstallSkillsIdempotent re-runs an install: nothing is fetched,
// rewritten or backed up the second time.
func TestInstallSkillsIdempotent(t *testing.T) {
	for _, kind := range skillKinds {
		t.Run(kind, func(t *testing.T) {
			env, root := skillsEnv(t)
			b := skillsBundle(t, 1, defaultSkillPolicies()...)
			if _, _, err := installSkills(t, env, orgSkills(t), b, kind, false); err != nil {
				t.Fatal(err)
			}
			before := tree(t, root)
			src := orgSkills(t)
			out, _, err := installSkills(t, env, src, b, kind, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(src.fetched) != 0 {
				t.Fatalf("re-run fetched %v", src.fetched)
			}
			if strings.Contains(out, "install ") || strings.Contains(out, "update ") || strings.Contains(out, "remove ") {
				t.Fatalf("re-run planned changes:\n%s", out)
			}
			if after := tree(t, root); after != before {
				t.Fatalf("re-run changed files\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}
		})
	}
}

// TestInstallSkillsSkipsUserOwned never overwrites a user's own skill of the
// same name: it is skipped with a warning and left byte for byte.
func TestInstallSkillsSkipsUserOwned(t *testing.T) {
	for _, kind := range skillKinds {
		t.Run(kind, func(t *testing.T) {
			env, _ := skillsEnv(t)
			h, _ := Default.Get(kind)
			dir := h.SkillDirs(env)[0]
			mine := filepath.Join(dir, "lint", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mine, []byte("my own lint\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			b := skillsBundle(t, 1, defaultSkillPolicies()...)
			for _, dryRun := range []bool{true, false} {
				out, result, err := installSkills(t, env, orgSkills(t), b, kind, dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out, "skip-user-owned lint") {
					t.Fatalf("dry=%v plan does not skip lint:\n%s", dryRun, out)
				}
				if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "lint") {
					t.Fatalf("dry=%v warnings = %v", dryRun, result.Warnings)
				}
			}
			if body, _ := os.ReadFile(mine); string(body) != "my own lint\n" {
				t.Fatalf("user skill overwritten: %q", body)
			}
			if _, err := os.Stat(filepath.Join(dir, "demo", "SKILL.md")); err != nil {
				t.Fatalf("demo not installed beside the user skill: %v", err)
			}
			// A later deny of lint must not remove the user's own copy.
			deny := skillsBundle(t, 2, skillPolicy{"skill:lint", "deny"}, skillPolicy{"skill:demo", "permit"})
			if _, _, err := installSkills(t, env, orgSkills(t), deny, kind, false); err != nil {
				t.Fatal(err)
			}
			if body, _ := os.ReadFile(mine); string(body) != "my own lint\n" {
				t.Fatalf("deny removed the user skill: %q", body)
			}
		})
	}
}

// TestInstallSkillsDenyRemoves removes the Kei-managed copy of a skill that a
// skill: deny now covers, or that is no longer permitted, backing it up
// outside the skill dir.
func TestInstallSkillsDenyRemoves(t *testing.T) {
	for _, kind := range skillKinds {
		t.Run(kind, func(t *testing.T) {
			env, _ := skillsEnv(t)
			h, _ := Default.Get(kind)
			dir := h.SkillDirs(env)[0]
			if _, _, err := installSkills(t, env, orgSkills(t), skillsBundle(t, 1, defaultSkillPolicies()...), kind, false); err != nil {
				t.Fatal(err)
			}
			// demo is now denied ahead of its permit; lint lost its permit.
			next := skillsBundle(t, 2, skillPolicy{"skill:demo", "deny"}, skillPolicy{"skill:demo", "permit"})
			plan, _, err := installSkills(t, env, orgSkills(t), next, kind, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"remove demo", "remove lint"} {
				if !strings.Contains(plan, want) {
					t.Fatalf("plan missing %q:\n%s", want, plan)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "demo")); err != nil {
				t.Fatalf("dry run removed demo: %v", err)
			}
			if _, _, err := installSkills(t, env, orgSkills(t), next, kind, false); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"demo", "lint"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s still installed: %v", name, err)
				}
			}
			backups, _ := filepath.Glob(filepath.Join(ledgerDir(env), "skill-backups", kind, "demo-*", "SKILL.md"))
			if len(backups) != 1 {
				t.Fatalf("demo backups = %v", backups)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), "backup") {
					t.Fatalf("leftover %s in the skill dir", e.Name())
				}
			}
		})
	}
}

// TestInstallSkillsUpdatesChangedSkill replaces a Kei-managed copy whose
// digest changed, backing up the old copy.
func TestInstallSkillsUpdatesChangedSkill(t *testing.T) {
	env, _ := skillsEnv(t)
	b := skillsBundle(t, 1, defaultSkillPolicies()...)
	if _, _, err := installSkills(t, env, orgSkills(t), b, "claude_code", false); err != nil {
		t.Fatal(err)
	}
	src := orgSkills(t)
	demo := providers.SkillsRepoSkill{Name: "demo", Version: "1.1.0", Path: "skills/demo"}
	src.listing.Commit = "beef"
	src.listing.Skills[0] = demo
	src.archives["demo"] = skillArchive(t, demo, map[string]string{"SKILL.md": skillMD("demo", "1.1.0")})
	out, _, err := installSkills(t, env, src, b, "claude_code", false)
	if err != nil {
		t.Fatal(err)
	}
	// lint's digest is unchanged at the new commit: it is not rewritten.
	if !strings.Contains(out, "update demo 1.1.0") || strings.Contains(out, "lint") {
		t.Fatalf("output:\n%s", out)
	}
	dir := filepath.Join(env.Home, ".claude", "skills", "demo")
	if body, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); !strings.Contains(string(body), "1.1.0") {
		t.Fatalf("demo not updated: %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file from the old copy survived: %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(ledgerDir(env), "skill-backups", "claude_code", "demo-*", "scripts", "run.sh"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
}

// TestInstallSkillsRejectsDigestMismatch verifies the archive digest before
// extracting anything.
func TestInstallSkillsRejectsDigestMismatch(t *testing.T) {
	env, root := skillsEnv(t)
	src := orgSkills(t)
	archive := src.archives["demo"]
	archive.Digest = "sha256:" + strings.Repeat("0", 64)
	src.archives["demo"] = archive
	_, _, err := installSkills(t, env, src, skillsBundle(t, 1, defaultSkillPolicies()...), "claude_code", false)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v, want a digest mismatch", err)
	}
	if got := tree(t, root); strings.Contains(got, "demo") {
		t.Fatalf("demo written despite the digest mismatch:\n%s", got)
	}
}

// TestInstallSkillsRejectsTraversal refuses archives whose entries leave the
// skill dir: absolute paths, .. segments, another skill's folder, and links.
func TestInstallSkillsRejectsTraversal(t *testing.T) {
	cases := map[string][]tarEntry{
		"dotdot":         {{name: "demo/SKILL.md", body: "ok"}, {name: "demo/../../escape", body: "x"}},
		"absolute":       {{name: "demo/SKILL.md", body: "ok"}, {name: "/etc/escape", body: "x"}},
		"other skill":    {{name: "demo/SKILL.md", body: "ok"}, {name: "lint/SKILL.md", body: "x"}},
		"escape symlink": {{name: "demo/SKILL.md", body: "ok"}, {name: "demo/link", typeflag: tar.TypeSymlink, linkname: "../../../escape"}},
		"abs symlink":    {{name: "demo/SKILL.md", body: "ok"}, {name: "demo/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		"hardlink":       {{name: "demo/SKILL.md", body: "ok"}, {name: "demo/link", typeflag: tar.TypeLink, linkname: "demo/../../escape"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			env, root := skillsEnv(t)
			src := orgSkills(t)
			src.archives["demo"] = rawSkillArchive(t, src.listing.Skills[0], entries)
			_, _, err := installSkills(t, env, src, skillsBundle(t, 1, defaultSkillPolicies()...), "claude_code", false)
			if err == nil {
				t.Fatal("unsafe archive accepted")
			}
			got := tree(t, root)
			if strings.Contains(got, "escape") || strings.Contains(got, "skills/demo") || strings.Contains(got, ".kei-skill") {
				t.Fatalf("unsafe archive left files:\n%s", got)
			}
		})
	}
}

// TestSkillDirsResolution checks each harness kind's skill dir, including
// OpenCode's XDG resolution; harnesses with no skills report none.
func TestSkillDirsResolution(t *testing.T) {
	env, root := skillsEnv(t)
	want := map[string]string{
		"claude_code": filepath.Join(root, "home", ".claude", "skills"),
		"codex":       filepath.Join(root, "home", ".codex", "skills"),
		"opencode":    filepath.Join(root, "xdg", "opencode", "skills"),
	}
	for kind, dir := range want {
		h, _ := Default.Get(kind)
		if got := h.SkillDirs(env); len(got) != 1 || got[0] != dir {
			t.Fatalf("%s SkillDirs = %v, want %s", kind, got, dir)
		}
	}
	env.Getenv = func(string) string { return "" }
	if h, _ := Default.Get("opencode"); h.SkillDirs(env)[0] != filepath.Join(root, "home", ".config", "opencode", "skills") {
		t.Fatalf("opencode without XDG: %v", h.SkillDirs(env))
	}
	for _, kind := range []string{"custom", "openwebui"} {
		if h, _ := Default.Get(kind); len(h.SkillDirs(env)) != 0 {
			t.Fatalf("%s SkillDirs = %v, want none", kind, h.SkillDirs(env))
		}
	}
}
