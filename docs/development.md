# Development

Requires Go 1.26+ and [Task](https://taskfile.dev) as the task runner
(`brew install go-task`, or `go install github.com/go-task/task/v3/cmd/task@latest`).
The GUI additionally needs a Swift toolchain (Command Line Tools, macOS 13+).

`task` with no arguments is the entry point: on a TTY it opens an interactive
picker (choose a flow, answer its prompts, it runs the task); piped or in CI it
prints the static grouped menu (`task help` prints it directly, `task --list`
is the flat reference, `task --list-all` includes hidden plumbing). This page
explains the loops. Tasks marked `(sudo)` ask for your password up front, then
run the privileged steps on the cached credential.

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
task gui:build               # macOS app -> ./dist/Dezhban.app
task gui:build UNIVERSAL=1   # same, as an arm64+x86_64 universal bundle
task clean                   # remove dist/, ./dezhban, Swift .build

# a single target by hand
GOOS=linux GOARCH=amd64 go build ./cmd/dezhban
```

`task build:all` produces darwin arm64/amd64, linux amd64/arm64, and windows
amd64, each stamped via `-ldflags` with the version, commit and build date (from
`git describe`, overridable with `task build:all VERSION=vX.Y.Z`). macOS still
requires the system `pfctl` at runtime (shelled, not linked). Cutting an actual
release (tagging, publishing binaries) is a separate, automated flow — see
[releasing.md](releasing.md).

`dezhban version` prints the stamp; `dezhban -v version` prints commit, build
date and Go version too. A binary built *without* the Taskfile (a plain
`go build ./cmd/dezhban`) carries no ldflags, and falls back to the VCS data the
Go toolchain embeds automatically — so it still reports the commit it came from
and whether the tree was dirty, rather than an anonymous `dev`.

### The `v` prefix

`VERSION` may or may not carry a leading `v` — a tag does (`v0.2.0`), a bare
`git describe --always` SHA does not. It is normalised in exactly **one** place,
the `VERSION_BARE` var in `Taskfile.yml`, because artifact *filenames* never
carry it (`packaging/macos/build-pkg.sh` writes `dezhban-${VERSION#v}.pkg`).
Don't re-derive a filename from `VERSION` anywhere else.

`task gui:build` is deliberately kept out of `build:all` — a separate
Swift/AppKit target with no effect on the Go binary.

## The fast dev loop (macOS) — everyday testing

Once dezhban is installed (via `task install` or `sh scripts/install-local.sh`),
rolling a new build onto the machine is one command:

```sh
task dev         # rebuild CLI + GUI, swap both in place, restart daemon, relaunch app
task dev:cli     # (hidden) just the CLI: rebuild, swap /usr/local/bin/dezhban, restart daemon
task dev:gui     # (hidden) just the app: rebuild, swap /Applications/Dezhban.app, relaunch
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
task install     # build everything + .pkg, install it, open the app; asks "wipe first?"
task pkg         # just build ./dist/dezhban-<version>.pkg, don't install
task uninstall   # run the uninstaller; asks whether to keep /etc/dezhban
```

Behavior knobs are asked **on-demand**: leave them unset on a TTY and the flow
asks (wipe first? default no; keep config? default keep). Pass them explicitly —
`FRESH=1`/`FRESH=0`, `KEEP_CONFIG=1`/`KEEP_CONFIG=0` — and the question is
skipped, which is also what happens with no TTY (safe defaults: no wipe, keep
config).

`task install` installs over the existing installation (the installer overwrites
the payload and the `postinstall`'s service registration is idempotent) — the
same upgrade path a real user takes. Answer yes to "wipe first?" (or pass
`FRESH=1`) when you suspect leftover-file bugs that an upgrade would mask.

Gotchas:

- The `.pkg` filename embeds `git describe --dirty`. `task install` builds and
  installs in one invocation so the name can't drift, but if you build with
  `task pkg` and install by hand, editing files in between changes the expected
  filename. (This used to misfire on *every* invocation once a `v`-prefixed tag
  existed, because the installer looked for `dezhban-v0.1-…​.pkg` while
  `build-pkg.sh` had written `dezhban-0.1-…​.pkg`. Both sides now go through
  `VERSION_BARE`.)
- After a **fresh** install there is no config yet: run `sudo dezhban setup`,
  then `sudo dezhban start` (or use the menubar app). See
  [usage.md](usage.md#create--manage-the-config).

## Safe dev loop (no root, no firewall effects)

Iterate on rules and config without root and without risking a lockout:

```sh
task validate CONFIG=configs/dezhban.dev.json    # parse + validate a config
task rules MODE=guard CONFIG=...                 # print the ruleset, don't apply it
task doctor CONFIG=... -- --discover             # diagnose tunnels / lockout risks
task monitor                                     # build + run the monitor in dry-run, no firewall touch
task status                                      # current posture (installed CLI or go run)
```

Sample configs live in `configs/` (see [config.md](config.md) for what each one
is for).

## Service lifecycle from source

Scripts for running the *source tree* as the installed service, without
building a `.pkg`. They have no task wrappers — the macOS installer loop above
is the blessed install path — but they remain the standalone / non-macOS path:

```sh
sh scripts/install-local.sh     # validate, build, install config + service, start it
sh scripts/reinstall.sh         # tear down, then install fresh
sh scripts/uninstall-local.sh   # panic-teardown, unregister, remove config (KEEP_CONFIG=1 keeps it)
sh scripts/panic.sh             # force-remove dezhban's firewall rules (also: task panic)
```

The scripts prompt for sudo themselves and stay standalone on purpose:
recovery must not depend on dev tooling, so `sh scripts/panic.sh` always works
even with Task missing or the Taskfile broken (as does the `dezhban panic`
subcommand itself).

`uninstall-local.sh` panic-flushes the firewall rules first (so a wedged
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
one posture constructor, an unknown country holding rather than escalating), and
[adr/](adr/README.md) for why the shape is what it is.
