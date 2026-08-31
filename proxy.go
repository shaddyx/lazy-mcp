package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Proxy lazily connects to downstream MCP servers via a ServerCache and
// forwards tool listing and execution requests.
type Proxy struct {
	cache  *ServerCache
	root   *Category
	logger *slog.Logger // nil = discard
}

// NewProxy returns a Proxy backed by the given cache and category root.
func NewProxy(root *Category, cache *ServerCache) *Proxy {
	root.linkParents()
	return &Proxy{cache: cache, root: root}
}

// log returns the proxy's logger, or a no-op logger when none is set.
func (p *Proxy) log() *slog.Logger { return loggerOrDefault(p.logger) }

// ListTools returns the cached tool list for the server at serverPath,
// lazily connecting on first use.
func (p *Proxy) ListTools(ctx context.Context, serverPath string) ([]*mcp.Tool, error) {
	def, _, ok := p.findServer(serverPath)
	if !ok {
		return nil, fmt.Errorf("no server at path %q", serverPath)
	}
	cs := p.cache.GetOrCreate(def, serverPath)
	if err := cs.ensureConnected(ctx); err != nil {
		return nil, err
	}
	return cs.Tools, nil
}

// CallTool forwards a tool call to the server at serverPath, returning the
// downstream result as-is (content, IsError, StructuredContent preserved).
func (p *Proxy) CallTool(ctx context.Context, serverPath, toolName string, args any) (*mcp.CallToolResult, error) {
	def, _, ok := p.findServer(serverPath)
	if !ok {
		return nil, fmt.Errorf("no server at path %q", serverPath)
	}
	cs := p.cache.GetOrCreate(def, serverPath)
	if err := cs.ensureConnected(ctx); err != nil {
		return nil, err
	}
	return cs.Session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

// DescribeTool returns the full metadata of the tool at toolPath. It resolves
// the path (same rules as execute_tool), lazily connects to the downstream
// server, and returns the matching tool from its cached tool list.
func (p *Proxy) DescribeTool(ctx context.Context, toolPath string) (*mcp.Tool, error) {
	serverPath, toolName, err := p.ResolveToolPath(ctx, toolPath)
	if err != nil {
		return nil, err
	}
	def, _, ok := p.findServer(serverPath)
	if !ok {
		return nil, fmt.Errorf("no server at path %q", serverPath)
	}
	cs := p.cache.GetOrCreate(def, serverPath)
	if err := cs.ensureConnected(ctx); err != nil {
		return nil, err
	}
	for _, t := range cs.Tools {
		if t.Name == toolName {
			return t, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found on server %q (available: %s)",
		toolName, serverPath, strings.Join(availableToolNames(cs.Tools), ", "))
}

// ResolveToolTimeout returns the timeout to apply to a call of toolName on the
// server at serverPath. Resolution order (highest priority first):
//  1. per-tool: server.ToolTimeouts[<toolName>]
//  2. per-server: server.Timeout
//  3. category chain: nearest ancestor category Timeout (the root category
//     carries the global timeout from the config)
//  4. default: DefaultToolTimeout
//
// A configured value of "0s" at any level resolves to 0, disabling the timeout
// for that scope (the call is left unbounded); an unset level is skipped. The
// returned duration is always valid: LoadConfig validates configured durations,
// and parse errors here are treated as unset.
func (p *Proxy) ResolveToolTimeout(serverPath, toolName string) time.Duration {
	def, cat, ok := p.findServer(serverPath)
	if !ok {
		return DefaultToolTimeout
	}
	if s, ok := def.ToolTimeouts[toolName]; ok {
		if d, err := parseTimeout(s); err == nil {
			return d
		}
	}
	if def.Timeout != "" {
		if d, err := parseTimeout(def.Timeout); err == nil {
			return d
		}
	}
	for c := cat; c != nil; c = c.parent {
		if c.Timeout != "" {
			if d, err := parseTimeout(c.Timeout); err == nil {
				return d
			}
		}
	}
	return DefaultToolTimeout
}

// ResolveToolPath takes a full path like "coding_tools.serena.find_symbol"
// and returns the server path ("coding_tools.serena") and tool name
// ("find_symbol"). The path is dot-delimited; it is walked left-to-right:
// segments that match child categories descend the tree; the first segment
// that matches a server name in the current category marks the server; the
// final segment is the tool name. If a single category contains only one
// server, the server segment may be omitted (e.g. "coding.find_symbol").
func (p *Proxy) ResolveToolPath(ctx context.Context, fullPath string) (serverPath, toolName string, _ error) {
	segments := strings.Split(fullPath, ".")
	if len(segments) < 2 {
		return "", "", fmt.Errorf("invalid tool path %q: need at least <category>.<tool>", fullPath)
	}
	toolName = segments[len(segments)-1]
	body := segments[:len(segments)-1]

	cur := p.root
	categoryPath := ""
	resolved := false

	for _, seg := range body {
		if child, ok := cur.Children[seg]; ok {
			cur = child
			categoryPath = serverPathFor(categoryPath, seg)
			continue
		}
		// Not a child category: interpret this segment as a server name.
		if _, ok := cur.MCP[seg]; ok {
			serverPath = serverPathFor(categoryPath, seg)
			resolved = true
			break
		}
		return "", "", fmt.Errorf("no category or server named %q under %q", seg, categoryPath)
	}

	if !resolved {
		// All body segments were categories; the final category must contain
		// servers. With a single server, use it directly.
		if len(cur.MCP) == 0 {
			return "", "", fmt.Errorf("category %q has no servers", categoryPath)
		}
		if len(cur.MCP) > 1 {
			// Multiple servers: find the one advertising the tool.
			for name, def := range cur.MCP {
				sp := serverPathFor(categoryPath, name)
				cs := p.cache.GetOrCreate(def, sp)
				if err := cs.ensureConnected(ctx); err != nil {
					continue
				}
				for _, t := range cs.Tools {
					if t.Name == toolName {
						return sp, toolName, nil
					}
				}
			}
			return "", "", fmt.Errorf("no server in category %q advertises tool %q", categoryPath, toolName)
		}
		for name := range cur.MCP {
			serverPath = serverPathFor(categoryPath, name)
		}
	}

	return serverPath, toolName, nil
}

// findServer returns the ServerDef at the given path (categoryPath.serverName),
// plus the category that contains it. The path is split into category path +
// server name on the last dot.
func (p *Proxy) findServer(serverPath string) (def *ServerDef, cat *Category, ok bool) {
	categoryPath, serverName := SplitPath(serverPath)
	if serverName == "" {
		return nil, nil, false
	}
	cat = FindCategory(p.root, categoryPath)
	if cat == nil {
		return nil, nil, false
	}
	def, ok = cat.MCP[serverName]
	if !ok {
		return nil, nil, false
	}
	return def, cat, true
}

// serverPathFor returns the full server path for a server name within a
// category path.
func serverPathFor(categoryPath, serverName string) string {
	if categoryPath == "" {
		return serverName
	}
	return categoryPath + "." + serverName
}

// toolListToMap converts a tool list into a name-to-description map.
func toolListToMap(tools []*mcp.Tool) map[string]string {
	out := make(map[string]string, len(tools))
	for _, t := range tools {
		out[t.Name] = t.Description
	}
	return out
}

// availableToolNames returns the sorted names of the given tools.
func availableToolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}