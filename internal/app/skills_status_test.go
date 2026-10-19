package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// haikeiSkillsAPI points the upstream skills lookup at a test server and
// restores the real GitHub base when the test ends.
func haikeiSkillsAPI(t *testing.T, server *httptest.Server) {
	t.Helper()
	prev := skillsUpstreamAPIBase
	skillsUpstreamAPIBase = server.URL
	t.Cleanup(func() { skillsUpstreamAPIBase = prev })
}

// haikeiContentsServer serves the GitHub contents listing for
// HaikeiLabs/skills/skills as dir entries and counts requests.
func haikeiContentsServer(t *testing.T, names map[string]string) (*httptest.Server, *int) {
	t.Helper()
	requests := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/HaikeiLabs/skills/contents/skills" {
			http.NotFound(w, r)
			return
		}
		*requests++
		entries := make([]map[string]string, 0, len(names))
		for name, sha := range names {
			entries = append(entries, map[string]string{"name": name, "path": "skills/" + name, "sha": sha, "type": "dir"})
		}
		_ = json.NewEncoder(w).Encode(entries)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// writeSkillsLock writes an npx `skills` lock file with the given entries
// (name -> raw lock entry).
func writeSkillsLock(t *testing.T, path string, entries map[string]map[string]string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 3, "skills": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func haikeiLockEntry(hash string) map[string]string {
	return map[string]string{
		"source":          "HaikeiLabs/skills",
		"sourceType":      "github",
		"sourceUrl":       "https://github.com/HaikeiLabs/skills.git",
		"skillPath":       "skills/haikei/SKILL.md",
		"skillFolderHash": hash,
	}
}

func TestSkillsStatusReportsStaleHaikeiSkills(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, _ := haikeiContentsServer(t, map[string]string{
		"haikei":            "upstream-new",
		"kei-cli":           "same-hash",
		"kei-harness-setup": "setup-hash",
	})
	haikeiSkillsAPI(t, server)

	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei": haikeiLockEntry("old-hash"),
		"skill-creator": {
			"source":          "anthropics/skills",
			"skillFolderHash": "not-haikei",
		},
	})
	writeSkillsLock(t, "skills-lock.json", map[string]map[string]string{
		"kei-cli": haikeiLockEntry("same-hash"),
	})

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"status"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "1 of 2") {
		t.Fatalf("want 1 of 2 stale, got:\n%s", out)
	}
	if !strings.Contains(out, "haikei") || !strings.Contains(out, "out of date") {
		t.Fatalf("stale skill not reported:\n%s", out)
	}
	if !strings.Contains(out, "kei-cli") || !strings.Contains(out, "up to date") {
		t.Fatalf("up-to-date skill not reported:\n%s", out)
	}
	if strings.Contains(out, "skill-creator") || strings.Contains(out, "kei-harness-setup") {
		t.Fatalf("status counted a skill that is not installed:\n%s", out)
	}
}

func TestSkillsStatusCachesUpstreamForADay(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server, requests := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new"})
	haikeiSkillsAPI(t, server)
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei": haikeiLockEntry("old-hash"),
	})

	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		if code := runSkillsCommand([]string{"status"}, &stdout, &stderr, server.Client()); code != 0 {
			t.Fatalf("status run %d exit=%d stderr=%s", i, code, stderr.String())
		}
	}
	if *requests != 1 {
		t.Fatalf("upstream requests=%d, want 1 (second run must use the cache)", *requests)
	}
	cache := filepath.Join(home, ".config", skillsUpstreamCacheFile)
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("cache not written to the Kei config dir: %v", err)
	}
}

func TestSkillsStatusWithNoHaikeiSkills(t *testing.T) {
	skillsHome(t)
	t.Chdir(t.TempDir())
	server, requests := haikeiContentsServer(t, map[string]string{"haikei": "upstream-new"})
	haikeiSkillsAPI(t, server)

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"status"}, &stdout, &stderr, server.Client()); code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No Haikei skills") {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
	if *requests != 0 {
		t.Fatalf("upstream requests=%d, want 0 when nothing is installed", *requests)
	}
}

func TestSkillsStatusFailsClosedWhenUpstreamIsDown(t *testing.T) {
	home := skillsHome(t)
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	haikeiSkillsAPI(t, server)
	writeSkillsLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]map[string]string{
		"haikei": haikeiLockEntry("old-hash"),
	})

	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"status"}, &stdout, &stderr, server.Client()); code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("reported up to date without an upstream answer:\n%s", stdout.String())
	}
}

func TestSkillsStatusRejectsBadUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSkillsCommand([]string{"status", "extra"}, &stdout, &stderr, http.DefaultClient); code != 2 {
		t.Fatalf("status with an argument exit=%d, want 2", code)
	}
	if code := runSkillsCommand([]string{"bogus"}, &stdout, &stderr, http.DefaultClient); code != 2 {
		t.Fatalf("unknown subcommand exit=%d, want 2", code)
	}
}
