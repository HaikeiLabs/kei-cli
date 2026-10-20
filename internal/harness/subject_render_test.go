package harness

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	policybundle "github.com/HaikeiLabs/kei-connector-contracts/bundle"
	policyapi "github.com/HaikeiLabs/kei-connector-contracts/policy"
)

// TestMatchPrincipalUserSchemePinsHAI609 verifies that user:<userID> matches
// by user ID only (not email), per the shared contracts exact-match rule.
// Use email:<addr> for email matching instead. This is fail-closed: a
// user:<email> policy that previously matched a subject by email no longer
// applies, forcing administrators to be explicit about the scheme.
func TestMatchPrincipalUserSchemePinsHAI609(t *testing.T) {
	s := &policybundle.InstallationSubject{UserID: "usr-fixture-dev", Email: "dev@example.com"}
	if policyapi.MatchPrincipal("user:dev@example.com", s.UserID, s.Email, nil) {
		t.Error("user:dev@example.com matched a subject whose user ID is usr-fixture-dev; user: matches user ID only, not email")
	}
	if !policyapi.MatchPrincipal("email:dev@example.com", s.UserID, s.Email, nil) {
		t.Error("email:dev@example.com did not match a subject whose email is dev@example.com")
	}
}

func TestBundleSubjectMatches(t *testing.T) {
	s := &policybundle.InstallationSubject{UserID: "usr-fixture-dev", Email: "dev@example.com", Groups: []string{"admins", "release_eng"}}
	for src, want := range map[string]bool{
		"user:usr-fixture-dev":   true,
		"email:dev@example.com":  true,
		"group:release_eng":      true,
		"group:admins":           true,
		"user:usr-fixture-other": false,
		"user:dev@example.com":   false,
		"email:DEV@example.com":  false,
		"group:members":          false,
		"group:":                 false,
		"user:":                  false,
		"group:*":                false,
		"org:admin":              false,
	} {
		got := policyapi.MatchPrincipal(src, s.UserID, s.Email, s.Groups)
		if got != want {
			t.Errorf("MatchPrincipal(%q, %q, %q, %v)=%v want %v", src, s.UserID, s.Email, s.Groups, got, want)
		}
	}
	// nil subject: MatchPrincipal with empty user/email/groups
	if policyapi.MatchPrincipal("user:usr-fixture-dev", "", "", nil) {
		t.Error("MatchPrincipal with empty identity matched")
	}
}

func TestRenderSubjectSourcedPolicies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policies := []map[string]any{
		{"id": "p1", "src_pattern": "user:usr-fixture-dev", "dst_pattern": "shell:make", "action": "permit", "enabled": true},
		{"id": "p2", "src_pattern": "group:release_eng", "dst_pattern": "skill:deploy", "action": "permit", "enabled": true},
		{"id": "p3", "src_pattern": "email:dev@example.com", "dst_pattern": "shell:rm", "action": "deny", "enabled": true},
		{"id": "p4", "src_pattern": "group:release_eng", "dst_pattern": "shell:kubectl", "action": "permit", "enabled": true, "scope": map[string]any{"agent_id": "agent-other"}},
		{"id": "p5", "src_pattern": "user:usr-fixture-other", "dst_pattern": "shell:curl", "action": "permit", "enabled": true},
		{"id": "p6", "src_pattern": "group:release_eng", "dst_pattern": "shell:helm", "action": "permit", "enabled": false},
	}
	decode := func(schema string) Bundle {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"schema": schema, "policy_set": map[string]any{"policies": policies},
			"subject": map[string]any{"user_id": "usr-fixture-dev", "email": "dev@example.com", "groups": []string{"release_eng"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var b Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := decode(BundleSchemaV2)
	if b.Subject == nil || b.Subject.UserID != "usr-fixture-dev" {
		t.Fatalf("subject not decoded: %+v", b.Subject)
	}

	r, err := render(syntaxFor("claude_code"), "", b)
	if err != nil {
		t.Fatal(err)
	}
	wantAllows := []string{shellEntryFor("claude_code", "make"), "Skill(deploy)"}
	slices.Sort(wantAllows)
	if !slices.Equal(r.Allows, wantAllows) {
		t.Errorf("Allows=%v want %v (agent-scoped, other-user and disabled policies excluded)", r.Allows, wantAllows)
	}
	if !slices.Equal(r.Denies, []string{shellEntryFor("claude_code", "rm")}) {
		t.Errorf("Denies=%v", r.Denies)
	}
	if r.PersonSourcesSkipped != 0 {
		t.Errorf("v2 PersonSourcesSkipped=%d want 0", r.PersonSourcesSkipped)
	}

	// v1 is frozen and never carries a subject: one it names anyway is
	// ignored, the person sources are skipped, and sync prints one notice.
	v1, err := render(syntaxFor("claude_code"), "", decode("kei.policy-bundle/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(v1.Allows)+len(v1.Denies) != 0 {
		t.Errorf("v1: rendered allow=%v deny=%v, want user:/email:/group: sources skipped", v1.Allows, v1.Denies)
	}
	if v1.PersonSourcesSkipped != 5 {
		t.Errorf("v1 PersonSourcesSkipped=%d want 5 (enabled person-sourced policies)", v1.PersonSourcesSkipped)
	}
	var hints strings.Builder
	printRenderHints(&hints, "Claude Code", v1)
	if got := strings.Count(hints.String(), "\n"); got != 1 || !strings.Contains(hints.String(), "5 user:/group: policies not rendered; the v1 policy bundle carries no subject") {
		t.Errorf("v1 notice = %q, want one line", hints.String())
	}
	hints.Reset()
	printRenderHints(&hints, "Claude Code", r)
	if strings.Contains(hints.String(), "carries no subject") {
		t.Errorf("v2 printed the v1 notice: %q", hints.String())
	}

	b.Subject = nil
	r, err = render(syntaxFor("claude_code"), "", b)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Allows)+len(r.Denies) != 0 {
		t.Errorf("no subject: rendered allow=%v deny=%v, want user:/email:/group: sources skipped", r.Allows, r.Denies)
	}
}

func shellEntryFor(kind, prefix string) string { return syntaxFor(kind).shellEntry(prefix) }
