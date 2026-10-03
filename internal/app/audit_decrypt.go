package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"filippo.io/age"
)

type auditRecord struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Decision        string   `json:"decision,omitempty"`
	ArgsDigest      string   `json:"args_digest,omitempty"`
	DigestKeyID     string   `json:"digest_key_id,omitempty"`
	ArgsCiphertext  string   `json:"args_ciphertext,omitempty"`
	RecipientKeyIDs []string `json:"recipient_key_ids,omitempty"`
}

func loadAgeIdentity(path string) (age.Identity, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read identity file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	var keyStr string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			keyStr = line
			break
		}
	}
	if keyStr == "" {
		return nil, "", fmt.Errorf("no age secret key found in %q", path)
	}
	identity, err := age.ParseX25519Identity(keyStr)
	if err != nil {
		return nil, "", fmt.Errorf("parse identity: %w", err)
	}
	return identity, identity.Recipient().String(), nil
}

// runAuditDecryptCommand fetches an audit record, decrypts the args_ciphertext
// field with the age identity, and writes the plaintext to a file or stdout.
// Behavior is specified in ADR-030:
// https://github.com/HaikeiLabs/kei/blob/main/docs/adr/030-audit-args-encryption.md
func runAuditDecryptCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	flags := flag.NewFlagSet("audit decrypt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	recordID := flags.String("record", "", "audit record ID to decrypt")
	identityPath := flags.String("identity", "", "path to age identity file (default ~/.config/kei/audit-identity.txt)")
	outPath := flags.String("out", "", "output file path")
	toStdout := flags.Bool("stdout", false, "print decrypted plaintext to stdout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "audit decrypt accepts no positional arguments")
		return 2
	}
	if *recordID == "" {
		fmt.Fprintln(stderr, "audit decrypt: --record is required")
		return 2
	}

	idPath := *identityPath
	if idPath == "" {
		var err error
		idPath, err = defaultIdentityPath()
		if err != nil {
			fmt.Fprintf(stderr, "audit decrypt: %v\n", err)
			return 1
		}
	}

	identity, _, err := loadAgeIdentity(idPath)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: %v\n", err)
		return 1
	}

	baseURL, token, ok := loadCLIWebTokenAndBaseURL(store, stderr)
	if !ok {
		return 1
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: cannot determine organization; run kei login again\n")
		return 1
	}

	target := baseURL + "/api/v1/organizations/" + url.PathEscape(orgID) + "/auditRecords/" + url.PathEscape(*recordID)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintln(stderr, "audit decrypt: not logged in; run kei login first")
		return 1
	}
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(stderr, "audit decrypt: record %q not found\n", *recordID)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "audit decrypt: server returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	var record auditRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		fmt.Fprintf(stderr, "audit decrypt: decode record: %v\n", err)
		return 1
	}

	if record.ArgsCiphertext == "" {
		fmt.Fprintln(stderr, "audit decrypt: record has no encrypted content; encryption may not have been enabled for this record")
		return 1
	}

	ciphertext, err := base64.StdEncoding.DecodeString(record.ArgsCiphertext)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: decode ciphertext: %v\n", err)
		return 1
	}

	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: decryption failed (wrong identity?): %v\n", err)
		return 1
	}

	plaintext, err := io.ReadAll(reader)
	if err != nil {
		fmt.Fprintf(stderr, "audit decrypt: read decrypted content: %v\n", err)
		return 1
	}

	if *outPath == "" && !*toStdout {
		fmt.Fprintln(stderr, "audit decrypt: specify --out FILE or --stdout (or both)")
		return 2
	}

	if *toStdout {
		if _, err := stdout.Write(plaintext); err != nil {
			fmt.Fprintf(stderr, "audit decrypt: write output: %v\n", err)
			return 1
		}
		if !bytes.HasSuffix(plaintext, []byte("\n")) {
			fmt.Fprintln(stdout)
		}
	}
	if *outPath != "" {
		if err := os.WriteFile(*outPath, plaintext, 0o600); err != nil {
			fmt.Fprintf(stderr, "audit decrypt: write output: %v\n", err)
			return 1
		}
	}
	return 0
}
