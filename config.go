package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ServerDef is a single downstream MCP server. Exactly one of Command or
// ServerURL must be set.
type ServerDef struct {
	// Command is the executable to run for a stdio server.
	Command string `json:"command,omitempty"`
	// Args are the arguments passed to Command.
	Args []string `json:"args,omitempty"`
	// Env is the environment variables to set on the child process.
	Env map[string]string `json:"env,omitempty"`
	// ServerURL is the base URL of an HTTP (streamable) server.
	ServerURL string `json:"server_url,omitempty"`
	// Tools is an optional allow-list of tool names. If non-empty, only tools
	// with these names are exposed for this server.
	Tools []string `json:"tools,omitempty"`
	// Timeout is the maximum duration of a call to any tool of this server, as
	// a Go duration string (e.g. "60s", "2m"). ToolTimeouts take precedence for
	// the tools they name; "0s" disables the timeout for this server.
	Timeout string `json:"timeout,omitempty"`
	// ToolTimeouts maps a downstream tool name to its call timeout, as a Go
	// duration string. Takes precedence over Timeout for the named tool; "0s"
	// disables the timeout for that tool.
	ToolTimeouts map[string]string `json:"timeouts,omitempty"`
}

// IsStdio reports whether this server is a stdio (command) server.
func (s *ServerDef) IsStdio() bool { return s.Command != "" }

// Validate returns an error if the server definition is invalid.
func (s *ServerDef) Validate() error {
	if s.Command != "" && s.ServerURL != "" {
		return fmt.Errorf("server has both command and server_url; exactly one is allowed")
	}
	if s.Command == "" && s.ServerURL == "" {
		return fmt.Errorf("server must have either command or server_url")
	}
	if s.Timeout != "" {
		if _, err := parseTimeout(s.Timeout); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", s.Timeout, err)
		}
	}
	for name, t := range s.ToolTimeouts {
		if _, err := parseTimeout(t); err != nil {
			return fmt.Errorf("invalid timeout for tool %q: %w", name, err)
		}
	}
	return nil
}

// Category is a node in the category tree. A category may contain nested
// subcategories (Children) and/or downstream MCP servers (MCP).
type Category struct {
	// Description is a human-facing description of the category.
	Description string `json:"description,omitempty"`
	// Timeout is the default call timeout for tools in this category and its
	// descendants, as a Go duration string. Servers and nested categories may
	// override it; the root category carries the global timeout. "0s" disables
	// the timeout for the scope of this category.
	Timeout string `json:"timeout,omitempty"`
	// MCP holds the downstream servers directly in this category, keyed by name.
	MCP map[string]*ServerDef `json:"mcpServers,omitempty"`
	// Children holds nested subcategories, keyed by name. This is populated
	// during UnmarshalJSON from any key whose value is a Category object.
	Children map[string]*Category `json:"-"`
	// parent points to the containing category, or nil for the root. It is set
	// during UnmarshalJSON so timeout resolution can walk up the tree.
	parent *Category
}

// UnmarshalJSON parses a category object, splitting the reserved keys
// ("description", "timeout", "mcpServers") from nested subcategories.
func (c *Category) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["description"]; ok {
		if err := json.Unmarshal(v, &c.Description); err != nil {
			return fmt.Errorf("description: %w", err)
		}
		delete(raw, "description")
	}
	if v, ok := raw["timeout"]; ok {
		if err := json.Unmarshal(v, &c.Timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
		delete(raw, "timeout")
	}
	if v, ok := raw["mcpServers"]; ok {
		if err := json.Unmarshal(v, &c.MCP); err != nil {
			return fmt.Errorf("mcpServers: %w", err)
		}
		delete(raw, "mcpServers")
	}
	// Any remaining keys are nested subcategories.
	for key, v := range raw {
		child := &Category{}
		if err := json.Unmarshal(v, child); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		child.parent = c
		if c.Children == nil {
			c.Children = map[string]*Category{}
		}
		c.Children[key] = child
	}
	return nil
}

// validate walks the category tree and validates every server definition and
// timeout value.
func (c *Category) validate(path string) error {
	if c.Timeout != "" {
		if _, err := parseTimeout(c.Timeout); err != nil {
			name := path
			if name == "" {
				name = "<root>"
			}
			return fmt.Errorf("category %s: invalid timeout %q: %w", name, c.Timeout, err)
		}
	}
	for name, def := range c.MCP {
		if err := def.Validate(); err != nil {
			return fmt.Errorf("server %s.%s: %w", path, name, err)
		}
	}
	for name, child := range c.Children {
		childPath := name
		if path != "" {
			childPath = path + "." + name
		}
		if err := child.validate(childPath); err != nil {
			return err
		}
	}
	return nil
}

// config is the new-format root of the configuration file: the global timeout
// and the category tree ("tools") as separate top-level keys. The legacy
// format has the categories at the top level with no "tools" wrapper.
type config struct {
	// Timeout is the global default timeout for all downstream tool calls, as
	// a Go duration string. "0s" disables timeouts globally.
	Timeout string `json:"timeout,omitempty"`
	// Tools is the category tree.
	Tools *Category `json:"tools,omitempty"`
}

// DefaultToolTimeout is the timeout applied to a tool call when no timeout is
// configured anywhere in the resolution chain.
const DefaultToolTimeout = 60 * time.Second

// parseTimeout parses a Go duration string such as "60s", "2m", or "500ms".
func parseTimeout(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// LoadConfig reads the configuration from the path given by the
// LAZY_MCP_SERVER_CONFIG environment variable, or, if unset, from
// "lazy_mcp_server_config.json" in the current working directory.
func LoadConfig() (*Category, error) {
	path := os.Getenv("LAZY_MCP_SERVER_CONFIG")
	if path == "" {
		path = "lazy_mcp_server_config.json"
	}
	if !filepath.IsAbs(path) {
		// Prefer a path relative to the executable directory if the file is
		// not present in the cwd, so `go run` and built binaries both work.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if exe, err := os.Executable(); err == nil {
				cand := filepath.Join(filepath.Dir(exe), path)
				if _, err := os.Stat(cand); err == nil {
					path = cand
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// New format: {"timeout": ..., "tools": {...}}.
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	root := cfg.Tools
	if root == nil {
		// Legacy format: the whole document is the category tree. (A top-level
		// category literally named "tools" would be mistaken for the new
		// format; the shipped configs do not use such a name.)
		root = &Category{}
		if err := json.Unmarshal(data, root); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if cfg.Timeout != "" {
		// Promote the global timeout onto the root category, where the
		// timeout resolution walk finds it.
		root.Timeout = cfg.Timeout
	}
	if err := root.validate(""); err != nil {
		return nil, err
	}
	return root, nil
}