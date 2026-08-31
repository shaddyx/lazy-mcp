package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes content to a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lazy_mcp_server_config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfig_ExampleFile(t *testing.T) {
	// Use the checked-in example config in the package directory.
	t.Setenv("LAZY_MCP_SERVER_CONFIG", "lazy_mcp_server_config.json")
	root, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Top-level categories.
	if len(root.Children) != 2 {
		t.Fatalf("expected 2 top-level categories, got %d", len(root.Children))
	}
	coding := root.Children["coding_tools"]
	if coding == nil {
		t.Fatal("missing coding_tools")
	}
	if coding.Description == "" {
		t.Error("coding_tools has empty description")
	}
	// coding_tools has a single mcpServer: serena (stdio).
	if len(coding.MCP) != 1 {
		t.Fatalf("coding_tools: expected 1 server, got %d", len(coding.MCP))
	}
	serena := coding.MCP["serena"]
	if serena == nil || !serena.IsStdio() {
		t.Errorf("serena: expected stdio server, got %+v", serena)
	}
	if serena.Command != "docker" {
		t.Errorf("serena command = %q, want docker", serena.Command)
	}

	// web_tools has both a nested category (browsers) and direct servers (github).
	web := root.Children["web_tools"]
	if web == nil {
		t.Fatal("missing web_tools")
	}
	browsers := web.Children["browsers"]
	if browsers == nil {
		t.Fatal("missing web_tools.browsers nested category")
	}
	if len(browsers.MCP) != 1 {
		t.Fatalf("browsers: expected 1 server, got %d", len(browsers.MCP))
	}
	chrome := browsers.MCP["chrome"]
	if chrome == nil || !chrome.IsStdio() {
		t.Errorf("chrome: expected stdio server, got %+v", chrome)
	}
	if chrome.Command != "npx" {
		t.Errorf("chrome command = %q, want npx", chrome.Command)
	}
	if len(chrome.Args) != 2 || chrome.Args[1] != "chrome-devtools-mcp@latest" {
		t.Errorf("chrome args = %+v, want [-y chrome-devtools-mcp@latest]", chrome.Args)
	}

	// github is a direct stdio server under web_tools, with env.
	if len(web.MCP) != 1 {
		t.Fatalf("web_tools: expected 1 direct server, got %d", len(web.MCP))
	}
	github := web.MCP["github"]
	if github == nil || !github.IsStdio() {
		t.Errorf("github: expected stdio server, got %+v", github)
	}
	if github.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "<YOUR_TOKEN>" {
		t.Errorf("github env not parsed: %+v", github.Env)
	}
}

func TestLoadConfig_NestedCategoryParsing(t *testing.T) {
	const cfg = `{
		"a": {
			"description": "top",
			"b": {
				"description": "mid",
				"c": {
					"description": "leaf",
					"mcpServers": {
						"s1": {"command": "echo"}
					}
				}
			}
		}
	}`
	path := writeTempConfig(t, cfg)
	t.Setenv("LAZY_MCP_SERVER_CONFIG", path)
	root, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Walk a.b.c.s1.
	a := root.Children["a"]
	if a == nil || a.Description != "top" {
		t.Fatalf("a: %+v", a)
	}
	b := a.Children["b"]
	if b == nil || b.Description != "mid" {
		t.Fatalf("b: %+v", b)
	}
	c := b.Children["c"]
	if c == nil || c.Description != "leaf" {
		t.Fatalf("c: %+v", c)
	}
	if len(c.MCP) != 1 || c.MCP["s1"] == nil {
		t.Fatalf("c.mcpServers: %+v", c.MCP)
	}
}

func TestServerDef_Validate(t *testing.T) {
	tests := []struct {
		name    string
		def     *ServerDef
		wantErr bool
	}{
		{"stdio ok", &ServerDef{Command: "docker", Args: []string{"run"}}, false},
		{"http ok", &ServerDef{ServerURL: "http://localhost:5001"}, false},
		{"both set", &ServerDef{Command: "docker", ServerURL: "http://x"}, true},
		{"neither set", &ServerDef{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.def.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "cfg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "lazy_mcp_server_config.json")
	if err := os.WriteFile(path, []byte(`{"a": {"mcpServers": {"s1": {"command": "echo"}}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("LAZY_MCP_SERVER_CONFIG", "~/cfg/lazy_mcp_server_config.json")
	root, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with ~ path: %v", err)
	}
	if root.Children["a"] == nil {
		t.Fatal("expected category a to be loaded from ~ path")
	}
}

func TestLoadConfig_ValidationErrors(t *testing.T) {
	const cfg = `{
		"bad": {
			"mcpServers": {
				"s": {"command": "x", "server_url": "http://y"}
			}
		}
	}`
	path := writeTempConfig(t, cfg)
	t.Setenv("LAZY_MCP_SERVER_CONFIG", path)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected validation error for server with both command and server_url, got nil")
	}
}

func TestLoadConfig_NewFormat(t *testing.T) {
	const cfg = `{
		"timeout": "30s",
		"tools": {
			"coding_tools": {
				"description": "coding",
				"timeout": "15s",
				"mcpServers": {
					"serena": {
						"timeout": "120s",
						"timeouts": {"find_symbol": "5s"},
						"command": "docker"
					}
				}
			}
		}
	}`
	path := writeTempConfig(t, cfg)
	t.Setenv("LAZY_MCP_SERVER_CONFIG", path)
	root, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// The global timeout is promoted onto the root category.
	if root.Timeout != "30s" {
		t.Errorf("root.Timeout = %q, want 30s (global promoted)", root.Timeout)
	}
	coding := root.Children["coding_tools"]
	if coding == nil {
		t.Fatal("missing coding_tools")
	}
	if coding.Timeout != "15s" {
		t.Errorf("coding.Timeout = %q, want 15s", coding.Timeout)
	}
	serena := coding.MCP["serena"]
	if serena == nil {
		t.Fatal("missing serena")
	}
	if serena.Timeout != "120s" {
		t.Errorf("serena.Timeout = %q, want 120s", serena.Timeout)
	}
	if serena.ToolTimeouts["find_symbol"] != "5s" {
		t.Errorf("serena.ToolTimeouts = %+v, want find_symbol:5s", serena.ToolTimeouts)
	}
	// Parent links are set during UnmarshalJSON.
	if coding.parent != root {
		t.Error("coding.parent not linked to root")
	}
}

func TestLoadConfig_LegacyFormat(t *testing.T) {
	const cfg = `{
		"a": {
			"description": "legacy",
			"mcpServers": {"s1": {"command": "echo"}}
		}
	}`
	path := writeTempConfig(t, cfg)
	t.Setenv("LAZY_MCP_SERVER_CONFIG", path)
	root, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	a := root.Children["a"]
	if a == nil || a.Description != "legacy" {
		t.Fatalf("a: %+v", a)
	}
	if len(a.MCP) != 1 || a.MCP["s1"] == nil {
		t.Fatalf("a.mcpServers: %+v", a.MCP)
	}
	if root.Timeout != "" {
		t.Errorf("root.Timeout = %q, want empty for legacy format", root.Timeout)
	}
}

func TestLoadConfig_InvalidTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
	}{
		{
			"global",
			`{"timeout": "not-a-duration", "tools": {"a": {"mcpServers": {"s": {"command": "x"}}}}}`,
		},
		{
			"category",
			`{"timeout": "60s", "tools": {"a": {"timeout": "abc", "mcpServers": {"s": {"command": "x"}}}}}`,
		},
		{
			"server",
			`{"timeout": "60s", "tools": {"a": {"mcpServers": {"s": {"timeout": "xyz", "command": "x"}}}}}`,
		},
		{
			"tool",
			`{"timeout": "60s", "tools": {"a": {"mcpServers": {"s": {"timeouts": {"t": "nope"}, "command": "x"}}}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.cfg)
			t.Setenv("LAZY_MCP_SERVER_CONFIG", path)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("expected error for invalid duration, got nil")
			}
		})
	}
}