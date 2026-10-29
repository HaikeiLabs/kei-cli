package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultReleaseBase = "https://kei-cli-releases.s3.us-east-1.amazonaws.com"

// piUpdateCommand is the exact command that updates the kei-lime-pi Pi
// extension. kei upgrade prints it and never runs it: pi update shells out to
// git over SSH and can prompt for signing, which would hang an unattended
// upgrade.
const piUpdateCommand = "pi update git:github.com/HaikeiLabs/kei-lime-pi"

// piExtensionInstalled reports whether the kei-lime-pi Pi extension is
// configured at user scope. Tests override it; the default reads Pi's user
// settings and never executes pi, git, or ssh.
var piExtensionInstalled = probePiExtensionInstalled

// probePiExtensionInstalled looks for the extension's package reference in
// Pi's user settings (~/.pi/agent/settings.json). Package entries are either
// source strings or objects with a source field, so it checks for the
// repository path anywhere in the file. Project-local installs
// (pi install -l) live in the project's .pi/settings.json and are not
// detected.
func probePiExtensionInstalled() bool {
	home, err := userHomeDirFn()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "settings.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "github.com/HaikeiLabs/kei-lime-pi")
}

// printPiUpdateHint reminds a Pi user that kei upgrade never runs pi update
// and prints the exact command to update the extension instead.
func printPiUpdateHint(stdout io.Writer) {
	if !piExtensionInstalled() {
		return
	}
	fmt.Fprintf(stdout, "kei upgrade does not run pi update; to update the Pi extension run: %s\n", piUpdateCommand)
}

var releaseVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9._-]+)?$`)

// runUpgradeCommand installs the published platform binary directly from S3.
// The archive checksum is verified before extraction or replacement.
func runUpgradeCommand(args []string, stdout, stderr io.Writer, client *http.Client, executable func() (string, error), getenv func(string) string) int {
	lc := newCommandFlags("", "upgrade", "Upgrade the CLI to a newer release", stdout, stderr)
	requested := lc.fs.String("version", "latest", "release version to install (default: latest)")
	force := lc.fs.Bool("force", false, "reinstall even when the running version already matches the target")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "upgrade accepts no positional arguments")
		return 2
	}
	version := strings.TrimSpace(*requested)
	explicit := version != "latest"
	if explicit {
		if !releaseVersionPattern.MatchString(version) || strings.ContainsAny(version, "/\\") {
			fmt.Fprintln(stderr, "upgrade failed: invalid version; expected VERSION or vVERSION")
			return 2
		}
		version = strings.TrimPrefix(version, "v")
	}
	osName, arch, err := releasePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: %v\n", err)
		return 1
	}
	base := strings.TrimRight(getenv("AWS_S3_RELEASES_URL_BASE"), "/")
	if base == "" {
		base = defaultReleaseBase
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	current, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: locate current executable: %v\n", err)
		return 1
	}

	if !explicit {
		version, err = resolveReleaseVersion(client, base, "kei-cli")
		if err != nil {
			fmt.Fprintf(stderr, "upgrade failed: resolve latest kei release: %v\n", err)
			return 1
		}
	}

	// Read the versions of the binaries we are about to replace, before we
	// touch them, so the result can report the transition.
	proxyPath := filepath.Join(filepath.Dir(current), "kei-proxy")
	runningKei, keiKnown := binaryVersion(current, "kei")
	runningProxy, proxyKnown := binaryVersion(proxyPath, "kei-proxy")
	if !keiKnown {
		runningKei = "(unknown)"
	}
	if !proxyKnown {
		runningProxy = "(none)"
	}

	// Never "upgrade" to the version that is already running, and never let a
	// stale latest.txt silently downgrade the running binary.
	if keiKnown {
		switch compareVersions(version, runningKei) {
		case 0:
			if !*force {
				fmt.Fprintf(stdout, "kei %s is already the latest release\n", version)
				printPiUpdateHint(stdout)
				return 0
			}
		case -1:
			if !explicit {
				fmt.Fprintf(stderr, "upgrade failed: latest release %s is older than the running version %s; pass --version to install it explicitly\n", version, runningKei)
				return 1
			}
		}
	}

	cliArchiveName := fmt.Sprintf("kei-cli_%s_%s_%s.tar.gz", version, osName, arch)
	cliArchive, err := fetchVerifiedArchive(client, base, "kei-cli", version, cliArchiveName)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: kei release: %v\n", err)
		return 1
	}
	cliStage, err := extractNamedReleaseBinary(cliArchive, "kei", filepath.Dir(current))
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: extract kei binary: %v\n", err)
		return 1
	}
	defer os.Remove(cliStage)
	// Install the kei-proxy bundled in the target kei-cli archive, as
	// install.sh does. The release pins it per kei-cli version and the
	// archive checksum covers it, so it is never the latest kei-proxy release
	// and never the pin of a different kei-cli release.
	proxyStage, err := extractNamedReleaseBinary(cliArchive, "kei-proxy", filepath.Dir(proxyPath))
	bundledProxy := err == nil
	if err != nil && !errors.Is(err, errBinaryNotInArchive) {
		fmt.Fprintf(stderr, "upgrade failed: extract kei-proxy binary: %v\n", err)
		return 1
	}
	newProxyVersion := runningProxy
	proxyTransition := runningProxy + " (unchanged; release bundles no kei-proxy)"
	if bundledProxy {
		defer os.Remove(proxyStage)
		proxyVersion, ok := binaryVersion(proxyStage, "kei-proxy")
		if !ok {
			proxyVersion = "(unknown)"
		}
		newProxyVersion = proxyVersion
		proxyTransition = runningProxy + " -> " + proxyVersion
	}
	if err := os.Rename(cliStage, current); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", current, err)
		return 1
	}
	if bundledProxy {
		if err := os.Rename(proxyStage, proxyPath); err != nil {
			fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", proxyPath, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "kei %s -> %s, kei-proxy %s\n", runningKei, version, proxyTransition)
	// The service (if installed) still runs the pre-upgrade binaries until it
	// is restarted; do that now. A no-op upgrade returned earlier, so this only
	// runs after the binaries were actually replaced.
	if restarted, err := restartRuntimeService(); err != nil {
		fmt.Fprintf(stderr, "warning: could not restart the Kei runtime service: %v\n", err)
	} else if restarted {
		fmt.Fprintf(stdout, "Restarted the Kei runtime service; it now runs kei %s / kei-proxy %s.\n", version, newProxyVersion)
	}
	// The kei-proxy serve agent (if installed) also still runs the pre-upgrade
	// binary until it is restarted; do that now, the same way the runtime
	// service is restarted. A failure is a warning: the upgrade itself
	// succeeded.
	if restarted, err := restartServeAgent(); err != nil {
		fmt.Fprintf(stderr, "warning: could not restart the kei-proxy serve agent: %v\n", err)
	} else if restarted {
		fmt.Fprintf(stdout, "Restarted the kei-proxy serve agent (%s).\n", serveAgentNameFor(runtime.GOOS))
	}
	printPiUpdateHint(stdout)
	return 0
}

func releasePlatform(goos, goarch string) (string, string, error) {
	var osName string
	switch goos {
	case "darwin":
		osName = "macOS"
	case "linux":
		osName = "Linux"
	default:
		return "", "", fmt.Errorf("unsupported operating system %s (releases support macOS and Linux)", goos)
	}
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture %s (releases support amd64 and arm64)", goarch)
	}
	return osName, arch, nil
}

func resolveReleaseVersion(client *http.Client, base, project string) (string, error) {
	data, err := fetchRelease(client, base+"/"+project+"/latest.txt", 4096)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if !releaseVersionPattern.MatchString(version) || strings.ContainsAny(version, "/\\") {
		return "", errors.New("release endpoint returned an invalid version")
	}
	return strings.TrimPrefix(version, "v"), nil
}

// binaryVersion reports the version a built binary prints for --version,
// from a first line such as "<name> 1.2.3" or "<name> version v1.2.3". It
// returns ok=false when the binary is missing or prints no release version,
// so callers can degrade gracefully.
func binaryVersion(path, name string) (string, bool) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != name {
		return "", false
	}
	v := fields[len(fields)-1]
	if !releaseVersionPattern.MatchString(v) {
		return "", false
	}
	return strings.TrimPrefix(v, "v"), true
}

// compareVersions orders two release versions by semver. It returns -1 if a
// is older than b, 0 if they are equal, and 1 if a is newer. A release
// (no prerelease tag) sorts after its own prereleases.
func compareVersions(a, b string) int {
	an, ap := numericAndPrerelease(a)
	bn, bp := numericAndPrerelease(b)
	for i := range an {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	if ap == bp {
		return 0
	}
	if ap == "" {
		return 1
	}
	if bp == "" {
		return -1
	}
	if ap < bp {
		return -1
	}
	return 1
}

func numericAndPrerelease(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	prerelease := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		prerelease = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var nums [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		nums[i] = n
	}
	return nums, prerelease
}

func fetchVerifiedArchive(client *http.Client, base, project, version, archiveName string) ([]byte, error) {
	prefix := base + "/" + project + "/" + version + "/"
	archive, err := fetchRelease(client, prefix+archiveName, 512<<20)
	if err != nil {
		return nil, fmt.Errorf("download archive: %w", err)
	}
	checksumsName := fmt.Sprintf("%s_%s_checksums.txt", project, version)
	checksums, err := fetchRelease(client, prefix+checksumsName, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyReleaseChecksum(archiveName, archive, checksums); err != nil {
		return nil, err
	}
	return archive, nil
}

func fetchRelease(client *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds size limit", url)
	}
	return data, nil
}

func verifyReleaseChecksum(name string, archive, checksums []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			if expected != "" {
				return fmt.Errorf("checksums contain duplicate entries for %s", name)
			}
			expected = fields[0]
		}
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%s is missing a valid SHA-256 checksum", name)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("%s has an invalid SHA-256 checksum", name)
	}
	sum := sha256.Sum256(archive)
	if !strings.EqualFold(expected, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

var errBinaryNotInArchive = errors.New("binary not found in release archive")

func extractReleaseBinary(archive []byte, destination string) (string, error) {
	return extractNamedReleaseBinary(archive, "kei", destination)
}

func extractNamedReleaseBinary(archive []byte, binaryName, destination string) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Name != binaryName {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() || header.Size <= 0 || header.Size > 256<<20 {
			return "", errors.New(fmt.Sprintf("archive contains an invalid %s binary", binaryName))
		}
		f, err := os.CreateTemp(destination, ".kei-upgrade-*")
		if err != nil {
			return "", err
		}
		if _, err = io.CopyN(f, tr, header.Size); err == nil {
			err = f.Chmod(0o755)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(f.Name())
			return "", err
		}
		return f.Name(), nil
	}
	return "", fmt.Errorf("%s: %w", binaryName, errBinaryNotInArchive)
}
