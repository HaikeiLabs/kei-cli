package harness

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeInstallations struct {
	items  []RuntimeInstallation
	health map[string]RuntimeHealth
}

func (f fakeInstallations) ListInstallations(context.Context) ([]RuntimeInstallation, error) {
	return f.items, nil
}

func (f fakeInstallations) InstallationHealth(_ context.Context, id string) (RuntimeHealth, error) {
	if h, ok := f.health[id]; ok {
		return h, nil
	}
	return RuntimeHealth{}, errors.New("status unavailable")
}

func TestOpenWebUIInstallationsFilterMergeAndFormat(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	listed, reported := now.Add(-time.Hour), now.Add(-3*time.Minute)
	version, state, revision := "0.4.2", "current", int64(17)
	src := fakeInstallations{
		items: []RuntimeInstallation{
			{ID: "owui-1", Platform: "openwebui", DisplayName: "chat", Status: "active", LastHeartbeatAt: &listed},
			{ID: "owui-2", Platform: "openwebui", DisplayName: "staging", Status: "pending"},
			{ID: "teams-1", Platform: "teams", DisplayName: "teams-bot"},
		},
		health: map[string]RuntimeHealth{"owui-1": {RuntimeVersion: &version, LastHeartbeatAt: &reported, PolicyRevision: &revision, BundleState: &state}},
	}
	items, err := ListOpenWebUIInstallations(t.Context(), src)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	var out bytes.Buffer
	WriteOpenWebUIInstallations(&out, items, now)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "chat owui-1 active 3m ago 0.4.2 17 current" {
		t.Fatalf("healthy row = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "staging owui-2 pending - - - -" {
		t.Fatalf("unknown row = %q", got)
	}
}
