package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// v6TestMonitor returns a Monitor whose IPv6 lookup hits the given endpoints
// over a plain client — httptest listens on 127.0.0.1, which the real
// tcp6-forced client can never reach, and that refusal is the production
// behavior under test elsewhere; here we test the fallback/parse logic.
func v6TestMonitor(endpoints ...string) *Monitor {
	m := New(nil, time.Second, testLogger(), false)
	m.v6client = &http.Client{Timeout: time.Second}
	m.v6endpoints = endpoints
	return m
}

func TestOnceIPv6FallsBackAndParses(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2001:db8::1234\n"))
	}))
	defer good.Close()

	m := v6TestMonitor(bad.URL, good.URL)
	ip, err := m.OnceIPv6(context.Background())
	if err != nil {
		t.Fatalf("OnceIPv6: %v", err)
	}
	if got := ip.String(); got != "2001:db8::1234" {
		t.Errorf("ip = %s, want 2001:db8::1234", got)
	}
}

func TestOnceIPv6RejectsNonV6Payloads(t *testing.T) {
	// An endpoint echoing a v4 (or v4-mapped) address must be rejected, not
	// reported as the host's IPv6 address — and neither may an address that is
	// v6 by family but never public: netip's Is6 is true for loopback,
	// link-local and ULA alike, so an intercepting proxy could otherwise put
	// any of them in a field the app labels "Public IPv6".
	for _, payload := range []string{
		"203.0.113.9", "::ffff:203.0.113.9", "not an ip",
		"::1", "fe80::1", "fd00::1", "ff02::1",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(payload))
		}))
		m := v6TestMonitor(srv.URL)
		if _, err := m.OnceIPv6(context.Background()); err == nil {
			t.Errorf("OnceIPv6 accepted payload %q, want an error", payload)
		}
		srv.Close()
	}
}

// The sequential fallback must survive a first endpoint that HANGS, not just one
// that fails fast — a hang is the failure mode it mainly exists for.
//
// Each attempt's context derives from the caller's, so the caller's budget has to
// fit MORE than one ipv6LookupTimeout. The run loop's ipv6LookupBudget was
// exactly one (2s), which meant a hung first endpoint consumed the whole budget
// and the second attempt failed instantly against an already-cancelled context:
// the fallback was structurally unreachable for a hang. The second half of this
// test pins that relationship, so shrinking the budget back cannot pass silently.
func TestOnceIPv6FallsBackPastAHang(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Longer than ipv6LookupTimeout, and context-aware so Close doesn't wait.
		select {
		case <-time.After(10 * time.Second):
			_, _ = w.Write([]byte("2001:db8::dead"))
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2001:db8::1234"))
	}))
	defer good.Close()

	newM := func() *Monitor {
		m := New(nil, time.Second, testLogger(), false)
		// No client timeout: the per-attempt context is what must bound this, and
		// a client timeout shorter than ipv6LookupTimeout would hide the very
		// interaction under test.
		m.v6client = &http.Client{}
		m.v6endpoints = []string{hang.URL, good.URL}
		return m
	}

	// A budget with room for two attempts: the hang is cut at its own timeout and
	// the fallback still has a context to run in.
	ctx, cancel := context.WithTimeout(context.Background(), 3*ipv6LookupTimeout)
	defer cancel()
	ip, err := newM().OnceIPv6(ctx)
	if err != nil {
		t.Fatalf("OnceIPv6 with room for two attempts: %v — the fallback did not run past the hang", err)
	}
	if got := ip.String(); got != "2001:db8::1234" {
		t.Errorf("ip = %s, want 2001:db8::1234 from the second endpoint", got)
	}

	// The regression: a budget equal to one attempt leaves the fallback nothing.
	tight, cancelTight := context.WithTimeout(context.Background(), ipv6LookupTimeout)
	defer cancelTight()
	if _, err := newM().OnceIPv6(tight); err == nil {
		t.Error("OnceIPv6 succeeded with a budget of exactly one attempt — the caller's budget must exceed ipv6LookupTimeout for the fallback to be reachable")
	}
}

func TestOnceIPv6AllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer srv.Close()
	m := v6TestMonitor(srv.URL, srv.URL)
	if _, err := m.OnceIPv6(context.Background()); err == nil {
		t.Fatal("OnceIPv6 = nil error with every endpoint failing, want an error")
	}
}
