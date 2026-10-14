package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HaikeiLabs/kei-connector-contracts/providers"
)

// backupStamp is the timestamp layout of a Kei backup name.
const backupStamp = "20060102T150405.000000000Z"

// backupSuffix is what follows a native config's name in its Kei backup.
const backupSuffix = ".kei-backup-"

// renameFile is os.Rename; tests replace it to fail a write.
var renameFile = os.Rename

// writeFileAtomic replaces path with data: a temp file in the same
// directory, fsync, then a rename, so a reader sees the old or the new file
// and never a partial one. The file is 0600, or the existing mode when that
// is stricter. A symlinked path is written through to its target.
func writeFileAtomic(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm() & 0o600
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".kei-tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(name)
		}
	}()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := renameFile(name, path); err != nil {
		return err
	}
	committed = true
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry change; it is best effort, since not
// every platform can fsync a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// syncWrite is the native-config writes of one sync. Before replacing a
// file it saves the file beside it as <file>.kei-backup-<stamp>. When the
// sync succeeds the backup is kept, so the user can return to the config
// from before the sync, and the backups it supersedes are pruned (see
// pruneFileBackups). When the sync fails, rollback restores every file
// written so far from its fresh backup and deletes that backup, leaving the
// previous sync's backup in place (HAI-582).
type syncWrite struct {
	now     time.Time
	written []writtenFile
}

type writtenFile struct {
	path string
	// backup is empty when the file did not exist before the sync.
	backup string
	mode   os.FileMode
}

func newSyncWrite(now time.Time) *syncWrite {
	return &syncWrite{now: now.UTC()}
}

// write backs up path, writes data atomically and checks it landed. On an
// error the caller rolls back.
func (w *syncWrite) write(path string, data []byte) error {
	file := writtenFile{path: path}
	if _, err := os.Lstat(path); err == nil {
		// Stat and ReadFile follow a symlink, as writeFileAtomic does.
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file.mode = info.Mode().Perm()
		// A name already taken belongs to an earlier backup, which a failed
		// sync must leave as it is: step the stamp to the next free one.
		for stamp := w.now; ; stamp = stamp.Add(time.Nanosecond) {
			file.backup = path + backupSuffix + stamp.Format(backupStamp)
			err = writeNew(file.backup, original, file.mode)
			if !errors.Is(err, os.ErrExist) {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	w.written = append(w.written, file)
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("verify %s: the written file does not match", path)
	}
	return nil
}

// writeNew creates path, which must not exist, with data and mode.
func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// fresh maps each file the sync replaced to the backup it made.
func (w *syncWrite) fresh() map[string]string {
	backups := map[string]string{}
	for _, file := range w.written {
		if file.backup != "" {
			backups[file.path] = file.backup
		}
	}
	return backups
}

// pruneEarlier removes the backups of the files the sync replaced that
// earlier syncs kept, once the sync has succeeded.
func (w *syncWrite) pruneEarlier() error {
	fresh := w.fresh()
	files := make([]string, 0, len(fresh))
	for file := range fresh {
		files = append(files, file)
	}
	return pruneFileBackups(files, fresh, false, io.Discard)
}

// rollback restores every file the sync wrote to its bytes before the
// sync, removes the files it created, and deletes the backups. It returns
// cause joined with any restore error.
func (w *syncWrite) rollback(cause error) error {
	errs := []error{cause}
	for i := len(w.written) - 1; i >= 0; i-- {
		file := w.written[i]
		if file.backup == "" {
			if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("roll back %s: %w", file.path, err))
			}
			continue
		}
		original, err := os.ReadFile(file.backup)
		if err == nil {
			err = writeFileAtomic(file.path, original)
		}
		if err == nil {
			err = os.Chmod(file.path, file.mode)
		}
		if err != nil {
			// Keep the backup: it is the only copy of the original.
			errs = append(errs, fmt.Errorf("roll back %s (its original is in %s): %w", file.path, file.backup, err))
			continue
		}
		if err := os.Remove(file.backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove sync backup: %w", err))
		}
	}
	w.written = nil
	return errors.Join(errs...)
}

// keiBackup is one Kei backup of a managed file or skill: its stamp and,
// for a skill backup whose name was taken, its -n suffix order it.
type keiBackup struct {
	path  string
	stamp string
	n     int
}

func (b keiBackup) newerThan(o keiBackup) bool {
	return b.stamp > o.stamp || (b.stamp == o.stamp && b.n > o.n)
}

// validStamp reports whether s is exactly a backup timestamp.
func validStamp(s string) bool {
	t, err := time.Parse(backupStamp, s)
	return err == nil && t.Format(backupStamp) == s
}

// fileBackups returns the Kei backups beside file: regular files named
// exactly <file>.kei-backup-<stamp>.
func fileBackups(file string) ([]keiBackup, error) {
	dir, base := filepath.Dir(file), filepath.Base(file)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []keiBackup
	for _, entry := range entries {
		stamp, ok := strings.CutPrefix(entry.Name(), base+backupSuffix)
		if ok && entry.Type().IsRegular() && validStamp(stamp) {
			found = append(found, keiBackup{path: filepath.Join(dir, entry.Name()), stamp: stamp})
		}
	}
	return found, nil
}

// skillBackupName is a skill folder backup: <skill>-<stamp>, or
// <skill>-<stamp>-<n> when that name was taken.
var skillBackupName = regexp.MustCompile(`^(.+)-(\d{8}T\d{6}\.\d{9}Z)(?:-([1-9][0-9]*))?$`)

// skillBackupsDir holds kind's skill folder backups, outside every skill
// dir so the harness never loads a backup as a skill.
func skillBackupsDir(env Env, kind string) string {
	return filepath.Join(ledgerDir(env), "skill-backups", kind)
}

// skillBackups returns kind's skill folder backups by skill name. Only
// folders named exactly as kei-cli names them match.
func skillBackups(env Env, kind string) (map[string][]keiBackup, error) {
	dir := skillBackupsDir(env, kind)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found := map[string][]keiBackup{}
	for _, entry := range entries {
		m := skillBackupName.FindStringSubmatch(entry.Name())
		if m == nil || !entry.IsDir() || providers.ValidateSkillName(m[1]) != nil || !validStamp(m[2]) {
			continue
		}
		n, _ := strconv.Atoi(m[3])
		found[m[1]] = append(found[m[1]], keiBackup{path: filepath.Join(dir, entry.Name()), stamp: m[2], n: n})
	}
	return found, nil
}

// superseded returns the backups in group that a successful sync removes.
// When the sync backed the item up (fresh), only that backup (keep; empty
// on a dry run, where it is only planned) survives. Otherwise the newest
// backup survives: the state before the last sync that changed the item.
func superseded(group []keiBackup, fresh bool, keep string) []string {
	var newest *keiBackup
	if !fresh {
		for i := range group {
			if newest == nil || group[i].newerThan(*newest) {
				newest = &group[i]
			}
		}
	}
	var stale []string
	for _, b := range group {
		if b.path == keep || (newest != nil && b.path == newest.path) {
			continue
		}
		stale = append(stale, b.path)
	}
	return stale
}

// pruneFileBackups removes (or, on a dry run, lists) the Kei backups of
// files that a successful sync supersedes, leaving at most one per file.
// fresh maps a file the sync backed up to that backup (empty on a dry run).
func pruneFileBackups(files []string, fresh map[string]string, dryRun bool, out io.Writer) error {
	var stale []string
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file] {
			continue
		}
		seen[file] = true
		group, err := fileBackups(file)
		if err != nil {
			return err
		}
		keep, isFresh := fresh[file]
		stale = append(stale, superseded(group, isFresh, keep)...)
	}
	return removeBackups(stale, dryRun, out)
}

// pruneSkillBackups does the same for kind's skill backups: fresh maps a
// skill the sync backed up to that backup.
func pruneSkillBackups(env Env, kind string, fresh map[string]string, dryRun bool, out io.Writer) error {
	groups, err := skillBackups(env, kind)
	if err != nil {
		return err
	}
	var stale []string
	for name, group := range groups {
		keep, isFresh := fresh[name]
		stale = append(stale, superseded(group, isFresh, keep)...)
	}
	return removeBackups(stale, dryRun, out)
}

func removeBackups(stale []string, dryRun bool, out io.Writer) error {
	sort.Strings(stale)
	for _, path := range stale {
		if dryRun {
			fmt.Fprintf(out, "Would remove earlier Kei backup %s\n", path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed earlier Kei backup %s\n", path)
	}
	return nil
}
