# lazy-mcp

A proxy MCP server that **lazily loads downstream MCP servers on demand** and
caches them for future use. It solves the context bloat that comes from loading
a huge number of MCP tools up front: instead of exposing every tool from every
server at startup, it exposes a small, fixed set of tools that let a client
discover and invoke downstream tools only when they are actually needed.

Downstream servers are connected on first use, their tool lists are cached, and
their sessions are reused for the lifetime of the process.

## How it works

`lazy-mcp` is a single MCP server (over stdio) that acts as a proxy. It reads a
configuration file describing a tree of **categories** and the **MCP servers**
under them. It exposes three tools:

| Tool | Description |
|------|-------------|
| `get_tools_in_category` | List the subcategories or tools in a category path. Empty path returns top-level categories. A category containing servers returns their tools (lazily loaded). |
| `execute_tool` | Execute a tool on a downstream MCP server. The server is lazily loaded and cached on first use. The downstream result (content, errors, structured content) is returned as-is. |
| `describe_tool` | Return the full metadata of a tool on a downstream server, including its input schema. The server is lazily loaded and cached on first use. |

### Example flow

```
1. get_tools_in_category("") → {
     "categories": {
       "coding_tools": "Development tools for coding, debugging, and analyzing code.",
       "web_tools": "Web tools for browsing and interacting with web content."
     }
   }

2. get_tools_in_category("coding_tools") → {
     "categories": {
       "serena": "Development tools for coding, debugging, and analyzing code."
     }
   }

3. get_tools_in_category("coding_tools.serena") → {
     "tools": {"find_symbol": "...", "get_symbols_overview": "..."}
   }

4. execute_tool("coding_tools.serena.find_symbol", {...})
   → Lazy loads the Serena server (if not already loaded)
   → Proxies the request to Serena
   → Returns the result
```

### Tool paths

Tools are addressed by a dot-delimited path: `<category>.<server>.<tool>`, e.g.
`coding_tools.serena.find_symbol`. The path is walked left-to-right through the
category tree; the first segment that matches a server name marks the server,
and the final segment is the tool name. If a category contains only one server,
the server segment may be omitted (e.g. `coding.find_symbol`).

## Configuration

The server reads its configuration from the path given by the
`LAZY_MCP_SERVER_CONFIG` environment variable, or, if unset, from
`lazy_mcp_server_config.json` in the current working directory (falling back to
the executable's directory).

The config is a JSON document with a global `timeout` and a `tools` category
tree. A legacy format (categories at the top level, no `tools` wrapper) is also
accepted.

```json
{
  "timeout": "60s",
  "tools": {
    "coding_tools": {
      "description": "Development tools for coding, debugging, and analyzing code.",
      "mcpServers": {
        "serena": {
          "command": "docker",
          "args": [
            "run", "-i", "--rm", "-p", "5000:5000",
            "ghcr.io/serena/serena-mcp-server"
          ]
        }
      }
    },
    "web_tools": {
      "description": "Web tools for browsing and interacting with web content.",
      "browsers": {
        "description": "Web browsers for interacting with web content.",
        "mcpServers": {
          "chrome": {
            "server_url": "http://localhost:5001",
            "tools": ["open_url", "get_page_source"]
          }
        }
      },
      "mcpServers": {
        "github": {
          "command": "docker",
          "args": [
            "run", "-i", "--rm", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN",
            "ghcr.io/github/github-mcp-server"
          ],
          "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "<YOUR_TOKEN>" }
        }
      }
    }
  }
}
```

### Server definition

Each server under `mcpServers` supports:

- `command` / `args` — run a stdio server as a child process. Exactly one of
  `command` or `server_url` must be set.
- `env` — environment variables to set on the child process.
- `server_url` — the base URL of an HTTP (streamable) server.
- `tools` — optional allow-list of tool names. If non-empty, only these tools
  are exposed for the server.
- `timeout` — maximum duration of a call to any tool of this server, as a Go
  duration string (e.g. `"60s"`, `"2m"`). `"0s"` disables the timeout.
- `timeouts` — a map of tool name to call timeout, taking precedence over
  `timeout` for the named tool.

### Timeout resolution

The timeout applied to a tool call is resolved in priority order:

1. per-tool: `server.timeouts[<tool>]`
2. per-server: `server.timeout`
3. category chain: nearest ancestor category `timeout` (the root category
   carries the global timeout from the config)
4. default: `60s`

A configured value of `"0s"` at any level disables the timeout for that scope
(the call is left unbounded).

### Logging

The server writes structured logs to `<lazy-mcp>/logs/lazy-mcp.log` with
size-based rotation. Logging is configured via environment variables:

- `LAZY_MCP_LOG_DIR` — log directory. Defaults to the `logs` folder next to the
  server.
- `LAZY_MCP_LOG_MAX_SIZE_MB` — size in megabytes at which a log file is rotated.
  Default `10`.
- `LAZY_MCP_LOG_MAX_BACKUPS` — number of rotated log files to keep. Default `5`.
- `LAZY_MCP_LOG_LEVEL` — minimum severity to record: `debug`, `info`, `warn`, or
  `error`. Default `info`.

Logs never go to stdout, keeping the stdio MCP protocol clean.

## Building and running

```sh
# Build and run over stdio (skips rebuild when sources are unchanged)
./run.sh

# Or build directly
go build -o lazy-mcp .
```

The server runs over stdio, so it is typically registered as an MCP server in a
client (e.g. Claude, an editor, or another MCP host) pointing at the `lazy-mcp`
binary.

## Project layout

- `main.go` — entry point: config + logging setup, server bootstrap.
- `config.go` — configuration loading, validation, and the category tree model.
- `category.go` — category tree helpers (path lookup, listing, parent linking).
- `cache.go` — lazy server cache, transport construction, and tool filtering.
- `proxy.go` — the proxy: lazy connect, tool listing, execution, and path/timeout
  resolution.
- `handlers.go` — the three exposed MCP tools.
- `logging.go` — structured, rotating file logging.
- `spec/` — design notes and the original spec.

## Implementation notes

- Written in Go using `github.com/modelcontextprotocol/go-sdk/mcp`.
- Downstream servers are connected lazily and cached in a map keyed by their
  full category path; a mutex guards the cache and a per-server mutex makes
  connection a singleflight (concurrent callers block until the first finishes).
- Sessions are never evicted; they live until the cache is closed on shutdown.
- Command arguments support shell-style expansion: `$(pwd)` expands to the
  current working directory, and `$VAR` / `${VAR}` expand to environment
  variables.
