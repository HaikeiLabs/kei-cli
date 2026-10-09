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

	// The golden annotations describe the principal the user:/group: sources
	// name (the console's request fixture is dev@example.com). The bundle
	// subject (HAI-433) is that principal for "creator"; "other" and "none"
	// prove those sources are not rendered for anyone else, or without a
	// subject.
	subjects := []struct {
		name    string
		subject *BundleSubject
	}{
		{"creator", &BundleSubject{UserID: "usr-fixture-dev", Email: "dev@example.com", Groups: []string{"admins", "members"}}},
		{"other", &BundleSubject{UserID: "usr-fixture-other", Email: "other@example.com", Groups: []string{"members"}}},
		{"none", nil},
	}

	for _, ac := range g.Cases {
		t.Run(cleanName(ac.Name), func(t *testing.T) {
			policySet, err := json.Marshal(map[string][]bundlePolicy{"policies": ac.Policies})
			if err != nil {
				t.Fatal(err)
			}
			for _, sub := range subjects {
				t.Run(sub.name, func(t *testing.T) {
					bundle := Bundle{Schema: BundleSchemaV2, PolicySet: policySet, Subject: sub.subject}
					for _, kind := range harnessKinds {
						t.Run(kind, func(t *testing.T) {
							r, err := render(syntaxFor(kind), "test-harness-id", bundle)
							if err != nil {
								t.Fatal(err)
							}

							for _, exp := range ac.Expected {
								switch exp.Kind {
								case "except":
									testExceptAnnotation(t, kind, "test-harness-id", sub.subject, ac.Policies, exp, r)
								case "never":
									testNeverAnnotation(t, kind, "test-harness-id", sub.subject, ac.Policies, exp, r)
								default:
									t.Fatalf("unknown expected kind: %s", exp.Kind)
								}
							}
						})
					}
				})
			}
		})
	}
}

// overrideApplies reports whether src applies to a call on kind for subject,
// the way the renderer decides it.
func overrideApplies(src, kind, harnessID string, subject *BundleSubject) bool {
	applies, human := harnessmatch.MatchSrc(src, harnessmatch.Call{Kind: kind, HarnessID: harnessID})
	return applies || (human && subject.matches(src))
}

func cleanName(name string) string {
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ":", "")
	name = strings.ReplaceAll(name, "(", "")
	name = strings.ReplaceAll(name, ")", "")
	return name
}

func testExceptAnnotation(t *testing.T, kind, harnessID string, subject *BundleSubject, policies []bundlePolicy, exp annotationExpected, r Rendered) {
	override := findPolicyByID(policies, exp.By)
	if override == nil {
		override = findNonTargetPolicy(policies)
	}
	target := findTargetPolicy(policies)

	if override == nil {
		t.Fatal("no override policy found")
	}

	denyEntry := nativeEntryFor(syntaxFor(kind), override.DstPattern)

	if !overrideApplies(override.SrcPattern, kind, harnessID, subject) {
		// The override names another harness or principal: it is not
		// rendered. Another harness's deny leaves the target allowed; a
		// person's deny the renderer skipped still decides first, so an
		// overlapping lower target is withheld (HAI-447).
		if contains(r.Denies, denyEntry) {
			t.Errorf("override src %q does not apply on %s but deny %q rendered: Denies=%v",
				override.SrcPattern, kind, denyEntry, r.Denies)
		}
		targetEntry := nativeEntryFor(syntaxFor(kind), target.DstPattern)
		_, human := harnessmatch.MatchSrc(override.SrcPattern, harnessmatch.Call{Kind: kind})
		if human && blockedBySkippedDeny(target, []bundlePolicy{*override}) {
			if contains(r.Allows, targetEntry) || !contains(r.PermitsWithheld, policyDisplayName(target)) {
				t.Errorf("skipped deny %q precedes target: want %q withheld, got Allows=%v Withheld=%v", override.ID, targetEntry, r.Allows, r.PermitsWithheld)
			}
			return
		}
		if !contains(r.Allows, targetEntry) {
			t.Errorf("expected target allow %q in %s Allows=%v", targetEntry, kind, r.Allows)
		}
		return
	}

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

func testNeverAnnotation(t *testing.T, kind, harnessID string, subject *BundleSubject, policies []bundlePolicy, exp annotationExpected, r Rendered) {
	override := findPolicyByID(policies, exp.By)
	if override == nil {
		override = findNonTargetPolicy(policies)
	}
	if override == nil {
		t.Fatal("no override policy found")
	}

	denyEntry := nativeEntryFor(syntaxFor(kind), override.DstPattern)

	if !overrideApplies(override.SrcPattern, kind, harnessID, subject) {
		if contains(r.Denies, denyEntry) {
			t.Errorf("override src %q does not apply on %s but deny %q rendered: Denies=%v",
				override.SrcPattern, kind, denyEntry, r.Denies)
		}
		return
	}

	if !exp.Harnesses.Contains(kind) {
		return
	}

	// The renderer keeps the shadowed target allow; the target never applies
	// because every native store lets the rendered deny win (Claude Code
	// deny-beats-allow, Codex strictest decision, OpenCode denies written
	// after allows).
	if !contains(r.Denies, denyEntry) {
		t.Errorf("expected shadowing deny %q for resource %q in %s Denies=%v",
			denyEntry, exp.Resource, kind, r.Denies)
	}
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
