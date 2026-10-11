package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// doctorHarness is an in-memory harness: it is detected when detected is set
// and installs hook as its audit hook.
type doctorHarness struct {
	kind     string
	detected bool
	hook     map[string][]byte
}

func (h doctorHarness) Kind() string                                  { return h.kind }
func (h doctorHarness) Detect(harness.Env) bool                       { return h.detected }
func (h doctorHarness) Targets(harness.Env) ([]harness.Target, error) { return nil, nil }
func (h doctorHarness) Render(harness.Bundle) (harness.Rendered, error) {
	return harness.Rendered{}, nil
}
func (h doctorHarness) Apply(context.Context, harness.Rendered, harness.ApplyOpts) (harness.Result, error) {
	return harness.Result{}, nil
}
func (h doctorHarness) ImportRules(harness.Env, string) ([]harness.Rule, error) { return nil, nil }
func (h doctorHarness) HookSpec(harness.Env) *harness.HookSpec {
	if h.hook == nil {
		return nil
	}
	return &harness.HookSpec{Files: h.hook}
}

const doctorTestToken = "kei_rt_secret_value_do_not_print"

var doctorNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// fakeDoctorProbes returns probes for a healthy macOS machine: the service is
// installed and loaded, no harness is detected and the bundle is active.
func fakeDoctorProbes(t *testing.T, files map[string][]byte) doctorProbes {
	t.Helper()
	oldHome := userHomeDirFn
	userHomeDirFn = func() (string, error) { return "/home/test", nil }
	t.Cleanup(func() { userHomeDirFn = oldHome })
	if files == nil {
		files = map[string][]byte{}
	}
	if _, ok := files[serviceDefinitionPathFor("darwin")]; !ok {
		files[serviceDefinitionPathFor("darwin")] = []byte("<key>RunAtLoad</key>\n\t<true/>")
	}
	return doctorProbes{
		goos: "darwin",
		now:  func() time.Time { return doctorNow },
		home: func() (string, error) { return "/home/test", nil },
		readFile: func(path string) ([]byte, error) {
			if b, ok := files[path]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		serviceLoaded:  func(string, string) bool { return true },
		serviceEnabled: func(string) bool { return true },
		runProxy: func(string, []string, ...string) ([]byte, error) {
			return []byte(`{"state":"active","bundle_version":7,"not_after":"2026-10-11T12:00:00Z"}`), nil
		},
		loadConfig: func(string) (runtimeConfig, error) {
			return runtimeConfig{ControlPlaneURL: "https://kei.example", RuntimeToken: doctorTestToken, ProxyPath: "/opt/kei-proxy"}, nil
		},
		defaultProxy: func() string { return "kei-proxy" },
		configPath:   "/home/test/.config/kei/config.yaml",
		registry:     harness.NewRegistry(),
	}
}

func TestCheckRuntimeService(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		plist   []byte
		missing bool
		loaded  bool
		enabled bool
		want    string
		detail  string
	}{
		{name: "darwin healthy", goos: "darwin", loaded: true, want: doctorPass},
		{name: "darwin not installed", goos: "darwin", missing: true, loaded: true, want: doctorFail, detail: "not installed"},
		{name: "darwin not loaded", goos: "darwin", loaded: false, want: doctorFail, detail: "not loaded"},
		{name: "darwin no RunAtLoad", goos: "darwin", plist: []byte("<key>KeepAlive</key><true/>"), loaded: true, want: doctorFail, detail: "start at login"},
		{name: "linux healthy", goos: "linux", loaded: true, enabled: true, want: doctorPass},
		{name: "linux not enabled", goos: "linux", loaded: true, enabled: false, want: doctorFail, detail: "start at login"},
		{name: "unsupported platform", goos: "windows", want: doctorWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fakeDoctorProbes(t, nil)
			p.goos = tt.goos
			files := map[string][]byte{}
			if !tt.missing {
				plist := tt.plist
				if plist == nil {
					plist = []byte("<key>RunAtLoad</key>\n\t<true/>")
				}
				files[serviceDefinitionPathFor(tt.goos)] = plist
			}
			p.readFile = func(path string) ([]byte, error) {
				if b, ok := files[path]; ok {
					return b, nil
				}
				return nil, os.ErrNotExist
			}
			p.serviceLoaded = func(string, string) bool { return tt.loaded }
			p.serviceEnabled = func(string) bool { return tt.enabled }
			got := checkRuntimeService(p)
			if got.Status != tt.want || !strings.Contains(got.Detail, tt.detail) {
				t.Fatalf("got %s %q, want %s containing %q", got.Status, got.Detail, tt.want, tt.detail)
			}
			if got.Status == doctorFail && got.Fix != "kei runtime service install" {
				t.Fatalf("fix = %q", got.Fix)
			}
		})
	}
}

func TestCheckHeartbeat(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		service string
		detail  string
	}{
		{name: "darwin points at the log", goos: "darwin", service: doctorPass, detail: "/home/test/Library/Logs/kei-runtime.log"},
		{name: "linux points at journalctl", goos: "linux", service: doctorPass, detail: "journalctl --user -u kei"},
		{name: "service down", goos: "darwin", service: doctorFail, detail: "not running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fakeDoctorProbes(t, nil)
			p.goos = tt.goos
			got := checkHeartbeat(p, doctorResult{Status: tt.service})
			if got.Status != doctorWarn || !strings.Contains(got.Detail, tt.detail) || got.Fix != "kei runtime service status" {
				t.Fatalf("got %+v, want WARN containing %q", got, tt.detail)
			}
		})
	}
}

func TestCheckHarnessHooks(t *testing.T) {
	const settings = "/home/test/.claude/settings.json"
	const plugin = "/home/test/.config/opencode/plugins/kei-audit.js"
	jsonHook := map[string][]byte{settings: []byte(`{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"kei-proxy hook claude"}]}]}`)}
	fileHook := map[string][]byte{plugin: []byte("// managed by kei harness sync\nspawn(['kei-proxy','hook','opencode'])\n")}
	tests := []struct {
		name    string
		h       doctorHarness
		files   map[string][]byte
		want    string
		detail  string
		results int
	}{
		{name: "no harness detected", h: doctorHarness{kind: "claude_code", hook: jsonHook}, want: doctorPass, detail: "no harness"},
		{name: "detected harness without hook spec is skipped", h: doctorHarness{kind: "custom", detected: true}, want: doctorPass, detail: "no harness"},
		{name: "json hook installed", h: doctorHarness{kind: "claude_code", detected: true, hook: jsonHook},
			files: map[string][]byte{settings: []byte(`{"permissions":{},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"kei-proxy hook claude","timeout":5}]}]}}`)},
			want:  doctorPass, detail: "installed"},
		{name: "json config missing", h: doctorHarness{kind: "claude_code", detected: true, hook: jsonHook}, want: doctorFail, detail: "not readable"},
		{name: "json hook absent", h: doctorHarness{kind: "claude_code", detected: true, hook: jsonHook},
			files: map[string][]byte{settings: []byte(`{"hooks":{}}`)}, want: doctorFail, detail: "kei-proxy hook claude missing"},
		{name: "stale harness uuid hook", h: doctorHarness{kind: "claude_code", detected: true, hook: jsonHook},
			files: map[string][]byte{settings: []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"kei-proxy hook claude --harness 6f1c0b8e-0000-4000-8000-000000000001"}]}]}}`)},
			want:  doctorFail, detail: "stale --harness"},
		{name: "unparseable config", h: doctorHarness{kind: "codex", detected: true, hook: jsonHook},
			files: map[string][]byte{settings: []byte(`{`)}, want: doctorFail, detail: "cannot parse"},
		{name: "owned file current", h: doctorHarness{kind: "opencode", detected: true, hook: fileHook},
			files: map[string][]byte{plugin: fileHook[plugin]}, want: doctorPass},
		{name: "owned file out of date", h: doctorHarness{kind: "opencode", detected: true, hook: fileHook},
			files: map[string][]byte{plugin: []byte("// old plugin\n")}, want: doctorFail, detail: "out of date"},
		{name: "owned file with stale uuid", h: doctorHarness{kind: "opencode", detected: true, hook: fileHook},
			files: map[string][]byte{plugin: []byte("spawn(['kei-proxy','hook','opencode','--harness ','abc'])")}, want: doctorFail, detail: "stale --harness"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fakeDoctorProbes(t, tt.files)
			p.registry = harness.NewRegistry(tt.h)
			got := checkHarnessHooks(p)
			if len(got) != 1 {
				t.Fatalf("got %d results, want 1: %+v", len(got), got)
			}
			if got[0].Status != tt.want || !strings.Contains(got[0].Detail, tt.detail) {
				t.Fatalf("got %s %q, want %s containing %q", got[0].Status, got[0].Detail, tt.want, tt.detail)
			}
			if got[0].Status == doctorFail && got[0].Fix != "kei harness sync --harness "+tt.h.kind {
				t.Fatalf("fix = %q", got[0].Fix)
			}
		})
	}
}

func TestCheckPolicyBundle(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		err    error
		want   string
		detail string
	}{
		{name: "active", out: `{"state":"active","bundle_version":7,"not_after":"2026-10-11T12:00:00Z"}`, want: doctorPass, detail: "bundle 7 active, not_after 2026-10-11T12:00:00Z"},
		{name: "stale but valid", out: `{"state":"stale_but_valid","bundle_version":7,"not_after":"2026-10-11T12:00:00Z"}`, want: doctorWarn, detail: "stale_but_valid"},
		{name: "expired by state", out: `{"state":"expired","bundle_version":7,"not_after":"2026-10-09T12:00:00Z"}`, err: errors.New("exit status 1"), want: doctorFail, detail: "expired (not_after 2026-10-09T12:00:00Z)"},
		{name: "active but past not_after", out: `{"state":"active","bundle_version":7,"not_after":"2026-10-10T11:59:59Z"}`, want: doctorFail, detail: "expired"},
		{name: "cold", out: `{"state":"cold","policy_count":0,"tool_count":0}`, err: errors.New("exit status 1"), want: doctorFail, detail: "bundle state cold, not_after unknown"},
		{name: "proxy missing", err: errors.New("executable file not found"), want: doctorFail, detail: "cannot run /opt/kei-proxy policy show"},
		{name: "garbage output", out: "not json", want: doctorFail, detail: "no bundle state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fakeDoctorProbes(t, nil)
			var gotPath string
			var gotArgs []string
			p.runProxy = func(path string, env []string, args ...string) ([]byte, error) {
				gotPath, gotArgs = path, args
				return []byte(tt.out), tt.err
			}
			got := checkPolicyBundle(p)
			if got.Status != tt.want || !strings.Contains(got.Detail, tt.detail) {
				t.Fatalf("got %s %q, want %s containing %q", got.Status, got.Detail, tt.want, tt.detail)
			}
			if gotPath != "/opt/kei-proxy" || strings.Join(gotArgs, " ") != "policy show" {
				t.Fatalf("ran %s %v", gotPath, gotArgs)
			}
			if got.Fix != "kei-proxy policy sync" {
				t.Fatalf("fix = %q", got.Fix)
			}
		})
	}
}

func TestCheckPolicyBundleProxyLookup(t *testing.T) {
	p := fakeDoctorProbes(t, nil)
	p.loadConfig = func(string) (runtimeConfig, error) { return runtimeConfig{}, os.ErrNotExist }
	var gotPath string
	var gotEnv []string
	p.runProxy = func(path string, env []string, args ...string) ([]byte, error) {
		gotPath, gotEnv = path, env
		return nil, errors.New("not found")
	}
	checkPolicyBundle(p)
	if gotPath != "kei-proxy" || len(gotEnv) != 0 {
		t.Fatalf("ran %q with env %v, want the default proxy and no runtime env", gotPath, gotEnv)
	}

	p = fakeDoctorProbes(t, nil)
	p.runProxy = func(path string, env []string, args ...string) ([]byte, error) {
		gotEnv = env
		return []byte(`{"state":"active","not_after":"2026-10-11T12:00:00Z"}`), nil
	}
	checkPolicyBundle(p)
	if !strings.Contains(strings.Join(gotEnv, "\n"), "KEI_RUNTIME_TOKEN="+doctorTestToken) {
		t.Fatalf("runtime env not passed to kei-proxy: %v", len(gotEnv))
	}
}

func TestRunDoctor(t *testing.T) {
	t.Run("all pass exits 0", func(t *testing.T) {
		var out bytes.Buffer
		p := fakeDoctorProbes(t, nil)
		if code := runDoctor(p, &out); code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out.String())
		}
		for _, want := range []string{"PASS  runtime service:", "WARN  heartbeat:", "PASS  harness hooks:", "PASS  policy bundle: bundle 7 active"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("output missing %q:\n%s", want, out.String())
			}
		}
		if strings.Contains(out.String(), "fix: kei runtime service install") {
			t.Fatalf("fix printed for a passing check:\n%s", out.String())
		}
	})
	t.Run("any fail exits 1 with the fix", func(t *testing.T) {
		var out bytes.Buffer
		p := fakeDoctorProbes(t, nil)
		p.serviceLoaded = func(string, string) bool { return false }
		if code := runDoctor(p, &out); code != 1 {
			t.Fatalf("exit %d, want 1", code)
		}
		if !strings.Contains(out.String(), "FAIL  runtime service:") || !strings.Contains(out.String(), "fix: kei runtime service install") {
			t.Fatalf("output:\n%s", out.String())
		}
	})
	t.Run("never prints the runtime token", func(t *testing.T) {
		var out bytes.Buffer
		p := fakeDoctorProbes(t, nil)
		p.runProxy = func(string, []string, ...string) ([]byte, error) {
			return nil, errors.New("failed with " + doctorTestToken)
		}
		runDoctor(p, &out)
		if strings.Contains(out.String(), doctorTestToken) {
			t.Fatalf("token leaked:\n%s", out.String())
		}
	})
}

func TestDoctorRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDoctorCommand([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
