package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/HaikeiLabs/kei-connector-contracts/contract"
)

// Transport limits for an http_api connector the CLI registers. The contract
// caps them; these are the defaults the console uses for a JSON API.
const (
	httpAPIRequestMaxBytes  int64 = 1 << 20
	httpAPIResponseMaxBytes int64 = 1 << 20
	httpAPITimeoutMS        int64 = 10_000
)

// httpAPIFlags are the connectors create flags that register an http_api
// connector's destination (HAI-384). They apply to --provider http_api only.
type httpAPIFlags struct {
	flags      *flag.FlagSet
	baseURL    *string
	methods    *string
	paths      *string
	query      *string
	authHeader *string
	authScheme *string
}

var httpAPIFlagNames = []string{"base-url", "methods", "paths", "query", "auth-header", "auth-scheme"}

func registerHTTPAPIFlags(flags *flag.FlagSet) httpAPIFlags {
	return httpAPIFlags{
		flags:      flags,
		baseURL:    flags.String("base-url", "", "http_api: registered https base URL"),
		methods:    flags.String("methods", "GET", "http_api: comma-separated allowed methods (GET, HEAD, POST, PATCH)"),
		paths:      flags.String("paths", "/*", "http_api: comma-separated allowed path patterns relative to the base URL (a trailing * matches a prefix)"),
		query:      flags.String("query", "", "http_api: comma-separated allowed query parameter names"),
		authHeader: flags.String("auth-header", "Authorization", "http_api: header the runtime puts the API key in"),
		authScheme: flags.String("auth-scheme", "", "http_api: auth scheme before the key (default Bearer for Authorization, none for other headers)"),
	}
}

func (f httpAPIFlags) passed(name string) bool {
	passed := false
	f.flags.Visit(func(fl *flag.Flag) { passed = passed || fl.Name == name })
	return passed
}

// check rejects http_api flags on another provider and a missing base URL on
// http_api, before anything is read or sent.
func (f httpAPIFlags) check(provider contract.Provider, stderr io.Writer) bool {
	if provider != contract.ProviderHTTPAPI {
		for _, name := range httpAPIFlagNames {
			if f.passed(name) {
				fmt.Fprintf(stderr, "connectors create: --%s applies only to --provider http_api\n", name)
				return false
			}
		}
		return true
	}
	if *f.baseURL == "" {
		fmt.Fprintln(stderr, "connectors create --provider http_api requires --base-url")
		return false
	}
	return true
}

// registration builds the http_api block and runs the contract's check on
// it, so an invalid destination never leaves the CLI.
func (f httpAPIFlags) registration() (*contract.HTTPAPI, error) {
	base, err := url.Parse(*f.baseURL)
	if err != nil {
		return nil, errors.New("--base-url must be an https URL")
	}
	var methods []contract.HTTPMethod
	for _, method := range splitList(*f.methods) {
		if m := contract.HTTPMethod(strings.ToUpper(method)); !slices.Contains(methods, m) {
			methods = append(methods, m)
		}
	}
	scheme := *f.authScheme
	if !f.passed("auth-scheme") && strings.EqualFold(*f.authHeader, "Authorization") {
		scheme = "Bearer"
	}
	h := &contract.HTTPAPI{
		RegisteredBaseURL:   *f.baseURL,
		RegisteredHostname:  base.Hostname(),
		AllowedMethods:      methods,
		AllowedPathPatterns: splitList(*f.paths),
		QueryAllowlist:      orEmpty(splitList(*f.query)),
		RequestMaxBytes:     httpAPIRequestMaxBytes,
		ResponseMaxBytes:    httpAPIResponseMaxBytes,
		TimeoutMS:           httpAPITimeoutMS,
		CredentialInjection: contract.CredentialInjection{Location: contract.CredentialInjectionHeader, Name: *f.authHeader, Scheme: scheme},
		Redaction:           contract.RedactionRules{Headers: []string{*f.authHeader}, QueryParameters: []string{}, ResponseFields: []string{}},
	}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return h, nil
}

func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// httpAPIMethodAllowed reports whether an http_api capability (http.get) names
// one of the connector's allowed methods. The capability table itself is the
// contract's.
func httpAPIMethodAllowed(h *contract.HTTPAPI, capability string) bool {
	verb, ok := strings.CutPrefix(capability, "http.")
	return ok && h != nil && slices.Contains(h.AllowedMethods, contract.HTTPMethod(strings.ToUpper(verb)))
}

// printHTTPAPI renders an http_api connector's registration. It holds no
// credential material; the key is only ever named by its reference.
func printHTTPAPI(w io.Writer, h *contract.HTTPAPI) {
	methods := make([]string, len(h.AllowedMethods))
	for i, method := range h.AllowedMethods {
		methods[i] = string(method)
	}
	fmt.Fprintf(w, "Base URL:    %s\nMethods:     %s\nPaths:       %s\n", h.RegisteredBaseURL, strings.Join(methods, ", "), strings.Join(h.AllowedPathPatterns, ", "))
	if len(h.QueryAllowlist) > 0 {
		fmt.Fprintf(w, "Query:       %s\n", strings.Join(h.QueryAllowlist, ", "))
	}
	auth := h.CredentialInjection.Name
	if h.CredentialInjection.Scheme != "" {
		auth += " (" + h.CredentialInjection.Scheme + ")"
	}
	fmt.Fprintf(w, "Auth header: %s\n", auth)
}
