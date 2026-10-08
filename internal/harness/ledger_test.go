package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessRemoveCleansOnlyItsManagedLocalEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := "77777777-7777-7777-7777-777777777777"
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"permissions":{"allow":["Bash(user:*)","Bash(kei:*)"]},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"user-hook"},{"type":"command","command":"kei-proxy hook claude --harness ` + id + `"}]}]}}`)
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeLedger(ledgerPath(OSEnv(), id), syncLedger{HarnessID: id, Kind: "claude_code", Files: map[string]fileLedger{path: {AllowEntries: []string{"Bash(kei:*)"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := Default.RemoveLocal(OSEnv(), id); err != nil {
		t.Fatal(err)
	}
	cleaned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleaned), "Bash(kei:*)") || strings.Contains(string(cleaned), "--harness "+id) || !strings.Contains(string(cleaned), "Bash(user:*)") || !strings.Contains(string(cleaned), "user-hook") {
		t.Fatalf("cleanup damaged config: %s", cleaned)
	}
}

// An expired ledger loses its Kei-written allows; the user's allows and every
// deny stay, and the ledger is marked expired.
func TestExpireEntriesRemovesOnlyManagedAllows(t *testing.T) {
	home := t.TempDir()
	env := hermeticEnv(home, nil)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["Bash(user:*)","Bash(kei:*)"],"deny":["Bash(rm:*)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := writeLedger(ledgerPath(env, "claude_code"), syncLedger{HarnessID: "claude_code", Kind: "claude_code", NotAfter: now.Add(-time.Minute), Files: map[string]fileLedger{path: {AllowEntries: []string{"Bash(kei:*)"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := Default.ExpireEntries(env, now); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(path)
	if strings.Contains(string(updated), "Bash(kei:*)") || !strings.Contains(string(updated), "Bash(user:*)") || !strings.Contains(string(updated), "Bash(rm:*)") {
		t.Fatalf("expired config = %s", updated)
	}
	if backups, _ := filepath.Glob(path + ".kei-backup-*"); len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if ledger := readLedger(ledgerPath(env, "claude_code")); !ledger.NotAfter.IsZero() {
		t.Fatalf("ledger not marked expired: %v", ledger.NotAfter)
	}
}
