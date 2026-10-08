package harness

import (
	"errors"
	"testing"
)

func TestCustomHarnessHasNoNativeStore(t *testing.T) {
	env := hermeticEnv(t.TempDir(), nil)
	h := custom{}
	if h.Detect(env) {
		t.Fatal("custom harness detected")
	}
	if targets, err := h.Targets(env); err != nil || len(targets) != 0 {
		t.Fatalf("targets = %v err=%v", targets, err)
	}
	if h.HookSpec(env) != nil {
		t.Fatal("custom harness has a hook")
	}
	if _, err := h.ImportRules(env, ""); !errors.Is(err, ErrNoNativeStore) {
		t.Fatalf("import err = %v", err)
	}
	result, err := h.Apply(t.Context(), Rendered{}, ApplyOpts{Env: env})
	if err != nil || !result.Skipped || len(result.Notes) != 1 {
		t.Fatalf("apply = %+v err=%v", result, err)
	}
}
