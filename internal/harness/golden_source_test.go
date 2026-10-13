package harness

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyPrecedenceGoldenSource guards the vendored policy-precedence
// golden against drift: the file's git blob SHA must equal the blob_sha its
// .source sidecar records. Editing the golden without re-syncing it from
// upstream (and updating the sidecar) fails here.
func TestPolicyPrecedenceGoldenSource(t *testing.T) {
	dir := filepath.Join("testdata", "annotations")
	goldenPath := filepath.Join(dir, "policy-precedence.golden.json")
	sourcePath := filepath.Join(dir, "policy-precedence.golden.source")

	src, err := readGoldenSource(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repo", "path", "ref", "blob_sha"} {
		if src[key] == "" {
			t.Fatalf("%s: missing %q", sourcePath, key)
		}
	}

	content, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitBlobSHA(content); got != src["blob_sha"] {
		t.Errorf("%s has drifted: git blob SHA is %s, %s records %s.\n"+
			"Re-sync it from %s %s at %s (keep the header), then set blob_sha to the output of\n"+
			"  git hash-object internal/harness/%s",
			goldenPath, got, sourcePath, src["blob_sha"], src["repo"], src["path"], src["ref"], filepath.ToSlash(goldenPath))
	}
}

// gitBlobSHA is the SHA git assigns a blob: sha1 of "blob <len>\0" + content.
func gitBlobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// readGoldenSource parses a sidecar of "key: value" lines; blank lines and
// lines starting with # are ignored.
func readGoldenSource(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("%s: malformed line %q", path, line)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields, sc.Err()
}
