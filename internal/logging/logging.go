// Package logging builds dezhban's structured logger.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// parseLevel maps a config level string to a slog.Level. Unknown levels are info.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New returns a text logger writing to stderr at the given level
// (debug, info, warn, error). Unknown levels fall back to info.
func New(level string) *slog.Logger {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(h)
}

// ServiceLogger is the subset of github.com/kardianos/service.Logger that
// NewService forwards records to. The platform logger returned by a kardianos
// service satisfies it, so the logging package stays free of that dependency.
type ServiceLogger interface {
	Error(v ...any) error
	Warning(v ...any) error
	Info(v ...any) error
}

// NewService returns a logger that forwards records to the OS service logger
// (syslog/journald on unix, the Windows Event Log) at the given minimum level.
// It is used when dezhban runs under a service manager, where stderr is not
// captured anywhere useful.
func NewService(level string, sl ServiceLogger) *slog.Logger {
	return slog.New(&serviceHandler{level: parseLevel(level), sl: sl})
}

// serviceHandler is a minimal slog.Handler that renders each record as a single
// "msg key=val ..." line and dispatches it to the platform logger by severity.
// Groups are flattened away — the service logs are line-oriented, not nested.
type serviceHandler struct {
	level slog.Level
	sl    ServiceLogger
	attrs string // preformatted attrs accumulated via WithAttrs
}

func (h *serviceHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *serviceHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	msg := b.String()
	switch {
	case r.Level >= slog.LevelError:
		_ = h.sl.Error(msg)
	case r.Level >= slog.LevelWarn:
		_ = h.sl.Warning(msg)
	default:
		_ = h.sl.Info(msg)
	}
	return nil
}

func (h *serviceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		writeAttr(&b, a)
	}
	nh := *h
	nh.attrs = b.String()
	return &nh
}

// WithGroup is a no-op: the service logs are flat lines, so group nesting is
// dropped rather than encoded.
func (h *serviceHandler) WithGroup(string) slog.Handler { return h }

func writeAttr(b *strings.Builder, a slog.Attr) {
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	b.WriteString(a.Value.String())
}
