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
	// quorum requires a strict majority of providers to agree on the country
	// before a reading is trusted; otherwise the first successful provider wins.
	quorum bool
}

// New builds a Monitor. interval is the poll period; a sane HTTP client with
// per-lookup timeouts is constructed internally. quorum selects majority-vote
// resolution (defends against a single spoofed/wrong provider) over the default
// first-success fallback.
func New(providers []GeoProvider, interval time.Duration, log *slog.Logger, quorum bool) *Monitor {
	return &Monitor{
		providers:     providers,
		interval:      interval,
		lookupTimeout: defaultLookupTimeout,
		client:        &http.Client{Timeout: defaultClientTimeout},
		log:           log,
		quorum:        quorum,
	}
}

// Once resolves the current reading. In the default mode it tries providers in
// order and returns the first success. In quorum mode it queries every provider
// and requires a strict majority to agree on the country. If all providers fail
// (or quorum is not reached) an error is returned.
func (m *Monitor) Once(ctx context.Context) (Reading, error) {
	if len(m.providers) == 0 {
		return Reading{}, errors.New("no geo providers configured")
	}
	if m.quorum {
		return m.onceQuorum(ctx)
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

// onceQuorum queries every provider and returns the country a strict majority of
// the *responding* providers agree on. A single spoofed or misreporting provider
// cannot move the result. Disagreements are logged at warn. If no country reaches
// a majority of the responders, an error is returned (the caller's fail-mode then
// decides what to do with an undeterminable country).
func (m *Monitor) onceQuorum(ctx context.Context) (Reading, error) {
	var (
		readings []Reading
		errs     []error
	)
	for _, p := range m.providers {
		pctx, cancel := context.WithTimeout(ctx, m.lookupTimeout)
		r, err := p.Lookup(pctx, m.client)
		cancel()
		if err != nil {
			m.log.Debug("provider lookup failed", "provider", p.Name(), "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		readings = append(readings, r)
	}
	if len(readings) == 0 {
		return Reading{}, fmt.Errorf("all providers failed: %w", errors.Join(errs...))
	}

	counts := make(map[string]int, len(readings))
	for _, r := range readings {
		counts[r.CountryCode]++
	}
	if len(counts) > 1 {
		m.log.Warn("providers disagree on country", "counts", counts, "responders", len(readings))
	}

	need := len(readings)/2 + 1 // strict majority of responders
	for _, r := range readings {
		if counts[r.CountryCode] >= need {
			return r, nil
		}
	}
	return Reading{}, fmt.Errorf("no provider quorum: %d responders, no majority country (%v)", len(readings), counts)
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
