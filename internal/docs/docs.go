// Package docs fetches and prints pages from the public Haikei documentation site.
package docs

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"
)

const DefaultBaseURL = "https://docs.haikeilabs.com"

type indexEntry struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Product string `json:"product"`
}

// Run handles the arguments and output for `kei docs`.
func Run(args []string, stdout, stderr io.Writer, defaultBaseURL string) int {
	flags := flag.NewFlagSet("docs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baseURL := flags.String("base-url", defaultBaseURL, "docs site base URL")

	// Handle -h/--help explicitly: print usage to stdout, exit 0.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			docsUsage(stdout, flags)
			return 0
		}
	}

	if err := flags.Parse(args); err != nil {
		// flag.ContinueOnError already wrote the error to stderr.
		// Print the standard usage so the user sees the right format.
		docsUsage(stderr, flags)
		return 2
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if flags.NArg() == 0 {
		return list(client, stdout, stderr, *baseURL)
	}
	return fetch(client, flags.Arg(0), stdout, stderr, *baseURL)
}

func docsUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: kei docs [flags]\n\n")
	fmt.Fprintln(w, "Fetch and print pages from the Haikei documentation site.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', tabwriter.DiscardEmptyColumns)
	fs.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(tw, "  --%s\t%s\n", f.Name, f.Usage)
	})
	tw.Flush()
}

func list(client *http.Client, stdout, stderr io.Writer, baseURL string) int {
	pages, err := fetchIndex(client, baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "docs: %v\n", err)
		return 1
	}
	if len(pages) == 0 {
		fmt.Fprintln(stdout, "No docs pages found.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tTITLE")
	for _, page := range pages {
		fmt.Fprintf(tw, "%s\t%s\n", page.Slug, page.Title)
	}
	_ = tw.Flush()
	return 0
}

func fetchIndex(client *http.Client, baseURL string) ([]indexEntry, error) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/index.json")
	if err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch index: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	var pages []indexEntry
	if err := json.Unmarshal(body, &pages); err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	return pages, nil
}

func fetch(client *http.Client, slug string, stdout, stderr io.Writer, baseURL string) int {
	pages, err := fetchIndex(client, baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "docs: %v\n", err)
		return 1
	}
	resolved := resolveSlug(pages, slug)
	if len(resolved) == 0 {
		fmt.Fprintf(stderr, "docs: page %q not found; run kei docs to list pages\n", slug)
		return 1
	}
	if len(resolved) > 1 {
		fmt.Fprintf(stderr, "docs: %q is ambiguous; matches:\n", slug)
		for _, page := range resolved {
			fmt.Fprintf(stderr, "  %s\n", page.Slug)
		}
		return 2
	}
	markdown, err := fetchPage(client, baseURL, resolved[0].Slug)
	if err != nil {
		fmt.Fprintf(stderr, "docs: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprint(stdout, markdown)
	return 0
}

func fetchPage(client *http.Client, baseURL, slug string) (string, error) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/" + slug + ".md")
	if err != nil {
		return "", fmt.Errorf("fetch page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch page %q: HTTP %d", slug, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read page: %w", err)
	}
	return string(body), nil
}

func resolveSlug(pages []indexEntry, query string) []indexEntry {
	var exact []indexEntry
	for _, page := range pages {
		if page.Slug == query {
			exact = append(exact, page)
		}
	}
	if len(exact) == 1 {
		return exact
	}
	segment := query
	if idx := strings.LastIndex(query, "/"); idx >= 0 {
		segment = query[idx+1:]
	}
	var matches []indexEntry
	for _, page := range pages {
		parts := strings.Split(page.Slug, "/")
		if len(parts) > 0 && parts[len(parts)-1] == segment {
			matches = append(matches, page)
		}
	}
	if len(matches) != 0 {
		return matches
	}
	return exact
}
