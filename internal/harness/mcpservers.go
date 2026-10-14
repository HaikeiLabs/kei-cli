package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
)

// BundleConnector is a connector bound to the installation's workspace. Only
// the slug and kind are read: a harness reaches an mcp connector through
// kei-proxy, which holds its upstream URL and credential, so nothing secret
// is ever decoded here.
//
// TODO(HAI-485): the catalog does not serve connectors in the policy bundle
// yet; this is the CLI's input shape until the connector kinds land.
type BundleConnector struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
}

// mcpConnectorKind is the connector kind rendered as an MCP server.
const mcpConnectorKind = "mcp"

// mcpRoute is kei-proxy's governed MCP route (HAI-488); a server named slug
// is reached at <base>/mcp/<slug>, and kei-proxy decides every call.
const mcpRoute = "/mcp/"

// mcpDefaultListenAddr is kei-proxy's default MCP listener.
const mcpDefaultListenAddr = "127.0.0.1:8086"

// mcpSlugRe is a slug safe as a server name in every harness config (a JSON
// key, a bare TOML key) and as a URL path segment.
var mcpSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// mcpServer is one remote MCP server Kei renders.
type mcpServer struct {
	Name string
	URL  string
}

// mcpLedger records the MCP servers a sync wrote into one harness config, so
// the next sync removes only those.
type mcpLedger struct {
	Path    string   `json:"path"`
	Servers []string `json:"servers,omitempty"`
}

// mcpConfig is a file harness that also renders MCP servers. It is a step of
// its own, separate from the permission entries and the audit hook.
type mcpConfig interface {
	// mcpConfigPath is the config file holding the harness's MCP servers.
	mcpConfigPath(env Env) string
	// mergeMCPServers removes the servers named in remove, then adds each of
	// servers whose name the user's config does not already hold. It returns
	// the names it added and the names it skipped. With nothing to remove or
	// add it returns cfg unchanged.
	mergeMCPServers(cfg []byte, remove []string, servers []mcpServer) (out []byte, added, skipped []string, err error)
}

// renderMCPConnectors returns the slugs of the bundle's mcp connectors,
// sorted and unique, and the slugs that are not a valid server name.
func renderMCPConnectors(connectors []BundleConnector) (slugs, invalid []string) {
	seen := map[string]bool{}
	for _, c := range connectors {
		if c.Kind != mcpConnectorKind || seen[c.Slug] {
			continue
		}
		seen[c.Slug] = true
		if !mcpSlugRe.MatchString(c.Slug) {
			invalid = append(invalid, c.Slug)
			continue
		}
		slugs = append(slugs, c.Slug)
	}
	sort.Strings(slugs)
	sort.Strings(invalid)
	return slugs, invalid
}

// mcpProxyBaseURL is the base URL of the local kei-proxy daemon, from the
// MCP listener (KEI_PROXY_MCP_LISTEN_ADDR, default 127.0.0.1:8086). A
// wildcard or empty host is reached on loopback. When the host is not a
// loopback address the function returns "" so the caller renders nothing.
func mcpProxyBaseURL(env Env) string {
	addr := env.getenv("KEI_PROXY_MCP_LISTEN_ADDR")
	if addr == "" {
		addr = mcpDefaultListenAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		host, port, _ = net.SplitHostPort(mcpDefaultListenAddr)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	if !isLoopbackHost(host) {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

// isLoopbackHost reports whether host is a loopback address or localhost.
func isLoopbackHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// applyMCPServers is the MCP-servers render step of applyFiles. It merges
// r's MCP servers into the harness's MCP config, starting from the bytes the
// earlier steps produced for that file (outputs) or from disk, and records
// the file's original bytes in inputs for the diff. It returns
// the ledger of what it manages, or nil for a harness with no MCP config.
func applyMCPServers(f fileHarness, r Rendered, env Env, out io.Writer, prior *mcpLedger, inputs, outputs map[string][]byte) (*mcpLedger, error) {
	m, ok := f.(mcpConfig)
	if !ok {
		return nil, nil
	}
	if _, err := env.home(); err != nil {
		return nil, nil
	}
	path := m.mcpConfigPath(env)
	var remove []string
	if prior != nil && prior.Path == path {
		remove = prior.Servers
	}
	for _, slug := range r.MCPInvalid {
		fmt.Fprintf(out, "%s: MCP connector %q not rendered; its slug is not a valid server name\n", f.displayName(), slug)
	}
	if len(remove) == 0 && len(r.MCPConnectors) == 0 {
		return nil, nil
	}
	base, rendered := outputs[path]
	if !rendered {
		cfg, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		inputs[path], base = cfg, cfg
	}
	routeBase := mcpProxyBaseURL(env)
	if routeBase == "" {
		if len(r.MCPConnectors) > 0 {
			fmt.Fprintf(out, "%s: MCP connectors not rendered; KEI_PROXY_MCP_LISTEN_ADDR must be a loopback address\n", f.displayName())
		}
		return nil, nil
	}
	servers := make([]mcpServer, len(r.MCPConnectors))
	for i, slug := range r.MCPConnectors {
		servers[i] = mcpServer{Name: slug, URL: routeBase + mcpRoute + slug}
	}
	merged, added, skipped, err := m.mergeMCPServers(base, remove, servers)
	if err != nil {
		return nil, fmt.Errorf("render MCP servers into %s: %w", path, err)
	}
	for _, name := range skipped {
		fmt.Fprintf(out, "%s: MCP connector %q not rendered; %s already has a server with that name\n", f.displayName(), name, path)
	}
	outputs[path] = merged
	return &mcpLedger{Path: path, Servers: added}, nil
}

// mergeMCPServersJSON merges servers into the object at key of a JSON config.
// Every other top-level key and every server it does not manage keeps its
// original bytes. entry renders one managed server.
func mergeMCPServersJSON(cfg []byte, key string, remove []string, servers []mcpServer, entry func(mcpServer) *orderedObject) ([]byte, []string, []string, error) {
	if len(remove) == 0 && len(servers) == 0 {
		return cfg, nil, nil, nil
	}
	root := &orderedObject{values: map[string]any{}}
	if len(bytes.TrimSpace(cfg)) > 0 {
		parsed, err := parseTopLevel(cfg, nil)
		if err != nil {
			return nil, nil, nil, err
		}
		root = parsed
	}
	existing := &orderedObject{values: map[string]any{}}
	if raw, ok := root.get(key); ok {
		parsed, err := parseTopLevel(raw.(json.RawMessage), nil)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", key, err)
		}
		existing = parsed
	}
	for _, name := range remove {
		existing.delete(name)
	}
	var added, skipped []string
	for _, server := range servers {
		if _, taken := existing.get(server.Name); taken {
			skipped = append(skipped, server.Name)
			continue
		}
		existing.set(server.Name, entry(server))
		added = append(added, server.Name)
	}
	root.set(key, existing)
	out, err := marshalOrdered(root, "  ")
	if err != nil {
		return nil, nil, nil, err
	}
	return append(out, '\n'), added, skipped, nil
}

// orderedEntry builds a managed server entry with its keys in order.
func orderedEntry(kv ...any) *orderedObject {
	obj := &orderedObject{values: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		obj.set(kv[i].(string), kv[i+1])
	}
	return obj
}
