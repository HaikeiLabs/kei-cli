package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hermeticEnv is an Env rooted at home with the given variables and no
// working directory, so nothing reads the real machine.
func hermeticEnv(home string, vars map[string]string) Env {
	return Env{
		Home:   home,
		Getenv: func(key string) string { return vars[key] },
		Getwd:  func() (string, error) { return home, nil },
	}
}

func TestDefaultRegistryOrderAndLookup(t *testing.T) {
	if got := strings.Join(Default.Kinds(), ","); got != "claude_code,codex,opencode,custom" {
		t.Fatalf("kinds = %s", got)
	}
	if got := JoinOr(Default.Kinds()); got != "claude_code, codex, opencode, or custom" {
		t.Fatalf("JoinOr(kinds) = %q", got)
	}
	if got := strings.Join(Default.ImportNames(), "|"); got != "claude|codex|opencode" {
		t.Fatalf("import names = %s", got)
	}
	if h, ok := Default.Importer("claude"); !ok || h.Kind() != "claude_code" {
		t.Fatalf("Importer(claude) = %v, %v", h, ok)
	}
	if _, ok := Default.Importer("custom"); ok {
		t.Fatal("custom harness must not be importable")
	}
	if _, ok := Default.Get("pi"); ok {
		t.Fatal("unregistered kind resolved")
	}
}

func TestRegisterReplacesSameKind(t *testing.T) {
	r := NewRegistry(claudeCode{}, codex{})
	r.Register(codex{})
	if got := strings.Join(r.Kinds(), ","); got != "claude_code,codex" {
		t.Fatalf("kinds after re-register = %s", got)
	}
}

// Detection reads only the Env it is given: the real HOME is never consulted.
func TestDetectedUsesOnlyEnv(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".codex", filepath.Join("xdg", "opencode")} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")})
	var kinds []string
	for _, h := range Default.Detected(env) {
		kinds = append(kinds, h.Kind())
	}
	if got := strings.Join(kinds, ","); got != "codex,opencode" {
		t.Fatalf("detected = %s", got)
	}
	if got := Default.Detected(Env{HomeErr: os.ErrNotExist}); got != nil {
		t.Fatalf("detected without a home = %v", got)
	}
}

func TestTargetsAreLocalFiles(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	for _, h := range Default.All() {
		targets, err := h.Targets(env)
		if err != nil {
			t.Fatalf("%s targets: %v", h.Kind(), err)
		}
		for _, target := range targets {
			if target.Remote() || !strings.HasPrefix(target.Path, home) {
				t.Errorf("%s target %+v is not a file under the env home", h.Kind(), target)
			}
		}
	}
}
