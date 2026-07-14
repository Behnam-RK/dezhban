# Development

Requires Go 1.26+ and [Task](https://taskfile.dev) as the task runner
(`brew install go-task`, or `go install github.com/go-task/task/v3/cmd/task@latest`).
The GUI additionally needs a Swift toolchain (Command Line Tools, macOS 13+).

`task` with no arguments lists every task with a one-line description — that
list is the reference; this page explains the loops. Tasks marked `(sudo)`
prompt for your password (or Touch ID) at the privileged step.

Everything also works without Task — the plain Go commands:

```sh
go build ./...                          # build everything
go vet ./...                            # static checks
go test ./...                           # all tests
go test ./internal/config -run TestLoad # one package / test
go run ./cmd/dezhban status             # run a subcommand without installing
```

## Build & cross-compile

```sh
task build                   # host build, version-stamped, into ./dezhban
task build:all               # cross-compile all 5 targets into ./dist/
task gui:build               # macOS menubar app -> ./dist/Dezhban.app
task gui:build UNIVERSAL=1   # same, as an arm64+x86_64 universal bundle
task clean                   # remove dist/, ./dezhban, Swift .build

# a single target by hand
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

`task build:all` produces darwin arm64/amd64, linux amd64/arm64, and windows
amd64, each with the version stamped via `-ldflags -X main.version` (from
`git describe`, overridable with `task build:all VERSION=vX.Y.Z`). macOS still
requires the system `pfctl` at runtime (shelled, not linked). Cutting an actual
release (tagging, publishing binaries) is a separate, automated flow — see
[releasing.md](releasing.md).

`task gui:build` is deliberately kept out of `build:all` — a separate
Swift/AppKit target with no effect on the Go binary.

## The fast dev loop (macOS) — everyday testing

Once dezhban is installed (via the `.pkg` or `task install-local`), rolling a
new build onto the machine is one command:

```sh
task dev:all     # rebuild CLI + GUI, swap both in place, restart daemon, relaunch app
task dev:cli     # just the CLI: rebuild, swap /usr/local/bin/dezhban, restart daemon
task dev:gui     # just the app: rebuild, swap /Applications/Dezhban.app, relaunch
```

Seconds, not minutes: host-arch builds only, no installer. What `dev:cli` does
and why it's safe:

1. `go build` the host binary.
2. `sudo install` it over `/usr/local/bin/dezhban` — `install(1)` unlinks the
   destination before writing, which matters on Apple Silicon: modifying a
   running signed binary in place gets the process killed by the kernel. The
   running daemon keeps executing its old image.
3. If the LaunchDaemon is registered, `sudo dezhban restart` picks up the new
   binary (stop → wait → start; the guard posture is re-applied by the daemon
   on startup as usual). If it isn't, the swap still happens and the restart is
   skipped with a hint.

`dev:gui` quits the menubar app, replaces the bundle with a root-owned copy
(matching what the `.pkg` installs), and reopens it.

The fast loop bypasses the installer, `postinstall`, and packaging — when you
change anything under `packaging/`, or want to test what users actually
experience, use the full loop below.

## The full installer loop (macOS) — test what ships

```sh
task pkg:cycle       # build everything + .pkg, install it, open the app
task pkg:fresh       # same, but uninstall first (config kept) — a clean slate
```

Or piecewise:

```sh
task pkg:build       # cross-compile + universal app + ./dist/dezhban-<version>.pkg
task pkg:install     # sudo installer -pkg ... -target /
task pkg:uninstall   # run the uninstaller; keeps /etc/dezhban (KEEP_CONFIG=0 to purge)
```

`pkg:cycle` installs over the existing installation (the installer overwrites
the payload and the `postinstall`'s service registration is idempotent) — the
same upgrade path a real user takes. Reach for `pkg:fresh` when you suspect
leftover-file bugs that an upgrade would mask.

Gotchas:

- The `.pkg` filename embeds `git describe --dirty`, so editing files between
  `pkg:build` and `pkg:install` changes the expected name — `pkg:install` has a
  precondition that catches this and tells you to rebuild.
- After a **fresh** install there is no config yet: run `sudo dezhban setup`,
  then `sudo dezhban start` (or use the menubar app). See
  [usage.md](usage.md#install).

## Safe dev loop (no root, no firewall effects)

Iterate on rules and config without root and without risking a lockout:

```sh
task validate CONFIG=configs/dezhban.dev.json    # parse + validate a config
task rules MODE=guard CONFIG=...                 # print the ruleset, don't apply it
task doctor CONFIG=... -- --discover             # diagnose tunnels / lockout risks
task run-dry                                     # build + run the monitor, no firewall touch
task status                                      # current posture (installed CLI or go run)
```

Sample configs live in `configs/` (see [config.md](config.md) for what each one
is for).

## Service lifecycle from source

Wrappers for running the *source tree* as the installed service, without
building a `.pkg`:

```sh
task install-local        # validate, build, install config + service, start it
task reinstall            # tear down, then install fresh
task uninstall-local      # panic-teardown, unregister, remove config (KEEP_CONFIG=1 keeps it)
task panic                # force-remove dezhban's firewall rules — the lockout escape hatch
```

These wrap `scripts/*.sh`, which prompt for sudo themselves — run `task`
unprivileged, never `sudo task`. The scripts stay standalone on purpose:
recovery must not depend on dev tooling, so `sh scripts/panic.sh` always works
even with Task missing or the Taskfile broken (as does the `dezhban panic`
subcommand itself).

`task uninstall-local` panic-flushes the firewall rules first (so a wedged
service can't leave you blocked), unregisters the service, then deletes the
installed `/etc/dezhban` config; pass `KEEP_CONFIG=1` to keep the config. It also
flags a `go install` copy of `dezhban` left on your `$PATH`, which would otherwise
shadow your local build — a common "why is an old version installed?" surprise.

## CI

`.github/workflows/ci.yml` runs `go vet` + `go test` on macOS, Linux, and Windows
(with `-race` on Linux) and a `task build:all` cross-compile, so the per-OS
build-tag backends can't silently break.

## Pre-commit hook

A native git hook (no extra dependencies) runs gofmt, `go vet`, `go build`, and
`go test` before each commit. Enable it once after cloning:

```sh
git config core.hooksPath .githooks
```

See [architecture.md](architecture.md) for the design invariants any change must
preserve (the `FirewallBackend` seam, idempotent `Block`, always-safe `Cleanup`,
fail-closed defaults).
