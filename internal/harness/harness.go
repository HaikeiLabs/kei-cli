// Package harness renders Kei harness command policy bundles into each coding
// harness's native permission store. Every harness kind is one file that
// implements Harness; the Registry is how the kei CLI finds them.
package harness

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Harness is one native harness kind: where its native store lives, how a
// policy bundle maps onto it, and how the rendered entries reach it.
type Harness interface {
	// Kind is the harness kind as the control plane names it (claude_code).
	Kind() string
	// Detect reports whether this harness is installed in env.
	Detect(env Env) bool
	// Targets lists the native stores the harness renders into: local files
	// or a remote API.
	Targets(env Env) ([]Target, error)
	// Render maps a policy bundle onto native entries. It does no I/O.
	Render(b Bundle) (Rendered, error)
	// Apply writes rendered entries to the targets, backing up what it
	// replaces. With DryRun it prints the planned changes and writes nothing.
	Apply(ctx context.Context, r Rendered, opts ApplyOpts) (Result, error)
	// HookSpec is the report-only audit hook the harness installs (HP-C6), or
	// nil when it has none.
	HookSpec(env Env) *HookSpec
	// ImportRules reads native allow/deny rules from path, or from the
	// harness's default location when path is empty.
	ImportRules(env Env, path string) ([]Rule, error)
}

// ImportNamer is implemented by a harness whose native rules can be imported
// with `kei policies import --from NAME`.
type ImportNamer interface {
	ImportName() string
}

// ErrNoNativeStore is returned by a harness that has nothing native to read
// or write (the custom kind; authorization is enforced by kei-proxy).
var ErrNoNativeStore = errors.New("harness has no native permission store")

// Env is the process environment a harness resolves paths from. Tests build
// one by hand so nothing reads the real home directory.
type Env struct {
	Home    string
	HomeErr error
	Getenv  func(string) string
	Getwd   func() (string, error)
}

// OSEnv is the environment of the running process.
func OSEnv() Env {
	home, err := os.UserHomeDir()
	return Env{Home: home, HomeErr: err, Getenv: os.Getenv, Getwd: os.Getwd}
}

func (e Env) home() (string, error) { return e.Home, e.HomeErr }

func (e Env) getenv(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// XDGConfigHome is $XDG_CONFIG_HOME, or "" when it is unset.
func (e Env) XDGConfigHome() string { return e.getenv("XDG_CONFIG_HOME") }

func (e Env) getwd() (string, error) {
	if e.Getwd == nil {
		return "", errors.New("working directory unavailable")
	}
	return e.Getwd()
}

// Target is one native store a harness renders into. Exactly one of Path (a
// local file) or URL (a remote API, such as an admin endpoint) is set.
type Target struct {
	Path string
	URL  string
}

// Remote reports whether the target is a remote API rather than a file.
func (t Target) Remote() bool { return t.URL != "" }

func fileTargets(paths ...string) []Target {
	targets := make([]Target, len(paths))
	for i, path := range paths {
		targets[i] = Target{Path: path}
	}
	return targets
}

// Rendered is a policy bundle mapped onto one harness's native entries.
type Rendered struct {
	Kind   string
	Allows []string
	Denies []string
	// NotEnforceable names the policies that apply to this harness but have
	// no native equivalent (for example skill: and tool: policies in Codex).
	NotEnforceable []string
	// DecidedLive names the policies that apply to a harness with no native
	// allowlist, where kei-proxy decides every call at run time (openwebui).
	DecidedLive []string
	// ScopedElsewhere counts the policies whose src names a different
	// harness; ScopedElsewhereExample is one such src for the sync hint.
	ScopedElsewhere        int
	ScopedElsewhereExample string
	// PersonSourcesSkipped counts the user:/email:/group: policies a v1
	// bundle could not render: only v2 carries the subject they need.
	PersonSourcesSkipped int
	// PermitsWithheld names the permits not rendered because a
	// higher-precedence deny the renderer skipped (a user:/email:/group:
	// source it cannot resolve) overlaps them; the harness asks instead.
	PermitsWithheld []string
	// The bundle identity, kept so Apply can refuse a rollback.
	BundleVersion int64
	BundleDigest  string
	NotAfter      time.Time
}

// ApplyOpts controls one Apply.
type ApplyOpts struct {
	Env    Env
	DryRun bool
	// Out receives the planned or applied changes; it must not be nil.
	Out io.Writer
	// Now stamps backups.
	Now time.Time
}

// Result is what an Apply did beyond the changes it printed.
type Result struct {
	// Skipped is set when the harness has no native store: nothing was
	// written and the sync is not reported to the control plane.
	Skipped bool
	// Notes are printed to stdout after the sync is reported.
	Notes []string
	// Warnings are printed to stderr before the sync is reported.
	Warnings []string
}

// HookSpec is the audit hook a harness installs: the documents merged into
// (or written as) each target file.
type HookSpec struct {
	Files map[string][]byte
}

// Rule is one native rule read by ImportRules.
type Rule struct {
	Name   string
	Dst    string
	Effect string
	// Native and SkipReason are set instead of Dst/Effect for an entry that
	// has no Kei equivalent.
	Native     string
	SkipReason string
}

// Registry holds the harness kinds the CLI knows, in registration order.
type Registry struct {
	harnesses []Harness
}

// NewRegistry returns a registry holding hs.
func NewRegistry(hs ...Harness) *Registry {
	r := &Registry{}
	for _, h := range hs {
		r.Register(h)
	}
	return r
}

// Default is the registry of built-in harnesses. A new harness kind is one
// file plus one entry here.
var Default = NewRegistry(claudeCode{}, codex{}, openCode{}, custom{}, OpenWebUI{})

// Register adds h, replacing any harness of the same kind. A Registry is not
// safe for concurrent registration; register at startup.
func (r *Registry) Register(h Harness) {
	if h == nil {
		panic("harness: Register of a nil Harness")
	}
	for i, existing := range r.harnesses {
		if existing.Kind() == h.Kind() {
			r.harnesses[i] = h
			return
		}
	}
	r.harnesses = append(r.harnesses, h)
}

// Get returns the harness of the given kind.
func (r *Registry) Get(kind string) (Harness, bool) {
	for _, h := range r.harnesses {
		if h.Kind() == kind {
			return h, true
		}
	}
	return nil, false
}

// All returns every registered harness in registration order.
func (r *Registry) All() []Harness {
	return append([]Harness(nil), r.harnesses...)
}

// Kinds returns every registered kind in registration order.
func (r *Registry) Kinds() []string {
	kinds := make([]string, len(r.harnesses))
	for i, h := range r.harnesses {
		kinds[i] = h.Kind()
	}
	return kinds
}

// Detected returns the harnesses installed in env, in registration order. It
// drives `kei harness sync` when no --harness KIND is given (HP-C11).
func (r *Registry) Detected(env Env) []Harness {
	var found []Harness
	for _, h := range r.harnesses {
		if h.Detect(env) {
			found = append(found, h)
		}
	}
	return found
}

// Importer returns the harness whose ImportName is name.
func (r *Registry) Importer(name string) (Harness, bool) {
	for _, h := range r.harnesses {
		if n, ok := h.(ImportNamer); ok && n.ImportName() == name {
			return h, true
		}
	}
	return nil, false
}

// ImportNames returns the import names of every importable harness.
func (r *Registry) ImportNames() []string {
	var names []string
	for _, h := range r.harnesses {
		if n, ok := h.(ImportNamer); ok {
			names = append(names, n.ImportName())
		}
	}
	return names
}

// JoinOr formats names as "a, b, or c" for usage text.
func JoinOr(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ledgerDir holds the local sync ledgers, one per harness kind.
func ledgerDir(env Env) string {
	return filepath.Join(env.Home, ".config", "kei", "harness-sync")
}
