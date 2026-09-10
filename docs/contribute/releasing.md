# Releasing

A release is one command:

```sh
task release BUMP=minor          # or: task release VERSION=0.2.0
```

It runs a local preflight, rolls the CHANGELOG for real, shows you the diff,
asks you to type the tag to confirm, **commits and pushes the roll to `main`
as you**, dispatches the `Release` workflow, and streams it.

Everything below is what that command sets in motion, and how to drive it by
hand if you'd rather.

## The order is the design

```
you       roll CHANGELOG.md locally, commit + push to main (final only)
   |
prepare   resolve the version; require it's ALREADY rolled; require CI green
   |      -- writes nothing, anywhere --
build     cross-compile all 5 CLI targets + 4 tarballs + 4 .deb/.rpm;
   |      build the macOS .pkg; install it on a runner and uninstall it again
publish   sign SHA256SUMS, tag the tested commit, publish the release,
          index the tag on pkg.go.dev (warn-only — see below)
```

Nothing touches the repository until every artifact has been built and the
installer has been proven to install and uninstall cleanly. **A failed build
leaves no tag behind** — just re-dispatch. (The old order tagged first, so a
failed build stranded a tag that the workflow's own "tag already exists" guard
then refused to retry.)

A failure *inside* `publish` is the one case that can leave a tag, because
tagging is the step before publishing — v0.10.1 hit a GitHub 503 on
`gh release create` after the tag had been pushed. Re-run the `publish` job:
the tag step reuses a tag that already points at the pinned commit, so the
retry picks up where it stopped, and it still refuses a tag pointing anywhere
else. A *fresh* dispatch is the wrong tool there — `resolve` sees the tag and
stops, by design, so if you want a clean run instead of a re-run, delete the
stranded tag first (`git push --delete origin vX.Y.Z`).

`publish` also re-checks that `main` still points at the commit `prepare` pinned.
If something merged mid-release, it stops rather than tag a tree that was never
built.

## The workflow never writes to `main` — you do, locally, first

Earlier, a final's CHANGELOG roll happened in CI and landed as an
auto-opened `chore(release)` PR, because `main`'s ruleset requires a pull
request and `github-actions[bot]` **cannot** bypass it (GitHub only offers
that bypass to the Actions app on **org-owned** repos; this one is
user-owned). That PR was pure friction — a manual merge-click standing between
"the release is out" and "the changelog says so."

The repo owner, however, **is** a ruleset bypass actor on their own repo
(`current_user_can_bypass` in `gh api repos/OWNER/REPO/rulesets` — check this
before assuming it for a fork or a different repo). So `task release` now
rolls `CHANGELOG.md` for real, commits it, and `git push origin main`
directly, under your own git identity, before ever dispatching the workflow.
No PR, because nothing needs one: it's the actual repo owner pushing, not the
bot.

The gate that keeps this honest: `prepare`'s **"Require CHANGELOG already
rolled"** step. It fails loudly if `[Unreleased]` still has content for a
final, or if it's empty for an rc (rc's read notes from `[Unreleased]`
directly and never roll it) — so forgetting to run `task release` (and
instead dispatching the workflow some other way) can't ship a release whose
notes are the previous version's.

Tags still aren't branch-ruled, so tagging and publishing the release stay a
direct CI action either way.

## Before you release

Keep [CHANGELOG.md](../../CHANGELOG.md)'s `## [Unreleased]` section current as you
land changes — **it is the release notes**. The release fails if it is empty, so
there is nothing to accidentally ship with no notes.

## Preview it first

```sh
task release:check                    # on main? clean? synced? CI green? notes present?
task release:preview VERSION=0.2.0    # the resolved tag, the notes, the CHANGELOG diff
```

`release:preview` rolls a **scratch copy** of `CHANGELOG.md` to show you the
diff, then restores your working tree — unlike `task release` itself, which
rolls for real. Both run `scripts/release.sh` — **the same code the workflow
runs** — so what either prints is what CI will do.

For a full-fidelity rehearsal, dispatch the workflow with **`dry_run: true`**: it
resolves, gates on CI, cross-compiles everything, builds the `.pkg` and runs the
install/uninstall smoke test — then stops. No tag, no commit, no release; the
artifacts are attached to the run so you can inspect them.

## Versions

Pass **either** an exact `version` **or** a `bump`:

| Input | Result |
|---|---|
| `version: 0.2.0` | exactly `v0.2.0` |
| `version: 0.2.0-rc.1` | opens an rc line |
| `bump: patch` \| `minor` \| `major` | computed from the latest **final** tag |
| `bump: rc` | advances an open rc line (`v0.2.0-rc.1` → `v0.2.0-rc.2`) |

The version must be strict `X.Y.Z` or `X.Y.Z-rc.N`, must not already be tagged,
and must outrank the latest tag under **semver** precedence — which is why
`0.2.0` may follow `0.2.0-rc.1` (the promotion) but `0.2.0-rc.2` may not follow
`0.2.0`. An explicit `version` is required for the first release, and to open a
new rc line.

### Release candidates are pure snapshots

An rc **tags only**. It does not roll the CHANGELOG, does not commit to `main`,
and is published `--prerelease` so it never becomes "latest". Its notes are the
current `[Unreleased]` section with a pre-release banner. Abandoning an rc line
therefore costs nothing — no history to unwind.

The eventual final release rolls the whole accumulated `[Unreleased]` section in
one go, exactly as if the rc's had never happened. `bump: patch|minor|major`
always counts from the last **final** tag, so an open rc line never drags the
next release with it.

## The CI gate

`prepare` refuses to release a commit that CI has not blessed. It reads `ci.yml`'s
conclusion for the pinned SHA and:

- **green** → proceeds,
- **still running** → waits for it (up to 15 minutes) — you can dispatch straight
  after a merge,
- **red, cancelled, or never ran** → aborts.

`force: true` skips the gate for a genuine emergency. It is not silent: the run
is annotated with a warning and the job summary says the gate was forced.

## Cutting one by hand

Actions → **Release** → **Run workflow**, from `main`. The inputs are `version`,
`bump`, `dry_run` and `force`, as above.

**If you dispatch by hand rather than through `task release`**, roll the
CHANGELOG and push it to `main` yourself first (`bash scripts/release.sh roll
X.Y.Z`, then commit and push) — `prepare`'s "Require CHANGELOG already
rolled" step will refuse the dispatch otherwise.

## Assets

Each release carries:

- `dezhban-X.Y.Z.pkg` — the macOS installer (unsigned; `scripts/install.sh`
  is the friction-free path — see [install.md](../usage/install.md))
- `dezhban-darwin-arm64`, `dezhban-darwin-amd64`
- `dezhban-linux-amd64`, `dezhban-linux-arm64`
- `dezhban-windows-amd64.exe`
- `dezhban-X.Y.Z-{darwin,linux}-{arm64,amd64}.tar.gz` — 4 tarballs (binary +
  LICENSE + README), for package managers that want an archive rather than a
  bare binary (the Homebrew formula, when it exists — see docs/install.md)
- `dezhban_X.Y.Z_{amd64,arm64}.deb`, `dezhban-X.Y.Z-1.{x86_64,aarch64}.rpm` —
  built with `nfpm` (`task pkg:linux`), declaring an `nftables` dependency
- `Dezhban-macos.app.zip` — the menubar app alone
- `SHA256SUMS` — covering everything above
- `SHA256SUMS.sig` — an **ed25519** signature over `SHA256SUMS` (see below)

## pkg.go.dev

The tag is what publishes the module. pkg.go.dev is not a publish target — it
is a read-through cache over `proxy.golang.org`, and a version lands there only
once somebody asks the proxy for it. Left alone, that is whenever the first
person happens to fetch the module.

So `publish`'s last step asks, for the tag it just pushed:

```sh
curl -fsS "https://proxy.golang.org/github.com/behnam-rk/dezhban/@v/v0.13.0.info"
curl -fsS "https://pkg.go.dev/github.com/behnam-rk/dezhban@v0.13.0"
```

The first is what actually indexes the version; the second just makes
pkg.go.dev build the page now rather than on its next index poll. Run those two
by hand for any tag that was cut before this step existed, or that it missed.

**A version that has been indexed can never be re-cut.** The proxy and
`sum.golang.org` record its content hash permanently, so re-tagging `vX.Y.Z` at
a different commit gives every user a `checksum mismatch` security error on that
version forever. This was always true of anyone who fetched a tag; the step only
makes it certain, and immediate. It does not endanger the stranded-tag recovery
above — that happens when `publish` fails *before* this step, so the version was
never indexed — but never re-use a version number that reached a release. Bump.

It **warns, it never fails**. By the time it runs, the tag is pushed and the
release is created — a cache warm-up that timed out is not a failed release,
and painting the run red would say it was. It also runs for rc tags: pkg.go.dev
files prereleases separately and never shows an rc as the latest version.

**pkg.go.dev shows no documentation for dezhban, and that is expected.** It
renders docs only for licenses on its allow-list, and this project's is not on
it (see the LICENSE header for what it is and why). The page still carries the
import path, the version list and the install line, with a
not-legally-redistributable note where the docs would be. Nothing is lost in
practice: every package here is under `internal/` or is a `main` package, so
nothing is importable from outside the module — there is no library surface for
those docs to have described.

## Unsigned artifacts, signed checksums

There is no Apple Developer certificate in this project ($99/yr, and this is
a hobby project with no revenue), so neither the `.pkg` nor `Dezhban.app` is
**code-signed by Apple's Developer ID or notarized**. Gatekeeper blocks a
plain double-click of either.

That is not the same as "unverifiable". `SHA256SUMS.sig` is a real
**ed25519** signature (`tools/relsign`, verified in Go by
`internal/update.VerifySignature`) over the release's checksums — this is
what `dezhban upgrade` checks before ever applying a downloaded `.pkg`, since
that path replaces a root-owned LaunchDaemon binary and "download whatever
the CDN served" is not an acceptable bar there. It is deliberately **not**
cosign/sigstore: verifying a keyless Sigstore bundle needs `sigstore-go`,
which drags in `go-containerregistry`, protobuf, and friends — dozens of
extra modules in a binary that runs as a root daemon, against this project's
"dependency-light standalone binary" convention (see the top-level
CLAUDE.md). Plain `crypto/ed25519` is ~20 lines of stdlib and adds nothing.

`scripts/install.sh`/`install.ps1` verify the **checksum** (mandatory, aborts
on mismatch) but not this signature — a bare macOS system's `/usr/bin/openssl`
is LibreSSL, which can't verify raw ed25519 signatures the way a modern
OpenSSL 3.2+ can, and an installer that's silently weaker depending on
whether the user happens to have Homebrew's `openssl` on `PATH` is worse than
one guarantee applied consistently everywhere. The private signing key is a
GitHub Actions secret (`RELEASE_SIGNING_KEY`); see `internal/update/sig.go`
for the full story and how to rotate it if it's ever exposed.

For the terminal install, skip Gatekeeper's GUI assessment entirely:

```sh
shasum -a 256 -c SHA256SUMS --ignore-missing
sudo installer -pkg dezhban-X.Y.Z.pkg -target /
```

Through the UI: double-click, dismiss the warning, then **System Settings →
Privacy & Security → Open Anyway**. (macOS 15 removed the old right-click →
**Open** bypass for packages; on macOS 14 and earlier it still works.)

### Adding Apple signing later

`packaging/macos/build-pkg.sh` already has the seams, so this is a
two-environment-variable change, not a rewrite:

```sh
INSTALLER_SIGN_IDENTITY="Developer ID Installer: Your Name (TEAMID)" \
NOTARIZE_PROFILE="my-notary-profile" \
task pkg VERSION=vX.Y.Z
```

`INSTALLER_SIGN_IDENTITY` alone signs the package; adding `NOTARIZE_PROFILE` also
submits it with `notarytool` and staples the ticket. Both come from an Apple
Developer account; wire them into the `build-macos` job as repository secrets when
one exists. Note this only signs the **installer** — the app bundle inside would
still need its own `codesign` + hardened runtime pass added to
`gui/macos/build-app.sh` (which today only ad-hoc signs it, enough to satisfy
Apple Silicon's "no unsigned arm64 binaries" kernel rule, nothing more).

## Requirements

- The default `GITHUB_TOKEN` needs to push **tags** (`contents: write`, set in
  the workflow). It does **not** need to write to `main` at all anymore —
  that happens locally, as you, before dispatch.
- `gh` locally, for `task release` and `task release:check`. `task release:preview`
  needs only git.
- You must be able to push directly to `main` as yourself (check
  `current_user_can_bypass` via `gh api repos/OWNER/REPO/rulesets`) — this is
  true for the repo owner by default, but verify it if you're not.

## Where the logic lives

All version resolution, CHANGELOG rolling and notes rendering is in
**`scripts/release.sh`**, called identically by `.github/workflows/release.yml`
and by the `task release:*` family. That is deliberate: a local preview cannot
drift from what CI does, because it is the same code. It runs standalone
(`bash scripts/release.sh resolve --bump minor`) with no dev tooling.

See [development.md](development.md) for the build/version-stamping mechanics,
[install.md](../usage/install.md) for how these assets actually get onto a user's
machine, and [upgrade.md](../usage/upgrade.md) for `dezhban upgrade`.
