package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerHandlers installs the three exposed tools on the server.
func registerHandlers(server *mcp.Server, proxy *Proxy) {
	registerGetToolsInCategory(server, proxy)
	registerExecuteTool(server, proxy)
	registerDescribeTool(server, proxy)
}

// --- get_tools_in_category -------------------------------------------------

type getToolsInCategoryInput struct {
	CategoryPath string `json:"category_path" jsonschema:"dot-delimited category path; empty for top level"`
}

type getToolsInCategoryOutput struct {
	Categories map[string]string `json:"categories,omitempty" jsonschema:"subcategories of the category, name to description"`
	Tools      map[string]string `json:"tools,omitempty"      jsonschema:"tools in the category, name to description"`
}

func registerGetToolsInCategory(server *mcp.Server, proxy *Proxy) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_tools_in_category",
		Description: "List the subcategories or tools in the given category path. Empty path returns top-level categories. A category containing servers returns their tools (lazily loaded).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in getToolsInCategoryInput) (*mcp.CallToolResult, getToolsInCategoryOutput, error) {
		start := time.Now()
		cat, containingPath, isServerPath := resolveForListing(proxy.root, in.CategoryPath)
		if cat == nil {
			proxy.log().Warn("get_tools_in_category: category not found", "category_path", in.CategoryPath)
			return nil, getToolsInCategoryOutput{}, fmt.Errorf("category not found: %q", in.CategoryPath)
		}

		out := getToolsInCategoryOutput{}

		// If the category has subcategories, list those first.
		if len(cat.Children) > 0 {
			out.Categories = ListChildCategories(cat)
		}

		// If the category also (or only) has servers, list their tools.
		if len(cat.MCP) > 0 {
			tools := map[string]string{}
			for name, def := range cat.MCP {
				// For a server-resolved path (e.g. "coding.serena") the server
				// name is already the final segment; use the path as-is.
				sp := in.CategoryPath
				if !isServerPath {
					sp = serverPathFor(containingPath, name)
				}
				cs := proxy.cache.GetOrCreate(def, sp)
				if err := cs.ensureConnected(ctx); err != nil {
					return nil, getToolsInCategoryOutput{}, fmt.Errorf("load server %s: %w", sp, err)
				}
				for tName, desc := range toolListToMap(cs.Tools) {
					tools[tName] = desc
				}
			}
			out.Tools = tools
		}

		// Serialize the output as human-readable JSON text content as well.
		data, _ := json.MarshalIndent(out, "", "  ")
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}
		proxy.log().Info("get_tools_in_category",
			"category_path", in.CategoryPath,
			"categories", len(out.Categories),
			"tools", len(out.Tools),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return result, out, nil
	})
}

// --- execute_tool -----------------------------------------------------------

// executeToolInput is the input schema for the execute_tool tool. It is
// intentionally loose: tool_args is an arbitrary object forwarded as-is.
type executeToolInput struct {
	ToolPath string         `json:"tool_path" jsonschema:"full dot-delimited path to the tool, e.g. coding_tools.serena.find_symbol"`
	ToolArgs map[string]any `json:"tool_args" jsonschema:"arguments object to pass to the downstream tool"`
}

// executeToolResult mirrors the downstream CallToolResult so it can be
// serialized to the caller without losing content.
type executeToolResult struct {
	Content           []mcp.Content `json:"content"`
	IsError           bool          `json:"isError,omitempty"`
	StructuredContent any           `json:"structuredContent,omitempty"`
}

func registerExecuteTool(server *mcp.Server, proxy *Proxy) {
	// Use the raw AddTool form so the downstream CallToolResult is forwarded
	// as-is (content slice, IsError, StructuredContent) without re-encoding.
	server.AddTool(&mcp.Tool{
		Name:        "execute_tool",
		Description: "Execute a tool on a downstream MCP server. The server is lazily loaded and cached on first use. The downstream result (including content, errors, and structured content) is returned as-is.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "tool_path": {
      "type": "string",
      "description": "full dot-delimited path to the tool, e.g. coding_tools.serena.find_symbol"
    },
    "tool_args": {
      "type": "object",
      "description": "arguments object to pass to the downstream tool"
    }
  },
  "required": ["tool_path", "tool_args"]
}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		var in executeToolInput
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if in.ToolPath == "" {
			return nil, fmt.Errorf("tool_path is required")
		}

		serverPath, toolName, err := proxy.ResolveToolPath(ctx, in.ToolPath)
		if err != nil {
			proxy.log().Warn("execute_tool: path resolution failed", "tool_path", in.ToolPath, "error", err)
			return errorResult(err), nil
		}

		timeout := proxy.ResolveToolTimeout(serverPath, toolName)
		callCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		res, err := proxy.CallTool(callCtx, serverPath, toolName, in.ToolArgs)
		if err != nil {
			proxy.log().Error("execute_tool failed",
				"tool_path", in.ToolPath,
				"server", serverPath,
				"tool", toolName,
				"timeout", timeout.String(),
				"error", err,
			)
			return errorResult(err), nil
		}
		// Forward the downstream result as-is.
		proxy.log().Info("execute_tool",
			"tool_path", in.ToolPath,
			"server", serverPath,
			"tool", toolName,
			"timeout", timeout.String(),
			"is_error", res.IsError,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return res, nil
	})
}

// --- describe_tool ----------------------------------------------------------

// describeToolInput is the input schema for the describe_tool tool.
type describeToolInput struct {
	ToolPath string `json:"tool_path" jsonschema:"full dot-delimited path to the tool, e.g. coding_tools.serena.find_symbol"`
}

func registerDescribeTool(server *mcp.Server, proxy *Proxy) {
	// Raw AddTool form (like execute_tool) so path-resolution failures surface
	// as IsError=true tool errors via errorResult, not protocol errors.
	server.AddTool(&mcp.Tool{
		Name:        "describe_tool",
		Description: "Return the full metadata of a tool on a downstream MCP server, including its input schema. The server is lazily loaded and cached on first use.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "tool_path": {
      "type": "string",
      "description": "full dot-delimited path to the tool, e.g. coding_tools.serena.find_symbol"
    }
  },
  "required": ["tool_path"]
}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		var in describeToolInput
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		if in.ToolPath == "" {
			return errorResult(fmt.Errorf("tool_path is required")), nil
		}

		tool, err := proxy.DescribeTool(ctx, in.ToolPath)
		if err != nil {
			proxy.log().Warn("describe_tool failed", "tool_path", in.ToolPath, "error", err)
			return errorResult(err), nil
		}
		// Return the full tool object as structured content, plus a readable
		// JSON rendering as text (matching get_tools_in_category).
		data, _ := json.MarshalIndent(tool, "", "  ")
		result := &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
			StructuredContent: tool,
		}
		proxy.log().Info("describe_tool",
			"tool_path", in.ToolPath,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return result, nil
	})
}

// errorResult wraps an error as a tool error result (IsError=true) so the
// upstream client sees a tool error rather than a transport-level error.
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: err.Error()},
		},
	}
}

// resolveForListing resolves a category path for the get_tools_in_category
// handler. It returns:
//   - the category node to list (a real category, or a synthetic one wrapping
//     a single server if the final segment names a server),
//   - the containing category path (for building full server paths),
//   - isServerPath: true when the path ended at a server name.
//
// Returns a nil category if the path does not resolve.
func resolveForListing(root *Category, path string) (cat *Category, containingPath string, isServerPath bool) {
	if path == "" {
		return root, "", false
	}
	cur := root
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		if child, ok := cur.Children[seg]; ok {
			cur = child
			continue
		}
		// Only the final segment may be a server name.
		if i == len(segs)-1 {
			if def, ok := cur.MCP[seg]; ok {
				return &Category{MCP: map[string]*ServerDef{seg: def}}, containingPathFor(segs), true
			}
		}
		return nil, "", false
	}
	return cur, path, false
}

// containingPathFor returns all segments except the last (the server name),
// rejoined as the containing category path.
func containingPathFor(segs []string) string {
	if len(segs) <= 1 {
		return ""
	}
	return strings.Join(segs[:len(segs)-1], ".")
}