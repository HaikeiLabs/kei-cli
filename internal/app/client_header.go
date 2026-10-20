package app

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// clientHeaderName identifies the CLI to the Kei control plane.
const clientHeaderName = "X-Kei-Client"

// cliSubcommands is the dispatch tree used to name the subcommand path. Only
// real subcommand words are followed, so arguments, flags, paths and values
// never become part of the header.
var cliSubcommands = map[string]map[string]bool{
	"audit":                   {"keys": true, "decrypt": true},
	"audit.keys":              {"create": true, "list": true, "disable": true},
	"bot":                     {"list": true, "init": true, "agents": true, "status": true, "delete": true, "credential": true, "bind": true},
	"bot.agents":              {"list": true, "add": true, "remove": true},
	"connectors":              {"create": true, "test": true, "list": true, "get": true, "connect": true, "reconnect": true, "delete": true, "manifests": true, "tools": true, "templates": true},
	"connectors.tools":        {"list": true, "show": true, "approve": true},
	"connectors.manifests":    {"list": true, "show": true, "approve": true},
	"credential-store":        {"get": true, "put": true, "update": true},
	"directory-sync":          {"get": true, "set": true, "enable": true, "disable": true, "report": true},
	"groups":                  {"list": true, "create": true, "members": true},
	"groups.members":          {"list": true, "add": true, "remove": true},
	"harness":                 {"add": true, "list": true, "remove": true, "sync": true},
	"model-profiles":          {"list": true, "get": true, "create": true, "update": true, "delete": true, "set-default": true, "assign": true, "unassign": true, "assignment": true, "readiness": true, "test": true},
	"policies":                {"list": true, "get": true, "create": true, "update": true, "delete": true, "import": true},
	"runtime":                 {"bootstrap": true, "heartbeat": true, "service": true},
	"runtime.service":         {"install": true, "uninstall": true, "status": true},
	"service":                 {"install": true, "uninstall": true, "status": true},
	"skills":                  {"status": true, "update": true, "add": true, "install": true},
	"users":                   {"list": true, "get": true, "create": true, "update": true, "resolve": true, "identities": true, "remote-identities": true},
	"users.identities":        {"list": true},
	"users.remote-identities": {"list": true},
	"workspaces":              {"list": true, "use": true, "current": true, "create": true},
}

// clientHeaderTransport adds X-Kei-Client to requests sent to the Kei
// control plane. Requests to other hosts (the GitHub API, a self-hosted Open
// WebUI) are left untouched.
type clientHeaderTransport struct {
	base    http.RoundTripper
	version string
	command string
	hosts   map[string]bool
}

func (t *clientHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && t.command != "" && t.isKeiHost(req.URL.Hostname()) {
		req = req.Clone(req.Context())
		req.Header.Set(clientHeaderName, clientHeaderValue(t.version, t.command))
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func (t *clientHeaderTransport) isKeiHost(host string) bool {
	host = strings.ToLower(host)
	return t.hosts[host] || strings.HasSuffix(host, ".haikeilabs.com")
}

// keiControlPlaneHosts lists the hosts the CLI is configured to talk to.
// api.haikeilabs.com and runtime.haikeilabs.com are the defaults; the
// environment overrides cover custom and test deployments.
func keiControlPlaneHosts() map[string]bool {
	hosts := map[string]bool{
		"api.haikeilabs.com":     true,
		"runtime.haikeilabs.com": true,
	}
	for _, raw := range []string{os.Getenv("KEI_WEB_URL"), os.Getenv("KEI_RUNTIME_CONTROL_PLANE_URL")} {
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil && parsed.Hostname() != "" {
			hosts[strings.ToLower(parsed.Hostname())] = true
		}
	}
	return hosts
}

// newHTTPClient builds an HTTP client that identifies this CLI and subcommand
// to the Kei control plane.
func newHTTPClient(version, command string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &clientHeaderTransport{
			base:    http.DefaultTransport,
			version: version,
			command: command,
			hosts:   keiControlPlaneHosts(),
		},
	}
}

// dispatchedCommandPath resolves the dot-joined subcommand path from the
// arguments the dispatch tree recognizes.
func dispatchedCommandPath(command string, args []string) string {
	path := command
	for _, arg := range args {
		if !cliSubcommands[path][arg] {
			break
		}
		path += "." + arg
	}
	return path
}

func clientHeaderValue(version, command string) string {
	return "kei-cli/" + clientVersion(version) + "; cmd=" + clientCommand(command)
}

// clientVersion keeps only the characters the contract allows. An unknown or
// unset version is "dev".
func clientVersion(version string) string {
	var b strings.Builder
	for _, r := range version {
		if b.Len() == 32 {
			break
		}
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '.' || r == '+' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

func clientCommand(command string) string {
	var b strings.Builder
	for _, r := range command {
		if b.Len() == 64 {
			break
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
