# Releasing

Releases are cut by hand-dispatching the `Release` GitHub Actions workflow — there
is no tag-push trigger. One run: resolves the version, rolls the CHANGELOG, tags
it, cross-compiles every platform plus the macOS GUI, and publishes a GitHub
Release with checksums.

## Before you release

Keep [CHANGELOG.md](../CHANGELOG.md)'s `## [Unreleased]` section current as you
land changes — it's what becomes the release notes. The workflow **fails** if
`[Unreleased]` is empty, so there's nothing to accidentally ship with no notes.

## Cutting a release

1. Go to **Actions → Release → Run workflow**.
2. Fill in the inputs:
   - **`version`** — exact `X.Y.Z` (no leading `v`). **Required for the very
     first release**, since there's no prior tag to bump from.
   - **`bump`** — `patch` / `minor` / `major`, applied to the latest tag. Used
     only when `version` is left blank.
3. Run it. The workflow:
   - resolves and validates the new version (must be a valid semver strictly
     greater than the latest tag, and not already tagged),
   - renames `## [Unreleased]` to `## [X.Y.Z] - <date>` and opens a fresh empty
     `## [Unreleased]` above it, refreshing the compare links at the bottom of
     the file,
   - commits that as `chore(release): vX.Y.Z [skip ci]` to `main` and pushes the
     annotated tag,
   - cross-compiles the 5 CLI targets (`task build:all`) and the macOS GUI,
     both version-stamped from the new tag,
   - builds the macOS installer (`task pkg:build`) and **smoke-tests it on the
     runner**: installs it, asserts the payload landed and the service was
     registered but *not started*, then runs the shipped uninstaller and asserts it
     left nothing behind. A broken installer fails the release instead of shipping,
   - publishes a GitHub Release titled `vX.Y.Z` with the extracted changelog
     entry as the body, and attaches:
     - `dezhban-X.Y.Z.pkg` ← the macOS installer (the headline asset)
     - `dezhban-darwin-arm64`, `dezhban-darwin-amd64`
     - `dezhban-linux-amd64`, `dezhban-linux-arm64`
     - `dezhban-windows-amd64.exe`
     - `Dezhban-macos.app.zip`
     - `SHA256SUMS`

`[skip ci]` in the release commit keeps `ci.yml`'s push-to-`main` trigger from
firing redundantly on the changelog bump.

## The macOS artifacts are unsigned

There's no Apple Developer certificate in this project, so neither
`dezhban-X.Y.Z.pkg` nor `Dezhban.app` is **code-signed or notarized**. Gatekeeper
will refuse both on a plain double-click.

For the **installer**, the cleanest path is the terminal, which skips Gatekeeper's
GUI assessment entirely:

```sh
sudo installer -pkg dezhban-X.Y.Z.pkg -target /
```

Through the UI: double-click, dismiss the warning, then **System Settings → Privacy
& Security → Open Anyway**. (macOS 15 removed the old right-click → **Open** bypass
for packages; on macOS 14 and earlier that still works.)

For the standalone **app zip**: right-click → **Open**, or
`xattr -dr com.apple.quarantine Dezhban.app`. Once per downloaded copy.

### Adding signing later

`packaging/macos/build-pkg.sh` already has the seams, so signing is a
two-environment-variable change, not a rewrite:

```sh
INSTALLER_SIGN_IDENTITY="Developer ID Installer: Your Name (TEAMID)" \
NOTARIZE_PROFILE="my-notary-profile" \
task pkg:build VERSION=vX.Y.Z
```

`INSTALLER_SIGN_IDENTITY` alone signs the package; setting `NOTARIZE_PROFILE` as
well also submits it with `notarytool` and staples the ticket. Both come from an
Apple Developer account; wire them into the `macos` job as repository secrets when
one exists. (The app bundle inside would need `codesign` + hardened runtime too —
add that to `macos-gui/build-app.sh` at the same time.)

## Requirements for the workflow to succeed

- `main` must accept a direct push from the default `GITHUB_TOKEN` (the
  `prepare` job pushes the changelog commit and tag straight to `main`). If
  branch protection blocks that, either allow the GitHub Actions bot to bypass
  it or switch the workflow to a personal access token.
- The macOS GUI job needs a `macos-latest` runner with the Swift toolchain
  (preinstalled on GitHub-hosted macOS runners).

See [development.md](development.md) for the underlying build/version-stamping
mechanics (`git describe`, `-ldflags -X main.version`, `VERSION=` overrides).
