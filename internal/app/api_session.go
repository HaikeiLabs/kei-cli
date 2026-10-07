package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

// apiSession is a logged-in CLI call against the default Kei host (keiWebURL).
// The users and groups commands use it to send org-scoped requests; it never
// writes the token to the store.
type apiSession struct {
	client  *http.Client
	baseURL string
	token   string
	orgID   string
}

// openAPISession loads the CLI token from the default host, reads its
// organization, and returns a session that sends requests to that same host.
func openAPISession(command string, stderr io.Writer, client *http.Client, store credentialStore) (*apiSession, bool) {
	base, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return nil, false
	}
	token, err := store.Load(base)
	if err != nil {
		fmt.Fprintln(stderr, "not logged in; run kei login first")
		return nil, false
	}
	orgID, err := organizationIDFromCLIToken(token)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v; run kei login again\n", command, err)
		return nil, false
	}
	return &apiSession{client: client, baseURL: base, token: token, orgID: orgID}, true
}

// do sends one request to the AIP host and returns the status and body of a
// 2xx response, or an error carrying the server's message and status.
func (s *apiSession) do(method, path string, query url.Values, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := s.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	status, payload, err := doRequest(s.client, req, 4<<20)
	if err != nil {
		return 0, nil, err
	}
	if status < 200 || status > 299 {
		return status, nil, fmt.Errorf("%s (HTTP %d)", connectorErrorMessage(payload), status)
	}
	return status, payload, nil
}

// confirmWrite prompts for y/N confirmation before a mutating users or groups
// command. With --yes it returns true without prompting. When stdin is not a
// terminal and --yes is absent, it prints the action to stderr and returns
// false (the write is skipped), so scripts must pass --yes.
func confirmWrite(stdin io.Reader, stderr io.Writer, yes bool, action string) bool {
	if yes {
		return true
	}
	fmt.Fprintln(stderr, action)
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(stderr, "Continue? [y/N] ")
		response, _ := bufio.NewReader(stdin).ReadString('\n')
		response = strings.TrimSpace(response)
		return strings.EqualFold(response, "y") || strings.EqualFold(response, "yes")
	}
	return false
}

// parsePositionals extracts exactly n positional arguments from args (which may
// appear before or after flags) and parses the remaining flags. The flag package
// stops at the first non-flag argument, so the leading positionals are pulled
// out first and any trailing ones are read back from flags.Args().
func parsePositionals(flags *flag.FlagSet, args []string, n int) ([]string, error) {
	var pos []string
	i := 0
	for i < len(args) && len(pos) < n && !strings.HasPrefix(args[i], "-") {
		pos = append(pos, args[i])
		i++
	}
	if err := flags.Parse(args[i:]); err != nil {
		return nil, err
	}
	pos = append(pos, flags.Args()...)
	if len(pos) != n {
		return nil, fmt.Errorf("expected exactly %d argument(s)", n)
	}
	return pos, nil
}
