# Installing dezhban

## The short version

**macOS or Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.sh | sudo bash
```

**Windows** (elevated PowerShell):

```powershell
irm https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.ps1 | iex
```

Either installs the CLI, the menubar app on macOS, and registers the
background service — **without starting it**. Finish with:

```sh
sudo dezhban setup     # choose your settings
sudo dezhban start     # arm it
```

Everything below is why this is the recommended path, what else exists, and
how to verify what you downloaded.

## Why curl-pipe-bash, and why it's not a hack

macOS's Gatekeeper blocks a double-clicked, unsigned `.pkg` or `.app` because
the file carries a `com.apple.quarantine` extended attribute — set by
whatever downloaded it (a browser, `AirDrop`, Mail). **`curl` does not set
that attribute.** This is documented, intentional Apple behavior, not a
loophole: Apple's own guidance describes Unix networking tools like `curl`
and `scp` as exempt, because Gatekeeper's quarantine model is about
"downloaded from the internet by an end-user-facing app", not about content
provenance in general.

So `scripts/install.sh` genuinely installs with **zero Gatekeeper friction** —
not because it evades a check, but because the check was never designed to
fire on this path in the first place. This is the same mechanism rustup,
Homebrew's own installer, and most modern CLI tools rely on.

### Why isn't there a signed `.pkg` instead?

Because a signed, notarized `.pkg` requires enrolling in the **Apple
Developer Program — $99/year** — and there is no free tier or workaround.
dezhban is a hobby project with no revenue to justify that recurring cost for
what curl-pipe-bash already solves for free. If you're the kind of user who
specifically wants Apple's own chain of trust (some VPN-kill-switch users
are, and reasonably so), the honest answer is: not yet. The seam is there —
`packaging/macos/build-pkg.sh`'s `INSTALLER_SIGN_IDENTITY`/`NOTARIZE_PROFILE`
— so if that ever changes, it's a two-variable config change, not a rewrite
(see [docs/releasing.md](releasing.md#adding-apple-signing-later)).

What dezhban does instead, and does today: **checksums, always**, and an
**ed25519 signature** over every release's checksums — see
[docs/releasing.md](releasing.md#unsigned-artifacts-signed-checksums)
for the full story, and why that signature is checked by `dezhban upgrade`
but deliberately not by the install scripts.

## What each installer does, precisely

1. Detects OS/arch, resolves the requested version (`VERSION=X.Y.Z` env var
   pins one; otherwise the latest release — which is never a `-rc` build).
2. Downloads the platform binary (+ the app bundle on macOS) and
   `SHA256SUMS`.
3. **Verifies the checksum. A mismatch aborts the install outright** — this
   is not a warning, it's a hard stop. This is what actually protects you on
   this path: HTTPS gets the bytes to you unmodified in transit, and the
   checksum proves those are the bytes CI actually built.
4. Installs the binary (and app, on macOS) and registers the service —
   **never starting it**. A kill switch that arms itself during install, before
   you've configured anything, is how you lock yourself out of your own
   network. See `packaging/macos/scripts/postinstall` and
   `packaging/linux/postinstall.sh` for the same rule enforced by the `.pkg`
   and `.deb`/`.rpm` paths too.
5. Fetches the matching uninstaller (`packaging/macos/uninstall.sh` or
   `packaging/linux/uninstall.sh`, from the **same tag** just installed) to
   `/usr/local/share/dezhban/uninstall.sh`.

Re-running either script is safe: it stops the service first only if it was
already running, replaces the binary, and restarts only if it was running —
never touching `/etc/dezhban/` (your config) or `/var/db/dezhban/` (learned
endpoints, state) either way.

## Other ways to install

### The `.pkg` (macOS)

Download `dezhban-<version>.pkg` from the
[Releases page](https://github.com/Behnam-RK/dezhban/releases) and:

```sh
sudo installer -pkg dezhban-<version>.pkg -target /
```

Unsigned, so a double-click hits Gatekeeper — see
[docs/releasing.md](releasing.md#unsigned-artifacts-signed-checksums)
for the terminal-only workaround, or use `install.sh` instead, which doesn't
have this problem at all.

### `.deb` / `.rpm` (Linux)

Also on the Releases page:

```sh
sudo dpkg -i dezhban_<version>_<arch>.deb      # Debian/Ubuntu
sudo rpm -i dezhban-<version>-1.<arch>.rpm     # Fedora/RHEL
```

Declares an `nftables` dependency (the Linux backend shells out to `nft`).
Registers the service on install (`postinstall.sh`, same never-auto-start
rule) and tears down rules + unregisters on removal (`preremove.sh` — it has
to run *before* the package manager deletes the binary, or there's nothing
left to call `panic`/`stop`/`uninstall` on).

### Bare binaries

`dezhban-<os>-<arch>` (or `.exe` on Windows) on the Releases page, for
scripting your own install, or `dezhban-<version>-<os>-<arch>.tar.gz` if you
want an archive with the binary, `LICENSE`, and `README.md` together. These
are what package managers consume — see the Homebrew note below.

### Homebrew — not published yet

A formula template exists (`packaging/homebrew/dezhban.rb.tmpl` +
`scripts/gen-homebrew-formula.sh`), but it isn't published: that requires a
separate `behnam-rk/homebrew-tap` repository that doesn't exist yet. When it
does, it will be a CLI-only **formula**, not a cask — Homebrew is removing
unsigned casks from the official tap by September 2026, but that policy
targets signed/notarized `.app` bundles, not plain binary formulae, so this
stays viable without an Apple Developer ID. Homebrew can't register a
privileged system service either way, so even once published, `brew install`
would still need `sudo dezhban install && sudo dezhban setup` to finish.

### Build from source

```sh
git clone https://github.com/Behnam-RK/dezhban && cd dezhban
task build
sudo bash scripts/install-local.sh
```

Different from everything above: this compiles locally rather than
downloading a release. See [docs/development.md](development.md).

## Verifying a download by hand

Every release publishes `SHA256SUMS` and `SHA256SUMS.sig`:

```sh
curl -fsSLO https://github.com/Behnam-RK/dezhban/releases/download/vX.Y.Z/SHA256SUMS
curl -fsSLO https://github.com/Behnam-RK/dezhban/releases/download/vX.Y.Z/dezhban-darwin-arm64
shasum -a 256 -c SHA256SUMS --ignore-missing
```

`SHA256SUMS.sig` is the ed25519 signature `dezhban upgrade` verifies
internally (`internal/update.VerifySignature`); there's no convenient CLI
verifier for it outside the Go binary itself today (see
[docs/releasing.md](releasing.md#unsigned-artifacts-signed-checksums)
for why the install scripts don't check it directly).

## Staying current

Once installed, [docs/upgrade.md](upgrade.md) covers `dezhban upgrade` — the
in-place update path (macOS: fully self-serve; Linux/Windows: checks and
tells you, but leaves the actual update to your package manager).
