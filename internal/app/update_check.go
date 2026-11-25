package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/updatecheck"
)

const (
	// updateCheckStateFile caches the daily release lookup next to the other
	// kei state files in the config dir.
	updateCheckStateFile = "kei-update-check.json"
	// updateCheckTimeout bounds the release lookup, and how long a command
	// waits for it after the command itself finished.
	updateCheckTimeout = 1500 * time.Millisecond
	// loginSummaryTimeout bounds the post-login installation lookups.
	loginSummaryTimeout = 5 * time.Second
)

// The update check's seams, replaced in tests.
var (
	updateCheckGetenv         = os.Getenv
	updateCheckStatePath      = defaultUpdateCheckStatePath
	updateCheckResolve        = resolveLatestRelease
	updateCheckInstalledProxy = installedProxyVersion
	updateCheckConfigDisabled = func() bool {
		path, err := defaultConfigPath()
		return err == nil && configDisablesUpdateCheck(path)
	}
	updateCheckStdoutTTY = writerIsTerminalAny
)

// updateNoticeSkips are the commands that never print the per-command
// notice: they print versions themselves, or must stay minimal.
var updateNoticeSkips = map[string]bool{
	"": true, "help": true, "--help": true, "-h": true,
	"version": true, "--version": true, "-v": true,
	"upgrade": true, "doctor": true, "login": true,
}

func writerIsTerminalAny(w any) bool {
	writer, ok := w.(io.Writer)
	return ok && writerIsTerminal(writer)
}

func defaultUpdateCheckStatePath() (string, error) {
	configPath, err := defaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), updateCheckStateFile), nil
}

// configDisablesUpdateCheck reports whether the kei config file sets
// update_check: false. A missing or unreadable config leaves the check on.
func configDisablesUpdateCheck(path string) bool {
	config, err := loadRuntimeConfig(path)
	return err == nil && config.UpdateCheckDisabled
}

// releaseBaseURL is where kei upgrade reads releases: the published release
// index, or AWS_S3_RELEASES_URL_BASE when set.
func releaseBaseURL(getenv func(string) string) string {
	if base := strings.TrimRight(getenv("AWS_S3_RELEASES_URL_BASE"), "/"); base != "" {
		return base
	}
	return defaultReleaseBase
}

// resolveLatestRelease reads the release index kei upgrade uses: latest.txt
// and the kei-proxy version that release pins.
func resolveLatestRelease(ctx context.Context) (updatecheck.Versions, error) {
	client := &http.Client{Timeout: updateCheckTimeout}
	if deadline, ok := ctx.Deadline(); ok {
		client.Timeout = time.Until(deadline)
	}
	kei, proxy, err := upgradeReleaseInfo(client, releaseBaseURL(os.Getenv))
	if err != nil {
		return updatecheck.Versions{}, err
	}
	return updatecheck.Versions{Kei: kei, Proxy: proxy}, nil
}

// installedProxyVersion is the version of the kei-proxy kei upgrade manages
// (next to kei), falling back to the one on PATH; "" when none is found.
func installedProxyVersion() string {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "kei-proxy"))
	}
	if path, err := exec.LookPath("kei-proxy"); err == nil {
		candidates = append(candidates, path)
	}
	for _, path := range candidates {
		if v, ok := binaryVersion(path, "kei-proxy"); ok {
			return v
		}
	}
	return ""
}

func updateCheckDebug(stderr io.Writer) func(string, ...any) {
	if updateCheckGetenv("KEI_DEBUG") == "" {
		return nil
	}
	return func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "kei debug: "+format+"\n", args...)
	}
}

func newUpdateChecker(stderr io.Writer) (updatecheck.Checker, bool) {
	path, err := updateCheckStatePath()
	if err != nil {
		return updatecheck.Checker{}, false
	}
	return updatecheck.Checker{StatePath: path, Resolve: updateCheckResolve, Debug: updateCheckDebug(stderr)}, true
}

func cliVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// startUpdateNotice starts the daily release check alongside the command and
// returns a function that, once the command is done, prints the one-line
// upgrade notice on stderr. It waits for the check only until the timeout
// that began when the check started, and prints nothing when suppressed,
// offline or up to date.
func startUpdateNotice(command string, args []string, stdout, stderr io.Writer, version string) func() {
	noop := func() {}
	if updateNoticeSkips[command] || updatecheck.Suppressed(updatecheck.Options{
		Getenv:         updateCheckGetenv,
		ConfigDisabled: updateCheckConfigDisabled(),
		StdoutTTY:      updateCheckStdoutTTY(stdout),
		Args:           args,
	}) {
		return noop
	}
	var debug strings.Builder
	checker, ok := newUpdateChecker(&debug)
	if !ok {
		return noop
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	result := make(chan string, 1)
	go func() {
		result <- checker.Notice(ctx, func() updatecheck.Versions {
			return updatecheck.Versions{Kei: cliVersion(version), Proxy: updateCheckInstalledProxy()}
		})
	}()
	return func() {
		defer cancel()
		select {
		case line := <-result:
			out := debug.String()
			if line != "" {
				out += line + "\n"
			}
			_, _ = io.WriteString(stderr, out)
		case <-ctx.Done():
		}
	}
}

// writeLoginUpdateSummary prints, after a successful kei login, how fresh
// this kei is and which of the org's runtime installations run a kei-proxy
// below the org minimum. It never fails the login: every lookup is best
// effort and failures go to the debug log only.
//
// The catalog gates bundle features by runtime version
// (bundleFeatureMinRuntime) but does not expose that map, so the summary
// reports below_minimum_version only.
func writeLoginUpdateSummary(ctx context.Context, stdout io.Writer, client *http.Client, baseURL string, store credentialStore, version string) {
	debug := updateCheckDebug(os.Stderr)
	debugf := func(format string, args ...any) {
		if debug != nil {
			debug(format, args...)
		}
	}
	var summary updatecheck.LoginSummary
	if !updatecheck.Disabled(updateCheckGetenv, updateCheckConfigDisabled()) {
		if checker, ok := newUpdateChecker(os.Stderr); ok {
			checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
			latest, ok := checker.Latest(checkCtx)
			cancel()
			if ok {
				summary.HaveLatest = true
				summary.Status = updatecheck.Status{
					Installed: updatecheck.Versions{Kei: cliVersion(version), Proxy: updateCheckInstalledProxy()},
					Latest:    latest,
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, loginSummaryTimeout)
	defer cancel()
	token, err := store.Load(baseURL)
	if err != nil {
		debugf("login summary: load CLI token: %v", err)
	} else if orgID, err := organizationIDFromCLIToken(token); err != nil {
		debugf("login summary: %v", err)
	} else if items, _, err := listBotInstallations(ctx, client, baseURL, token, orgID, true); err != nil {
		debugf("login summary: list runtime installations: %v", err)
	} else {
		summary.HaveInstallations = true
		installations := make([]updatecheck.Installation, 0, len(items))
		for _, item := range items {
			inst := updatecheck.Installation{Name: item.DisplayName, BelowMinimum: item.BelowMinimumVersion}
			if inst.Name == "" {
				inst.Name = item.ID
			}
			if item.RuntimeVersion != nil {
				inst.Version = *item.RuntimeVersion
			}
			installations = append(installations, inst)
		}
		summary.BelowMinimum = updatecheck.BelowMinimum(installations)
		if len(summary.BelowMinimum) > 0 {
			minimum, err := organizationMinRuntimeVersion(ctx, client, baseURL, token, orgID)
			if err != nil {
				debugf("login summary: read org minimum runtime version: %v", err)
			}
			summary.OrgMinimum = minimum
		}
	}
	updatecheck.WriteLoginSummary(stdout, summary)
}

// organizationMinRuntimeVersion reads min_runtime_version from the
// organization; "" when the org sets none.
func organizationMinRuntimeVersion(ctx context.Context, client *http.Client, baseURL, token, orgID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/organizations/"+url.PathEscape(orgID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	status, body, err := doRequest(client, req, 64<<10)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("get organization returned %d", status)
	}
	var org struct {
		MinRuntimeVersion *string `json:"min_runtime_version"`
	}
	if err := json.Unmarshal(body, &org); err != nil {
		return "", fmt.Errorf("decode organization: %w", err)
	}
	if org.MinRuntimeVersion == nil {
		return "", nil
	}
	return strings.TrimPrefix(strings.TrimSpace(*org.MinRuntimeVersion), "v"), nil
}
