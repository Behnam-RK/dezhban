package monitor

import (
	"context"
	"strings"
)

// pollOncer is the monitor surface SimMonitor wraps. *Monitor satisfies it.
type pollOncer interface {
	Poll(ctx context.Context) <-chan Result
	Once(ctx context.Context) (Reading, error)
}

// SimMonitor wraps a real monitor and overrides the resolved country code, so the
// full decision→enforcement pipeline can be exercised from anywhere without being
// in a sanctioned region. The real lookup still runs (IP/provider stay genuine)
// — only a successful reading's CountryCode is rewritten; errors pass through so
// fail-mode behavior is unchanged.
type SimMonitor struct {
	inner   pollOncer
	country string
}

// NewSimMonitor returns a SimMonitor forcing the given (upper-cased) country.
func NewSimMonitor(inner pollOncer, country string) *SimMonitor {
	return &SimMonitor{inner: inner, country: strings.ToUpper(strings.TrimSpace(country))}
}

func (s *SimMonitor) Once(ctx context.Context) (Reading, error) {
	r, err := s.inner.Once(ctx)
	if err == nil {
		r.CountryCode = s.country
	}
	return r, err
}

func (s *SimMonitor) Poll(ctx context.Context) <-chan Result {
	in := s.inner.Poll(ctx)
	out := make(chan Result)
	go func() {
		defer close(out)
		for res := range in {
			if res.Err == nil {
				res.Reading.CountryCode = s.country
			}
			select {
			case out <- res:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
