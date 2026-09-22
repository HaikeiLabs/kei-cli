package app

import (
	"flag"
	"fmt"
	"io"
)

// runLogoutCommand removes the CLI token for the given Kei URL from the OS
// credential store. It is idempotent: logging out while not logged in is not
// an error.
func runLogoutCommand(args []string, stdout, stderr io.Writer, store credentialStore) int {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apiURL := flags.String("api-url", keiWebURL(), "Kei web URL")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "logout accepts no positional arguments")
		return 2
	}
	baseURL, err := normalizedKeiWebURL(*apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "logout: %v\n", err)
		return 2
	}
	removed, err := store.Delete(baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "logout failed: %v\n", err)
		return 1
	}
	host := keychainAccount(baseURL)
	if removed {
		fmt.Fprintf(stdout, "Logged out of Kei for %s.\n", host)
	} else {
		fmt.Fprintf(stdout, "Not logged in to %s.\n", host)
	}
	return 0
}
