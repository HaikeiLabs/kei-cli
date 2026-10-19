package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// BundleConnector is a connector bound to the installation's workspace.
// The slug, kind, and mcp_url are read; the harness renders the mcp_url
// directly (ADR-039: the runtime decides and passes credentials through,
// it no longer routes provider calls). Nothing secret is decoded here.
//
// TODO(HAI-485): the catalog does not serve connectors in the policy bundle
// yet; this is the CLI's input shape until the connector kinds land.
type BundleConnector struct {
	Slug   string `json:"slug"`
	Kind   string `json:"kind"`
	MCPURL string `json:"mcp_url,omitempty"`
}

// mcpConnectorKind is the connector kind rendered as an MCP server.
const mcpConnectorKind = "mcp"

// mcpSlugRe is a slug safe as a server name in every harness config (a JSON
// key, a bare TOML key) and as a URL path segment.
var mcpSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// mcpServer is one remote MCP server Kei renders. The URL is the connector's
// upstream MCP endpoint (ADR-039); AuthEnvVar is the environment variable
// name that holds the bearer token for that endpoint. The variable name is
// derived from the slug so each connector has its own credential reference.
type mcpServer struct {
	Name       string
	URL        string
	AuthEnvVar string
}

// mcpAuthEnvVar returns the environment variable name for a connector's
// MCP bearer token: KEI_MCP_<SLUG_UPPER>_TOKEN.
func mcpAuthEnvVar(slug string) string {
	return "KEI_MCP_" + strings.ToUpper(slug) + "_TOKEN"
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

// renderMCPConnectors returns the bundle's mcp connectors as mcpServer
// entries (upstream URL + auth env-var name), sorted by slug, and the
// slugs that are not a valid server name. A connector without an mcp_url
// is skipped with a warning.
func renderMCPConnectors(connectors []BundleConnector) (servers []mcpServer, invalid []string, skipped []string) {
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
		if c.MCPURL == "" {
			skipped = append(skipped, c.Slug)
			continue
		}
		servers = append(servers, mcpServer{
			Name:       c.Slug,
			URL:        c.MCPURL,
			AuthEnvVar: mcpAuthEnvVar(c.Slug),
		})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	sort.Strings(invalid)
	sort.Strings(skipped)
	return servers, invalid, skipped
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
	for _, slug := range r.MCPSkipped {
		fmt.Fprintf(out, "%s: MCP connector %q not rendered; no upstream mcp_url in the bundle\n", f.displayName(), slug)
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
	merged, added, skipped, err := m.mergeMCPServers(base, remove, r.MCPConnectors)
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
