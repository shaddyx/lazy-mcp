package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLogConfig_Defaults(t *testing.T) {
	cfg, err := LoadLogConfig()
	if err != nil {
		t.Fatalf("LoadLogConfig: %v", err)
	}
	if cfg.MaxSizeMB != 10 {
		t.Errorf("MaxSizeMB = %d, want 10", cfg.MaxSizeMB)
	}
	if cfg.MaxBackups != 5 {
		t.Errorf("MaxBackups = %d, want 5", cfg.MaxBackups)
	}
	if cfg.Level != slog.LevelInfo {
		t.Errorf("Level = %v, want info", cfg.Level)
	}
	if cfg.Dir == "" {
		t.Error("Dir is empty")
	}
}

func TestLoadLogConfig_EnvOverrides(t *testing.T) {
	t.Setenv("LAZY_MCP_LOG_DIR", "/tmp/lazy-mcp-test-logs")
	t.Setenv("LAZY_MCP_LOG_MAX_SIZE_MB", "25")
	t.Setenv("LAZY_MCP_LOG_MAX_BACKUPS", "3")
	t.Setenv("LAZY_MCP_LOG_LEVEL", "debug")

	cfg, err := LoadLogConfig()
	if err != nil {
		t.Fatalf("LoadLogConfig: %v", err)
	}
	if cfg.Dir != "/tmp/lazy-mcp-test-logs" {
		t.Errorf("Dir = %q, want /tmp/lazy-mcp-test-logs", cfg.Dir)
	}
	if cfg.MaxSizeMB != 25 {
		t.Errorf("MaxSizeMB = %d, want 25", cfg.MaxSizeMB)
	}
	if cfg.MaxBackups != 3 {
		t.Errorf("MaxBackups = %d, want 3", cfg.MaxBackups)
	}
	if cfg.Level != slog.LevelDebug {
		t.Errorf("Level = %v, want debug", cfg.Level)
	}
}

func TestLoadLogConfig_Invalid(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"bad size", map[string]string{"LAZY_MCP_LOG_MAX_SIZE_MB": "abc"}},
		{"zero size", map[string]string{"LAZY_MCP_LOG_MAX_SIZE_MB": "0"}},
		{"negative size", map[string]string{"LAZY_MCP_LOG_MAX_SIZE_MB": "-5"}},
		{"bad backups", map[string]string{"LAZY_MCP_LOG_MAX_BACKUPS": "x"}},
		{"bad level", map[string]string{"LAZY_MCP_LOG_LEVEL": "verbose"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := LoadLogConfig(); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestNewLogger_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	logger, closeLog, err := NewLogger(LogConfig{Dir: dir, MaxSizeMB: 10, MaxBackups: 5, Level: slog.LevelInfo})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closeLog()

	logger.Info("hello from test", "key", "value")
	closeLog()

	data, err := os.ReadFile(filepath.Join(dir, "lazy-mcp.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "hello from test") {
		t.Errorf("log file missing message: %q", text)
	}
	if !strings.Contains(text, "key=value") {
		t.Errorf("log file missing attribute: %q", text)
	}
}

func TestNewLogger_Rotates(t *testing.T) {
	dir := t.TempDir()
	// 1 MB per file, keep 2 backups.
	logger, closeLog, err := NewLogger(LogConfig{Dir: dir, MaxSizeMB: 1, MaxBackups: 2, Level: slog.LevelDebug})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closeLog()

	// Write well over 1 MB so the file must rotate at least once.
	payload := strings.Repeat("x", 6000)
	for i := 0; i < 250; i++ {
		logger.Debug("rotation test", "payload", payload)
	}
	closeLog()

	// Rotated backups are named lazy-mcp-<timestamp>.log, so match the prefix.
	matches, err := filepath.Glob(filepath.Join(dir, "lazy-mcp*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) < 2 {
		t.Errorf("expected at least 2 log files after rotation, got %d: %v", len(matches), matches)
	}
}

func TestLoggerOrDefault(t *testing.T) {
	if got := loggerOrDefault(nil); got == nil {
		t.Error("loggerOrDefault(nil) returned nil")
	}
	l := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if got := loggerOrDefault(l); got != l {
		t.Error("loggerOrDefault(l) did not return the same logger")
	}
}
