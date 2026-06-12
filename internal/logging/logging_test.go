package logging

import (
	"strings"
	"testing"
)

// capture records the severity + message of each forwarded log line.
type capture struct {
	errs  []string
	warns []string
	infos []string
}

func (c *capture) Error(v ...any) error   { c.errs = append(c.errs, sprint(v)); return nil }
func (c *capture) Warning(v ...any) error { c.warns = append(c.warns, sprint(v)); return nil }
func (c *capture) Info(v ...any) error    { c.infos = append(c.infos, sprint(v)); return nil }

func sprint(v []any) string {
	if len(v) == 1 {
		if s, ok := v[0].(string); ok {
			return s
		}
	}
	return ""
}

func TestServiceLoggerDispatchesBySeverity(t *testing.T) {
	c := &capture{}
	log := NewService("info", c)

	log.Info("blocking", "country", "RU", "hosts", 3)
	log.Warn("lookup failed")
	log.Error("cleanup failed", "err", "boom")
	log.Debug("noisy") // below info → dropped

	if len(c.infos) != 1 || !strings.HasPrefix(c.infos[0], "blocking") {
		t.Fatalf("info: got %v", c.infos)
	}
	if got := c.infos[0]; !strings.Contains(got, "country=RU") || !strings.Contains(got, "hosts=3") {
		t.Fatalf("attrs not rendered: %q", got)
	}
	if len(c.warns) != 1 || c.warns[0] != "lookup failed" {
		t.Fatalf("warn: got %v", c.warns)
	}
	if len(c.errs) != 1 || !strings.Contains(c.errs[0], "err=boom") {
		t.Fatalf("error: got %v", c.errs)
	}
}

func TestServiceLoggerWithAttrs(t *testing.T) {
	c := &capture{}
	log := NewService("debug", c).With("daemon", "dezhban")
	log.Info("started", "pid", 7)

	if len(c.infos) != 1 {
		t.Fatalf("want 1 info, got %v", c.infos)
	}
	got := c.infos[0]
	if !strings.Contains(got, "daemon=dezhban") || !strings.Contains(got, "pid=7") {
		t.Fatalf("with-attrs not rendered: %q", got)
	}
}
