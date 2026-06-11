// Package logging builds dezhban's structured logger.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a text logger writing to stderr at the given level
// (debug, info, warn, error). Unknown levels fall back to info.
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}
