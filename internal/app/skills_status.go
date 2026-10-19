package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// haikeiSkillsSource is the npx `skills` lock source value that identifies
	// the skills this repo installs. Entries from any other source are ignored.
	haikeiSkillsSource = "HaikeiLabs/skills"
	// haikeiSkillsContentsPath is the one unauthenticated GitHub contents call
	// that lists the skill folders upstream; each entry's sha is its tree SHA.
	haikeiSkillsContentsPath = "/repos/HaikeiLabs/skills/contents/skills"
	// skillsUpstreamCacheFile holds the last upstream listing in the Kei config
	// dir, so a day of status checks costs one request.
	skillsUpstreamCacheFile = "kei-skills-cache.json"
	skillsUpstreamCacheTTL  = 24 * time.Hour
)

// skillsUpstreamAPIBase is the GitHub API root; tests point it at an httptest
// server so no test reaches the network.
var skillsUpstreamAPIBase = "https://api.github.com"

// skillsLockFile is the part of an npx `skills` lock file (the global
// ~/.agents/.skill-lock.json or a project ./skills-lock.json) that decides
// whether an installed skill is out of date.
type skillsLockFile struct {
	Skills map[string]skillsLockEntry `json:"skills"`
}

type skillsLockEntry struct {
	Source          string `json:"source"`
	SkillFolderHash string `json:"skillFolderHash"`
}

// readHaikeiSkillLock returns the HaikeiLabs/skills entries (skill name ->
// installed folder tree SHA) in one lock file. A missing lock file is not an
// error: it just means nothing was installed from that location.
func readHaikeiSkillLock(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lock skillsLockFile
	if err := json.Unmarshal(contents, &lock); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	skills := map[string]string{}
	for name, entry := range lock.Skills {
		if strings.EqualFold(entry.Source, haikeiSkillsSource) && entry.SkillFolderHash != "" {
			skills[name] = entry.SkillFolderHash
		}
	}
	return skills, nil
}

// installedHaikeiSkills merges the global and the project lock files. A skill
// installed in both is the project install, so that entry wins.
func installedHaikeiSkills() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	installed := map[string]string{}
	for _, path := range []string{
		filepath.Join(home, ".agents", ".skill-lock.json"),
		"skills-lock.json",
	} {
		skills, err := readHaikeiSkillLock(path)
		if err != nil {
			return nil, err
		}
		for name, hash := range skills {
			installed[name] = hash
		}
	}
	return installed, nil
}

// skillsUpstreamCache is the cached GitHub contents listing.
type skillsUpstreamCache struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Skills    map[string]string `json:"skills"`
}

// upstreamSkillSHAs returns the current folder tree SHA of each skill upstream,
// from the cache when it is less than a day old and otherwise from one
// unauthenticated GitHub contents call.
func upstreamSkillSHAs(ctx context.Context, client *http.Client, cachePath string, now time.Time) (map[string]string, error) {
	if cached, ok := readSkillsUpstreamCache(cachePath, now); ok {
		return cached, nil
	}
	shas, err := fetchUpstreamSkillSHAs(ctx, client)
	if err != nil {
		return nil, err
	}
	writeSkillsUpstreamCache(cachePath, shas, now)
	return shas, nil
}

func readSkillsUpstreamCache(path string, now time.Time) (map[string]string, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cache skillsUpstreamCache
	if err := json.Unmarshal(contents, &cache); err != nil {
		return nil, false
	}
	if cache.FetchedAt.IsZero() || now.Sub(cache.FetchedAt) >= skillsUpstreamCacheTTL || len(cache.Skills) == 0 {
		return nil, false
	}
	return cache.Skills, true
}

// writeSkillsUpstreamCache is best effort: a cache that cannot be written only
// costs the next status check one request.
func writeSkillsUpstreamCache(path string, shas map[string]string, now time.Time) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	body, err := json.Marshal(skillsUpstreamCache{FetchedAt: now, Skills: shas})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, body, 0o600)
}

func fetchUpstreamSkillSHAs(ctx context.Context, client *http.Client) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, skillsUpstreamAPIBase+haikeiSkillsContentsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", haikeiSkillsSource, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", haikeiSkillsSource, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list %s: unexpected status %s", haikeiSkillsSource, resp.Status)
	}
	var entries []struct {
		Name string `json:"name"`
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("list %s: decode: %w", haikeiSkillsSource, err)
	}
	shas := map[string]string{}
	for _, entry := range entries {
		if entry.Type == "dir" && entry.Name != "" && entry.SHA != "" {
			shas[entry.Name] = entry.SHA
		}
	}
	return shas, nil
}

// skillStaleness is one installed Haikei skill compared with upstream.
type skillStaleness struct {
	Name      string
	Installed string
	Upstream  string
	UpToDate  bool
}

// haikeiSkillsStaleness compares installed skill folder hashes with the
// upstream ones. A skill upstream no longer publishes counts as out of date.
func haikeiSkillsStaleness(installed, upstream map[string]string) []skillStaleness {
	names := make([]string, 0, len(installed))
	for name := range installed {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]skillStaleness, 0, len(names))
	for _, name := range names {
		sha := upstream[name]
		rows = append(rows, skillStaleness{
			Name:      name,
			Installed: installed[name],
			Upstream:  sha,
			UpToDate:  sha != "" && sha == installed[name],
		})
	}
	return rows
}

// staleHaikeiSkills returns the installed skills that differ from upstream and
// the total number installed; the prompt in step 4 uses both.
func staleHaikeiSkills(ctx context.Context, client *http.Client, cachePath string, now time.Time) (stale []string, total int, err error) {
	installed, err := installedHaikeiSkills()
	if err != nil {
		return nil, 0, err
	}
	if len(installed) == 0 {
		return nil, 0, nil
	}
	upstream, err := upstreamSkillSHAs(ctx, client, cachePath, now)
	if err != nil {
		return nil, 0, err
	}
	for _, row := range haikeiSkillsStaleness(installed, upstream) {
		if !row.UpToDate {
			stale = append(stale, row.Name)
		}
	}
	return stale, len(installed), nil
}

func defaultSkillsUpstreamCachePath() (string, error) {
	configPath, err := defaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), skillsUpstreamCacheFile), nil
}

// runSkillsStatus is `kei skills status`: report each installed Haikei skill as
// up to date or out of date against the folders upstream.
func runSkillsStatus(stdout, stderr io.Writer, client *http.Client) int {
	cachePath, err := defaultSkillsUpstreamCachePath()
	if err != nil {
		fmt.Fprintf(stderr, "skills status: %v\n", err)
		return 1
	}
	installed, err := installedHaikeiSkills()
	if err != nil {
		fmt.Fprintf(stderr, "skills status: %v\n", err)
		return 1
	}
	if len(installed) == 0 {
		fmt.Fprintln(stdout, "No Haikei skills are installed.")
		return 0
	}
	upstream, err := upstreamSkillSHAs(context.Background(), client, cachePath, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "skills status: %v\n", err)
		return 1
	}
	rows := haikeiSkillsStaleness(installed, upstream)
	stale := 0
	for _, row := range rows {
		state := "up to date"
		if !row.UpToDate {
			state = "out of date"
			stale++
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", row.Name, shortSkillHash(row.Installed), state)
	}
	fmt.Fprintf(stdout, "%d of %d Haikei skills are out of date.\n", stale, len(rows))
	return 0
}

func shortSkillHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
