package harness

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	before := []string{"a", "b", "c", "d"}
	after := []string{"a", "B", "c", "d", "e"}
	diff := unifiedDiff(before, after, 1)
	if !strings.Contains(diff, "@@") {
		t.Fatalf("missing hunk header: %s", diff)
	}
	if !strings.Contains(diff, "-b\n") || !strings.Contains(diff, "+B\n") {
		t.Fatalf("missing replacement: %s", diff)
	}
	if !strings.Contains(diff, "+e\n") {
		t.Fatalf("missing insertion: %s", diff)
	}
	if got := unifiedDiff(before, before, 3); got != "" {
		t.Fatalf("expected empty diff for equal inputs, got: %s", got)
	}
}
