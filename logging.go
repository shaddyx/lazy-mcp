package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LogConfig holds the file logging configuration. All fields are
// configurable via environment variables (see LoadLogConfig).
type LogConfig struct {
	// Dir is the directory where log files are written.
	Dir string
	// MaxSizeMB is the size in megabytes at which a log file is rotated.
	MaxSizeMB int
	// MaxBackups is the number of rotated log files to keep.
	MaxBackups int
	// Level is the minimum severity to record.
	Level slog.Level
}

// DefaultLogConfig returns the default logging configuration: logs in
// <lazy-mcp>/logs, 10 MB per file, 5 rotated files, info level.
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Dir:        resolveLogDir(),
		MaxSizeMB:  10,
		MaxBackups: 5,
		Level:      slog.LevelInfo,
	}
}

// LoadLogConfig reads the logging configuration from the environment:
//
//	LAZY_MCP_LOG_DIR          log directory (default: <lazy-mcp>/logs)
//	LAZY_MCP_LOG_MAX_SIZE_MB  per-file size in MB before rotation (default 10)
//	LAZY_MCP_LOG_MAX_BACKUPS  rotated files to keep (default 5)
//	LAZY_MCP_LOG_LEVEL        debug|info|warn|error (default info)
func LoadLogConfig() (LogConfig, error) {
	cfg := DefaultLogConfig()
	if v := os.Getenv("LAZY_MCP_LOG_DIR"); v != "" {
		cfg.Dir = v
	}
	if v := os.Getenv("LAZY_MCP_LOG_MAX_SIZE_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("LAZY_MCP_LOG_MAX_SIZE_MB: invalid value %q", v)
		}
		cfg.MaxSizeMB = n
	}
	if v := os.Getenv("LAZY_MCP_LOG_MAX_BACKUPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("LAZY_MCP_LOG_MAX_BACKUPS: invalid value %q", v)
		}
		cfg.MaxBackups = n
	}
	if v := os.Getenv("LAZY_MCP_LOG_LEVEL"); v != "" {
		lvl, err := parseLevel(v)
		if err != nil {
			return cfg, err
		}
		cfg.Level = lvl
	}
	return cfg, nil
}

// parseLevel converts a level name to a slog.Level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LAZY_MCP_LOG_LEVEL: invalid level %q (want debug|info|warn|error)", s)
}

// resolveLogDir returns the default log directory: <lazy-mcp>/logs when the
// executable lives in the source directory (the run.sh build layout), and
// ./logs otherwise (e.g. `go run`). LAZY_MCP_LOG_DIR overrides it.
func resolveLogDir() string {
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(exeDir, "main.go")); err == nil {
			return filepath.Join(exeDir, "logs")
		}
	}
	return "logs"
}

// NewLogger creates a slog logger that writes to <Dir>/lazy-mcp.log with
// size-based rotation (MaxSizeMB per file, MaxBackups rotated files kept).
// The returned close function flushes and closes the underlying log file;
// call it on shutdown. If the log directory cannot be created or the file
// cannot be opened, an error is returned.
func NewLogger(cfg LogConfig) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log dir %s: %w", cfg.Dir, err)
	}
	lj := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, "lazy-mcp.log"),
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		LocalTime:  true,
	}
	level := new(slog.LevelVar)
	level.Set(cfg.Level)
	handler := slog.NewTextHandler(lj, &slog.HandlerOptions{Level: level})
	return slog.New(handler), func() { _ = lj.Close() }, nil
}

// discardLogger is used when no logger is configured (e.g. in tests).
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// loggerOrDefault returns l, or a no-op logger when l is nil.
func loggerOrDefault(l *slog.Logger) *slog.Logger {
	if l == nil {
		return discardLogger
	}
	return l
}
