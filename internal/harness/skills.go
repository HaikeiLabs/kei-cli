package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	policybundle "github.com/HaikeiLabs/kei-connector-contracts/bundle"
	"github.com/HaikeiLabs/kei-connector-contracts/contract"
	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
	policyapi "github.com/HaikeiLabs/kei-connector-contracts/policy"
	"github.com/HaikeiLabs/kei-connector-contracts/providers"
)

// SkillSource is one skills_repo connector (HAI-431, HAI-723). The harness
// reads skills directly from the repository; this package never calls the
// repository host or reads a credential.
type SkillSource interface {
	ListSkills(ctx context.Context) (providers.SkillsRepoListing, error)
	FetchSkill(ctx context.Context, name string) (providers.SkillsRepoArchive, error)
}

// SkillsOpts controls one InstallSkills.
type SkillsOpts struct {
	Env Env
	// DryRun prints the per-harness plan and writes nothing.
	DryRun bool
	// Out receives the plan; it must not be nil.
	Out io.Writer
	// Now stamps the skill backups.
	Now time.Time
}

// SkillsResult is what an InstallSkills did beyond the plan it printed.
type SkillsResult struct {
	// Warnings are printed to stderr: skipped user-owned skills and
	// listing entries that were ignored.
	Warnings []string
}

// skillLedger records one Kei-managed skill copy in the harness's sync
// ledger. A skill dir entry without a record is the user's own.
type skillLedger struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest"`
	Commit  string `json:"commit"`
}

// SkillsRepoConnectors returns the ids of the bundle's skills_repo
// connectors (policy_set.connectors), in bundle order.
func SkillsRepoConnectors(b Bundle) ([]string, error) {
	var set struct {
		Connectors []policybundle.Connector `json:"connectors"`
	}
	if len(b.PolicySet) > 0 {
		if err := json.Unmarshal(b.PolicySet, &set); err != nil {
			return nil, fmt.Errorf("decode bundle connectors: %w", err)
		}
	}
	var ids []string
	for _, c := range set.Connectors {
		if c.Provider == string(contract.ProviderSkillsRepo) && c.ID != "" {
			ids = append(ids, c.ID)
		}
	}
	return ids, nil
}

// skillPermitted reports whether the bundle permits the skill on a harness
// of kind: the first policy matching the skill call (kei.harness-match/v1)
// must be a permit whose dst is a skill: pattern. A deny, no match, or a
// source the renderer cannot resolve is not a permit.
func skillPermitted(kind string, b Bundle, policies []bundlePolicy, name string) bool {
	call := harnessmatch.Call{Kind: kind, Skill: name}
	subject := b.Subject
	if b.Schema != BundleSchemaV2 {
		subject = nil
	}
	matchPolicies := make([]harnessmatch.Policy, 0, len(policies))
	for _, p := range policies {
		effect := p.Effect
		if effect == "" {
			effect = p.Action
		}
		src := p.SrcPattern
		if _, human := harnessmatch.MatchSrc(src, call); human && subject != nil && policyapi.MatchPrincipal(src, subject.UserID, subject.Email, subject.Groups) {
			src = "*"
		}
		matchPolicies = append(matchPolicies, harnessmatch.Policy{ID: p.ID, Src: src, Dst: p.DstPattern, Action: effect, Enabled: p.Enabled, Scope: p.Scope.AgentID})
	}
	result := harnessmatch.Evaluate(call, matchPolicies)
	if result.Outcome != harnessmatch.OutcomePermit {
		return false
	}
	for _, p := range matchPolicies {
		if p.ID == result.PolicyID {
			return strings.HasPrefix(p.Dst, "skill:")
		}
	}
	return false
}

// skillForHarness reports whether a skill's harnesses list (empty for every
// harness) names h by kind or import name.
func skillForHarness(skill providers.SkillsRepoSkill, h Harness) bool {
	if len(skill.Harnesses) == 0 || slices.Contains(skill.Harnesses, h.Kind()) {
		return true
	}
	n, ok := h.(ImportNamer)
	return ok && slices.Contains(skill.Harnesses, n.ImportName())
}

// catalogSkill is a listed skill and the connector it came from.
type catalogSkill struct {
	skill  providers.SkillsRepoSkill
	commit string
	source SkillSource
}

// skillAction is one planned change in a skill dir.
type skillAction struct {
	verb   string // install, update, remove, skip-user-owned
	name   string
	target string
	entry  catalogSkill
}

const (
	skillInstall       = "install"
	skillUpdate        = "update"
	skillRemove        = "remove"
	skillSkipUserOwned = "skip-user-owned"
)

// InstallSkills installs the org skills the bundle permits into each
// harness's skill dirs as Kei-managed copies, and removes the Kei-managed
// copies of skills no longer permitted. A skill dir entry Kei did not
// install is never touched: a listed skill of the same name is skipped with
// a warning. Archives are verified against their digest and extracted
// atomically (a temp dir beside the target, then a rename). Every replaced
// or removed copy is backed up under the ledger dir, and each skill keeps
// only its newest backup once the harness's skills sync succeeds.
func InstallSkills(ctx context.Context, sources []SkillSource, b Bundle, hs []Harness, opts SkillsOpts) (SkillsResult, error) {
	var result SkillsResult
	var set struct {
		Policies []bundlePolicy `json:"policies"`
	}
	if len(b.PolicySet) > 0 {
		if err := json.Unmarshal(b.PolicySet, &set); err != nil {
			return result, fmt.Errorf("decode bundle policies: %w", err)
		}
	}
	var names []string
	catalog := map[string]catalogSkill{}
	for _, source := range sources {
		listing, err := source.ListSkills(ctx)
		if err != nil {
			return result, fmt.Errorf("list skills: %w", err)
		}
		for _, skill := range listing.Skills {
			if err := providers.ValidateSkillName(skill.Name); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("skills: ignored listed skill %q: %v", skill.Name, err))
				continue
			}
			if _, dup := catalog[skill.Name]; dup {
				result.Warnings = append(result.Warnings, fmt.Sprintf("skills: %s is in more than one skills repository; using the first", skill.Name))
				continue
			}
			catalog[skill.Name] = catalogSkill{skill: skill, commit: listing.Commit, source: source}
			names = append(names, skill.Name)
		}
	}
	in := skillInstaller{opts: opts, archives: map[string]providers.SkillsRepoArchive{}}
	for _, h := range hs {
		dirs := h.SkillDirs(opts.Env)
		if len(dirs) == 0 {
			continue
		}
		in.fresh = map[string]string{}
		var desired []catalogSkill
		for _, name := range names {
			entry := catalog[name]
			if skillForHarness(entry.skill, h) && skillPermitted(h.Kind(), b, set.Policies, name) {
				desired = append(desired, entry)
			}
		}
		for _, dir := range dirs {
			warnings, err := in.syncDir(ctx, h, dir, desired)
			result.Warnings = append(result.Warnings, warnings...)
			if err != nil {
				return result, err
			}
		}
		if err := pruneSkillBackups(opts.Env, h.Kind(), in.fresh, opts.DryRun, opts.Out); err != nil {
			return result, fmt.Errorf("remove earlier Kei skill backups: %w", err)
		}
	}
	return result, nil
}

// skillInstaller fetches each archive once across harnesses.
type skillInstaller struct {
	opts     SkillsOpts
	archives map[string]providers.SkillsRepoArchive
	// fresh maps each skill of the current harness that was backed up to
	// its backup (empty on a dry run, where the backup is only planned).
	fresh map[string]string
}

// fetch returns the verified archive of entry's skill.
func (in *skillInstaller) fetch(ctx context.Context, entry catalogSkill) (providers.SkillsRepoArchive, error) {
	name := entry.skill.Name
	if archive, ok := in.archives[name]; ok {
		return archive, nil
	}
	archive, err := entry.source.FetchSkill(ctx, name)
	if err != nil {
		return archive, fmt.Errorf("fetch skill %s: %w", name, err)
	}
	if err := verifySkillArchive(name, archive); err != nil {
		return archive, err
	}
	in.archives[name] = archive
	return archive, nil
}

// verifySkillArchive checks a skills.fetch result before anything is
// unpacked: the skill it names, its format, its size and its digest.
func verifySkillArchive(name string, archive providers.SkillsRepoArchive) error {
	if archive.Skill.Name != name {
		return fmt.Errorf("skill %s: fetch returned skill %q", name, archive.Skill.Name)
	}
	if archive.Format != providers.SkillsRepoArchiveFormat {
		return fmt.Errorf("skill %s: archive format %q, want %s", name, archive.Format, providers.SkillsRepoArchiveFormat)
	}
	if archive.Size != len(archive.Archive) {
		return fmt.Errorf("skill %s: archive is %d bytes, fetch reported %d", name, len(archive.Archive), archive.Size)
	}
	sum := sha256.Sum256(archive.Archive)
	if want := "sha256:" + hex.EncodeToString(sum[:]); archive.Digest != want {
		return fmt.Errorf("skill %s: archive digest mismatch (fetch reported %s)", name, archive.Digest)
	}
	return nil
}

// syncDir plans and, unless DryRun, applies the changes to one skill dir,
// recording Kei-managed copies in the harness kind's sync ledger.
func (in *skillInstaller) syncDir(ctx context.Context, h Harness, dir string, desired []catalogSkill) ([]string, error) {
	opts := in.opts
	ledgerFile := ledgerPath(opts.Env, h.Kind())
	// The skills share the kind's sync ledger with the permission entries,
	// but only a permission sync sets its identity: a skills-only ledger
	// still reads as not synced to drift checks.
	ledger := readLedger(ledgerFile)
	if ledger.Skills == nil {
		ledger.Skills = map[string]skillLedger{}
	}
	var actions []skillAction
	var warnings []string
	refreshed := false
	wanted := map[string]bool{}
	for _, entry := range desired {
		name := entry.skill.Name
		target := filepath.Join(dir, name)
		wanted[target] = true
		record, managed := ledger.Skills[target]
		_, statErr := os.Lstat(target)
		exists := statErr == nil
		switch {
		case !managed && exists:
			warnings = append(warnings, fmt.Sprintf("skills: skipped %s in %s: a skill Kei did not install has that name", name, dir))
			actions = append(actions, skillAction{verb: skillSkipUserOwned, name: name, target: target, entry: entry})
		case !exists:
			actions = append(actions, skillAction{verb: skillInstall, name: name, target: target, entry: entry})
		case record.Commit == entry.commit && entry.commit != "":
			// Unchanged since the snapshot it was installed from.
		default:
			archive, err := in.fetch(ctx, entry)
			if err != nil {
				return warnings, err
			}
			if archive.Digest != record.Digest {
				actions = append(actions, skillAction{verb: skillUpdate, name: name, target: target, entry: entry})
				continue
			}
			record.Commit, record.Version = entry.commit, entry.skill.Version
			ledger.Skills[target] = record
			refreshed = true
		}
	}
	var stale []string
	for target := range ledger.Skills {
		if filepath.Dir(target) == dir && !wanted[target] {
			stale = append(stale, target)
		}
	}
	sort.Strings(stale)
	for _, target := range stale {
		actions = append(actions, skillAction{verb: skillRemove, name: ledger.Skills[target].Name, target: target})
	}

	printSkillPlan(opts.Out, harnessDisplayName(h), dir, opts.DryRun, actions)
	if opts.DryRun {
		for _, action := range actions {
			if action.verb == skillUpdate || action.verb == skillRemove {
				in.fresh[action.name] = ""
			}
		}
		// The plan must name updates correctly, so fetch is allowed, but
		// nothing below writes.
		return warnings, nil
	}
	if refreshed {
		if err := writeLedger(ledgerFile, ledger); err != nil {
			return warnings, err
		}
	}
	for _, action := range actions {
		var err error
		switch action.verb {
		case skillInstall, skillUpdate:
			var archive providers.SkillsRepoArchive
			if archive, err = in.fetch(ctx, action.entry); err != nil {
				return warnings, err
			}
			if err = in.place(h.Kind(), dir, action, archive); err != nil {
				return warnings, err
			}
			ledger.Skills[action.target] = skillLedger{Name: action.name, Version: action.entry.skill.Version, Digest: archive.Digest, Commit: action.entry.commit}
		case skillRemove:
			if err = in.remove(h.Kind(), action); err != nil {
				return warnings, err
			}
			delete(ledger.Skills, action.target)
		default:
			continue
		}
		// Record each change as it lands, so a later failure never leaves a
		// Kei copy that the next run would mistake for the user's own.
		if err := writeLedger(ledgerFile, ledger); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

func harnessDisplayName(h Harness) string {
	if d, ok := h.(interface{ displayName() string }); ok {
		return d.displayName()
	}
	return h.Kind()
}

func printSkillPlan(out io.Writer, harnessName, dir string, dryRun bool, actions []skillAction) {
	suffix := ""
	if dryRun {
		suffix = " (dry run)"
	}
	if len(actions) == 0 {
		fmt.Fprintf(out, "Skills for %s in %s: up to date%s\n", harnessName, dir, suffix)
		return
	}
	fmt.Fprintf(out, "Skills for %s in %s%s:\n", harnessName, dir, suffix)
	for _, action := range actions {
		line := "  " + action.verb + " " + action.name
		switch action.verb {
		case skillInstall, skillUpdate:
			if v := action.entry.skill.Version; v != "" {
				line += " " + v
			}
		case skillSkipUserOwned:
			line += ": a skill Kei did not install has that name"
		}
		fmt.Fprintln(out, line)
	}
}

// place extracts archive into a temp dir beside the target and renames it
// into place, backing up and replacing the previous Kei-managed copy. The
// previous copy is restored, and its fresh backup deleted, if that fails.
func (in *skillInstaller) place(kind, dir string, action skillAction, archive providers.SkillsRepoArchive) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".kei-skill-"+action.name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extractSkill(archive.Archive, action.name, tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if action.verb == skillInstall {
		return os.Rename(tmp, action.target)
	}
	backup, err := in.backup(kind, action)
	if err != nil {
		return err
	}
	old := tmp + ".old"
	if err := os.Rename(action.target, old); err != nil {
		_ = os.RemoveAll(backup)
		return err
	}
	if err := os.Rename(tmp, action.target); err != nil {
		_ = os.Rename(old, action.target)
		_ = os.RemoveAll(backup)
		return err
	}
	in.fresh[action.name] = backup
	return os.RemoveAll(old)
}

// remove backs up and deletes a Kei-managed copy. It is first moved aside
// within the skill dir, so a failed delete never leaves a partial skill.
func (in *skillInstaller) remove(kind string, action skillAction) error {
	if _, err := os.Lstat(action.target); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	backup, err := in.backup(kind, action)
	if err != nil {
		return err
	}
	aside, err := os.MkdirTemp(filepath.Dir(action.target), ".kei-skill-"+action.name+"-")
	if err == nil {
		if err = os.Rename(action.target, filepath.Join(aside, action.name)); err != nil {
			_ = os.Remove(aside)
		}
	}
	if err != nil {
		_ = os.RemoveAll(backup)
		return err
	}
	in.fresh[action.name] = backup
	return os.RemoveAll(aside)
}

// backup copies a Kei-managed skill folder under the ledger dir, outside
// every skill dir so the harness never loads a backup as a skill, and
// returns the copy's path.
func (in *skillInstaller) backup(kind string, action skillAction) (string, error) {
	base := filepath.Join(skillBackupsDir(in.opts.Env, kind), action.name+"-"+in.opts.Now.UTC().Format(backupStamp))
	dst := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s-%d", base, i)
	}
	if err := copySkillTree(action.target, dst); err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	return dst, nil
}

// copySkillTree copies the folders and regular files under src to dst.
// Anything else (a link a user added) is not followed or copied.
func copySkillTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o700)
		case d.Type().IsRegular():
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(out, body, 0o600)
		}
		return nil
	})
}

// extractSkill unpacks a skills.fetch archive of skill name into dest. Every
// entry must be a folder or regular file under name/; an absolute path, a ..
// segment, another skill's folder, or any link is refused, and the
// contract's file count and size bounds are enforced.
func extractSkill(archive []byte, name, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("skill %s: %w", name, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := 0
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("skill %s: %w", name, err)
		}
		rel, err := skillEntryPath(name, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dest, rel), 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if rel == "." {
				return fmt.Errorf("skill %s: archive entry %q is not a file in the skill folder", name, hdr.Name)
			}
			files++
			total += hdr.Size
			if files > providers.SkillsRepoMaxFiles || hdr.Size > providers.SkillsRepoMaxFileBytes || total > providers.SkillsRepoMaxArchiveBytes {
				return fmt.Errorf("skill %s: archive exceeds the skills_repo bounds", name)
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			out := filepath.Join(dest, rel)
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, hdr.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("skill %s: archive entry %q is not a file or folder (links are refused)", name, hdr.Name)
		}
	}
}

// skillEntryPath maps an archive entry name/<path> to its path inside the
// skill folder ("." for the folder itself), refusing any entry that would
// land outside it.
func skillEntryPath(name, entry string) (string, error) {
	unsafe := fmt.Errorf("skill %s: archive entry %q is outside the skill folder", name, entry)
	if entry == "" || path.IsAbs(entry) || strings.Contains(entry, `\`) || strings.Contains(entry, ":") {
		return "", unsafe
	}
	for _, segment := range strings.Split(entry, "/") {
		if segment == ".." {
			return "", unsafe
		}
	}
	cleaned := path.Clean(entry)
	if cleaned == name {
		return ".", nil
	}
	rel, ok := strings.CutPrefix(cleaned, name+"/")
	if !ok {
		return "", unsafe
	}
	return filepath.FromSlash(rel), nil
}
