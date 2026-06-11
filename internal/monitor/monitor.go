package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Default timeouts for a single provider lookup.
const (
	defaultLookupTimeout = 6 * time.Second
	defaultClientTimeout = 10 * time.Second
)

// Result is one polling outcome: a Reading, or an error if every provider failed.
type Result struct {
	Reading Reading
	Err     error
}

// Monitor polls the configured providers on an interval and reports the
// resolved country, falling back across providers for redundancy.
type Monitor struct {
	providers     []GeoProvider
	interval      time.Duration
	lookupTimeout time.Duration
	client        *http.Client
	log           *slog.Logger
}

// New builds a Monitor. interval is the poll period; a sane HTTP client with
// per-lookup timeouts is constructed internally.
func New(providers []GeoProvider, interval time.Duration, log *slog.Logger) *Monitor {
	return &Monitor{
		providers:     providers,
		interval:      interval,
		lookupTimeout: defaultLookupTimeout,
		client:        &http.Client{Timeout: defaultClientTimeout},
		log:           log,
	}
}

// Once resolves the current reading, trying providers in order and returning the
// first success. If all providers fail, the joined error is returned.
func (m *Monitor) Once(ctx context.Context) (Reading, error) {
	if len(m.providers) == 0 {
		return Reading{}, errors.New("no geo providers configured")
	}
	var errs []error
	for _, p := range m.providers {
		pctx, cancel := context.WithTimeout(ctx, m.lookupTimeout)
		r, err := p.Lookup(pctx, m.client)
		cancel()
		if err != nil {
			m.log.Debug("provider lookup failed", "provider", p.Name(), "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		return r, nil
	}
	return Reading{}, fmt.Errorf("all providers failed: %w", errors.Join(errs...))
}

// Poll resolves once immediately, then every interval, until ctx is cancelled.
// Each outcome (success or all-fail) is sent on the returned channel, which is
// closed when ctx is done.
func (m *Monitor) Poll(ctx context.Context) <-chan Result {
	ch := make(chan Result)
	go func() {
		defer close(ch)

		emit := func() bool {
			r, err := m.Once(ctx)
			select {
			case ch <- Result{Reading: r, Err: err}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if !emit() {
			return
		}
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !emit() {
					return
				}
			}
		}
	}()
	return ch
}
