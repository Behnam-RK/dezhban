# Installing dezhban

## The short version

**macOS or Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.sh -o ~/dezhban-install.sh && sudo bash ~/dezhban-install.sh
```

**Windows** (elevated PowerShell):

```powershell
irm https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.ps1 | iex
```

Left to their defaults, all of these install the CLI, the menubar app on macOS,
and register the background service — **without starting it**. Run
`install.sh` from a file at a terminal on a machine that does **not** yet have
dezhban and you can decline the app or the service, or cancel outright; the
section below says what it asks. The Windows script never prompts.

### Why download it instead of piping it

Because run from a file at a terminal, the installer **asks**. Piped, it is
defined not to.

That distinction is a policy, not a technical limit. Every prompt in
`scripts/install.sh` reads `/dev/tty` directly, and `/dev/tty` is there either
way — so a piped run *could* stop and ask. It does not, because the script gates
the terminal test on stdin — `[ -t 0 ]`, alongside a readable `/dev/tty` and
`DEZHBAN_ASSUME_YES` not being set to `1` (below). Stdin is your terminal when you run
the file and a pipe when you pipe it, and it is the only stream that tells the
two apart. Testing stdout would not work: `curl | sudo bash` still has your terminal
on stdout, so the pipe would go interactive — the one thing every comment and
doc here promises it does not. A piped install therefore behaves identically on
your laptop and in CI, which is the point.

Run from a file at a real terminal, here is what it asks. On a **fresh
machine**, a menu: install with today's defaults, choose components, or cancel.
Choosing components asks whether to install the menubar app (macOS only) and
whether to register the service — the command-line tool is always installed, so
it is not one of the questions. Then, on fresh installs only, it offers to run
`dezhban setup` there and then, so you finish with a configured guard rather
than a set of instructions.

On a machine that **already has dezhban**, a different menu: upgrade (or
reinstall, whichever the version comparison calls for), uninstall, or cancel.
Uninstall asks "keep your config?" and then makes you type `uninstall` before
anything is removed. The setup wizard is deliberately not offered on this path —
you configured this machine once already.

Downloading first also lets you read the script before running it as root,
which is the right habit for anything that installs a kill switch. The command
above chains both steps, so to read it first, run only the `curl` half, check
it exited 0, read the file, and then run the `sudo bash` half. Your reading is
what replaces the `&&` there — it is the same check, done by eye.

Run each of those as a separate command rather than pasting them together. A
prompt reads from your terminal, so anything pasted after a line that prompts —
`sudo` asking for your password, the installer's own menu, a pager — is eaten as
the answer to that prompt rather than run as a command.

Delete `~/dezhban-install.sh` when you are done, whichever way you installed.
[Re-running the installer](#what-each-installer-does-precisely) is how you
upgrade, and a `sudo bash ~/dezhban-install.sh` typed from memory would run the
copy you downloaded months ago.

Download into your home directory, and chain the two commands with `&&`. A
failed download can leave a stale **or half-written** file behind — `curl -o`
truncates the target and writes what it received, and a truncated script still
runs as far as it parses — so a bare second command would run that. And `/tmp`
is world-writable under a predictable name, so what is there might be a file
another account put there. It would run as root.

**Unattended** is still one line, and still supported:

```sh
curl -fsSL https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.sh | sudo bash
```

`DEZHBAN_ASSUME_YES=1` forces that same no-prompt behaviour even at a real
terminal, for a script that wants the defaults deliberately. Set it **inside**
the sudo command — `sudo`'s default `env_reset` drops variables set in front of
it, so the obvious spelling is silently inert:

```sh
sudo DEZHBAN_ASSUME_YES=1 bash ~/dezhban-install.sh   # works
DEZHBAN_ASSUME_YES=1 sudo bash ~/dezhban-install.sh   # ignored
```

`VERSION=X.Y.Z` pins an exact release and needs the same placement:

```sh
sudo VERSION=0.13.0 bash ~/dezhban-install.sh
curl -fsSL https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.sh | sudo VERSION=0.13.0 bash
```

### Finishing by hand

If you skipped the wizard (or used the unattended form):

```sh
sudo dezhban setup     # choose your settings
sudo dezhban start     # arm it
```

If you also declined the service at the component prompt, register it first —
this is what the installer itself prints in that case:

```sh
sudo dezhban install   # register the service (skipped above)
sudo dezhban start     # then arm the kill switch
```

Everything below is why this is the recommended path, what else exists, and
how to verify what you downloaded.

## Why `curl`, and why it's not a hack

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

The script does not merely rely on that, though. `ditto -xk` faithfully
restores whatever extended attributes an archive carries, so an asset that
ever arrived by some other route — a proxy that rewrites downloads, a mirror,
a user who fetched the zip in a browser and re-ran the script against it —
would drag a quarantine flag in with it. The install steps therefore strip
`com.apple.quarantine` from both `/usr/local/bin/dezhban` and
`/Applications/Dezhban.app` explicitly, and verify the app's ad-hoc signature
survived the round-trip. It is a no-op on the normal path; it turns the
guarantee from lucky into enforced on every other one.

This matters more than cosmetics for the CLI specifically: a quarantined bare
executable is refused on `exec` too, not just an app bundle on double-click.
A flagged `/usr/local/bin/dezhban` would fail when **launchd** tried to start
it — the kill switch would simply never come up.

### Why isn't there a signed `.pkg` instead?

There is no Apple Developer certificate ($99/yr; a hobby project with no
revenue), and the `install.sh` path already solves the friction that signing would
for free. What dezhban does instead: **checksums, always**, plus an
**ed25519 signature** over every release. Full story, why the install
scripts check the checksum but not the signature, and how to add real Apple
signing later: [releasing.md § Unsigned artifacts, signed
checksums](../contribute/releasing.md#unsigned-artifacts-signed-checksums).

## What each installer does, precisely

1. Detects OS/arch, resolves the requested version (`VERSION=X.Y.Z` env var
   pins one; otherwise the latest release — which is never a `-rc` build).
2. Downloads the platform binary (+ the app bundle on macOS, unless you opted
   out at the component prompt) and `SHA256SUMS`.
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
   `/usr/local/share/dezhban/uninstall.sh`, and the `LICENSE` from that same
   tag to `/usr/local/share/dezhban/LICENSE` — the license requires that
   anyone who receives the software receives it too. Both are downloaded to a
   temporary file and moved into place only once complete, so a fetch that fails
   or times out is a warning rather than a failed install *and* never replaces a
   good copy from an earlier install with a truncated one — or with nothing.
   `install.ps1` fetches no uninstaller (Windows has none) but does fetch the
   `LICENSE`, to `%ProgramFiles%\dezhban\LICENSE`, beside `dezhban.exe`.

Re-running either script upgrades or reinstalls: it replaces the binary, and
if a service was already running, stops it first and restarts it after — but
only when a restart is actually safe right now (`dezhban upgrade
can-activate`, the same rule `dezhban upgrade apply` honors — never through
FULL BLOCK or an open switch window, see [upgrade.md](upgrade.md)). Refused:
the new binary is installed and the old daemon keeps enforcing on it until you
run `sudo dezhban restart` yourself, once the posture clears. Either way,
`/etc/dezhban/` (your config) and `/var/db/dezhban/` (learned endpoints,
state) are never touched.

**Uninstalling.** Run from a file at a real terminal, an existing install
offers "Uninstall" in its menu — shows exactly what will be removed, asks whether to keep your
config (default: yes), and requires typing `uninstall` to confirm. Or run it
directly any time: `sudo sh /usr/local/share/dezhban/uninstall.sh` (add
`KEEP_CONFIG=1` to keep `/etc/dezhban`). It always runs `panic` first — removes
the firewall rules even with no daemon running — before touching anything
else, so you're never left locked out mid-removal.

The script alone is not a complete removal: it cannot reach your login keychain,
so Dezhban's Touch ID key survives it (it prints the command that clears that).
On macOS, **Settings → Remove Dezhban…** in the app does both halves — see
[Removing dezhban completely](troubleshooting.md#removing-dezhban-completely).

## Other ways to install

### The `.pkg` (macOS)

Download `dezhban-<version>.pkg` from the
[Releases page](https://github.com/Behnam-RK/dezhban/releases) and:

```sh
sudo installer -pkg dezhban-<version>.pkg -target /
```

Unsigned, so a double-click hits Gatekeeper — see
[releasing.md](../contribute/releasing.md#unsigned-artifacts-signed-checksums)
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
left to call `panic`/`stop`/`uninstall` on). The `LICENSE` lands at
`/usr/share/doc/dezhban/LICENSE` — the per-package documentation directory both
`dpkg` and `rpm` own, so the package manager removes it again on uninstall. The
`.pkg` and `install.sh` put it in `/usr/local/share/dezhban/` instead, and the
menubar app carries its own copy at
`Dezhban.app/Contents/Resources/LICENSE`.

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
downloading a release. See [development.md](../contribute/development.md).

## Verifying a download by hand

Every release publishes `SHA256SUMS` and `SHA256SUMS.sig`:

```sh
curl -fsSLO https://github.com/Behnam-RK/dezhban/releases/download/vX.Y.Z/SHA256SUMS
curl -fsSLO https://github.com/Behnam-RK/dezhban/releases/download/vX.Y.Z/dezhban-darwin-arm64
shasum -a 256 -c SHA256SUMS --ignore-missing
```

`SHA256SUMS.sig` is the ed25519 signature `dezhban upgrade` verifies
internally; there's no convenient CLI verifier for it outside the Go binary
itself today. Why: [releasing.md § Unsigned artifacts, signed
checksums](../contribute/releasing.md#unsigned-artifacts-signed-checksums).

## Staying current

Once installed, [upgrade.md](upgrade.md) covers `dezhban upgrade` — the
in-place update path (macOS: fully self-serve; Linux/Windows: checks and
tells you, but leaves the actual update to your package manager).
