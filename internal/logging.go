package internal

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ParseLogLevel converts a string log level to slog.Level
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warning", "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "Unknown log level: %s, using 'info'\n", level)
		return slog.LevelInfo
	}
}
