package harness

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
)

// BundleSchemaV2 is the current policy bundle contract; v1 is frozen and
// deprecated.
const BundleSchemaV2 = "kei.policy-bundle/v2"

// Bundle is an unsigned policy bundle fetched by the CLI and runtime.
type Bundle struct {
	Schema         string `json:"schema"`
	BundleID       string `json:"bundle_id"`
	BundleVersion  int64  `json:"bundle_version"`
	PolicyRevision int64  `json:"policy_revision"`
	Audience       struct {
		InstallationID string `json:"installation_id"`
		OrgID          string `json:"org_id"`
		WorkspaceID    string `json:"workspace_id"`
	} `json:"audience"`
	NotAfter              time.Time       `json:"not_after"`
	HarnessMatchSemantics string          `json:"harness_match_semantics"`
	Harnesses             []BundleHarness `json:"harnesses"`
	PolicySet             json.RawMessage `json:"policy_set"`
	Subject               *BundleSubject  `json:"subject"`
	PayloadDigest         string          `json:"-"`
}

// BundleSubject is the installation's principal as the control plane resolved
// it (HAI-433): the installation creator, their email, and their effective
// group names. Only v2 bundles carry it. It is the only source of identity for
// user:, email: and group: policy sources; the renderer never reads one from
// the environment, argv or the harness. A bundle without it, including every
// v1 bundle, renders none of those sources.
type BundleSubject struct {
	UserID string   `json:"user_id"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

// matches reports whether a human policy source names the subject, with the
// catalog's audit-precedence semantics: user:<id or email>, email:<address>
// and group:<name>, each an exact match. Any other source, or a nil subject,
// does not match.
func (s *BundleSubject) matches(src string) bool {
	if s == nil {
		return false
	}
	scheme, value, _ := strings.Cut(src, ":")
	if value == "" {
		return false
	}
	switch scheme {
	case "user":
		return (s.UserID != "" && value == s.UserID) || (s.Email != "" && value == s.Email)
	case "email":
		return s.Email != "" && value == s.Email
	case "group":
		return slices.Contains(s.Groups, value)
	}
	return false
}

// BundleHarness is a harness registered on the installation the bundle is for.
type BundleHarness struct {
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind"`
}

type bundlePolicy struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SrcPattern string `json:"src_pattern"`
	DstPattern string `json:"dst_pattern"`
	Effect     string `json:"effect"`
	Action     string `json:"action"`
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	Scope      struct {
		AgentID *string `json:"agent_id"`
	} `json:"scope"`
}

// nativeSyntax is what a harness's native store can express. The policy walk
// in nativePermissionEntries is shared; each harness supplies its syntax.
type nativeSyntax struct {
	kind string
	// shellEntry renders a shell command prefix ("*" for every command).
	shellEntry func(prefix string) string
	// skills and paths report whether skill: and path: policies have a
	// native form.
	skills, paths bool
	// argv, when set, restricts the harness to argv prefix rules (Codex):
	// every applicable policy argv rejects is reported as not enforceable,
	// and argvEntry renders the ones it accepts.
	argv      func(dst string) ([]string, bool)
	argvEntry func(argv []string) string
}

func rawShellEntry(prefix string) string { return prefix }

// render maps b onto syntax s for the harness registered as harnessID (empty
// for a desktop harness, which is a session of the installation, HP-C11).
func render(s nativeSyntax, harnessID string, b Bundle) (Rendered, error) {
	var set struct {
		Policies []bundlePolicy `json:"policies"`
	}
	if len(b.PolicySet) > 0 {
		if err := json.Unmarshal(b.PolicySet, &set); err != nil {
			return Rendered{}, fmt.Errorf("decode bundle policies: %w", err)
		}
	}
	var selected *BundleHarness
	for i := range b.Harnesses {
		if b.Harnesses[i].AgentID == harnessID && b.Harnesses[i].Kind == s.kind {
			selected = &b.Harnesses[i]
			break
		}
	}
	// Only the v2 contract carries the subject; a v1 bundle never names one.
	subject := b.Subject
	if b.Schema != BundleSchemaV2 {
		subject = nil
	}
	entries := nativePermissionEntries(s, harnessID, selected, subject, set.Policies)
	personSkipped := 0
	if b.Schema != BundleSchemaV2 {
		personSkipped = entries.PersonSources
	}
	return Rendered{
		Kind:                   s.kind,
		Allows:                 entries.Allows,
		Denies:                 entries.Denies,
		NotEnforceable:         entries.Unenforceable,
		ScopedElsewhere:        entries.OtherHarness.Count,
		ScopedElsewhereExample: entries.OtherHarness.Example,
		PersonSourcesSkipped:   personSkipped,
		PermitsWithheld:        entries.Withheld,
		BundleVersion:          b.BundleVersion,
		BundleDigest:           b.PayloadDigest,
		NotAfter:               b.NotAfter,
	}, nil
}

// otherHarnessScope counts policies whose src names a different harness, with
// one such src as an example for the sync hint.
type otherHarnessScope struct {
	Count   int
	Example string
}

// nativeEntries is the native rendering of a policy set for one harness kind.
type nativeEntries struct {
	Allows        []string
	Denies        []string
	Unenforceable []string // names of applicable policies with no native form
	OtherHarness  otherHarnessScope
	// PersonSources counts enabled user:, email: and group: sourced policies
	// that did not name the subject (all of them when there is no subject).
	PersonSources int
	// Withheld names the permits not rendered because a skipped
	// higher-precedence deny overlaps them (ADR-029 §4).
	Withheld []string
}

// renderedPermit is a permit the walk would render, kept until every skipped
// deny is known.
type renderedPermit struct {
	policy bundlePolicy
	entry  string
}

func nativePermissionEntries(s nativeSyntax, harnessID string, harness *BundleHarness, subject *BundleSubject, policies []bundlePolicy) nativeEntries {
	kind := s.kind
	var out nativeEntries
	var permits []renderedPermit
	// skippedDenies are the denies with a human source the renderer cannot
	// resolve. Each still decides before every lower-precedence policy, so an
	// overlapping lower permit is withheld rather than rendered as an allow.
	var skippedDenies []bundlePolicy
	for _, policy := range policies {
		effect := policy.Effect
		if effect == "" {
			effect = policy.Action
		}
		if !policy.Enabled || (effect != "permit" && effect != "deny") {
			continue
		}
		dst := policy.DstPattern
		call := rendererCall(kind, harnessID, harness, dst)
		scope := policy.Scope.AgentID
		matchPolicy := harnessmatch.Policy{ID: policy.ID, Src: policy.SrcPattern, Dst: dst, Action: effect, Enabled: policy.Enabled, Scope: scope}
		applies, human := harnessmatch.MatchSrc(matchPolicy.Src, call)
		if human && subject.matches(policy.SrcPattern) {
			// A source naming the installation's subject applies on every
			// harness of the installation, as "*" does; agent scope and the
			// dst still decide below.
			applies = true
			matchPolicy.Src = "*"
		}
		if human && !applies && isPersonSource(policy.SrcPattern) {
			out.PersonSources++
		}
		if human && !applies && effect == "deny" && (scope == nil || *scope == call.AgentID) {
			skippedDenies = append(skippedDenies, policy)
		}
		if !applies {
			if strings.HasPrefix(policy.SrcPattern, "harness:") {
				out.OtherHarness.Count++
				if out.OtherHarness.Example == "" || policy.SrcPattern < out.OtherHarness.Example {
					out.OtherHarness.Example = policy.SrcPattern
				}
			}
			continue
		}
		if s.argv != nil {
			// Argv prefix rules are all this harness can express. Every other
			// policy that applies to it is reported, never dropped silently.
			if scope != nil && *scope != call.AgentID {
				continue
			}
			if _, ok := s.argv(dst); !ok {
				out.Unenforceable = append(out.Unenforceable, policyDisplayName(policy))
				continue
			}
		}
		result := harnessmatch.Evaluate(call, []harnessmatch.Policy{matchPolicy})
		if result.Outcome != harnessmatch.OutcomePermit && result.Outcome != harnessmatch.OutcomeDeny {
			continue
		}
		if result.Outcome == harnessmatch.OutcomePermit && effect != "permit" || result.Outcome == harnessmatch.OutcomeDeny && effect != "deny" {
			continue
		}
		entry := ""
		switch {
		case s.argv != nil:
			argv, _ := s.argv(dst)
			entry = s.argvEntry(argv)
		case strings.HasPrefix(dst, "shell:"):
			tokens := strings.Fields(strings.TrimPrefix(dst, "shell:"))
			if len(tokens) > 0 && tokens[0] != "*" {
				entry = s.shellEntry(strings.Join(tokens, " "))
			} else if effect == "deny" {
				entry = s.shellEntry("*")
			}
		case dst == "*":
			if effect == "deny" {
				entry = s.shellEntry("*")
			}
		case strings.HasPrefix(dst, "skill:"):
			if !s.skills {
				continue
			}
			name := strings.TrimPrefix(dst, "skill:")
			if name != "" {
				entry = "Skill(" + name + ")"
			}
		case strings.HasPrefix(dst, "tool:"):
			prefix := kind + "."
			name := strings.TrimPrefix(dst, "tool:")
			if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, ".bash") || strings.HasSuffix(name, ".shell")) {
				if effect == "deny" {
					entry = s.shellEntry("*")
				} else {
					continue
				}
			}
		case strings.HasPrefix(dst, "path:"):
			if !s.paths {
				continue
			}
			entry = "path:" + strings.TrimPrefix(dst, "path:")
		}
		if entry == "" {
			continue
		}
		if effect == "permit" && strings.HasPrefix(dst, "shell:") && len(strings.Fields(strings.TrimPrefix(dst, "shell:"))) == 0 {
			continue
		}
		if result.Outcome == harnessmatch.OutcomePermit {
			permits = append(permits, renderedPermit{policy: policy, entry: entry})
		} else {
			out.Denies = append(out.Denies, entry)
		}
	}
	for _, permit := range permits {
		if blockedBySkippedDeny(permit.policy, skippedDenies) {
			out.Withheld = append(out.Withheld, policyDisplayName(permit.policy))
			continue
		}
		out.Allows = append(out.Allows, permit.entry)
	}
	sort.Strings(out.Allows)
	sort.Strings(out.Denies)
	sort.Strings(out.Unenforceable)
	sort.Strings(out.Withheld)
	return out
}

// blockedBySkippedDeny reports whether a skipped deny precedes permit and
// overlaps its dst.
func blockedBySkippedDeny(permit bundlePolicy, denies []bundlePolicy) bool {
	for _, deny := range denies {
		if precedes(deny, permit) && dstOverlaps(deny.DstPattern, permit.DstPattern) {
			return true
		}
	}
	return false
}

// precedes reports whether a decides before b under ADR-028 §2: priority
// descending, deny before permit at equal priority, then id ascending.
func precedes(a, b bundlePolicy) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if ad, bd := policyEffect(a) == "deny", policyEffect(b) == "deny"; ad != bd {
		return ad
	}
	return a.ID < b.ID
}

func policyEffect(p bundlePolicy) string {
	if p.Effect != "" {
		return p.Effect
	}
	return p.Action
}

// dstOverlaps reports whether some call could match both a deny's dst and a
// rendered permit's dst (shell:, skill: or path:). It errs toward overlap: a
// false positive only withholds a permit, so the harness asks.
func dstOverlaps(deny, permit string) bool {
	if deny == "*" {
		return true
	}
	dScheme, dValue, _ := strings.Cut(deny, ":")
	pScheme, pValue, _ := strings.Cut(permit, ":")
	if dScheme == "tool" {
		// A deny on the shell tool itself, or on every tool, covers every
		// shell command.
		return pScheme == "shell" && (dValue == "*" || strings.HasSuffix(dValue, ".bash") || strings.HasSuffix(dValue, ".shell"))
	}
	if dScheme != pScheme {
		return false
	}
	switch dScheme {
	case "shell":
		d, p := strings.Fields(dValue), strings.Fields(pValue)
		if len(d) == 0 || d[0] == "*" {
			return true
		}
		n := min(len(d), len(p))
		return slices.Equal(d[:n], p[:n])
	case "skill":
		return dValue == "*" || pValue == "*" || dValue == pValue
	case "path":
		return globsOverlap(strings.Split(dValue, "/"), strings.Split(pValue, "/"))
	}
	return false
}

// globsOverlap reports whether two path globs could match a common path. A
// ** or ~ segment overlaps anything that follows; a segment holding * is
// taken to overlap any segment.
func globsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	if a[0] == "**" || b[0] == "**" || a[0] == "*" && len(a) == 1 || b[0] == "*" && len(b) == 1 || a[0] == "~" || b[0] == "~" {
		return true
	}
	if a[0] != b[0] && !strings.Contains(a[0], "*") && !strings.Contains(b[0], "*") {
		return false
	}
	return globsOverlap(a[1:], b[1:])
}

// isPersonSource reports whether src names a person or group, the sources
// only the bundle subject can resolve.
func isPersonSource(src string) bool {
	return strings.HasPrefix(src, "user:") || strings.HasPrefix(src, "email:") || strings.HasPrefix(src, "group:")
}

func policyDisplayName(policy bundlePolicy) string {
	if policy.Name != "" {
		return policy.Name
	}
	return policy.ID
}

func rendererCall(kind, harnessID string, harness *BundleHarness, dst string) harnessmatch.Call {
	call := harnessmatch.Call{Kind: kind, HarnessID: harnessID}
	if harness != nil {
		call.AgentID = harness.AgentID
	}
	switch {
	case strings.HasPrefix(dst, "shell:"):
		call.Argv = strings.Fields(strings.TrimPrefix(dst, "shell:"))
		if len(call.Argv) == 0 || call.Argv[0] == "*" {
			call.Argv = []string{"kei-proxy", "run"}
		}
	case strings.HasPrefix(dst, "skill:"):
		call.Skill = strings.TrimPrefix(dst, "skill:")
	case strings.HasPrefix(dst, "path:"):
		call.Path = "/kei-render-path"
	case strings.HasPrefix(dst, "mcp:"):
		v := strings.TrimPrefix(dst, "mcp:")
		call.MCPServer, call.MCPTool, _ = strings.Cut(v, "/")
	case strings.HasPrefix(dst, "tool:"):
		call.Tool = strings.TrimPrefix(dst, "tool:")
	case dst == "*":
		call.Tool = "kei.render"
	}
	return call
}

func truncateName(s string, max int) string {
	s = strings.ReplaceAll(s, " ", "-")
	if len(s) > max {
		return s[:max]
	}
	return s
}

func mustJSON(value any) []byte { data, _ := json.MarshalIndent(value, "", "  "); return data }
