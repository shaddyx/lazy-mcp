package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// downstreamTool is a tool exposed by the fake downstream server in tests.
type downstreamToolInput struct {
	Value string `json:"value"`
}

// slowToolInput is the input schema of the slow downstream tool; the value is
// optional so a timeout test can call it with empty arguments.
type slowToolInput struct {
	Value string `json:"value,omitempty"`
}

// newFakeDownstream creates a downstream MCP server exposing one tool
// ("echo") and returns the client-side transport that the proxy should use
// to connect to it. The returned cleanup closes the server session.
//
// Per the SDK: the server must be connected before the client.
func newFakeDownstream(t *testing.T, ctx context.Context) (mcp.Transport, func()) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-downstream", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echo the provided value back as text",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in downstreamToolInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Value}},
		}, nil, nil
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("downstream server connect: %v", err)
	}
	return clientTransport, func() { _ = serverSession.Close() }
}

// newSlowDownstream creates a downstream MCP server whose only tool ("slow")
// does not respond for slowFor, so caller-side timeouts can be exercised. The
// returned cleanup closes the server session.
func newSlowDownstream(t *testing.T, ctx context.Context, slowFor time.Duration) (mcp.Transport, func()) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-slow-downstream", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "slow",
		Description: "does not respond until slowFor elapses",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ slowToolInput) (*mcp.CallToolResult, any, error) {
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("downstream cancelled: %w", ctx.Err())
		case <-time.After(slowFor):
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "slow:done"}},
			}, nil, nil
		}
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("downstream server connect: %v", err)
	}
	return clientTransport, func() { _ = serverSession.Close() }
}

// testProxy builds a Proxy whose cache uses the given transport factory.
func testProxy(root *Category, factory TransportFactory) *Proxy {
	return &Proxy{cache: NewServerCacheWithFactory(factory), root: root}
}

func TestProxy_ListTools_LazyLoad(t *testing.T) {
	ctx := context.Background()
	clientTransport, cleanup := newFakeDownstream(t, ctx)
	defer cleanup()

	// Factory returns the in-memory client transport regardless of def/path.
	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return clientTransport, nil
	}
	root := buildTestTree()
	proxy := testProxy(root, factory)

	// coding.serena -> downstream server exposes the "echo" tool.
	tools, err := proxy.ListTools(ctx, "coding.serena")
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("expected 1 tool named echo, got %+v", tools)
	}
	if tools[0].Description != "echo the provided value back as text" {
		t.Errorf("description = %q", tools[0].Description)
	}

	// Second call should reuse the cached session (no new connection).
	tools2, err := proxy.ListTools(ctx, "coding.serena")
	if err != nil {
		t.Fatalf("ListTools (cached): %v", err)
	}
	if len(tools2) != len(tools) {
		t.Errorf("cached tool count differs: %d vs %d", len(tools2), len(tools))
	}
}

func TestProxy_CallTool_ForwardsResultAsIs(t *testing.T) {
	ctx := context.Background()
	clientTransport, cleanup := newFakeDownstream(t, ctx)
	defer cleanup()

	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return clientTransport, nil
	}
	root := buildTestTree()
	proxy := testProxy(root, factory)

	res, err := proxy.CallTool(ctx, "coding.serena", "echo", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// Result forwarded as-is: single text content "echo:hello".
	if res.IsError {
		t.Errorf("unexpected IsError=true: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if tc.Text != "echo:hello" {
		t.Errorf("text = %q, want echo:hello", tc.Text)
	}
}

func TestProxy_DescribeTool_ReturnsTool(t *testing.T) {
	ctx := context.Background()
	clientTransport, cleanup := newFakeDownstream(t, ctx)
	defer cleanup()

	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return clientTransport, nil
	}
	root := buildTestTree()
	proxy := testProxy(root, factory)

	tool, err := proxy.DescribeTool(ctx, "coding.serena.echo")
	if err != nil {
		t.Fatalf("DescribeTool: %v", err)
	}
	if tool.Name != "echo" {
		t.Errorf("Name = %q, want echo", tool.Name)
	}
	if tool.Description != "echo the provided value back as text" {
		t.Errorf("Description = %q", tool.Description)
	}
	if tool.InputSchema == nil {
		t.Error("expected non-nil InputSchema")
	}

	// Unknown tool on a valid server surfaces an error naming the tool.
	_, err = proxy.DescribeTool(ctx, "coding.serena.nope")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not mention tool name", err)
	}
}

func TestProxy_ResolveToolPath_SingleServer(t *testing.T) {
	ctx := context.Background()
	clientTransport, cleanup := newFakeDownstream(t, ctx)
	defer cleanup()

	factory := func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return clientTransport, nil
	}
	root := buildTestTree()
	proxy := testProxy(root, factory)

	sp, tool, err := proxy.ResolveToolPath(ctx, "coding.serena.echo")
	if err != nil {
		t.Fatalf("ResolveToolPath: %v", err)
	}
	if sp != "coding.serena" {
		t.Errorf("serverPath = %q, want coding.serena", sp)
	}
	if tool != "echo" {
		t.Errorf("toolName = %q, want echo", tool)
	}
}

func TestProxy_ResolveToolPath_Errors(t *testing.T) {
	ctx := context.Background()
	root := buildTestTree()
	proxy := testProxy(root, func(_ *ServerDef, _ string) (mcp.Transport, error) {
		t.Fatal("factory should not be called for error cases")
		return nil, nil
	})

	// Missing tool name.
	if _, _, err := proxy.ResolveToolPath(ctx, ""); err == nil {
		t.Error("expected error for empty path")
	}
	// Nonexistent category.
	if _, _, err := proxy.ResolveToolPath(ctx, "nope.something"); err == nil {
		t.Error("expected error for nonexistent category")
	}
	// Category with no servers (web has subcategories, but "web" itself has a
	// server too; pick a path that lands on a no-server category). "web.browsers"
	// has a server, so use a truly child-only construct via a synthetic tree.
	synthetic := &Category{Children: map[string]*Category{
		"leaf": {Description: "no servers here"},
	}}
	p2 := testProxy(synthetic, func(_ *ServerDef, _ string) (mcp.Transport, error) { return nil, nil })
	if _, _, err := p2.ResolveToolPath(ctx, "leaf.x"); err == nil {
		t.Error("expected error for category with no servers")
	}
}

func TestProxy_ResolveToolTimeout(t *testing.T) {
	// Tree:
	//   root   (Timeout 10s)
	//   ├── coding (Timeout 5s)
	//   │   ├── serena (Timeout 3s, ToolTimeouts: echo=1s, free=0s)
	//   │   └── other  (no timeout)
	//   └── web        (no timeout)
	//       └── github (no timeout)
	root := &Category{
		Timeout: "10s",
		Children: map[string]*Category{
			"coding": {
				Timeout: "5s",
				MCP: map[string]*ServerDef{
					"serena": {
						Command:      "echo",
						Timeout:      "3s",
						ToolTimeouts: map[string]string{"echo": "1s", "free": "0s"},
					},
					"other": {Command: "echo"},
				},
			},
			"web": {
				MCP: map[string]*ServerDef{
					"github": {Command: "echo"},
				},
			},
		},
	}
	// Use NewProxy so the programmatic tree gets its parent links set.
	proxy := NewProxy(root, NewServerCacheWithFactory(func(_ *ServerDef, _ string) (mcp.Transport, error) {
		return nil, nil
	}))

	tests := []struct {
		name       string
		serverPath string
		toolName   string
		want       time.Duration
	}{
		{"per-tool wins", "coding.serena", "echo", 1 * time.Second},
		{"per-server", "coding.serena", "other_tool", 3 * time.Second},
		{"per-tool zero disables", "coding.serena", "free", 0},
		{"category", "coding.other", "x", 5 * time.Second},
		{"global root", "web.github", "x", 10 * time.Second},
		{"unknown server defaults", "nope.s", "x", DefaultToolTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxy.ResolveToolTimeout(tt.serverPath, tt.toolName)
			if got != tt.want {
				t.Errorf("ResolveToolTimeout(%q, %q) = %v, want %v", tt.serverPath, tt.toolName, got, tt.want)
			}
		})
	}
}