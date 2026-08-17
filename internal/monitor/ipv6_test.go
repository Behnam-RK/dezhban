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
