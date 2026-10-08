package harness

import "context"

// custom is an SDK harness registered with `kei harness add --kind custom`.
// It has no native allowlist: kei-proxy authorizes every call (ADR-011), so
// sync renders nothing for it.
type custom struct{}

func (custom) Kind() string                            { return "custom" }
func (custom) Detect(Env) bool                         { return false }
func (custom) Targets(Env) ([]Target, error)           { return nil, nil }
func (custom) Render(Bundle) (Rendered, error)         { return Rendered{Kind: "custom"}, nil }
func (custom) HookSpec(Env) *HookSpec                  { return nil }
func (custom) ImportRules(Env, string) ([]Rule, error) { return nil, ErrNoNativeStore }

func (custom) Apply(context.Context, Rendered, ApplyOpts) (Result, error) {
	return Result{Skipped: true, Notes: []string{"custom harness: skipped; no native allowlist (authorization is enforced by kei-proxy per ADR-011)."}}, nil
}
