package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No Kei-managed entries: an existing OpenCode config is left byte-for-byte
// untouched; a non-existent config is created as an empty object so Kei can
// put it under management.
func TestOpencodeRenderNoEntriesLeavesConfigUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bundle := Bundle{PolicySet: json.RawMessage(`{"policies":[]}`), Harnesses: []BundleHarness{{AgentID: "99999999-9999-9999-9999-999999999999", Kind: "opencode"}}}
	id := "99999999-9999-9999-9999-999999999999"

	userCfg := []byte(`{"model":"anthropic/claude-sonnet","permission":{"bash":{"npm":"allow"}}}`)
	path := filepath.Join(t.TempDir(), ".config", "opencode", "opencode.json")
	got, err := renderNative("opencode", id, bundle, map[string][]byte{path: userCfg})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Files[path], userCfg) {
		t.Fatalf("config changed with no entries:\n%s", got.Files[path])
	}

	// When no config exists, a minimal empty object is rendered so sync
	// creates the file and puts it under Kei management.
	emptyPath := filepath.Join(t.TempDir(), ".config", "opencode", "opencode.json")
	got2, err := renderNative("opencode", id, bundle, map[string][]byte{emptyPath: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(got2.Files[emptyPath]) != "{}\n" {
		t.Fatalf("expected empty config, got:\n%s", got2.Files[emptyPath])
	}
}

// A user-defined "*" catch-all is preserved (never overridden); Kei-managed
// entries are still added alongside it.
func TestOpencodeRenderPreservesUserCatchAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet := json.RawMessage(`{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","action":"permit","enabled":true}]}`)
	bundle := Bundle{PolicySet: policySet, Harnesses: []BundleHarness{{AgentID: "99999999-9999-9999-9999-999999999999", Kind: "opencode"}}}
	id := "99999999-9999-9999-9999-999999999999"
	userCfg := []byte(`{"permission":{"bash":{"*":"allow"}}}`)
	path := filepath.Join(t.TempDir(), ".config", "opencode", "opencode.json")
	got, err := renderNative("opencode", id, bundle, map[string][]byte{path: userCfg})
	if err != nil {
		t.Fatal(err)
	}
	out := string(got.Files[path])
	if !strings.Contains(out, `"*": "allow"`) {
		t.Fatalf("user catch-all not preserved:\n%s", out)
	}
	if !strings.Contains(out, `"git status": "allow"`) {
		t.Fatalf("managed entry missing:\n%s", out)
	}
}

// TestOpencodeConfigPath verifies that opencodeConfigPath resolves configs in
// the correct XDG-aware order.
func TestOpencodeConfigPath(t *testing.T) {
	type env struct{ name, value string }

	cases := []struct {
		name  string
		envs  []env
		chdir string   // relative to root
		files []string // relative to root; file content is irrelevant (empty)
		dirs  []string // relative to root; directories to create
		// wantPath is relative to root; empty means want default ~/.config/opencode/opencode.json
		wantPath   string
		wantExists bool
	}{
		{
			name:       "OPENCODE_CONFIG env var",
			envs:       []env{{"OPENCODE_CONFIG", "custom.json"}},
			files:      []string{"custom.json"},
			wantPath:   "custom.json",
			wantExists: true,
		},
		{
			name:       "OPENCODE_CONFIG missing file",
			envs:       []env{{"OPENCODE_CONFIG", "missing.json"}},
			wantPath:   "missing.json",
			wantExists: false,
		},
		{
			name:       "XDG_CONFIG_HOME opencode.json",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode"},
			files:      []string{"xdgconf/opencode/opencode.json"},
			wantPath:   "xdgconf/opencode/opencode.json",
			wantExists: true,
		},
		{
			name:       "XDG_CONFIG_HOME opencode.jsonc",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode"},
			files:      []string{"xdgconf/opencode/opencode.jsonc"},
			wantPath:   "xdgconf/opencode/opencode.jsonc",
			wantExists: true,
		},
		{
			name:       "XDG_CONFIG_HOME prefers json over jsonc",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode"},
			files:      []string{"xdgconf/opencode/opencode.json", "xdgconf/opencode/opencode.jsonc"},
			wantPath:   "xdgconf/opencode/opencode.json",
			wantExists: true,
		},
		{
			name:       "default HOME .config opencode.json",
			envs:       []env{},
			dirs:       []string{"home/.config/opencode"},
			files:      []string{"home/.config/opencode/opencode.json"},
			wantPath:   "home/.config/opencode/opencode.json",
			wantExists: true,
		},
		{
			name:       "default HOME .config opencode.jsonc",
			envs:       []env{},
			dirs:       []string{"home/.config/opencode"},
			files:      []string{"home/.config/opencode/opencode.jsonc"},
			wantPath:   "home/.config/opencode/opencode.jsonc",
			wantExists: true,
		},
		{
			name:       "project level opencode.json",
			envs:       []env{},
			dirs:       []string{"project"},
			files:      []string{"project/opencode.json"},
			chdir:      "project",
			wantPath:   "project/opencode.json",
			wantExists: true,
		},
		{
			name:       "project level opencode.jsonc",
			envs:       []env{},
			dirs:       []string{"project"},
			files:      []string{"project/opencode.jsonc"},
			chdir:      "project",
			wantPath:   "project/opencode.jsonc",
			wantExists: true,
		},
		{
			name:       "project walk-up to git root",
			envs:       []env{},
			dirs:       []string{"repo/subdir"},
			files:      []string{"repo/opencode.json"},
			chdir:      "repo/subdir",
			wantPath:   "repo/opencode.json",
			wantExists: true,
		},
		{
			name:       "project stops at git root",
			envs:       []env{},
			dirs:       []string{"repo/subdir", "parent", "repo/.git"},
			files:      []string{"parent/opencode.json"},
			chdir:      "repo/subdir",
			wantPath:   "",
			wantExists: false,
		},
		{
			name:       "project stops at filesystem root",
			envs:       []env{},
			dirs:       []string{"subdir"},
			chdir:      "subdir",
			wantPath:   "",
			wantExists: false,
		},
		{
			name:       "XDG_CONFIG_HOME preferred over HOME default",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode", "home/.config/opencode"},
			files:      []string{"xdgconf/opencode/opencode.json", "home/.config/opencode/opencode.json"},
			wantPath:   "xdgconf/opencode/opencode.json",
			wantExists: true,
		},
		{
			name:       "OPENCODE_CONFIG overrides XDG and default",
			envs:       []env{{"OPENCODE_CONFIG", "override.json"}, {"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode"},
			files:      []string{"override.json", "xdgconf/opencode/opencode.json"},
			wantPath:   "override.json",
			wantExists: true,
		},
		{
			name:       "no config exists returns default path",
			envs:       []env{},
			dirs:       []string{},
			wantPath:   "",
			wantExists: false,
		},
		{
			name:       "XDG_CONFIG_HOME set but no config returns XDG default path",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode"},
			wantPath:   "xdgconf/opencode/opencode.json",
			wantExists: false,
		},
		{
			name:       "project config found when XDG and HOME have none",
			envs:       []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			dirs:       []string{"xdgconf/opencode", "project"},
			files:      []string{"project/opencode.json"},
			chdir:      "project",
			wantPath:   "project/opencode.json",
			wantExists: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			for _, d := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.files {
				dir := filepath.Dir(filepath.Join(root, f))
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, f), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("HOME", filepath.Join(root, "home"))
			// Always clear XDG_CONFIG_HOME and OPENCODE_CONFIG first so no
			// system env leaks in; the test case override restores it.
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("OPENCODE_CONFIG", "")
			for _, e := range tc.envs {
				t.Setenv(e.name, filepath.Join(root, e.value))
			}

			if tc.chdir != "" {
				t.Chdir(filepath.Join(root, tc.chdir))
			} else {
				t.Chdir(root)
			}

			got, exists := opencodeConfigPath(OSEnv())
			var want string
			if tc.wantPath != "" {
				want = filepath.Join(root, tc.wantPath)
			} else {
				// Default path when none exists
				want = filepath.Join(root, "home", ".config", "opencode", "opencode.json")
			}
			if got != want {
				t.Errorf("opencodeConfigPath(OSEnv()) path = %q, want %q", got, want)
			}
			if exists != tc.wantExists {
				t.Errorf("opencodeConfigPath(OSEnv()) exists = %v, want %v", exists, tc.wantExists)
			}
		})
	}
}

func TestOpencodeConfigDir(t *testing.T) {
	type env struct{ name, value string }

	cases := []struct {
		name string
		envs []env
		want func(root string) string
	}{
		{
			name: "OPENCODE_CONFIG sets dir",
			envs: []env{{"OPENCODE_CONFIG", "some/path/opencode.json"}},
			want: func(root string) string { return filepath.Join(root, "some", "path") },
		},
		{
			name: "XDG_CONFIG_HOME",
			envs: []env{{"XDG_CONFIG_HOME", "xdgconf"}},
			want: func(root string) string { return filepath.Join(root, "xdgconf", "opencode") },
		},
		{
			name: "HOME default",
			envs: []env{},
			want: func(root string) string { return filepath.Join(root, "home", ".config", "opencode") },
		},
		{
			name: "OPENCODE_CONFIG overrides XDG",
			envs: []env{{"OPENCODE_CONFIG", "override.json"}, {"XDG_CONFIG_HOME", "xdgconf"}},
			want: func(root string) string { return filepath.Dir(filepath.Join(root, "override.json")) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("OPENCODE_CONFIG", "")
			for _, e := range tc.envs {
				t.Setenv(e.name, filepath.Join(root, e.value))
			}
			got := opencodeConfigDir(OSEnv())
			want := tc.want(root)
			if got != want {
				t.Errorf("opencodeConfigDir(OSEnv()) = %q, want %q", got, want)
			}
		})
	}
}

func TestOpencodeConfigName(t *testing.T) {
	names := opencodeConfigName()
	if len(names) < 2 {
		t.Fatalf("opencodeConfigName() = %v, want at least [opencode.json, opencode.jsonc]", names)
	}
	if names[0] != "opencode.json" {
		t.Errorf("opencodeConfigName()[0] = %q, want %q", names[0], "opencode.json")
	}
	if names[1] != "opencode.jsonc" {
		t.Errorf("opencodeConfigName()[1] = %q, want %q", names[1], "opencode.jsonc")
	}
}

// With no --file, import reads opencode.json from the env's config dir, which
// honours XDG_CONFIG_HOME.
func TestOpenCodeImportRulesDefaultsToEnvConfigDir(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"permission":{"bash":{"git *":"allow","rm":"ask"},"webfetch":"deny"}}`
	if err := os.WriteFile(filepath.Join(xdg, "opencode", "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := openCode{}.ImportRules(hermeticEnv(home, map[string]string{"XDG_CONFIG_HOME": xdg}), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Rule{}
	for _, rule := range rules {
		got[rule.Dst] = rule
	}
	if len(got) != 2 || got["shell:git "].Effect != "permit" || got["tool:webfetch"].Effect != "deny" {
		t.Fatalf("rules = %+v", rules)
	}
}
