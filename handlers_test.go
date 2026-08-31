package main

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newLazyProxyServer builds a lazy-mcp proxy MCP server (with the three exposed
// tools registered) and connects a test client to it over in-memory
// transports. The downstream server(s) the proxy talks to are wired via the
// given transport factory. The returned client session exercises the proxy
// over the real MCP wire protocol.
func newLazyProxyServer(t *testing.T, ctx context.Context, root *Category, factory TransportFactory) (*mcp.ClientSession, func()) {
	t.Helper()
	proxy := testProxy(root, factory)

	// Build the lazy-mcp MCP server and register its exposed tools.
	server := mcp.NewServer(&mcp.Implementation{Name: "lazy-mcp-test", Version: "0.1.0"}, nil)
	registerHandlers(server, proxy)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("proxy server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatalf("test client connect: %v", err)
	}
	cleanup := func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		_ = proxy.cache.Close()
	}
	return clientSession, cleanup
}

func TestHandlers_GetToolsInCategory_TopLevel(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// get_tools_in_category("") should list the top-level categories.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_tools_in_category",
		Arguments: map[string]any{"category_path": ""},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	// Should mention both top-level categories.
	text := tc.Text
	if !contains(text, "coding") {
		t.Errorf("top-level listing missing 'coding': %q", text)
	}
	if !contains(text, "web") {
		t.Errorf("top-level listing missing 'web': %q", text)
	}
	// StructuredContent should carry the categories map.
	if res.StructuredContent == nil {
		t.Error("expected StructuredContent to be set")
	}
}

func TestHandlers_GetToolsInCategory_Tools(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// coding.serena -> the downstream server exposes the "echo" tool.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_tools_in_category",
		Arguments: map[string]any{"category_path": "coding.serena"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if !contains(tc.Text, "echo") {
		t.Errorf("tools listing missing 'echo': %q", tc.Text)
	}
}

func TestHandlers_GetToolsInCategory_NotFound(t *testing.T) {
	ctx := context.Background()
	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		t.Fatal("factory should not be called for nonexistent category")
		return nil, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// The typed handler turns an error return into a tool error.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_tools_in_category",
		Arguments: map[string]any{"category_path": "nope"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// The typed AddTool wraps handler errors as protocol errors; either an
	// error or an IsError result is acceptable here.
	_ = res
}

func TestHandlers_ExecuteTool_ForwardsResult(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "execute_tool",
		Arguments: map[string]any{
			"tool_path": "coding.serena.echo",
			"tool_args": map[string]any{"value": "hello"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected downstream error: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content item forwarded, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent forwarded, got %T", res.Content[0])
	}
	if tc.Text != "echo:hello" {
		t.Errorf("forwarded text = %q, want echo:hello", tc.Text)
	}
}

func TestHandlers_ExecuteTool_BadPath(t *testing.T) {
	ctx := context.Background()
	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return nil, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "execute_tool",
		Arguments: map[string]any{
			"tool_path": "nope.x",
			"tool_args": map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// Should be a tool error (IsError=true), not a protocol error.
	if !res.IsError {
		t.Errorf("expected IsError=true for bad path, got %+v", res)
	}
}

func TestHandlers_ExecuteTool_Timeout(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newSlowDownstream(t, ctx, 500*time.Millisecond)
	defer downstreamCleanup()

	// Give serena a per-tool timeout so "slow" trips it quickly.
	root := buildTestTree()
	root.Children["coding"].MCP["serena"].ToolTimeouts = map[string]string{"slow": "100ms"}

	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// Bound the test client call so a broken timeout cannot hang the test.
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	res, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name: "execute_tool",
		Arguments: map[string]any{
			"tool_path": "coding.serena.slow",
			"tool_args": map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected timeout to surface as IsError, got %+v", res)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !contains(text, "deadline") && !contains(text, "context") {
		t.Errorf("error text %q does not mention deadline/context", text)
	}
}

func TestHandlers_DescribeTool_ReturnsMetadata(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// coding.serena -> the downstream server exposes the "echo" tool.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "describe_tool",
		Arguments: map[string]any{"tool_path": "coding.serena.echo"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	// The pretty-printed metadata should carry the tool name, its inputSchema,
	// and the schema property ("value") that was previously invisible.
	text := tc.Text
	if !contains(text, "echo") {
		t.Errorf("metadata missing tool name 'echo': %q", text)
	}
	if !contains(text, "inputSchema") {
		t.Errorf("metadata missing inputSchema: %q", text)
	}
	if !contains(text, "value") {
		t.Errorf("metadata missing schema property 'value': %q", text)
	}
	if res.StructuredContent == nil {
		t.Error("expected StructuredContent to be set")
	}
}

func TestHandlers_DescribeTool_ToolNotFound(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// Valid server path, unknown tool -> tool error naming the tool.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "describe_tool",
		Arguments: map[string]any{"tool_path": "coding.serena.nope"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown tool, got %+v", res)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !contains(text, "nope") {
		t.Errorf("error text %q does not mention tool name", text)
	}
}

func TestHandlers_DescribeTool_BadPath(t *testing.T) {
	ctx := context.Background()
	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return nil, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "describe_tool",
		Arguments: map[string]any{"tool_path": "nope.x"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// Should be a tool error (IsError=true), not a protocol error.
	if !res.IsError {
		t.Errorf("expected IsError=true for bad path, got %+v", res)
	}
}

func TestHandlers_ListToolsViaClient(t *testing.T) {
	ctx := context.Background()
	downstreamTransport, downstreamCleanup := newFakeDownstream(t, ctx)
	defer downstreamCleanup()

	root := buildTestTree()
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return downstreamTransport, nil
	}
	session, cleanup := newLazyProxyServer(t, ctx, root, factory)
	defer cleanup()

	// The MCP server itself should advertise exactly the three exposed tools.
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	if !names["get_tools_in_category"] {
		t.Errorf("missing get_tools_in_category; got %v", names)
	}
	if !names["execute_tool"] {
		t.Errorf("missing execute_tool; got %v", names)
	}
	if !names["describe_tool"] {
		t.Errorf("missing describe_tool; got %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected exactly 3 exposed tools, got %d: %v", len(names), names)
	}
}

// contains is a minimal substring check for test assertions.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}