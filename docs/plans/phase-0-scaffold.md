# Phase 0 — Project Scaffold

## Goal
Bootstrap a buildable Go project with CLI skeleton, config loading, logging, and
a privilege check. No firewall, no monitoring yet — just the skeleton everything
else hangs off.

## Scope
- Go module + directory layout
- CLI entrypoint with stub subcommands
- `Config` struct + loader with defaults
- Structured logging (`slog`)
- Privilege check at startup
- `CLAUDE.md` for future sessions

## Deliverables

### 1. Module + layout
```
go mod init github.com/behnam-rk/dezhban   # path adjustable
```
Create `cmd/dezhban/main.go` and the `internal/` package dirs (empty stubs OK).
Target **Go 1.23+**.

### 2. CLI (stdlib `flag`, subcommand dispatch)
`cmd/dezhban/main.go` dispatches on `os.Args[1]`:

| Subcommand | Phase 0 behavior |
|---|---|
| `run` | parse `--config`, `--dry-run`; print "not implemented" |
| `block` | print "not implemented" |
| `unblock` | print "not implemented" |
| `status` | print version + parsed config + privilege state |
| `panic` | print "not implemented" |
| `version` / `--version` | print version string |

Each subcommand gets its own `flag.NewFlagSet`. No external CLI dep.

### 3. Config (`internal/config`)
```go
type Config struct {
    PollInterval   time.Duration   // default 30s
    BlockedCountries []string       // ISO-3166 alpha-2, upper-cased on load
    FailClosed     bool             // default true
    Hysteresis     int              // consecutive readings, default 3
    Providers      []string         // geo provider URLs, defaults baked in
    Allowlist      AllowlistConfig  // DNS servers, extra hosts; loopback implicit
    LogLevel       string           // default "info"
}
```
- `Load(path string) (*Config, error)` — read file if present, else all defaults.
- Format: start with **stdlib JSON** (zero dep). If YAML is wanted later, swap
  to `goccy/go-yaml`. Decide here; doc assumes JSON for Phase 0.
- Validate: country codes are 2 letters; `PollInterval > 0`; `Hysteresis >= 1`.
- Ship `configs/dezhban.example.json` with annotated defaults.

### 4. Logging (`internal/logging`)
Thin wrapper over `log/slog`. `New(level string) *slog.Logger`. Text handler to
stderr for interactive use; structured for service mode (Phase 6).

### 5. Privilege check
`func IsPrivileged() bool` — unix: `os.Geteuid() == 0`. Windows stub returns true
for now (real check in Phase 5). `run`/`block`/`unblock`/`panic` must error
clearly if not privileged: `dezhban must run as root (try: sudo dezhban ...)`.
`status` and `version` run unprivileged.

### 6. CLAUDE.md
Build/test/run commands + the 3-layer architecture + interface seam + "edit
backends behind `FirewallBackend`, never call `pfctl` from `run` directly".

## Files touched
- `go.mod`, `go.sum`
- `cmd/dezhban/main.go`
- `internal/config/config.go`
- `internal/logging/logging.go`
- `internal/privilege/privilege.go` (+ `_windows.go` stub)
- `configs/dezhban.example.json`
- `CLAUDE.md`

## Dependencies
None (stdlib only).

## Acceptance / verification
- `go build ./...` and `go vet ./...` pass.
- `dezhban version` prints version.
- `dezhban status` prints parsed config + "privileged: false" when run as user.
- `dezhban run` (as user) errors with the root-required message.
- `go test ./internal/config` covers default load + validation errors.

## Out of scope
Any real IP/firewall logic. Windows privilege check. YAML.
