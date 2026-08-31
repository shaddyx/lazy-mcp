package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TransportFactory builds an MCP transport for a server definition and path.
// Tests inject a custom factory to wire up in-memory transports; production
// uses the default command/streamable transport built by transportFor.
type TransportFactory func(def *ServerDef, path string) (mcp.Transport, error)

// CachedServer is a lazily-loaded downstream MCP server. The first call to
// ensureConnected establishes the client session and lists tools; subsequent
// calls reuse the cached session and tool list. Sessions are never evicted
// (they live until the cache is closed).
type CachedServer struct {
	Def              *ServerDef
	Path             string // full dot-delimited path, e.g. "coding_tools.serena"
	transportFactory TransportFactory
	Session          *mcp.ClientSession
	Tools            []*mcp.Tool
	logger           *slog.Logger

	mu     sync.Mutex
	loaded bool
}

// ensureConnected lazily connects to the downstream server and lists its
// tools. The per-server mutex makes this a singleflight: concurrent callers
// block until the first caller finishes connecting.
func (cs *CachedServer) ensureConnected(ctx context.Context) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.loaded {
		return nil
	}

	log := loggerOrDefault(cs.logger)
	log.Info("connecting to downstream server",
		"server", cs.Path,
		"command", cs.Def.Command,
		"url", cs.Def.ServerURL,
	)

	factory := cs.transportFactory
	if factory == nil {
		factory = defaultTransportFactory
	}
	transport, err := factory(cs.Def, cs.Path)
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "lazy-mcp-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect %s: %w", cs.Path, err)
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("list tools %s: %w", cs.Path, err)
	}
	cs.Session = session
	cs.Tools = filterTools(res.Tools, cs.Def.Tools)
	cs.loaded = true
	log.Info("downstream server loaded", "server", cs.Path, "tools", len(cs.Tools))
	return nil
}

// ServerCache stores lazily-loaded downstream servers keyed by their full
// category path.
type ServerCache struct {
	mu               sync.RWMutex
	servers          map[string]*CachedServer
	transportFactory TransportFactory // nil = default
	logger           *slog.Logger     // nil = discard
}

// NewServerCache returns an empty server cache using the default transport
// factory (command/streamable from ServerDef).
func NewServerCache() *ServerCache {
	return &ServerCache{servers: make(map[string]*CachedServer)}
}

// NewServerCacheWithFactory returns a server cache that uses the given
// transport factory for connecting to downstream servers. Intended for tests.
func NewServerCacheWithFactory(f TransportFactory) *ServerCache {
	return &ServerCache{servers: make(map[string]*CachedServer), transportFactory: f}
}

// SetLogger sets the logger used for downstream connection events. It must
// be called before any server is loaded.
func (sc *ServerCache) SetLogger(l *slog.Logger) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.logger = l
}

// defaultTransportFactory builds a transport from the server definition.
func defaultTransportFactory(def *ServerDef, _ string) (mcp.Transport, error) {
	return transportFor(def)
}

// GetOrCreate returns the CachedServer for the given path, creating an
// unloaded entry on first access. The entry is safe for concurrent use.
func (sc *ServerCache) GetOrCreate(def *ServerDef, path string) *CachedServer {
	sc.mu.RLock()
	if s, ok := sc.servers[path]; ok {
		sc.mu.RUnlock()
		return s
	}
	sc.mu.RUnlock()

	sc.mu.Lock()
	defer sc.mu.Unlock()
	if s, ok := sc.servers[path]; ok {
		return s
	}
	s := &CachedServer{Def: def, Path: path, transportFactory: sc.transportFactory, logger: sc.logger}
	sc.servers[path] = s
	return s
}

// Close closes all cached client sessions. Safe to call once on shutdown.
func (sc *ServerCache) Close() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var firstErr error
	for _, s := range sc.servers {
		if s.Session != nil {
			if err := s.Session.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// expandArgs substitutes shell-style variables in command arguments.
// exec.Command does not run through a shell, so "$(pwd)" and "$VAR" in config
// args would otherwise be passed literally (e.g. docker rejects "$(pwd)" as
// an invalid volume name). "$(pwd)" expands to the current working directory;
// "$VAR" / "${VAR}" expand to environment variables.
func expandArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = os.Expand(strings.ReplaceAll(a, "$(pwd)", pwd()), os.Getenv)
	}
	return out
}

// pwd returns the current working directory, or "" if it cannot be determined.
func pwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// transportFor builds the MCP transport for a server definition.
func transportFor(def *ServerDef) (mcp.Transport, error) {
	if def.IsStdio() {
		cmd := exec.Command(def.Command, expandArgs(def.Args)...)
		if len(def.Env) > 0 {
			env := os.Environ()
			for k, v := range def.Env {
				env = append(env, k+"="+v)
			}
			cmd.Env = env
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	}
	if def.ServerURL != "" {
		return &mcp.StreamableClientTransport{Endpoint: def.ServerURL}, nil
	}
	return nil, fmt.Errorf("server has neither command nor server_url")
}

// filterTools returns only the tools whose names appear in allow. If allow is
// empty, all tools are returned.
func filterTools(tools []*mcp.Tool, allow []string) []*mcp.Tool {
	if len(allow) == 0 {
		return tools
	}
	allowed := make(map[string]struct{}, len(allow))
	for _, t := range allow {
		allowed[t] = struct{}{}
	}
	out := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if _, ok := allowed[t.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}