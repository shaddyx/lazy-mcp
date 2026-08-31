// Command lazy-mcp is a proxy MCP server that lazily loads downstream MCP
// servers on demand and exposes three tools: get_tools_in_category, execute_tool,
// and describe_tool.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logCfg, err := LoadLogConfig()
	if err != nil {
		log.Fatalf("lazy-mcp: logging config: %v", err)
	}
	logger, closeLog, err := NewLogger(logCfg)
	if err != nil {
		// Logging is best-effort: fall back to stderr and keep serving.
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
		logger.Warn("file logging unavailable, falling back to stderr", "error", err)
		closeLog = func() {}
	}
	defer closeLog()

	root, err := LoadConfig()
	if err != nil {
		logger.Error("config load failed", "error", err)
		log.Fatalf("lazy-mcp: config: %v", err)
	}

	logger.Info("lazy-mcp starting",
		"log_dir", logCfg.Dir,
		"log_max_size_mb", logCfg.MaxSizeMB,
		"log_max_backups", logCfg.MaxBackups,
		"log_level", logCfg.Level.String(),
	)

	cache := NewServerCache()
	cache.SetLogger(logger)
	proxy := &Proxy{cache: cache, root: root, logger: logger}
	defer proxy.cache.Close()

	server := mcp.NewServer(&mcp.Implementation{Name: "lazy-mcp", Version: "0.1.0"}, nil)
	registerHandlers(server, proxy)

	logger.Info("lazy-mcp server running", "transport", "stdio")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Error("server stopped with error", "error", err)
		log.Fatalf("lazy-mcp: server: %v", err)
	}
	logger.Info("lazy-mcp stopped")
}