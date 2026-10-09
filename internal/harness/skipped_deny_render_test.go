package harness

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateSkippedDenyGolden = flag.Bool("update-skipped-deny", false, "rewrite testdata/harness/skipped-deny.golden.json")

// skippedDenyRender is the part of a render HAI-447 decides: what is allowed,
// what is denied, and which permits a skipped deny withholds.
type skippedDenyRender struct {
	Allows   []string `json:"allows"`
	Denies   []string `json:"denies"`
	Withheld []string `json:"withheld"`
}

// TestRenderWithholdsPermitsBelowSkippedDenies pins ADR-029 §4 (HAI-447): a
// person-sourced deny the renderer skips (v1 bundle, v2 without subject, or a
// source that does not name the subject) still blocks every overlapping
// lower-precedence permit, in every native renderer.
func TestRenderWithholdsPermitsBelowSkippedDenies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw, err := os.ReadFile(filepath.Join("testdata", "harness", "skipped-deny-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Subject BundleSubject `json:"subject"`
		Cases   []struct {
			Name        string          `json:"name"`
			Schema      string          `json:"schema"`
			WithSubject bool            `json:"with_subject"`
			Policies    json.RawMessage `json:"policies"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]skippedDenyRender{}
	for _, c := range fixture.Cases {
		b := Bundle{Schema: c.Schema, PolicySet: json.RawMessage(`{"policies":` + string(c.Policies) + `}`)}
		if c.WithSubject {
			subject := fixture.Subject
			b.Subject = &subject
		}
		got[c.Name] = map[string]skippedDenyRender{}
		for _, kind := range []string{"claude_code", "codex", "opencode"} {
			h, ok := Default.Get(kind)
			if !ok {
				t.Fatalf("%s not registered", kind)
			}
			r, err := h.Render(b)
			if err != nil {
				t.Fatalf("%s/%s: %v", c.Name, kind, err)
			}
			got[c.Name][kind] = skippedDenyRender{Allows: nonNil(r.Allows), Denies: nonNil(r.Denies), Withheld: nonNil(r.PermitsWithheld)}
		}
	}
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	goldenPath := filepath.Join("testdata", "harness", "skipped-deny.golden.json")
	if *updateSkippedDenyGolden {
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, golden) {
		t.Fatalf("render mismatch\ngot:\n%s\nwant:\n%s", out, golden)
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestDstOverlaps(t *testing.T) {
	for _, tc := range []struct {
		deny, permit string
		want         bool
	}{
		{"shell:git push", "shell:git", true},
		{"shell:git", "shell:git push", true},
		{"shell:git push", "shell:git status", false},
		{"shell:*", "shell:make", true},
		{"*", "skill:deploy", true},
		{"tool:claude_code.bash", "shell:make", true},
		{"tool:web_fetch", "shell:curl", false},
		{"skill:deploy", "skill:deploy", true},
		{"skill:deploy", "skill:lint", false},
		{"shell:git", "skill:git", false},
		{"path:/srv/**", "path:/srv/app/config", true},
		{"path:/srv/*/secrets", "path:/srv/app/secrets", true},
		{"path:/srv/app", "path:/home/dev", false},
		{"path:~/.ssh/**", "path:/home/dev/.ssh/id", true},
	} {
		if got := dstOverlaps(tc.deny, tc.permit); got != tc.want {
			t.Errorf("dstOverlaps(%q, %q)=%v want %v", tc.deny, tc.permit, got, tc.want)
		}
	}
}
