package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
)

func TestHarnessRendererGoldenConfigs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policySet := json.RawMessage(`{"policies":[{"id":"p1","src_pattern":"*","dst_pattern":"shell:git status","action":"permit","enabled":true},{"id":"p2","src_pattern":"*","dst_pattern":"shell:rm","action":"deny","enabled":true},{"id":"p3","src_pattern":"user:someone","dst_pattern":"shell:secret","action":"permit","enabled":true},{"id":"p4","src_pattern":"*","dst_pattern":"shell:*","action":"permit","enabled":true},{"id":"p5","src_pattern":"*","dst_pattern":"skill:my-skill","action":"permit","enabled":true}]}`)
	bundle := Bundle{PolicySet: policySet, Harnesses: []BundleHarness{{AgentID: "77777777-7777-7777-7777-777777777777", Kind: "claude_code"}, {AgentID: "88888888-8888-8888-8888-888888888888", Kind: "codex"}, {AgentID: "99999999-9999-9999-9999-999999999999", Kind: "opencode"}}}
	cases := []struct{ kind, id, input, golden, path string }{
		{"claude_code", bundle.Harnesses[0].AgentID, "claude.settings.input.json", "claude.settings.golden.json", filepath.Join(t.TempDir(), ".claude", "settings.json")},
		{"codex", bundle.Harnesses[1].AgentID, "codex.rules.input", "codex.rules.golden", filepath.Join(t.TempDir(), ".codex", "rules", "kei.rules")},
		{"opencode", bundle.Harnesses[2].AgentID, "opencode.input.json", "opencode.golden.json", filepath.Join(t.TempDir(), ".config", "opencode", "opencode.json")},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("testdata", "harness", tc.input))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "harness", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			got, err := renderNative(tc.kind, tc.id, bundle, map[string][]byte{tc.path: input})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(got.Files[tc.path]), bytes.TrimSpace(golden)) {
				t.Fatalf("rendered config mismatch\ngot:\n%s\nwant:\n%s", got.Files[tc.path], golden)
			}
			if len(got.AllowEntries[tc.path]) == 0 || len(got.DenyEntries[tc.path]) == 0 {
				t.Fatalf("managed allows/denies = %v / %v", got.AllowEntries[tc.path], got.DenyEntries[tc.path])
			}
		})
	}
}

func TestNativePermissionEntriesSharedHarnessFixtures(t *testing.T) {
	// FixturePath is the contracts package's exported fixture helper. The
	// contracts currently ships it from its test source, so resolve the same
	// testdata file through the module directory for this consumer test.
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/HaikeiLabs/kei-connector-contracts").Output()
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(strings.TrimSpace(string(module)), "harnessmatch", "testdata", "cases.v1.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Cases []struct {
			ID       string            `json:"id"`
			Call     harnessmatch.Call `json:"call"`
			Policies []struct {
				ID, Src, Dst, Action string
				Enabled              bool `json:"enabled"`
			} `json:"policies"`
			WantOutcome harnessmatch.Outcome `json:"want_outcome"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	for _, tc := range suite.Cases {
		if tc.WantOutcome != harnessmatch.OutcomePermit && tc.WantOutcome != harnessmatch.OutcomeDeny && tc.WantOutcome != harnessmatch.OutcomeUnmatched && tc.WantOutcome != harnessmatch.OutcomeNotRenderable {
			continue
		}
		if tc.ID != "shell-argv-prefix-permit" && tc.ID != "shell-deny-still-works" && tc.ID != "shell-star-permit-rejected" && tc.ID != "custom-kind-evaluate-via-pdp" {
			continue
		}
		policies := make([]bundlePolicy, 0, len(tc.Policies))
		for _, p := range tc.Policies {
			policies = append(policies, bundlePolicy{ID: p.ID, SrcPattern: p.Src, DstPattern: p.Dst, Action: p.Action, Effect: p.Action, Enabled: p.Enabled})
		}
		h := &BundleHarness{AgentID: tc.Call.AgentID, Kind: tc.Call.Kind}
		entries := nativePermissionEntries(syntaxFor(tc.Call.Kind), tc.Call.HarnessID, h, nil, policies)
		allows, denies := entries.Allows, entries.Denies
		switch tc.WantOutcome {
		case harnessmatch.OutcomePermit:
			if len(allows) == 0 {
				t.Errorf("%s: permit produced no allow", tc.ID)
			}
		case harnessmatch.OutcomeDeny:
			if len(denies) == 0 {
				t.Errorf("%s: deny produced no deny", tc.ID)
			}
		default:
			if len(allows)+len(denies) != 0 {
				t.Errorf("%s: %s rendered allow=%v deny=%v", tc.ID, tc.WantOutcome, allows, denies)
			}
		}
	}
}
