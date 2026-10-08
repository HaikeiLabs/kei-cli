package harness

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
)

// Bundle is the unsigned policy-bundle/v1 payload fetched by both the CLI and runtime.
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
	PayloadDigest         string          `json:"-"`
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
	entries := nativePermissionEntries(s, harnessID, selected, set.Policies)
	return Rendered{
		Kind:                   s.kind,
		Allows:                 entries.Allows,
		Denies:                 entries.Denies,
		NotEnforceable:         entries.Unenforceable,
		ScopedElsewhere:        entries.OtherHarness.Count,
		ScopedElsewhereExample: entries.OtherHarness.Example,
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
}

func nativePermissionEntries(s nativeSyntax, harnessID string, harness *BundleHarness, policies []bundlePolicy) nativeEntries {
	kind := s.kind
	var out nativeEntries
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
		if applies, _ := harnessmatch.MatchSrc(matchPolicy.Src, call); !applies {
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
			out.Allows = append(out.Allows, entry)
		} else {
			out.Denies = append(out.Denies, entry)
		}
	}
	sort.Strings(out.Allows)
	sort.Strings(out.Denies)
	sort.Strings(out.Unenforceable)
	return out
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
