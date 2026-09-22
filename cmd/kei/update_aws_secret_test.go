package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateAwsSecretScriptIsSecure statically checks the operator helper that
// consumes the `bot credential --rotate` stream. It must exist, be executable,
// parse cleanly, read the token from stdin, and hand it to jq/AWS via file
// rather than argv so it never reaches the process list.
func TestUpdateAwsSecretScriptIsSecure(t *testing.T) {
	scriptPath := filepath.Join(findRepoRoot(t), "scripts", "update-aws-secret.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script not found: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("script is not executable: %s", scriptPath)
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{"set -euo pipefail", "--rawfile", "file://", "read -r"} {
		if !strings.Contains(text, want) {
			t.Fatalf("script missing secure property %q", want)
		}
	}
	for _, leak := range []string{`--secret-string "$TOKEN"`, `--secret-string $TOKEN`, `--from-literal=`} {
		if strings.Contains(text, leak) {
			t.Fatalf("script passes the token in argv: %q", leak)
		}
	}
}
