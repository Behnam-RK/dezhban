package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubProvider is a controllable GeoProvider for Once/Poll tests.
type stubProvider struct {
	name  string
	r     Reading
	err   error
	calls int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) URL() string  { return "stub://" + s.name }
func (s *stubProvider) Lookup(context.Context, *http.Client) (Reading, error) {
	s.calls++
	return s.r, s.err
}

func TestOnceFallsBackToNextProvider(t *testing.T) {
	bad := &stubProvider{name: "bad", err: errors.New("boom")}
	good := &stubProvider{name: "good", r: Reading{CountryCode: "US", Provider: "good"}}
	m := New([]GeoProvider{bad, good}, time.Second, testLogger())

	r, err := m.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if r.CountryCode != "US" || r.Provider != "good" {
		t.Errorf("got %+v, want US/good", r)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Errorf("calls bad=%d good=%d, want 1/1", bad.calls, good.calls)
	}
}

func TestOnceAllFail(t *testing.T) {
	a := &stubProvider{name: "a", err: errors.New("a-down")}
	b := &stubProvider{name: "b", err: errors.New("b-down")}
	m := New([]GeoProvider{a, b}, time.Second, testLogger())

	if _, err := m.Once(context.Background()); err == nil {
		t.Fatal("Once = nil error, want all-providers-failed")
	}
}

func TestOnceNoProviders(t *testing.T) {
	m := New(nil, time.Second, testLogger())
	if _, err := m.Once(context.Background()); err == nil {
		t.Fatal("Once with no providers = nil error, want error")
	}
}

func TestPollEmitsAndStopsOnCancel(t *testing.T) {
	good := &stubProvider{name: "good", r: Reading{CountryCode: "US", Provider: "good"}}
	m := New([]GeoProvider{good}, time.Hour, testLogger()) // long interval: rely on immediate first emit

	ctx, cancel := context.WithCancel(context.Background())
	ch := m.Poll(ctx)

	res, ok := <-ch
	if !ok {
		t.Fatal("channel closed before first emit")
	}
	if res.Err != nil || res.Reading.CountryCode != "US" {
		t.Fatalf("first result = %+v / err %v", res.Reading, res.Err)
	}

	cancel()
	// Channel must close after cancel.
	for range ch { //nolint:revive // drain until closed
	}
}

// --- Parser tests via httptest ---

func TestProviderParsers(t *testing.T) {
	cases := []struct {
		name    string
		parse   parseFunc
		body    string
		wantIP  string
		wantCC  string
		wantErr bool
	}{
		{"ipinfo", parseIPInfo, `{"ip":"8.8.8.8","country":"us"}`, "8.8.8.8", "US", false},
		{"ip-api ok", parseIPAPI, `{"status":"success","query":"1.1.1.1","countryCode":"AU"}`, "1.1.1.1", "AU", false},
		{"ip-api fail", parseIPAPI, `{"status":"fail","message":"quota"}`, "", "", true},
		{"ip-api empty status", parseIPAPI, `{"query":"1.1.1.1","countryCode":"AU"}`, "", "", true},
		{"ifconfig", parseIfconfig, `{"ip":"9.9.9.9","country_iso":"CH"}`, "9.9.9.9", "CH", false},
		{"bad json", parseIPInfo, `{nope`, "", "", true},
		{"missing country", parseIPInfo, `{"ip":"8.8.8.8"}`, "", "", true},
		{"bad ip", parseIPInfo, `{"ip":"not-an-ip","country":"US"}`, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			p := &provider{name: tc.name, url: srv.URL, parse: tc.parse}
			r, err := p.Lookup(context.Background(), srv.Client())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Lookup = %+v, want error", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if r.IP.String() != tc.wantIP || r.CountryCode != tc.wantCC {
				t.Errorf("got ip=%s cc=%s, want ip=%s cc=%s", r.IP, r.CountryCode, tc.wantIP, tc.wantCC)
			}
		})
	}
}

func TestProviderNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := &provider{name: "x", url: srv.URL, parse: parseIPInfo}
	if _, err := p.Lookup(context.Background(), srv.Client()); err == nil {
		t.Fatal("Lookup on 503 = nil error, want error")
	}
}

func TestProvidersFromURLs(t *testing.T) {
	got := ProvidersFromURLs([]string{
		"https://ipinfo.io/json",
		"https://unknown.example/json",
	}, testLogger())
	if len(got) != 1 {
		t.Fatalf("got %d providers, want 1 (unknown skipped)", len(got))
	}
	if got[0].Name() != "ipinfo.io" {
		t.Errorf("name = %q, want ipinfo.io", got[0].Name())
	}
}
