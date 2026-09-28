package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const defaultReleaseBase = "https://releases.haikeilabs.com"

var releaseVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9._-]+)?$`)

// runUpgradeCommand installs the published platform binary directly from S3.
// The archive checksum is verified before extraction or replacement.
func runUpgradeCommand(args []string, stdout, stderr io.Writer, client *http.Client, executable func() (string, error), getenv func(string) string) int {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requested := flags.String("version", "latest", "release version to install (default: latest)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "upgrade accepts no positional arguments")
		return 2
	}
	version := strings.TrimSpace(*requested)
	if version != "latest" {
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

	tmp, err := os.MkdirTemp("", "kei-upgrade-*")
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: create temporary directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	if version == "latest" {
		fmt.Fprintln(stdout, "Resolving latest kei release...")
		data, err := fetchRelease(client, base+"/kei-cli/latest.txt", 4096)
		if err != nil {
			fmt.Fprintf(stderr, "upgrade failed: resolve latest release: %v\n", err)
			return 1
		}
		version = strings.TrimSpace(string(data))
		if !releaseVersionPattern.MatchString(version) || strings.ContainsAny(version, "/\\") {
			fmt.Fprintln(stderr, "upgrade failed: release endpoint returned an invalid version")
			return 1
		}
	}

	archiveName := fmt.Sprintf("kei-cli_%s_%s_%s.tar.gz", version, osName, arch)
	checksumsName := fmt.Sprintf("kei-cli_%s_checksums.txt", version)
	prefix := base + "/kei-cli/" + version + "/"
	fmt.Fprintf(stdout, "Downloading kei %s for %s/%s...\n", version, osName, arch)
	archive, err := fetchRelease(client, prefix+archiveName, 512<<20)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: download release archive: %v\n", err)
		return 1
	}
	checksums, err := fetchRelease(client, prefix+checksumsName, 1<<20)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: download checksums: %v\n", err)
		return 1
	}
	if err := verifyReleaseChecksum(archiveName, archive, checksums); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: %v\n", err)
		return 1
	}

	current, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: locate current executable: %v\n", err)
		return 1
	}
	staged, err := extractReleaseBinary(archive, filepath.Dir(current))
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: extract release binary: %v\n", err)
		return 1
	}
	defer os.Remove(staged)
	if err := os.Rename(staged, current); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", current, err)
		fmt.Fprintf(stderr, "Verified new binary is staged at %s; copy it over manually if needed.\n", staged)
		return 1
	}
	fmt.Fprintf(stdout, "Upgraded kei to %s at %s.\n", version, current)
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

func extractReleaseBinary(archive []byte, destination string) (string, error) {
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
		if header.Name != "kei" {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() || header.Size <= 0 || header.Size > 256<<20 {
			return "", errors.New("archive contains an invalid kei binary")
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
	return "", errors.New("kei binary not found in release archive")
}
