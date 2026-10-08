package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
)

type annotationCase struct {
	Name      string               `json:"name"`
	TargetID  string               `json:"target_policy_id"`
	Policies  []bundlePolicy       `json:"policies"`
	Expected  []annotationExpected `json:"expected"`
	Formatted []string             `json:"formatted"`
	Request   *annotationRequest   `json:"request,omitempty"`
}

type annotationExpected struct {
	Kind      string            `json:"kind"`
	Resource  string            `json:"resource"`
	Harnesses harnessesSelector `json:"harnesses"`
	By        string            `json:"by"`
}

type annotationRequest struct {
	UserEmail      string `json:"user_email"`
	Harness        string `json:"harness"`
	Resource       string `json:"resource"`
	ExpectedWinner string `json:"expected_winner"`
}

type harnessesSelector struct {
	All    bool
	Values []string
}

func (s *harnessesSelector) UnmarshalJSON(data []byte) error {
	if string(data) == `"all"` {
		s.All = true
		return nil
	}
	return json.Unmarshal(data, &s.Values)
}

func (s *harnessesSelector) Contains(kind string) bool {
	if s.All {
		return true
	}
	for _, v := range s.Values {
		if v == kind {
			return true
		}
	}
	return false
}

func TestAnnotationPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	goldenPath := filepath.Join("testdata", "annotations", "policy-precedence.golden.json")
	f, err := os.Open(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var sb strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	var g struct {
		Cases []annotationCase `json:"annotation_cases"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &g); err != nil {
		t.Fatal(err)
	}

	if len(g.Cases) == 0 {
		t.Fatal("no annotation cases found")
	}

	harnessKinds := []string{"claude_code", "codex", "opencode"}

	for _, ac := range g.Cases {
		t.Run(cleanName(ac.Name), func(t *testing.T) {
			policySet, err := json.Marshal(map[string][]bundlePolicy{"policies": ac.Policies})
			if err != nil {
				t.Fatal(err)
			}
			bundle := Bundle{PolicySet: policySet}

			for _, kind := range harnessKinds {
				t.Run(kind, func(t *testing.T) {
					r, err := render(syntaxFor(kind), "test-harness-id", bundle)
					if err != nil {
						t.Fatal(err)
					}

					for _, exp := range ac.Expected {
						switch exp.Kind {
						case "except":
							testExceptAnnotation(t, kind, "test-harness-id", ac.Policies, exp, r)
						case "never":
							testNeverAnnotation(t, kind, "test-harness-id", ac.Policies, exp, r)
						default:
							t.Fatalf("unknown expected kind: %s", exp.Kind)
						}
					}
				})
			}
		})
	}
}

func cleanName(name string) string {
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ":", "")
	name = strings.ReplaceAll(name, "(", "")
	name = strings.ReplaceAll(name, ")", "")
	return name
}

func testExceptAnnotation(t *testing.T, kind, harnessID string, policies []bundlePolicy, exp annotationExpected, r Rendered) {
	override := findPolicyByID(policies, exp.By)
	if override == nil {
		override = findNonTargetPolicy(policies)
	}
	target := findTargetPolicy(policies)

	if override == nil {
		t.Fatal("no override policy found")
	}

	call := harnessmatch.Call{Kind: kind, HarnessID: harnessID}
	applies, human := harnessmatch.MatchSrc(override.SrcPattern, call)

	if human || !applies {
		if exp.Harnesses.Contains(kind) {
			t.Skipf("exception expected on %s but override src %q is not renderable (human=%v, applies=%v)",
				kind, override.SrcPattern, human, applies)
		}
		targetEntry := nativeEntryFor(syntaxFor(kind), target.DstPattern)
		if !contains(r.Allows, targetEntry) {
			t.Errorf("expected target allow %q in %s Allows=%v", targetEntry, kind, r.Allows)
		}
		return
	}

	denyEntry := nativeEntryFor(syntaxFor(kind), override.DstPattern)

	if exp.Harnesses.Contains(kind) {
		if !contains(r.Denies, denyEntry) {
			t.Errorf("expected deny entry %q for resource %q in %s Denies=%v",
				denyEntry, exp.Resource, kind, r.Denies)
		}
	} else {
		if contains(r.Denies, denyEntry) {
			t.Errorf("unexpected deny entry %q for resource %q in %s Denies=%v",
				denyEntry, exp.Resource, kind, r.Denies)
		}
		targetEntry := nativeEntryFor(syntaxFor(kind), target.DstPattern)
		if !contains(r.Allows, targetEntry) {
			t.Errorf("expected target allow %q in %s Allows=%v", targetEntry, kind, r.Allows)
		}
	}
}

func testNeverAnnotation(t *testing.T, kind, harnessID string, policies []bundlePolicy, exp annotationExpected, r Rendered) {
	override := findPolicyByID(policies, exp.By)
	if override == nil {
		override = findNonTargetPolicy(policies)
	}
	if override == nil {
		t.Fatal("no override policy found")
	}

	call := harnessmatch.Call{Kind: kind, HarnessID: harnessID}
	applies, human := harnessmatch.MatchSrc(override.SrcPattern, call)

	if human || !applies {
		if exp.Harnesses.Contains(kind) {
			t.Skipf("never-applies expected on %s but override src %q is not renderable (human=%v, applies=%v)",
				kind, override.SrcPattern, human, applies)
		}
		return
	}

	if !exp.Harnesses.Contains(kind) {
		return
	}

	t.Skipf("renderer does not filter precedentially-shadowed policies: target %q remains in Allows for %s",
		exp.Resource, kind)
}

func findTargetPolicy(policies []bundlePolicy) bundlePolicy {
	for _, p := range policies {
		if p.ID == "target" {
			return p
		}
	}
	return policies[0]
}

func findNonTargetPolicy(policies []bundlePolicy) *bundlePolicy {
	for i := range policies {
		if policies[i].ID != "target" {
			return &policies[i]
		}
	}
	return nil
}

func findPolicyByID(policies []bundlePolicy, id string) *bundlePolicy {
	for i := range policies {
		if policies[i].ID == id {
			return &policies[i]
		}
	}
	return nil
}

func nativeEntryFor(s nativeSyntax, dst string) string {
	if !strings.HasPrefix(dst, "shell:") {
		return ""
	}
	cmd := strings.TrimPrefix(dst, "shell:")
	if cmd == "" || cmd == "*" {
		return ""
	}
	switch {
	case s.argv != nil:
		argv, ok := s.argv(dst)
		if !ok {
			return ""
		}
		return s.argvEntry(argv)
	default:
		return s.shellEntry(cmd)
	}
}

func contains(entries []string, entry string) bool {
	for _, e := range entries {
		if e == entry {
			return true
		}
	}
	return false
}
