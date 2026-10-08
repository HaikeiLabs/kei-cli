package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Drift statuses, one per harness in a `kei harness sync --check` report.
const (
	DriftInSync      = "in_sync"
	DriftDetected    = "drift"
	DriftNotSynced   = "not_synced"
	DriftUnsupported = "unsupported"
)

// DriftEntry is one Kei-managed native entry. It carries the rendered entry
// (a command prefix, skill or path pattern), never file content.
type DriftEntry struct {
	File   string `json:"file"`
	Effect string `json:"effect"`
	Entry  string `json:"entry"`
}

// Drift compares one harness's native store with the sync ledger and the
// current bundle.
type Drift struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// SyncedBundleVersion is the bundle the last sync rendered (0 when the
	// harness was never synced).
	SyncedBundleVersion int64 `json:"synced_bundle_version"`
	// Missing are entries the last sync wrote that are no longer in the
	// native store (removed or changed by another writer).
	Missing []DriftEntry `json:"missing"`
	// AlteredFiles are Kei-owned files edited or deleted since the last sync.
	AlteredFiles []string `json:"altered_files"`
	// Pending are entries the current bundle renders that the native store
	// lacks and the last sync did not write (the bundle changed).
	Pending []DriftEntry `json:"pending"`
	// Stale are entries the last sync wrote that the current bundle no longer
	// renders; sync removes them.
	Stale []DriftEntry `json:"stale"`
}

// Drifted reports whether a sync would change the native store.
func (d Drift) Drifted() bool { return d.Status == DriftDetected }

// DriftChecker is implemented by a harness that can compare its own native
// store with a render. File harnesses are checked without it.
type DriftChecker interface {
	CheckDrift(env Env, r Rendered) (Drift, error)
}

// CheckDrift compares h's native store with its sync ledger and with r, the
// current bundle rendered for h. It reads files but never writes.
func CheckDrift(h Harness, env Env, r Rendered) (Drift, error) {
	if c, ok := h.(DriftChecker); ok {
		return c.CheckDrift(env, r)
	}
	f, ok := h.(fileHarness)
	if !ok {
		return Drift{Kind: h.Kind(), Status: DriftUnsupported}, nil
	}
	return checkFileDrift(f, env, r)
}

func checkFileDrift(f fileHarness, env Env, r Rendered) (Drift, error) {
	drift := Drift{Kind: f.Kind(), Missing: []DriftEntry{}, AlteredFiles: []string{}, Pending: []DriftEntry{}, Stale: []DriftEntry{}}
	if _, err := env.home(); err != nil {
		return drift, err
	}
	ledger := readLedger(ledgerPath(env, f.Kind()))
	if ledger.HarnessID == "" {
		drift.Status = DriftNotSynced
		return drift, nil
	}
	drift.SyncedBundleVersion = ledger.Bundle
	targets, err := f.Targets(env)
	if err != nil {
		return drift, err
	}
	rendered := map[string]bool{}
	for _, e := range r.Allows {
		rendered["allow\x00"+e] = true
	}
	for _, e := range r.Denies {
		rendered["deny\x00"+e] = true
	}
	for _, target := range targets {
		if target.Remote() {
			continue
		}
		path := target.Path
		cfg, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return drift, err
		}
		record, synced := ledger.Files[path]
		if synced && f.ownsFile(path) {
			digest := sha256.Sum256(cfg)
			if cfg == nil || hex.EncodeToString(digest[:]) != record.Hash {
				drift.AlteredFiles = append(drift.AlteredFiles, path)
			}
		}
		missingAllows, missingDenies := f.missingEntries(path, cfg, record.AllowEntries, record.DenyEntries)
		missing := map[string]bool{}
		for _, e := range missingAllows {
			missing["allow\x00"+e] = true
			drift.Missing = append(drift.Missing, DriftEntry{File: path, Effect: "allow", Entry: e})
		}
		for _, e := range missingDenies {
			missing["deny\x00"+e] = true
			drift.Missing = append(drift.Missing, DriftEntry{File: path, Effect: "deny", Entry: e})
		}
		for _, e := range record.AllowEntries {
			if !rendered["allow\x00"+e] {
				drift.Stale = append(drift.Stale, DriftEntry{File: path, Effect: "allow", Entry: e})
			}
		}
		for _, e := range record.DenyEntries {
			if !rendered["deny\x00"+e] {
				drift.Stale = append(drift.Stale, DriftEntry{File: path, Effect: "deny", Entry: e})
			}
		}
		pendingAllows, pendingDenies := f.missingEntries(path, cfg, r.Allows, r.Denies)
		for _, e := range pendingAllows {
			if !missing["allow\x00"+e] {
				drift.Pending = append(drift.Pending, DriftEntry{File: path, Effect: "allow", Entry: e})
			}
		}
		for _, e := range pendingDenies {
			if !missing["deny\x00"+e] {
				drift.Pending = append(drift.Pending, DriftEntry{File: path, Effect: "deny", Entry: e})
			}
		}
	}
	sort.Strings(drift.AlteredFiles)
	drift.Status = DriftInSync
	if len(drift.Missing)+len(drift.AlteredFiles)+len(drift.Pending)+len(drift.Stale) > 0 {
		drift.Status = DriftDetected
	}
	return drift, nil
}

// LedgersUnexpired reports whether any local ledger still has allow entries
// in force, which an expired bundle would remove.
func LedgersUnexpired(env Env) bool {
	if _, err := env.home(); err != nil {
		return false
	}
	entries, err := os.ReadDir(ledgerDir(env))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ledger := readLedger(filepath.Join(ledgerDir(env), entry.Name()))
		if ledger.NotAfter.IsZero() {
			continue
		}
		for _, record := range ledger.Files {
			if len(record.AllowEntries) > 0 {
				return true
			}
		}
	}
	return false
}

// countReapplied returns how many of the entries the previous sync wrote to
// path are missing from cfg and are rendered again by r: the entries a sync
// restores after another writer removed them.
func countReapplied(f fileHarness, path string, cfg []byte, previous fileLedger, r Rendered) int {
	missingAllows, missingDenies := f.missingEntries(path, cfg, previous.AllowEntries, previous.DenyEntries)
	n := 0
	for _, e := range missingAllows {
		if slices.Contains(r.Allows, e) {
			n++
		}
	}
	for _, e := range missingDenies {
		if slices.Contains(r.Denies, e) {
			n++
		}
	}
	return n
}

// DriftSchema names the machine-readable `kei harness sync --check --json`
// report.
const DriftSchema = "kei.harness-drift/v1"

// DriftReport is the result of `kei harness sync --check`: every selected
// harness compared with its ledger and the current bundle. It never carries
// file content, tokens or bundle policies, only rendered native entries.
type DriftReport struct {
	Schema string `json:"schema"`
	// Drift is true when a `kei harness sync` would change a native store.
	Drift  bool        `json:"drift"`
	Bundle DriftBundle `json:"bundle"`
	// Harnesses is empty when the bundle expired: sync then removes every
	// Kei-written allow instead of rendering.
	Harnesses []Drift `json:"harnesses"`
}

// DriftBundle identifies the bundle a check compared against.
type DriftBundle struct {
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
	Expired bool   `json:"expired"`
}

// NewDriftReport returns an empty report for bundle b, expired at now or not.
func NewDriftReport(b Bundle, now time.Time) DriftReport {
	return DriftReport{Schema: DriftSchema, Bundle: DriftBundle{Version: b.BundleVersion, Digest: b.PayloadDigest, Expired: !b.NotAfter.After(now)}, Harnesses: []Drift{}}
}

// Add records d and folds it into the report's drift verdict.
func (r *DriftReport) Add(d Drift) {
	r.Harnesses = append(r.Harnesses, d)
	if d.Drifted() {
		r.Drift = true
	}
}

// WriteText writes the report for a person.
func (r DriftReport) WriteText(w io.Writer) {
	if r.Bundle.Expired {
		if r.Drift {
			fmt.Fprintln(w, "Harness policy bundle expired; Kei-written allow entries are still in place. Run kei harness sync to remove them.")
		} else {
			fmt.Fprintln(w, "Harness policy bundle expired; no Kei-written allow entries remain.")
		}
		return
	}
	for _, d := range r.Harnesses {
		switch d.Status {
		case DriftNotSynced:
			fmt.Fprintf(w, "%s: not synced yet\n", d.Kind)
			continue
		case DriftUnsupported:
			fmt.Fprintf(w, "%s: drift check not supported\n", d.Kind)
			continue
		case DriftInSync:
			fmt.Fprintf(w, "%s: in sync with bundle %d\n", d.Kind, r.Bundle.Version)
			continue
		}
		fmt.Fprintf(w, "%s: drift (last synced bundle %d, current %d)\n", d.Kind, d.SyncedBundleVersion, r.Bundle.Version)
		for _, e := range d.Missing {
			fmt.Fprintf(w, "  missing %s %s in %s (removed since last sync)\n", e.Effect, e.Entry, e.File)
		}
		for _, path := range d.AlteredFiles {
			fmt.Fprintf(w, "  altered Kei-owned file %s\n", path)
		}
		for _, e := range d.Pending {
			fmt.Fprintf(w, "  pending %s %s in %s (new in the current bundle)\n", e.Effect, e.Entry, e.File)
		}
		for _, e := range d.Stale {
			fmt.Fprintf(w, "  stale %s %s in %s (no longer in the current bundle)\n", e.Effect, e.Entry, e.File)
		}
	}
	if r.Drift {
		fmt.Fprintln(w, "Run kei harness sync to re-apply Kei-managed entries; restart running Claude Code sessions afterwards so they do not save stale settings again.")
	}
}
