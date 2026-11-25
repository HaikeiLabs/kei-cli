// Package updatecheck tells kei users when a newer kei, or a newer bundled
// kei-proxy, is published, and which of the org's runtime installations run a
// kei-proxy below the org minimum.
//
// It never blocks and never fails a command: the release lookup is cached in
// a small state file for a day, runs under a short timeout, and any error is
// sent to the debug log only. Fetching the release index is the caller's job
// (Checker.Resolve), so this package carries no network or release-source
// knowledge of its own.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultInterval is how often the release index is consulted, and how often
// the upgrade notice is shown.
const DefaultInterval = 24 * time.Hour

// DisableEnv turns the release check off (CI, air-gapped installs).
const DisableEnv = "KEI_NO_UPDATE_CHECK"

// UpgradeCommand is the step every notice points to.
const UpgradeCommand = "kei upgrade"

var releasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9._-]+)?$`)

// Versions is a kei version and the kei-proxy version that goes with it. For
// the latest release, Proxy is the kei-proxy that release pins (bundles). An
// empty field means unknown.
type Versions struct {
	Kei   string `json:"kei,omitempty"`
	Proxy string `json:"kei_proxy,omitempty"`
}

// Status pairs what is installed with what is published.
type Status struct {
	Installed Versions
	Latest    Versions
}

// isRelease reports whether v is a release version (so "dev" or "" never
// compare as older than anything).
func isRelease(v string) bool {
	return releasePattern.MatchString(v)
}

func behind(installed, latest string) bool {
	return isRelease(installed) && isRelease(latest) && Compare(installed, latest) < 0
}

// KeiBehind reports whether a newer kei release is published.
func (s Status) KeiBehind() bool { return behind(s.Installed.Kei, s.Latest.Kei) }

// ProxyBehind reports whether the latest release bundles a newer kei-proxy
// than the one installed next to kei.
func (s Status) ProxyBehind() bool { return behind(s.Installed.Proxy, s.Latest.Proxy) }

// Notice is the one-line upgrade suggestion, or "" when nothing newer is
// known.
func (s Status) Notice() string {
	switch {
	case s.KeiBehind() && s.ProxyBehind():
		return fmt.Sprintf("kei %s is available (you have %s; kei-proxy %s -> %s) — run %s",
			s.Latest.Kei, s.Installed.Kei, s.Installed.Proxy, s.Latest.Proxy, UpgradeCommand)
	case s.KeiBehind():
		return fmt.Sprintf("kei %s is available (you have %s) — run %s", s.Latest.Kei, s.Installed.Kei, UpgradeCommand)
	case s.ProxyBehind():
		return fmt.Sprintf("kei-proxy %s is available (you have %s) — run %s", s.Latest.Proxy, s.Installed.Proxy, UpgradeCommand)
	}
	return ""
}

// Summary reports installed against latest for kei and kei-proxy, for kei
// doctor and the kei login summary.
func (s Status) Summary() string {
	kei := "kei " + orUnknown(s.Installed.Kei) + " installed"
	if s.Latest.Kei != "" {
		kei += ", " + s.Latest.Kei + " latest"
	}
	proxy := "kei-proxy not found"
	if s.Installed.Proxy != "" {
		proxy = "kei-proxy " + s.Installed.Proxy + " installed"
	}
	if s.Latest.Proxy != "" {
		proxy += ", " + s.Latest.Proxy + " latest"
	}
	return kei + "; " + proxy
}

func orUnknown(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}

// State is the cached result of the last release lookup.
type State struct {
	// CheckedAt is the last lookup attempt, successful or not, so an offline
	// machine retries once per interval rather than on every command.
	CheckedAt time.Time `json:"checked_at"`
	// Latest is the last successfully resolved release.
	Latest Versions `json:"latest"`
	// NotifiedAt is when the upgrade notice was last printed.
	NotifiedAt time.Time `json:"notified_at,omitzero"`
}

// ReadState returns the cached state, or the zero State when the file is
// missing or unreadable.
func ReadState(path string) State {
	var state State
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	if json.Unmarshal(data, &state) != nil {
		return State{}
	}
	return state
}

// WriteState saves the state with owner-only permissions.
func WriteState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Checker resolves the latest release through a daily cache.
type Checker struct {
	// StatePath is the cache file.
	StatePath string
	// Interval between lookups and between notices; DefaultInterval when 0.
	Interval time.Duration
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Resolve fetches the latest release from the release index. The caller
	// bounds it with a short timeout.
	Resolve func(ctx context.Context) (Versions, error)
	// Debug receives failures; nil drops them.
	Debug func(format string, args ...any)
}

func (c Checker) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultInterval
	}
	return c.Interval
}

func (c Checker) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c Checker) debugf(format string, args ...any) {
	if c.Debug != nil {
		c.Debug(format, args...)
	}
}

// Latest returns the latest release, from the cache when it is younger than
// the interval and otherwise through Resolve. A failed lookup is recorded so
// it is not retried until the interval passes; the last known release is
// still returned. ok is false when no release is known.
func (c Checker) Latest(ctx context.Context) (Versions, bool) {
	state := c.refresh(ctx, ReadState(c.StatePath))
	return state.Latest, state.Latest.Kei != ""
}

func (c Checker) refresh(ctx context.Context, state State) State {
	now := c.now()
	if !state.CheckedAt.IsZero() && now.Sub(state.CheckedAt) < c.interval() && !state.CheckedAt.After(now) {
		return state
	}
	state.CheckedAt = now
	if c.Resolve != nil {
		latest, err := c.Resolve(ctx)
		if err != nil {
			c.debugf("update check: resolve latest release: %v", err)
		} else {
			state.Latest = latest
		}
	}
	if err := WriteState(c.StatePath, state); err != nil {
		c.debugf("update check: write state: %v", err)
	}
	return state
}

// Notice returns the upgrade line at most once per interval, or "". installed
// is called only when a notice could be shown, so the cost of probing the
// installed binaries is paid at most once a day.
func (c Checker) Notice(ctx context.Context, installed func() Versions) string {
	state := ReadState(c.StatePath)
	now := c.now()
	if !state.NotifiedAt.IsZero() && now.Sub(state.NotifiedAt) < c.interval() && !state.NotifiedAt.After(now) {
		return ""
	}
	state = c.refresh(ctx, state)
	if state.Latest.Kei == "" {
		return ""
	}
	line := Status{Installed: installed(), Latest: state.Latest}.Notice()
	if line == "" {
		return ""
	}
	state.NotifiedAt = now
	if err := WriteState(c.StatePath, state); err != nil {
		c.debugf("update check: write state: %v", err)
	}
	return line
}

// Options decide whether the per-command notice may print.
type Options struct {
	Getenv func(string) string
	// ConfigDisabled is the config file setting (update_check: false).
	ConfigDisabled bool
	// StdoutTTY reports whether stdout is a terminal.
	StdoutTTY bool
	// Args are the command's arguments, scanned for JSON and quiet output.
	Args []string
}

// Disabled reports whether the user turned the release check off.
func Disabled(getenv func(string) string, configDisabled bool) bool {
	if configDisabled {
		return true
	}
	if getenv == nil {
		return false
	}
	return envTrue(getenv(DisableEnv))
}

func envTrue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	b, err := strconv.ParseBool(value)
	return err != nil || b
}

// Suppressed reports whether the per-command notice must stay silent: the
// check is disabled, stdout is not a terminal, or the output is JSON or quiet.
func Suppressed(o Options) bool {
	if Disabled(o.Getenv, o.ConfigDisabled) || !o.StdoutTTY {
		return true
	}
	for _, arg := range o.Args {
		if arg == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if hasValue {
			if on, err := strconv.ParseBool(value); err == nil && !on {
				continue
			}
		}
		switch name {
		case "json", "quiet", "q":
			return true
		}
	}
	return false
}

// Installation is one runtime installation as the catalog lists it.
type Installation struct {
	Name         string
	Version      string
	BelowMinimum bool
}

// BelowMinimum keeps the installations the catalog flags as below the org
// minimum runtime version and that reported a version.
func BelowMinimum(items []Installation) []Installation {
	var out []Installation
	for _, item := range items {
		if item.BelowMinimum && item.Version != "" {
			out = append(out, item)
		}
	}
	return out
}

// LoginSummary is what kei login reports after a successful login.
type LoginSummary struct {
	Status     Status
	HaveLatest bool
	// HaveInstallations is false when the installation list could not be
	// read; the runtime part is then left out.
	HaveInstallations bool
	// OrgMinimum is the org's minimum runtime version, "" when unknown.
	OrgMinimum   string
	BelowMinimum []Installation
}

// missingBelowMinimum says what an admin loses while a runtime is below the
// minimum: the catalog omits every bundle field gated above the runtime's
// version (backwards compatible by omission).
const missingBelowMinimum = "bundle features gated above its version are left out of its policy bundle"

// WriteLoginSummary prints the short post-login summary. It prints nothing
// for the parts that could not be determined.
func WriteLoginSummary(w io.Writer, s LoginSummary) {
	_, _ = io.WriteString(w, loginSummaryText(s))
}

func loginSummaryText(s LoginSummary) string {
	var b strings.Builder
	if s.HaveLatest {
		line := "Versions: " + s.Status.Summary()
		notice := s.Status.Notice()
		if notice == "" {
			line += " (up to date)"
		}
		fmt.Fprintln(&b, line)
		if notice != "" {
			fmt.Fprintln(&b, notice)
		}
	}
	if !s.HaveInstallations {
		return b.String()
	}
	if len(s.BelowMinimum) == 0 {
		fmt.Fprintln(&b, "Runtime installations: none below the org minimum runtime version")
		return b.String()
	}
	minimum := ""
	if s.OrgMinimum != "" {
		minimum = " (" + s.OrgMinimum + ")"
	}
	if len(s.BelowMinimum) == 1 {
		fmt.Fprintf(&b, "1 runtime installation is below the org minimum runtime version%s:\n", minimum)
	} else {
		fmt.Fprintf(&b, "%d runtime installations are below the org minimum runtime version%s:\n", len(s.BelowMinimum), minimum)
	}
	needed := "the org minimum"
	if s.OrgMinimum != "" {
		needed = s.OrgMinimum + " or later"
	}
	for _, item := range s.BelowMinimum {
		fmt.Fprintf(&b, "  %s: kei-proxy %s, needs %s; missing: %s\n", item.Name, item.Version, needed, missingBelowMinimum)
	}
	fmt.Fprintln(&b, "  Upgrade: run kei upgrade on each runtime host (container runtimes: deploy the new kei-proxy image), then restart kei-proxy.")
	return b.String()
}

// Compare orders two release versions by semver: -1 if a is older than b, 0
// if equal, 1 if newer. A release sorts after its own prereleases.
func Compare(a, b string) int {
	an, ap := numericAndPrerelease(a)
	bn, bp := numericAndPrerelease(b)
	for i := range an {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	}
	return 1
}

func numericAndPrerelease(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	prerelease := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		prerelease = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var nums [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		nums[i] = n
	}
	return nums, prerelease
}
