# Development

Requires Go 1.26+.

```sh
go build ./...                          # build everything
go vet ./...                            # static checks
go test ./...                           # all tests
go test ./internal/config -run TestLoad # one package / test
go run ./cmd/dezhban status             # run a subcommand without installing
```

## Build & cross-compile

```sh
go build ./cmd/dezhban       # build the binary
go install ./cmd/dezhban     # install to $GOBIN

make build                   # host build, version-stamped, into ./dezhban
make build-all               # cross-compile all 5 targets into ./dist/

# a single target by hand
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

`make build-all` produces darwin arm64/amd64, linux amd64/arm64, and windows
amd64, each with the version stamped via `-ldflags -X main.version` (from
`git describe`, overridable with `make build-all VERSION=vX.Y.Z`). macOS still
requires the system `pfctl` at runtime (shelled, not linked). Cutting an actual
release (tagging, publishing binaries) is a separate, automated flow — see
[releasing.md](releasing.md).

```sh
make gui-macos               # build the macOS menubar app -> ./dist/Dezhban.app
```

`make gui-macos` builds the optional desktop GUI (needs a Swift toolchain; macOS
13+). It is deliberately kept out of `build-all` — a separate Swift/AppKit target
with no effect on the Go binary.

## Safe dev loop (no root)

The `Makefile` and `scripts/` wrap the read-only inspect commands so you can
iterate on rules and config without root and without risking a lockout:

```sh
make validate CONFIG=configs/dezhban.dev.json    # parse + validate a config
make rules MODE=guard CONFIG=...                  # print the ruleset, don't apply it
make doctor CONFIG=... [ARGS=--discover]          # diagnose tunnels / lockout risks
make run-dry                                      # build + run the monitor, no firewall touch
```

The privileged flows have wrappers too — `make install-local` / `reinstall` /
`uninstall-local` / `panic`, mirrored by `scripts/*.sh`. Sample configs live in
`configs/` (see [config.md](config.md) for what each one is for).

`make uninstall-local` panic-flushes the firewall rules first (so a wedged
service can't leave you blocked), unregisters the service, then deletes the
installed `/etc/dezhban` config; pass `KEEP_CONFIG=1` to keep the config. It also
flags a `go install` copy of `dezhban` left on your `$PATH`, which would otherwise
shadow your local build — a common "why is an old version installed?" surprise.

## CI

`.github/workflows/ci.yml` runs `go vet` + `go test` on macOS, Linux, and Windows
(with `-race` on Linux) and a `build-all` cross-compile, so the per-OS build-tag
backends can't silently break.

## Pre-commit hook

A native git hook (no extra dependencies) runs gofmt, `go vet`, `go build`, and
`go test` before each commit. Enable it once after cloning:

```sh
git config core.hooksPath .githooks
```

See [architecture.md](architecture.md) for the design invariants any change must
preserve (the `FirewallBackend` seam, idempotent `Block`, always-safe `Cleanup`,
fail-closed defaults).
