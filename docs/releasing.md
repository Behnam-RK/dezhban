# Releasing

A release is one command:

```sh
task release BUMP=minor          # or: task release VERSION=0.2.0
```

It runs a local preflight, shows you exactly what it is about to do, asks you to
type the tag to confirm, dispatches the `Release` workflow, and streams it.

Everything below is what that command sets in motion, and how to drive it by hand
if you'd rather.

## The order is the design

```
prepare   resolve the version; require CI green on this exact commit
   |      -- writes nothing, anywhere --
build     cross-compile all 5 CLI targets; build the macOS .pkg;
   |      install it on a runner and uninstall it again
publish   tag the tested commit, publish the release,
          open a PR with the rolled CHANGELOG
```

Nothing touches the repository until every artifact has been built and the
installer has been proven to install and uninstall cleanly. **A failed release
leaves no tag behind** — just re-dispatch. (The old order tagged first, so a
failed build stranded a tag that the workflow's own "tag already exists" guard
then refused to retry.)

`publish` also re-checks that `main` still points at the commit `prepare` pinned.
If something merged mid-release, it stops rather than tag a tree that was never
built.

## The workflow never pushes to `main`

The `main` ruleset requires a pull request, and `github-actions[bot]` **cannot**
bypass it: GitHub only allows the Actions app as a ruleset bypass actor on
**org-owned** repos, and this one is user-owned. (Note the legacy
`/branches/main/protection` API reports 404 here — it does not know about
rulesets. Check `gh api repos/OWNER/REPO/rulesets`.)

Tags are not branch-ruled, so the **tag and the release go out directly**. What
cannot go directly is the `chore(release)` CHANGELOG commit — so `publish` pushes
it to a `release/vX.Y.Z` branch and opens a PR for you to merge. One click, after
the release is already out.

Opening that PR needs **Settings → Actions → General → "Allow GitHub Actions to
create and approve pull requests"** (it is off by default; enabling it was a
deliberate choice). Note this is a single toggle: it also lets workflows *approve*
PRs, which can satisfy the ruleset's `required_approving_review_count`. If you
ever want that strictness back, turn it off — the release still works, it just
prints a one-click link instead of opening the PR itself.

The PR step is **non-fatal on purpose**. By the time it runs, the tag is pushed
and the release is published — that *is* the release. A failure there prints the
compare link and leaves the release green, rather than painting a perfectly good
release red and implying it should be re-run. (It must not be: the tag exists, so
a re-run would be rejected.)

Consequence worth knowing: `## [Unreleased]` on `main` still holds the shipped
entries until you merge that PR. Merge it promptly and the next release's notes
stay correct.

## Before you release

Keep [CHANGELOG.md](../CHANGELOG.md)'s `## [Unreleased]` section current as you
land changes — **it is the release notes**. The release fails if it is empty, so
there is nothing to accidentally ship with no notes.

## Preview it first

```sh
task release:check                    # on main? clean? synced? CI green? notes present?
task release:preview VERSION=0.2.0    # the resolved tag, the notes, the CHANGELOG diff
```

`release:preview` runs `scripts/release.sh` — **the same code the workflow runs** —
so what it prints is what CI will do. It never modifies your working tree.

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

## Assets

Each release carries:

- `dezhban-X.Y.Z.pkg` — the macOS installer (the headline asset)
- `dezhban-darwin-arm64`, `dezhban-darwin-amd64`
- `dezhban-linux-amd64`, `dezhban-linux-arm64`
- `dezhban-windows-amd64.exe`
- `Dezhban-macos.app.zip` — the menubar app alone
- `SHA256SUMS` — covering all of the above, the `.pkg` included

## The macOS artifacts are unsigned

There is no Apple Developer certificate in this project, so neither the `.pkg`
nor `Dezhban.app` is **code-signed or notarized**. Gatekeeper blocks both on a
plain double-click. Every release's notes therefore carry the working install
line; the short version is:

```sh
shasum -a 256 -c SHA256SUMS --ignore-missing
sudo installer -pkg dezhban-X.Y.Z.pkg -target /
```

The terminal skips Gatekeeper's GUI assessment entirely. Through the UI:
double-click, dismiss the warning, then **System Settings → Privacy & Security →
Open Anyway**. (macOS 15 removed the old right-click → **Open** bypass for
packages; on macOS 14 and earlier it still works.) For the standalone app zip:
right-click → **Open**, or `xattr -dr com.apple.quarantine Dezhban.app`.

### Adding signing later

`packaging/macos/build-pkg.sh` already has the seams, so this is a
two-environment-variable change, not a rewrite:

```sh
INSTALLER_SIGN_IDENTITY="Developer ID Installer: Your Name (TEAMID)" \
NOTARIZE_PROFILE="my-notary-profile" \
task pkg:build VERSION=vX.Y.Z
```

`INSTALLER_SIGN_IDENTITY` alone signs the package; adding `NOTARIZE_PROFILE` also
submits it with `notarytool` and staples the ticket. Both come from an Apple
Developer account; wire them into the `build-macos` job as repository secrets when
one exists. (The app bundle inside would need `codesign` + hardened runtime too —
add that to `macos-gui/build-app.sh` at the same time.)

## Requirements

- The default `GITHUB_TOKEN` must be able to push **tags** and **branches other
  than `main`**, and to open a PR (`contents: write` + `pull-requests: write`,
  both set in the workflow). It does **not** need to push to `main`, and the
  ruleset there is deliberately left intact.
- `gh` locally, for `task release` and `task release:check`. `task release:preview`
  needs only git.

## Where the logic lives

All version resolution, CHANGELOG rolling and notes rendering is in
**`scripts/release.sh`**, called identically by `.github/workflows/release.yml`
and by the `task release:*` family. That is deliberate: a local preview cannot
drift from what CI does, because it is the same code. It runs standalone
(`bash scripts/release.sh resolve --bump minor`) with no dev tooling.

See [development.md](development.md) for the build/version-stamping mechanics.
