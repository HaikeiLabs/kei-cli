package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// Doctor check outcomes, printed first on each line.
const (
	doctorPass = "PASS"
	doctorWarn = "WARN"
	doctorFail = "FAIL"
)

// doctorStaleFixHint is how the stale-process checks tell the user the one
// command that fixes a process still running the previous binary.
const doctorStaleFixHint = "run kei upgrade (it restarts installed services) or restart the process"

// doctorResult is one line of `kei doctor` output. Fix is printed only when
// the check did not pass.
type doctorResult struct {
	Status string
	Name   string
	Detail string
	Fix    string
}

// doctorProbes are every external probe the checks make. Tests replace them
// so no check touches the real machine.
type doctorProbes struct {
	goos string
	now  func() time.Time
	home func() (string, error)
	// readFile reads native config files and the service definition.
	readFile func(path string) ([]byte, error)
	// serviceLoaded reports whether the service manager has the runtime
	// service loaded (launchd) or active (systemd).
	serviceLoaded func(goos, name string) bool
	// serviceEnabled reports whether a systemd unit starts at login. launchd
	// is read from the plist's RunAtLoad instead.
	serviceEnabled func(name string) bool
	// runProxy runs kei-proxy with args and the runtime env, returning its
	// stdout. The env carries the runtime token; it is never printed.
	runProxy func(path string, env []string, args ...string) ([]byte, error)
	// loadConfig and defaultProxy locate kei-proxy the way kei runtime
	// bootstrap does.
	loadConfig   func(path string) (runtimeConfig, error)
	defaultProxy func() string
	// executable is the running kei binary; fileModTime reports a binary's
	// last-modified time; heartbeatProcessStart reports when the running
	// heartbeat process started; serveProcessStart reports when the running
	// kei-proxy serve process started. Together they let the stale-process
	// checks compare the running processes against the installed binaries.
	executable            func() (string, error)
	fileModTime           func(path string) (time.Time, bool)
	heartbeatProcessStart func() (time.Time, bool)
	serveProcessStart     func() (time.Time, bool)
	// upgradeRelease resolves the latest published kei-cli release and the
	// kei-proxy version that release pins; an error means the release could
	// not be fetched or parsed. binaryVersion reports the release version a
	// built binary prints for --version. Together they let the upgrade check
	// compare the running kei against the latest release.
	upgradeRelease func() (latest, proxy string, err error)
	binaryVersion  func(path, name string) (string, bool)
	// heartbeatFromServer reports the last heartbeat time from the Kei API.
	// Returns errHeartbeatNotAvailable when the installation cannot be
	// resolved or the user is not logged in (silent skip, keeps WARN).
	// Returns a non-nil error when a server request was attempted but failed
	// (caller keeps WARN on error).
	heartbeatFromServer func(config runtimeConfig) (*time.Time, error)
	configPath          string
	harnessEnv          harness.Env
	registry            *harness.Registry
}

func defaultDoctorProbes() (doctorProbes, error) {
	configPath, err := defaultConfigPath()
	if err != nil {
		return doctorProbes{}, err
	}
	return doctorProbes{
		goos:           runtime.GOOS,
		now:            time.Now,
		home:           userHomeDirFn,
		readFile:       os.ReadFile,
		serviceLoaded:  probeServiceLoaded,
		serviceEnabled: probeSystemdEnabled,
		runProxy:       probeRunProxy,
		loadConfig:     loadRuntimeConfig,
		defaultProxy:   defaultProxyPath,
		executable:     os.Executable,
		fileModTime:    probeFileModTime,
		heartbeatProcessStart: func() (time.Time, bool) {
			return probeHeartbeatProcessStart(time.Now())
		},
		serveProcessStart: func() (time.Time, bool) {
			return probeServeProcessStart(time.Now())
		},
		upgradeRelease:      probeUpgradeRelease,
		binaryVersion:       binaryVersion,
		configPath:          configPath,
		harnessEnv:          harness.OSEnv(),
		registry:            harness.Default,
		heartbeatFromServer: defaultHeartbeatFromServer,
	}, nil
}

// probeFileModTime reports the last-modified time of path, or ok=false when it
// cannot be stat'd.
func probeFileModTime(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// probeHeartbeatProcessStart finds the running "kei runtime heartbeat" process
// and returns when it started. It reads ps's elapsed time (etime), which is a
// locale-independent [DD-]HH:MM:SS duration, so the result does not depend on
// the locale's date format. It reports ok=false when no such process is
// running or ps is unavailable.
func probeHeartbeatProcessStart(now time.Time) (time.Time, bool) {
	out, err := exec.Command("ps", "-ww", "-eo", "etime,command").Output()
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "ELAPSED" {
			continue
		}
		if !strings.Contains(line, "runtime heartbeat") {
			continue
		}
		elapsed, ok := parsePSElapsed(fields[0])
		if !ok {
			continue
		}
		return now.Add(-elapsed), true
	}
	return time.Time{}, false
}

// parsePSElapsed parses ps's etime field, "[DD-]HH:MM:SS", into a duration.
func parsePSElapsed(s string) (time.Duration, bool) {
	var days int
	rest := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days = d
		rest = s[i+1:]
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	sec, errS := strconv.Atoi(parts[2])
	if errH != nil || errM != nil || errS != nil {
		return 0, false
	}
	return time.Duration(days)*24*time.Hour + time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute + time.Duration(sec)*time.Second, true
}

// probeServeProcessStart finds the running "kei-proxy serve" process and
// returns when it started, the same way probeHeartbeatProcessStart reads ps's
// elapsed time. It reports ok=false when no such process is running or ps is
// unavailable.
func probeServeProcessStart(now time.Time) (time.Time, bool) {
	out, err := exec.Command("ps", "-ww", "-eo", "etime,command").Output()
	if err != nil {
		return time.Time{}, false
	}
	return serveProcessStartFromPS(string(out), now)
}

// serveProcessStartFromPS picks the "kei-proxy serve" line out of ps output
// and returns when that process started.
func serveProcessStartFromPS(out string, now time.Time) (time.Time, bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "ELAPSED" {
			continue
		}
		if !strings.Contains(line, "kei-proxy serve") {
			continue
		}
		elapsed, ok := parsePSElapsed(fields[0])
		if !ok {
			continue
		}
		return now.Add(-elapsed), true
	}
	return time.Time{}, false
}

func probeServiceLoaded(goos, name string) bool {
	if goos == "darwin" {
		return isLaunchdServiceLoaded(name)
	}
	return sysCommand("systemctl", "--user", "is-active", "--quiet", name).Run() == nil
}

func probeSystemdEnabled(name string) bool {
	return sysCommand("systemctl", "--user", "is-enabled", "--quiet", name).Run() == nil
}

func probeRunProxy(path string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := runCommand(ctx, path, args...)
	command.Env = append(os.Environ(), env...)
	command.Stderr = io.Discard
	return command.Output()
}

func runDoctorCommand(args []string, stdout, stderr io.Writer) int {
	lc := newCommandFlags("", "doctor", "Diagnose local Kei setup", stdout, stderr)
	if rc, ok := parseLeaf(lc, args); !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor accepts no positional arguments")
		return 2
	}
	probes, err := defaultDoctorProbes()
	if err != nil {
		fmt.Fprintf(stderr, "doctor: %v\n", err)
		return 1
	}
	return runDoctor(probes, stdout)
}

// runDoctor runs every check, prints one line per result, and returns 1 when
// any check failed.
func runDoctor(p doctorProbes, stdout io.Writer) int {
	var results []doctorResult
	service := checkRuntimeService(p)
	results = append(results, service)
	results = append(results, checkHeartbeat(p, service))
	results = append(results, checkStaleProcess(p))
	results = append(results, checkStaleServeProcess(p))
	results = append(results, checkUpgradeAvailable(p))
	results = append(results, checkHarnessHooks(p)...)
	results = append(results, checkPolicyBundle(p))
	code := 0
	for _, r := range results {
		fmt.Fprintf(stdout, "%s  %s: %s\n", r.Status, r.Name, r.Detail)
		if r.Status != doctorPass && r.Fix != "" {
			fmt.Fprintf(stdout, "      fix: %s\n", r.Fix)
		}
		if r.Status == doctorFail {
			code = 1
		}
	}
	return code
}

var launchdRunAtLoad = regexp.MustCompile(`<key>RunAtLoad</key>\s*<true/>`)

// checkRuntimeService checks the LaunchAgent or systemd user unit is
// installed, loaded and set to start at login.
func checkRuntimeService(p doctorProbes) doctorResult {
	r := doctorResult{Name: "runtime service", Fix: "kei runtime service install"}
	path := serviceDefinitionPathFor(p.goos)
	if path == "" {
		r.Status, r.Detail, r.Fix = doctorWarn, "no supported service manager on "+p.goos, ""
		return r
	}
	definition, err := p.readFile(path)
	if err != nil {
		r.Status, r.Detail = doctorFail, "not installed ("+path+" missing)"
		return r
	}
	name := serviceNameFor(p.goos)
	if !p.serviceLoaded(p.goos, name) {
		r.Status, r.Detail = doctorFail, "installed but "+name+" is not loaded"
		return r
	}
	atLogin := false
	if p.goos == "darwin" {
		atLogin = launchdRunAtLoad.Match(definition)
	} else {
		atLogin = p.serviceEnabled(name)
	}
	if !atLogin {
		r.Status, r.Detail = doctorFail, name+" is loaded but not set to start at login"
		return r
	}
	r.Status, r.Detail = doctorPass, name+" installed, loaded, starts at login"
	return r
}

// errHeartbeatNotAvailable is returned by heartbeatFromServer when the
// installation cannot be resolved or the user is not logged in. The caller
// treats it as a silent skip and keeps the WARN fallback.
var errHeartbeatNotAvailable = errors.New("heartbeat data not available")

// defaultHeartbeatFromServer tries to fetch the last heartbeat from the Kei
// API. It loads the CLI bearer token from the keychain, resolves the runtime
// installation via the runtime whoami endpoint, and reads last_heartbeat_at
// from the runtime installation status endpoint. Returns errHeartbeatNotAvailable
// when not logged in or the installation cannot be resolved (silent skip).
func defaultHeartbeatFromServer(config runtimeConfig) (*time.Time, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		return nil, errHeartbeatNotAvailable
	}

	store := osKeychainStore{}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		return nil, errHeartbeatNotAvailable // not logged in
	}

	identity, err := verifyRuntimeToken(ctx, client, config.ControlPlaneURL, config.RuntimeToken)
	if err != nil {
		return nil, errHeartbeatNotAvailable // installation not resolvable
	}

	status, err := getRuntimeInstallationStatus(ctx, client, baseURL, cliToken, identity.OrgID, identity.ID)
	if err != nil {
		return nil, err // request error, caller keeps WARN
	}

	return status.LastHeartbeatAt, nil
}

// friendlyDuration formats a positive duration in a concise human-readable
// form: "40s", "1m 30s", "5m", "1h 2m", "2h".
func friendlyDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// checkHeartbeat reports on the runtime's heartbeat. The service runs
// kei-proxy runtime heartbeat in text mode, which records no per-beat
// timestamp locally. When the user is logged in and the installation can be
// resolved, the check reads last_heartbeat_at from the Kei API.
func checkHeartbeat(p doctorProbes, service doctorResult) doctorResult {
	r := doctorResult{Name: "heartbeat", Status: doctorWarn, Fix: "kei runtime service status"}
	if service.Status == doctorFail {
		r.Detail = "runtime service is not running, so no heartbeats are being sent"
		return r
	}

	// Attempt server-side heartbeat check when local config is available.
	if config, err := p.loadConfig(p.configPath); err == nil && config.RuntimeToken != "" && config.ControlPlaneURL != "" {
		lastHB, reqErr := p.heartbeatFromServer(config)
		if reqErr == nil {
			// Server responded with a heartbeat timestamp.
			if lastHB != nil {
				age := p.now().Sub(*lastHB)
				if age < 5*time.Minute {
					r.Status = doctorPass
					r.Detail = "last heartbeat " + friendlyDuration(age) + " ago"
					r.Fix = ""
					return r
				}
				r.Detail = "last heartbeat " + friendlyDuration(age) + " ago; restart the service if this seems wrong"
				return r
			}
			// Server returned null last_heartbeat_at — never heartbeated.
			r.Status = doctorFail
			r.Detail = "no heartbeat received yet; run 'kei runtime service install' and check the service is running"
			return r
		}
		if errors.Is(reqErr, errHeartbeatNotAvailable) {
			// Not logged in or installation not resolved — silently fall
			// through to the WARN fallback below.
		} else {
			// Network or server error — also fall through to WARN.
		}
	}

	logHint := "journalctl --user -u " + serviceNameFor(p.goos)
	if p.goos == "darwin" {
		logHint = "~/Library/Logs/kei-runtime.log"
		if home, err := p.home(); err == nil {
			logHint = launchdLogPath(home)
		}
	}
	r.Detail = "last heartbeat time is not recorded locally; check " + logHint
	return r
}

// doctorProxyPath locates the installed kei-proxy binary the way the checks
// do: the configured proxy path, else the default lookup.
func doctorProxyPath(p doctorProbes) string {
	if config, err := p.loadConfig(p.configPath); err == nil && config.ProxyPath != "" {
		return config.ProxyPath
	}
	return p.defaultProxy()
}

// checkStaleProcess warns when the running heartbeat process started before
// the installed kei or kei-proxy binary was last modified: an upgrade replaced
// the binaries but the service was never restarted, so the old code is still
// running. It passes when no heartbeat process is running or when the process
// is at least as new as both binaries.
func checkStaleProcess(p doctorProbes) doctorResult {
	r := doctorResult{Name: "stale runtime process", Status: doctorPass, Detail: "running process matches the installed binaries"}
	start, ok := p.heartbeatProcessStart()
	if !ok {
		r.Detail = "no running heartbeat process to compare"
		return r
	}
	var stale []string
	if keiPath, err := p.executable(); err == nil {
		if mod, ok := p.fileModTime(keiPath); ok && start.Before(mod) {
			stale = append(stale, "kei")
		}
	}
	proxyPath := doctorProxyPath(p)
	if mod, ok := p.fileModTime(proxyPath); ok && start.Before(mod) {
		stale = append(stale, "kei-proxy")
	}
	if len(stale) == 0 {
		return r
	}
	r.Status = doctorWarn
	r.Detail = "running process started before " + strings.Join(stale, " and ") + " was last modified; " + doctorStaleFixHint
	r.Fix = "kei runtime service install"
	return r
}

// checkStaleServeProcess warns when the running kei-proxy serve process
// started before the installed kei-proxy binary was last modified: the serve
// agent was not restarted after the binaries were replaced, so it still runs
// the old code. It passes when no serve process is running or when the
// process is at least as new as the binary.
func checkStaleServeProcess(p doctorProbes) doctorResult {
	r := doctorResult{Name: "stale serve process", Status: doctorPass, Detail: "running serve process matches the installed binary"}
	start, ok := p.serveProcessStart()
	if !ok {
		r.Detail = "no running serve process to compare"
		return r
	}
	proxyPath := ""
	if config, err := p.loadConfig(p.configPath); err == nil && config.ProxyPath != "" {
		proxyPath = config.ProxyPath
	}
	if proxyPath == "" {
		proxyPath = p.defaultProxy()
	}
	if mod, ok := p.fileModTime(proxyPath); ok && start.Before(mod) {
		r.Status = doctorWarn
		r.Detail = "running serve process started before kei-proxy was last modified; " + doctorStaleFixHint
		r.Fix = serveAgentRestartCommand(p.goos)
	}
	return r
}

// checkUpgradeAvailable reports when a newer kei-cli release is published, so
// `kei doctor` nudges the user to upgrade. It never fails the run and never
// upgrades: when the release endpoint cannot be reached or parsed, it passes
// with a quiet note.
func checkUpgradeAvailable(p doctorProbes) doctorResult {
	r := doctorResult{Name: "upgrade available", Status: doctorPass}
	latest, pinnedProxy, err := p.upgradeRelease()
	if err != nil {
		r.Detail = "could not check for a newer release (offline?)"
		return r
	}
	keiPath, err := p.executable()
	if err != nil {
		r.Detail = "could not check for a newer release (offline?)"
		return r
	}
	running, ok := p.binaryVersion(keiPath, "kei")
	if !ok {
		r.Detail = "could not check for a newer release (unknown running version)"
		return r
	}
	if compareVersions(running, latest) >= 0 {
		r.Detail = "kei " + running + " is the latest release"
		return r
	}
	r.Status = doctorWarn
	r.Detail = "kei " + running + " is installed, " + latest + " is available"
	// Report the kei-proxy transition only when both ends are known; the
	// upgrade command prints the real transition anyway.
	if installedProxy, ok := p.binaryVersion(doctorProxyPath(p), "kei-proxy"); ok && pinnedProxy != "" {
		r.Detail += fmt.Sprintf(" (kei-proxy %s -> %s)", installedProxy, pinnedProxy)
	}
	r.Detail += ". Run: kei upgrade"
	return r
}

// checkHarnessHooks checks every detected harness with an audit hook has
// that hook installed in its native config, and not a stale --harness <uuid>
// form.
func checkHarnessHooks(p doctorProbes) []doctorResult {
	var results []doctorResult
	for _, h := range p.registry.Detected(p.harnessEnv) {
		spec := h.HookSpec(p.harnessEnv)
		if spec == nil {
			continue
		}
		r := doctorResult{Name: "harness " + h.Kind(), Fix: "kei harness sync --harness " + h.Kind()}
		r.Status, r.Detail = doctorPass, "Kei hook installed"
		paths := make([]string, 0, len(spec.Files))
		for path := range spec.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if status, detail := checkHookFile(p, path, spec.Files[path]); status != doctorPass {
				r.Status, r.Detail = status, detail
				break
			}
		}
		results = append(results, r)
	}
	if len(results) == 0 {
		results = append(results, doctorResult{Name: "harness hooks", Status: doctorPass, Detail: "no harness with a Kei hook detected"})
	}
	return results
}

const keiHookPrefix = "kei-proxy hook "

func checkHookFile(p doctorProbes, path string, want []byte) (string, string) {
	got, err := p.readFile(path)
	if err != nil {
		return doctorFail, "hook missing: " + path + " not readable"
	}
	var wantDoc any
	if json.Unmarshal(want, &wantDoc) != nil {
		// A Kei-owned file (the OpenCode plugin) is written whole.
		if bytes.Contains(got, []byte("--harness ")) {
			return doctorFail, "stale --harness hook in " + path
		}
		if !bytes.Equal(got, want) {
			return doctorFail, "hook out of date in " + path
		}
		return doctorPass, ""
	}
	var gotDoc any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		return doctorFail, "cannot parse " + path
	}
	have := map[string]bool{}
	for _, command := range hookCommands(gotDoc) {
		if strings.HasPrefix(command, keiHookPrefix) && strings.Contains(command, "--harness ") {
			return doctorFail, "stale --harness hook in " + path
		}
		have[command] = true
	}
	for _, command := range hookCommands(wantDoc) {
		if !have[command] {
			return doctorFail, "hook " + command + " missing from " + path
		}
	}
	return doctorPass, ""
}

// hookCommands returns every "command" string nested in a decoded JSON
// document.
func hookCommands(value any) []string {
	var out []string
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if s, ok := child.(string); ok && key == "command" {
				out = append(out, s)
				continue
			}
			out = append(out, hookCommands(child)...)
		}
	case []any:
		for _, child := range node {
			out = append(out, hookCommands(child)...)
		}
	}
	return out
}

// policyShow is the part of `kei-proxy policy show` the check reads.
type policyShow struct {
	State         string    `json:"state"`
	BundleVersion int64     `json:"bundle_version"`
	NotAfter      time.Time `json:"not_after"`
}

// checkPolicyBundle runs kei-proxy policy show, located the same way as
// kei runtime bootstrap, and checks the persisted bundle is active and
// unexpired.
func checkPolicyBundle(p doctorProbes) doctorResult {
	r := doctorResult{Name: "policy bundle", Fix: "kei-proxy policy sync"}
	var env []string
	proxyPath := ""
	if config, err := p.loadConfig(p.configPath); err == nil {
		proxyPath = config.ProxyPath
		if config.RuntimeToken != "" {
			env = []string{"KEI_RUNTIME_CONTROL_PLANE_URL=" + config.ControlPlaneURL, "KEI_RUNTIME_TOKEN=" + config.RuntimeToken}
		}
	}
	if proxyPath == "" {
		proxyPath = p.defaultProxy()
	}
	out, runErr := p.runProxy(proxyPath, env, "policy", "show")
	var show policyShow
	if err := json.Unmarshal(out, &show); err != nil || show.State == "" {
		var exitErr *exec.ExitError
		if runErr != nil && !errors.As(runErr, &exitErr) {
			r.Status, r.Detail = doctorFail, "cannot run "+proxyPath+" policy show"
			return r
		}
		r.Status, r.Detail = doctorFail, "kei-proxy policy show returned no bundle state"
		return r
	}
	expiry := "not_after unknown"
	if !show.NotAfter.IsZero() {
		expiry = "not_after " + show.NotAfter.UTC().Format(time.RFC3339)
	}
	switch {
	case !show.NotAfter.IsZero() && !show.NotAfter.After(p.now()):
		r.Status, r.Detail = doctorFail, fmt.Sprintf("bundle %d expired (%s)", show.BundleVersion, expiry)
	case show.State == "active":
		r.Status, r.Detail = doctorPass, fmt.Sprintf("bundle %d active, %s", show.BundleVersion, expiry)
	case show.State == "stale_but_valid":
		r.Status, r.Detail = doctorWarn, fmt.Sprintf("bundle %d stale_but_valid (refresh failing), %s", show.BundleVersion, expiry)
	default:
		r.Status, r.Detail = doctorFail, fmt.Sprintf("bundle state %s, %s", show.State, expiry)
	}
	return r
}
