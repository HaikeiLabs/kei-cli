package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
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
	configPath   string
	harnessEnv   harness.Env
	registry     *harness.Registry
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
		configPath:     configPath,
		harnessEnv:     harness.OSEnv(),
		registry:       harness.Default,
	}, nil
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
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
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

// checkHeartbeat reports on the runtime's heartbeat. The service runs
// kei-proxy runtime heartbeat in text mode, which records no per-beat
// timestamp locally, so the check points at the log rather than guess.
func checkHeartbeat(p doctorProbes, service doctorResult) doctorResult {
	r := doctorResult{Name: "heartbeat", Status: doctorWarn, Fix: "kei runtime service status"}
	if service.Status == doctorFail {
		r.Detail = "runtime service is not running, so no heartbeats are being sent"
		return r
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
