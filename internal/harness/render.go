package harness

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	policybundle "github.com/HaikeiLabs/kei-connector-contracts/bundle"
	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
	policyapi "github.com/HaikeiLabs/kei-connector-contracts/policy"
)

// BundleSchemaV2 is the current policy bundle contract; v1 is frozen and
// deprecated.
const BundleSchemaV2 = "kei.policy-bundle/v2"

// Bundle is an unsigned policy bundle fetched by the CLI. The wire types are
// the shared contracts ones (HAI-609), so the CLI and the runtime read the
// same bundle; only the two fields the CLI alone knows stay here.
type Bundle struct {
	policybundle.Payload
	// PayloadDigest is the sha256 of the fetched bytes, as the runtime
	// records it.
	PayloadDigest string `json:"-"`
	// Connectors are the connectors bound to the workspace (TODO(HAI-485)).
	Connectors []BundleConnector `json:"connectors"`
}

type bundlePolicy struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SrcPattern string `json:"src_pattern"`
	DstPattern string `json:"dst_pattern"`
	Effect     string `json:"effect"`
	Action     string `json:"action"`
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
	var selected *policybundle.Harness
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
	mcpConnectors, mcpInvalid, mcpSkipped := renderMCPConnectors(b.Connectors)
	return Rendered{
		Kind:                   s.kind,
		Allows:                 entries.Allows,
		Denies:                 entries.Denies,
		NotEnforceable:         entries.Unenforceable,
		ScopedElsewhere:        entries.OtherHarness.Count,
		ScopedElsewhereExample: entries.OtherHarness.Example,
		PersonSourcesSkipped:   personSkipped,
		PermitsWithheld:        entries.Withheld,
		MCPConnectors:          mcpConnectors,
		MCPInvalid:             mcpInvalid,
		MCPSkipped:             mcpSkipped,
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

func nativePermissionEntries(s nativeSyntax, harnessID string, harness *policybundle.Harness, subject *policybundle.InstallationSubject, policies []bundlePolicy) nativeEntries {
	kind := s.kind
	var out nativeEntries

	// Build rules for policy.Render. All policies go through the shared
	// renderer so deny-overlap withholding applies uniformly. harness:/agent:
	// sources that match this harness are normalised to "*" so MatchPrincipal
	// resolves them; non-matching harness: sources are tracked as
	// ScopedElsewhere and skipped.
	rules := make([]policyapi.Rule, 0, len(policies))
	byID := make(map[string]bundlePolicy, len(policies))
	for _, p := range policies {
		effect := p.Effect
		if effect == "" {
			effect = p.Action
		}
		if !p.Enabled || (effect != "permit" && effect != "deny") {
			continue
		}
		dst := p.DstPattern
		call := rendererCall(kind, harnessID, harness, dst)
		scope := p.Scope.AgentID
		if scope != nil && *scope != call.AgentID {
			if isPersonSource(p.SrcPattern) {
				out.PersonSources++
			}
			continue
		}
		applies, human := harnessmatch.MatchSrc(p.SrcPattern, call)
		if !applies && !human {
			if strings.HasPrefix(p.SrcPattern, "harness:") {
				out.OtherHarness.Count++
				if out.OtherHarness.Example == "" || p.SrcPattern < out.OtherHarness.Example {
					out.OtherHarness.Example = p.SrcPattern
				}
			}
			continue
		}
		principal := p.SrcPattern
		if !human {
			// harness:/agent:/* sources that match this harness are
			// normalised to "*" so MatchPrincipal resolves them.
			principal = "*"
		}
		rules = append(rules, policyapi.Rule{
			ID:        p.ID,
			Principal: principal,
			Target:    p.DstPattern,
			Effect:    policyapi.Effect(effect),
			Enabled:   p.Enabled,
		})
		byID[p.ID] = p
	}

	if len(rules) == 0 {
		return out
	}

	user, email := "", ""
	var groups []string
	if subject != nil {
		user, email, groups = subject.UserID, subject.Email, subject.Groups
	}
	res := policyapi.Render(rules, user, email, groups, kind)

	for _, e := range res.Entries {
		p, ok := byID[e.RuleID]
		if !ok {
			continue
		}
		dst := p.DstPattern
		if s.argv != nil {
			if _, ok := s.argv(dst); !ok {
				out.Unenforceable = append(out.Unenforceable, policyDisplayName(p))
				continue
			}
		}
		entry := nativeEntry(s, kind, dst, string(e.Effect))
		if entry == "" {
			continue
		}
		if string(e.Effect) == "permit" && strings.HasPrefix(dst, "shell:") && len(strings.Fields(strings.TrimPrefix(dst, "shell:"))) == 0 {
			continue
		}
		if string(e.Effect) == "permit" {
			out.Allows = append(out.Allows, entry)
		} else {
			out.Denies = append(out.Denies, entry)
		}
	}
	for _, d := range res.Diagnostics {
		if d.Kind != "withheld" {
			continue
		}
		p, ok := byID[d.RuleID]
		if !ok {
			continue
		}
		if d.Reason == "principal does not match" {
			out.PersonSources++
		} else {
			out.Withheld = append(out.Withheld, policyDisplayName(p))
		}
	}

	sort.Strings(out.Allows)
	sort.Strings(out.Denies)
	sort.Strings(out.Unenforceable)
	sort.Strings(out.Withheld)
	return out
}

// nativeEntry maps a dst pattern and effect to a native syntax string for
// the given harness kind. Returns "" when the pattern has no native form.
func nativeEntry(s nativeSyntax, kind, dst, effect string) string {
	switch {
	case s.argv != nil:
		argv, _ := s.argv(dst)
		return s.argvEntry(argv)
	case strings.HasPrefix(dst, "shell:"):
		tokens := strings.Fields(strings.TrimPrefix(dst, "shell:"))
		if len(tokens) > 0 && tokens[0] != "*" {
			return s.shellEntry(strings.Join(tokens, " "))
		}
		if effect == "deny" {
			return s.shellEntry("*")
		}
	case dst == "*":
		if effect == "deny" {
			return s.shellEntry("*")
		}
	case strings.HasPrefix(dst, "skill:"):
		if !s.skills {
			return ""
		}
		name := strings.TrimPrefix(dst, "skill:")
		if name != "" {
			return "Skill(" + name + ")"
		}
	case strings.HasPrefix(dst, "tool:"):
		prefix := kind + "."
		name := strings.TrimPrefix(dst, "tool:")
		if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, ".bash") || strings.HasSuffix(name, ".shell")) {
			if effect == "deny" {
				return s.shellEntry("*")
			}
			return ""
		}
	case strings.HasPrefix(dst, "path:"):
		if !s.paths {
			return ""
		}
		return "path:" + strings.TrimPrefix(dst, "path:")
	}
	return ""
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

// precedes reports whether a decides before b under ADR-038 D-1: deny before
// permit, then id ascending. The stored priority is not read.
func precedes(a, b bundlePolicy) bool {
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

func rendererCall(kind, harnessID string, harness *policybundle.Harness, dst string) harnessmatch.Call {
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
