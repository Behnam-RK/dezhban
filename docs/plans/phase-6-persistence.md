# Phase 6 — Persistence (Run as a Service)

## Goal
Run dezhban as a managed background service on each OS, with install/uninstall,
start-on-boot, and clean teardown — using one cross-platform API.

## Scope
- Integrate [`kardianos/service`](https://github.com/kardianos/service)
- `install` / `uninstall` / `start` / `stop` subcommands
- Service `Start`/`Stop` lifecycle wrapping the Phase 3 run loop
- Boot persistence on launchd / systemd / Windows Service

## Design

### Why `kardianos/service`
One API installs and runs as: macOS **launchd**, Linux **systemd/upstart/sysv**,
Windows **Service**. Removes hand-written plists/units. Detects whether dezhban is
running interactively vs. under a service manager.

### Lifecycle (`internal/svc` or in main)
```go
type program struct{ runner *runner.Runner; cancel context.CancelFunc }
func (p *program) Start(s service.Service) error  // launch run loop in a goroutine, return immediately
func (p *program) Stop(s service.Service) error   // cancel ctx → run loop's defer Cleanup() fires
```
- `Start` must return promptly (service managers expect this) — do the work in a
  goroutine, store the cancel func.
- `Stop` cancels the context; the Phase 3 `defer Backend.Cleanup()` removes all
  firewall rules. **Critical:** stopping the service must never leave a block-all
  rule behind.

### New subcommands
| Subcommand | Action |
|---|---|
| `install` | `service.Install()` — register with the OS manager (root/admin) |
| `uninstall` | `service.Uninstall()` |
| `start` / `stop` | control the installed service |
| `run` | unchanged; also the entrypoint the service invokes |

### Service config
- Name: `dezhban`. Display name + description set in `service.Config`.
- Arguments: `["run", "--config", "/etc/dezhban/dezhban.json"]` (path per-OS:
  `/etc/dezhban/` on unix, `%ProgramData%\dezhban\` on Windows).
- `KeepAlive`/restart-on-failure enabled so a crash restarts enforcement.
- Logging: in service mode, route `slog` to the platform logger (syslog/journald/
  Windows Event Log) — `kardianos/service` exposes a logger; bridge it.

### ⚠️ Crash-safety interaction (ties to Phase 7)
If the service crashes while blocked, rules persist (by design — kill switch
shouldn't fail open). Restart restores enforcement. But the user needs an escape
hatch: the Phase 7 `panic` command must work standalone to flush rules even if the
service is dead. Note this dependency; don't solve it here.

## Files touched
- `cmd/dezhban/main.go` (install/uninstall/start/stop dispatch)
- `internal/svc/program.go` (Start/Stop wrapper)
- `internal/logging/logging.go` (service-logger bridge)

## Dependencies (new)
- `github.com/kardianos/service`

## Acceptance / verification
Per OS (privileged):
1. `dezhban install` → service registered (`launchctl list | grep dezhban` /
   `systemctl status dezhban` / `sc query dezhban`).
2. `dezhban start` → enforcement active; reboot → service auto-starts.
3. `dezhban stop` → run loop's `Cleanup()` fires, all rules removed, connectivity ok.
4. `dezhban uninstall` → fully removed.
5. Kill the service process while blocked → restart-on-failure brings it back and
   re-enforces.

## Out of scope
Packaging/cross-compile (Phase 7). The standalone `panic` command (Phase 7).
