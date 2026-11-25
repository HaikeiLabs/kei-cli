package updatecheck

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.3.0", -1},
		{"0.3.0", "0.2.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"0.10.0", "0.9.9", 1},
		{"0.3.0-rc1", "0.3.0", -1},
		{"0.3.0", "0.3.0-rc1", 1},
	}
	for _, tt := range tests {
		if got := Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestStatusNotice(t *testing.T) {
	tests := []struct {
		name      string
		installed Versions
		latest    Versions
		want      string
	}{
		{name: "newer kei", installed: Versions{Kei: "0.2.0", Proxy: "0.9.0"}, latest: Versions{Kei: "0.3.0", Proxy: "0.9.0"},
			want: "kei 0.3.0 is available (you have 0.2.0) — run kei upgrade"},
		{name: "newer kei and kei-proxy", installed: Versions{Kei: "0.2.0", Proxy: "0.9.0"}, latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"},
			want: "kei 0.3.0 is available (you have 0.2.0; kei-proxy 0.9.0 -> 0.12.0) — run kei upgrade"},
		{name: "only bundled kei-proxy newer", installed: Versions{Kei: "0.3.0", Proxy: "0.9.0"}, latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"},
			want: "kei-proxy 0.12.0 is available (you have 0.9.0) — run kei upgrade"},
		{name: "up to date", installed: Versions{Kei: "0.3.0", Proxy: "0.12.0"}, latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
		{name: "running newer than latest", installed: Versions{Kei: "9.9.9"}, latest: Versions{Kei: "0.3.0"}},
		{name: "dev build never nags", installed: Versions{Kei: "dev"}, latest: Versions{Kei: "0.3.0"}},
		{name: "unknown latest", installed: Versions{Kei: "0.2.0"}},
		{name: "no installed kei-proxy", installed: Versions{Kei: "0.3.0"}, latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Status{Installed: tt.installed, Latest: tt.latest}.Notice()
			if got != tt.want {
				t.Fatalf("Notice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusSummary(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   string
	}{
		{name: "both known", status: Status{Installed: Versions{Kei: "0.2.0", Proxy: "0.9.0"}, Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
			want: "kei 0.2.0 installed, 0.3.0 latest; kei-proxy 0.9.0 installed, 0.12.0 latest"},
		{name: "proxy missing", status: Status{Installed: Versions{Kei: "0.3.0"}, Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
			want: "kei 0.3.0 installed, 0.3.0 latest; kei-proxy not found, 0.12.0 latest"},
		{name: "pin unknown", status: Status{Installed: Versions{Kei: "0.3.0", Proxy: "0.12.0"}, Latest: Versions{Kei: "0.3.0"}},
			want: "kei 0.3.0 installed, 0.3.0 latest; kei-proxy 0.12.0 installed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckerUsesFreshCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteState(path, State{CheckedAt: now.Add(-time.Hour), Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}}); err != nil {
		t.Fatal(err)
	}
	c := Checker{StatePath: path, Now: func() time.Time { return now }, Resolve: func(context.Context) (Versions, error) {
		t.Fatal("Resolve called with a fresh cache")
		return Versions{}, nil
	}}
	got, ok := c.Latest(context.Background())
	if !ok || got != (Versions{Kei: "0.3.0", Proxy: "0.12.0"}) {
		t.Fatalf("Latest() = %+v, %v", got, ok)
	}
}

func TestCheckerRefreshesStaleCacheOncePerInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteState(path, State{CheckedAt: now.Add(-25 * time.Hour), Latest: Versions{Kei: "0.2.0"}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := Checker{StatePath: path, Now: func() time.Time { return now }, Resolve: func(context.Context) (Versions, error) {
		calls++
		return Versions{Kei: "0.3.0", Proxy: "0.12.0"}, nil
	}}
	for range 2 {
		got, ok := c.Latest(context.Background())
		if !ok || got.Kei != "0.3.0" {
			t.Fatalf("Latest() = %+v, %v", got, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("Resolve called %d times, want 1 (cached for a day)", calls)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCheckerOfflineIsSilentAndBacksOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	calls := 0
	var debug bytes.Buffer
	c := Checker{StatePath: path, Now: func() time.Time { return now }, Resolve: func(context.Context) (Versions, error) {
		calls++
		return Versions{}, errors.New("dial tcp: no route to host")
	}, Debug: func(format string, args ...any) { debug.WriteString(format) }}
	if _, ok := c.Latest(context.Background()); ok {
		t.Fatal("Latest() ok with no network and no cache")
	}
	if _, ok := c.Latest(context.Background()); ok {
		t.Fatal("Latest() ok on second offline call")
	}
	if calls != 1 {
		t.Fatalf("Resolve called %d times offline, want 1 (the failed attempt is cached)", calls)
	}
	if debug.Len() == 0 {
		t.Fatal("failure was not sent to the debug log")
	}
}

func TestCheckerKeepsLastKnownLatestWhenRefreshFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteState(path, State{CheckedAt: now.Add(-48 * time.Hour), Latest: Versions{Kei: "0.3.0"}}); err != nil {
		t.Fatal(err)
	}
	c := Checker{StatePath: path, Now: func() time.Time { return now }, Resolve: func(context.Context) (Versions, error) {
		return Versions{}, errors.New("offline")
	}}
	got, ok := c.Latest(context.Background())
	if !ok || got.Kei != "0.3.0" {
		t.Fatalf("Latest() = %+v, %v; want the last known release", got, ok)
	}
}

func TestCheckerNoticeOncePerInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteState(path, State{CheckedAt: now, Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}}); err != nil {
		t.Fatal(err)
	}
	c := Checker{StatePath: path, Now: func() time.Time { return now }, Resolve: func(context.Context) (Versions, error) {
		return Versions{}, errors.New("unused")
	}}
	installed := func() Versions { return Versions{Kei: "0.2.0", Proxy: "0.12.0"} }
	if got := c.Notice(context.Background(), installed); got != "kei 0.3.0 is available (you have 0.2.0) — run kei upgrade" {
		t.Fatalf("first Notice() = %q", got)
	}
	if got := c.Notice(context.Background(), installed); got != "" {
		t.Fatalf("second Notice() within a day = %q, want none", got)
	}
	later := now.Add(25 * time.Hour)
	c.Now = func() time.Time { return later }
	c.Resolve = func(context.Context) (Versions, error) { return Versions{Kei: "0.3.0"}, nil }
	if got := c.Notice(context.Background(), installed); got == "" {
		t.Fatal("Notice() a day later printed nothing")
	}
}

func TestCheckerNoticeSkipsInstalledProbeWhenAlreadyNotified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteState(path, State{CheckedAt: now, NotifiedAt: now, Latest: Versions{Kei: "0.3.0"}}); err != nil {
		t.Fatal(err)
	}
	c := Checker{StatePath: path, Now: func() time.Time { return now }}
	got := c.Notice(context.Background(), func() Versions {
		t.Fatal("installed versions probed although the notice was already shown today")
		return Versions{}
	})
	if got != "" {
		t.Fatalf("Notice() = %q", got)
	}
}

func TestSuppressed(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name string
		opts Options
		want bool
	}{
		{name: "interactive", opts: Options{Getenv: env(nil), StdoutTTY: true, Args: []string{"list"}}, want: false},
		{name: "env disables", opts: Options{Getenv: env(map[string]string{"KEI_NO_UPDATE_CHECK": "1"}), StdoutTTY: true}, want: true},
		{name: "env 0 keeps it on", opts: Options{Getenv: env(map[string]string{"KEI_NO_UPDATE_CHECK": "0"}), StdoutTTY: true}, want: false},
		{name: "config disables", opts: Options{Getenv: env(nil), ConfigDisabled: true, StdoutTTY: true}, want: true},
		{name: "stdout not a TTY", opts: Options{Getenv: env(nil), StdoutTTY: false}, want: true},
		{name: "json output", opts: Options{Getenv: env(nil), StdoutTTY: true, Args: []string{"list", "--json"}}, want: true},
		{name: "json=true output", opts: Options{Getenv: env(nil), StdoutTTY: true, Args: []string{"-json=true"}}, want: true},
		{name: "quiet output", opts: Options{Getenv: env(nil), StdoutTTY: true, Args: []string{"--quiet"}}, want: true},
		{name: "after --", opts: Options{Getenv: env(nil), StdoutTTY: true, Args: []string{"--", "--json"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Suppressed(tt.opts); got != tt.want {
				t.Fatalf("Suppressed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBelowMinimum(t *testing.T) {
	got := BelowMinimum([]Installation{
		{Name: "laptop", Version: "0.12.0"},
		{Name: "openwebui", Version: "0.10.1", BelowMinimum: true},
		{Name: "never-heartbeat", BelowMinimum: true},
		{Name: "teams", Version: "0.9.0", BelowMinimum: true},
	})
	if len(got) != 2 || got[0].Name != "openwebui" || got[1].Name != "teams" {
		t.Fatalf("BelowMinimum() = %+v", got)
	}
}

func TestWriteLoginSummary(t *testing.T) {
	t.Run("up to date, all runtimes current", func(t *testing.T) {
		var buf bytes.Buffer
		WriteLoginSummary(&buf, LoginSummary{
			Status:     Status{Installed: Versions{Kei: "0.3.0", Proxy: "0.12.0"}, Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
			HaveLatest: true, HaveInstallations: true,
		})
		want := "Versions: kei 0.3.0 installed, 0.3.0 latest; kei-proxy 0.12.0 installed, 0.12.0 latest (up to date)\n" +
			"Runtime installations: none below the org minimum runtime version\n"
		if buf.String() != want {
			t.Fatalf("got:\n%s\nwant:\n%s", buf.String(), want)
		}
	})
	t.Run("stale CLI and runtimes below the minimum", func(t *testing.T) {
		var buf bytes.Buffer
		WriteLoginSummary(&buf, LoginSummary{
			Status:     Status{Installed: Versions{Kei: "0.2.0", Proxy: "0.9.0"}, Latest: Versions{Kei: "0.3.0", Proxy: "0.12.0"}},
			HaveLatest: true, HaveInstallations: true, OrgMinimum: "0.12.0",
			BelowMinimum: []Installation{{Name: "openwebui", Version: "0.10.1", BelowMinimum: true}},
		})
		out := buf.String()
		for _, want := range []string{
			"Versions: kei 0.2.0 installed, 0.3.0 latest; kei-proxy 0.9.0 installed, 0.12.0 latest\n",
			"kei 0.3.0 is available (you have 0.2.0; kei-proxy 0.9.0 -> 0.12.0) — run kei upgrade\n",
			"1 runtime installation is below the org minimum runtime version (0.12.0):\n",
			"  openwebui: kei-proxy 0.10.1, needs 0.12.0 or later; missing: bundle features gated above its version are left out of its policy bundle\n",
			"  Upgrade: run kei upgrade on each runtime host (container runtimes: deploy the new kei-proxy image), then restart kei-proxy.\n",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("output missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("org minimum unknown", func(t *testing.T) {
		var buf bytes.Buffer
		WriteLoginSummary(&buf, LoginSummary{
			HaveInstallations: true,
			BelowMinimum:      []Installation{{Name: "a", Version: "0.1.0", BelowMinimum: true}, {Name: "b", Version: "0.2.0", BelowMinimum: true}},
		})
		out := buf.String()
		if strings.Contains(out, "Versions:") {
			t.Fatalf("printed a version line without a known latest release:\n%s", out)
		}
		for _, want := range []string{
			"2 runtime installations are below the org minimum runtime version:\n",
			"  a: kei-proxy 0.1.0, needs the org minimum; missing:",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("output missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("nothing known prints nothing", func(t *testing.T) {
		var buf bytes.Buffer
		WriteLoginSummary(&buf, LoginSummary{})
		if buf.Len() != 0 {
			t.Fatalf("got %q, want nothing", buf.String())
		}
	})
}
