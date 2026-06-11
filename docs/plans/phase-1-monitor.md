# Phase 1 — Monitor Layer

## Goal
Determine the machine's current public IP and its country, on a polling loop,
with multi-provider redundancy. Prints country each tick. No firewall.

## Scope
- `GeoProvider` interface + 2–3 API implementations
- Provider fallback (try in order until one succeeds)
- `Monitor` polling loop exposing the latest reading
- `dezhban run --dry-run` prints country per tick

## Design

### GeoProvider (`internal/monitor/provider.go`)
```go
type Reading struct {
    IP          netip.Addr
    CountryCode string   // ISO-3166 alpha-2, upper-case
    Provider    string   // which provider answered (for logs)
}

type GeoProvider interface {
    Name() string
    Lookup(ctx context.Context, client *http.Client) (Reading, error)
}
```

Implement 2–3 free endpoints, each parsing its own JSON shape:
- `ipinfo.io/json` → `{ip, country}`
- `ip-api.com/json` → `{query, countryCode}`
- `ifconfig.co/json` → `{ip, country_iso}`

Normalize country to upper-case alpha-2. Each provider has a per-call timeout
(`ctx` + `client.Timeout`, e.g. 5s).

### Monitor (`internal/monitor/monitor.go`)
```go
type Monitor struct {
    providers []GeoProvider
    interval  time.Duration
    client    *http.Client
    log       *slog.Logger
}

// Poll runs until ctx is cancelled, emitting Readings (or errors) on a channel.
func (m *Monitor) Poll(ctx context.Context) <-chan Result   // Result = {Reading, error}
// Once does a single resolution: tries providers in order, first success wins.
func (m *Monitor) Once(ctx context.Context) (Reading, error)
```
- `Once`: iterate providers in order; on error log at debug and try next; if all
  fail, return an aggregate error (Phase 3/4 maps this to fail-closed).
- `Poll`: ticker at `interval`; calls `Once`; pushes Result; respects `ctx` for
  clean shutdown.

### Wiring `run --dry-run`
`run` builds a `Monitor` from config providers, loops over `Poll`, and logs:
`tick: ip=X country=US provider=ipinfo.io` — or `tick: lookup failed: <errs>`.
No privilege required in `--dry-run`.

## Files touched
- `internal/monitor/provider.go`
- `internal/monitor/monitor.go`
- `cmd/dezhban/main.go` (wire `run --dry-run`)
- `internal/config/config.go` (provider list already present from Phase 0)

## Dependencies
stdlib only (`net/http`, `net/netip`, `encoding/json`, `context`, `time`).

## Acceptance / verification
- `dezhban run --dry-run` prints your real country code on each tick (default 30s;
  test with `--config` set to a short interval, e.g. 3s).
- Disable network (turn off Wi-Fi) → observe graceful "lookup failed" lines, no crash.
- Block one provider's host (e.g. `/etc/hosts`) → observe fallback to the next.
- `go test ./internal/monitor` with `httptest.Server` stubs covering: success,
  provider error → fallback, all-fail aggregate, country normalization.

## Out of scope
Decision/blocklist logic, firewall, hysteresis, fail-mode (Phase 3–4).
Offline mmdb (hybrid, deferred).
