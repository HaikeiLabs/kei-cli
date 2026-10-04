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

const defaultReleaseBase = "https://kei-cli-releases.s3.us-east-1.amazonaws.com"

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
		version, err = resolveReleaseVersion(client, base, "kei-cli")
		if err != nil {
			fmt.Fprintf(stderr, "upgrade failed: resolve latest kei release: %v\n", err)
			return 1
		}
	}
	proxyVersion, err := resolveReleaseVersion(client, base, "kei-proxy")
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: resolve latest kei-proxy release: %v\n", err)
		return 1
	}

	current, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: locate current executable: %v\n", err)
		return 1
	}
	cliArchiveName := fmt.Sprintf("kei-cli_%s_%s_%s.tar.gz", version, osName, arch)
	cliArchive, err := fetchVerifiedArchive(client, base, "kei-cli", version, cliArchiveName)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: kei release: %v\n", err)
		return 1
	}
	proxyArchiveName := fmt.Sprintf("kei-proxy_%s_%s_%s.tar.gz", proxyVersion, osName, arch)
	proxyArchive, err := fetchVerifiedArchive(client, base, "kei-proxy", proxyVersion, proxyArchiveName)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: kei-proxy release: %v\n", err)
		return 1
	}
	cliStage, err := extractNamedReleaseBinary(cliArchive, "kei", filepath.Dir(current))
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: extract kei binary: %v\n", err)
		return 1
	}
	defer os.Remove(cliStage)
	proxyPath := filepath.Join(filepath.Dir(current), "kei-proxy")
	proxyStage, err := extractNamedReleaseBinary(proxyArchive, "kei-proxy", filepath.Dir(proxyPath))
	if err != nil {
		fmt.Fprintf(stderr, "upgrade failed: extract kei-proxy binary: %v\n", err)
		return 1
	}
	defer os.Remove(proxyStage)
	if err := os.Rename(cliStage, current); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", current, err)
		return 1
	}
	if err := os.Rename(proxyStage, proxyPath); err != nil {
		fmt.Fprintf(stderr, "upgrade failed: replace %s: %v\n", proxyPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "Upgraded kei to %s and kei-proxy to %s in %s.\n", version, proxyVersion, filepath.Dir(current))
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
	return "", fmt.Errorf("%s binary not found in release archive", binaryName)
}
