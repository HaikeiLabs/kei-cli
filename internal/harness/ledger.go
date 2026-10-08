package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// syncLedger records what one sync wrote, so the next sync, an expiry, or a
// removal touches only Kei-managed entries.
type syncLedger struct {
	HarnessID string                `json:"harness_id"`
	Kind      string                `json:"kind"`
	Bundle    int64                 `json:"bundle_version"`
	Digest    string                `json:"bundle_digest"`
	NotAfter  time.Time             `json:"not_after"`
	Files     map[string]fileLedger `json:"files"`
}
type fileLedger struct {
	Hash         string   `json:"hash"`
	Content      []byte   `json:"content"`
	AllowEntries []string `json:"allow_entries,omitempty"`
	DenyEntries  []string `json:"deny_entries,omitempty"`
}

func ledgerPath(env Env, id string) string {
	return filepath.Join(ledgerDir(env), id+".json")
}
func readLedger(path string) syncLedger {
	var ledger syncLedger
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &ledger)
	return ledger
}
func writeLedger(path string, ledger syncLedger) error {
	b, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// legacyLedgerHarness reads ledgers whose kind is not a registered file
// harness: before harnesses were split out, every kind other than codex and
// opencode was cleaned with the Claude Code JSON format.
var legacyLedgerHarness fileHarness = claudeCode{}

// fileHarnessFor returns the file harness that wrote a ledger of the given kind.
func (r *Registry) fileHarnessFor(kind string) fileHarness {
	if h, ok := r.Get(kind); ok {
		if f, ok := h.(fileHarness); ok {
			return f
		}
	}
	return legacyLedgerHarness
}

func (r *Registry) ownsFile(path string) bool {
	for _, h := range r.harnesses {
		if f, ok := h.(fileHarness); ok && f.ownsFile(path) {
			return true
		}
	}
	return false
}

// RemoveLocal removes the Kei-managed local entries recorded in the ledger
// for id, leaving the user's own entries in place.
func (r *Registry) RemoveLocal(env Env, id string) error {
	path := ledgerPath(env, id)
	ledger := readLedger(path)
	if ledger.HarnessID == "" {
		return nil
	}
	now := time.Now().UTC()
	for target, record := range ledger.Files {
		cfg, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var cleaned []byte
		if r.ownsFile(target) {
			if !bytes.Contains(cfg, []byte("managed by kei harness sync")) {
				continue
			}
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		cleaned, err = r.fileHarnessFor(ledger.Kind).removeManaged(cfg, record.AllowEntries, record.DenyEntries)
		if err != nil {
			return err
		}
		if bytes.Contains(cfg, []byte("--harness "+id)) {
			cleaned, err = removeHookJSONEntries(cleaned, id)
			if err != nil {
				return err
			}
		}
		if bytes.Equal(cfg, cleaned) {
			continue
		}
		backup := target + ".kei-backup-" + now.Format("20060102T150405.000000000Z")
		if err := os.WriteFile(backup, cfg, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(target, cleaned, 0o600); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func removeHookJSONEntries(config []byte, id string) ([]byte, error) {
	var root any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	needle := "kei-proxy hook "
	idNeedle := "--harness " + id
	var prune func(any) any
	prune = func(value any) any {
		switch node := value.(type) {
		case []any:
			kept := make([]any, 0, len(node))
			for _, child := range node {
				remove := false
				if object, ok := child.(map[string]any); ok {
					if command, ok := object["command"].(string); ok && strings.Contains(command, needle) && strings.Contains(command, idNeedle) {
						remove = true
					}
				}
				if command, ok := child.(string); ok && strings.Contains(command, needle) && strings.Contains(command, idNeedle) {
					remove = true
				}
				if !remove {
					kept = append(kept, prune(child))
				}
			}
			return kept
		case map[string]any:
			for key, child := range node {
				node[key] = prune(child)
			}
			return node
		default:
			return value
		}
	}
	return json.MarshalIndent(prune(root), "", "  ")
}

// ExpireEntries removes the Kei-written allow entries of every ledger whose
// bundle expired at or before now. Deny entries stay in place.
func (r *Registry) ExpireEntries(env Env, now time.Time) error {
	if _, err := env.home(); err != nil {
		return err
	}
	dir := ledgerDir(env)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		ledger := readLedger(path)
		if ledger.NotAfter.After(now) {
			continue
		}
		for target, record := range ledger.Files {
			if len(record.AllowEntries) == 0 {
				continue
			}
			cfg, err := os.ReadFile(target)
			if err != nil {
				continue
			}
			cleaned, err := r.fileHarnessFor(ledger.Kind).removeManaged(cfg, record.AllowEntries, nil)
			if err != nil {
				return err
			}
			if bytes.Equal(cfg, cleaned) {
				continue
			}
			backup := target + ".kei-backup-" + now.UTC().Format("20060102T150405.000000000Z")
			if err := os.WriteFile(backup, cfg, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(target, cleaned, 0o600); err != nil {
				return err
			}
		}
		ledger.NotAfter = time.Time{}
		if err := writeLedger(path, ledger); err != nil {
			return err
		}
	}
	return nil
}
