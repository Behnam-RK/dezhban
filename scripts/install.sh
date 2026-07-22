#!/usr/bin/env bash
# Installs dezhban from a GitHub release. This is the primary distribution
# channel (see docs/install.md for why): curl deliberately does NOT set
# com.apple.quarantine on what it downloads — Apple's own documented behaviour,
# not a workaround — so this is the one macOS install path with zero Gatekeeper
# friction. That property is now ENFORCED rather than assumed: the install steps
# below strip com.apple.quarantine from both the binary and the app bundle, so
# the guarantee holds even if an asset ever arrives by some route that does flag
# it. There is no free way to make a double-clicked .pkg or .app behave the
# same; that needs a $99/yr Apple Developer ID (see
# packaging/macos/build-pkg.sh's dormant INSTALLER_SIGN_IDENTITY seam).
#
#   curl -fsSL https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.sh | sudo bash
#   curl -fsSL .../install.sh | sudo VERSION=0.2.0 bash    # pin an exact version
#
# Must run as root: it installs to /usr/local and /etc, and registers a system
# service. Written for bash 3.2 — that is what macOS ships at /bin/bash with no
# Homebrew on PATH, and a fresh machine with nothing else installed is exactly
# who runs this script.
set -euo pipefail

REPO="Behnam-RK/dezhban"
GH="https://github.com/$REPO"

die()  { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*"; }

[ "$(id -u)" -eq 0 ] || die "run as root — e.g. curl -fsSL .../install.sh | sudo bash"

# --- 1. detect platform -------------------------------------------------------
# Only the 4 unix release targets. Windows has no curl-pipe-bash story — see
# scripts/install.ps1 — and anything else isn't a supported build target at all
# (scripts/install-local.sh is the build-from-source path for those).

os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
	Darwin) goos=darwin ;;
	Linux)  goos=linux ;;
	*) die "unsupported OS '$os' — dezhban ships prebuilt binaries for macOS and Linux only (see scripts/install-local.sh to build from source)" ;;
esac

case "$arch" in
	arm64|aarch64) goarch=arm64 ;;
	x86_64|amd64)  goarch=amd64 ;;
	*) die "unsupported architecture '$arch' (want arm64 or amd64/x86_64)" ;;
esac

asset="dezhban-$goos-$goarch"
note "platform: $goos/$goarch"

# --- 2. resolve version --------------------------------------------------------
# VERSION pins an exact tag. Otherwise follow GitHub's /releases/latest
# redirect — a plain HTTP 302, no API call, no JSON to parse, and no rate
# limit. It already excludes rc builds: the release workflow always tags those
# --prerelease, and "latest" is defined to skip prereleases.

if [ -n "${VERSION:-}" ]; then
	version="${VERSION#v}"
	note "version: $version (pinned via VERSION=)"
else
	loc="$(curl -fsSI "$GH/releases/latest" | tr -d '\r' | grep -i '^location:' | awk '{print $2}')"
	[ -n "$loc" ] || die "could not resolve the latest release from $GH/releases/latest — pass VERSION=X.Y.Z to skip this lookup"
	version="${loc##*/}"
	version="${version#v}"
	note "version: $version (latest)"
fi
tag="v$version"

# --- 3. download + verify ------------------------------------------------------
# Checksum verification is mandatory and aborts on mismatch. This is deliberately
# NOT an ed25519 signature check: a bare macOS system's /usr/bin/openssl is
# LibreSSL, which cannot verify raw ed25519 signatures the way a modern OpenSSL
# 3.2+ can — a curl-pipe-bash installer that behaved differently (or silently
# weaker) depending on whether the user happened to have Homebrew's openssl on
# PATH would be worse than one guarantee applied consistently everywhere. The
# stronger, ed25519-verified path is `dezhban upgrade` (see internal/update),
# which is Go code with crypto/ed25519 natively available — no shell portability
# problem at all. Checksum + HTTPS transport is what protects THIS first install.

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

dl() { curl -fsSL -o "$tmp/$1" "$GH/releases/download/$tag/$1"; }

note "downloading $asset $tag"
dl "$asset"
dl "SHA256SUMS"
if [ "$goos" = darwin ]; then
	dl "Dezhban-macos.app.zip"
fi

sha256_check() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c -
	else
		shasum -a 256 -c -
	fi
}

verify() {
	# awk field match, not `grep " $1\$"`: $1 becomes part of a regex there, so
	# an asset name containing a "." (every name this script passes does —
	# "Dezhban-macos.app.zip") matches any character instead of a literal dot.
	# Not exploitable today (both call sites pass names this script itself
	# builds), but it is not an exact match either, and awk's field compare
	# costs nothing to get right.
	#
	# Everything after the FIRST separator is the name — not awk's $2, so that
	# a name containing a space still matches. The subs then normalise the two
	# formats sha256sum emits: text mode ("<hash>  <name>", two spaces — what
	# release.yml's `shasum -a 256` actually produces) and binary mode
	# ("<hash> *<name>", one space and a leading asterisk). This is a
	# deliberate line-for-line mirror of internal/update's checksumFor: both
	# parsers read the same SHA256SUMS file, and they must not disagree about
	# which lines exist in it.
	# The mode is decided by the ONE byte after the first space, and exactly one
	# byte is consumed either way — so a name legitimately starting with "*"
	# survives a text-mode line intact.
	line="$(awk -v n="$1" '
		{
			sub(/\r$/, "")
			i = index($0, " ")
			if (i == 0) next
			name = substr($0, i + 1)
			if (substr(name, 1, 1) == " " || substr(name, 1, 1) == "*") name = substr(name, 2)
			if (name == n) print
		}
	' "$tmp/SHA256SUMS")"

	# Assert the entry EXISTS before verifying it. Without this, a name that
	# matches nothing feeds empty stdin to sha256_check — and GNU `sha256sum -c -`
	# exits 0 on empty input (BSD `shasum -c -` exits 1), so on Linux a missing
	# or renamed SHA256SUMS entry would "verify" the download by never checking
	# it. A checksum step that passes when it found nothing to check is worse
	# than no checksum step, because it is trusted.
	[ -n "$line" ] || die "no checksum entry for $1 in SHA256SUMS — refusing to install unverified. This may mean a bad mirror or a tampered release; do not retry blindly."

	printf '%s\n' "$line" | ( cd "$tmp" && sha256_check ) >/dev/null \
		|| die "checksum mismatch for $1 — aborting install. This may mean a bad mirror or a tampered download; do not retry blindly."
}

note "verifying checksums"
verify "$asset"
[ "$goos" = darwin ] && verify "Dezhban-macos.app.zip"

# --- 4. install -----------------------------------------------------------
# If a service is already running (an upgrade over a live install), stop it
# before replacing the binary and restart after — restoring exactly the state
# it was already in. A FRESH install never touches this: was_running stays 0,
# so enforcement is never armed here. That is the same invariant the .pkg's
# postinstall holds: a kill switch must not arm itself during install.

was_running=0
if [ -x /usr/local/bin/dezhban ] \
	&& /usr/local/bin/dezhban status --json 2>/dev/null | grep -q '"service": *"installed, running"'; then
	was_running=1
	note "existing installation is running — stopping for the upgrade"
	/usr/local/bin/dezhban --no-sudo stop || true
fi

note "installing the CLI to /usr/local/bin/dezhban"
install -m 0755 "$tmp/$asset" /usr/local/bin/dezhban.new
mv -f /usr/local/bin/dezhban.new /usr/local/bin/dezhban

# Same enforcement as the .app below, and it matters just as much here: a
# quarantined bare executable is refused on exec too (not only bundles), so a
# flagged binary would fail as a launchd-started daemon — i.e. the kill switch
# silently never comes up. Cheap no-op when the flag was never set.
[ "$goos" = darwin ] && { xattr -d com.apple.quarantine /usr/local/bin/dezhban 2>/dev/null || true; }

if [ "$goos" = darwin ]; then
	note "installing the menubar app to /Applications/Dezhban.app"
	rm -rf /Applications/Dezhban.app
	ditto -xk "$tmp/Dezhban-macos.app.zip" /Applications

	# Gatekeeper: ENFORCE the invariant this script's header only asserts.
	#
	# The zero-friction property depends on nothing in the pipeline attaching
	# com.apple.quarantine — true today because curl doesn't, but `ditto -xk`
	# faithfully restores whatever xattrs the archive carries, so the moment an
	# asset is fetched by anything else (a corporate proxy that rewrites
	# downloads, a mirror, a user hand-fetching the zip in a browser and
	# re-running this against it) the app inherits a quarantine flag and macOS
	# refuses to launch it as "from an unidentified developer" — which, with no
	# Developer ID here, the user cannot clear from the Gatekeeper dialog at
	# all, only via this same xattr call. Stripping it unconditionally costs
	# nothing when it was already absent, and is the difference between a
	# documented guarantee and a lucky one.
	xattr -dr com.apple.quarantine /Applications/Dezhban.app 2>/dev/null || true

	# The bundle is ad-hoc signed at build time (gui/macos/build-app.sh) because
	# Apple Silicon's kernel will not exec an unsigned arm64 binary — that is a
	# hard launch requirement, not a Gatekeeper nicety. Verify the seal survived
	# the zip round-trip: a warning here explains a "damaged app" message that is
	# otherwise very hard to diagnose. Non-fatal — the CLI, which is the actual
	# kill switch, is already installed and works without the menubar app.
	if ! codesign --verify --deep /Applications/Dezhban.app 2>/dev/null; then
		echo "warning: /Applications/Dezhban.app failed signature verification — the menubar app may not launch." >&2
		echo "         The CLI is installed and fully functional; reinstall the app later if you want it." >&2
	fi
fi

CONFIG_DIR=/etc/dezhban
mkdir -p "$CONFIG_DIR"
note "registering the service (not starting it — see 'next steps' below)"
# Absolute path, never a bare `dezhban`: /usr/local/bin is not necessarily first
# on root's PATH — on Apple Silicon, Homebrew's /opt/homebrew/bin usually is,
# and this repo now ships a Homebrew formula that puts a dezhban there. Resolving
# through PATH could register the service using a DIFFERENT build than the one
# just installed two lines above.
/usr/local/bin/dezhban --no-sudo install --config "$CONFIG_DIR/dezhban.json" \
	|| die "could not register the service; the CLI is installed at /usr/local/bin/dezhban — retry with 'sudo dezhban install'"

if [ "$was_running" = 1 ]; then
	note "restarting the service"
	/usr/local/bin/dezhban --no-sudo start
fi

# The uninstaller comes from the SAME tag being installed — same guarantee the
# .pkg gives (it bakes in whichever uninstall.sh existed when that tag was
# built), just fetched instead of embedded in a payload.
SHARE_DIR=/usr/local/share/dezhban
mkdir -p "$SHARE_DIR"
uninstall_src="packaging/macos/uninstall.sh"
[ "$goos" = linux ] && uninstall_src="packaging/linux/uninstall.sh"
if curl -fsSL -o "$SHARE_DIR/uninstall.sh" "https://raw.githubusercontent.com/$REPO/$tag/$uninstall_src"; then
	chmod +x "$SHARE_DIR/uninstall.sh"
else
	echo "warning: could not fetch the uninstaller — install itself succeeded. Retry later with:" >&2
	echo "  curl -fsSL -o $SHARE_DIR/uninstall.sh https://raw.githubusercontent.com/$REPO/$tag/$uninstall_src" >&2
fi

echo
echo "dezhban $version installed."
echo
echo "next steps:"
echo "  sudo dezhban setup   # configure: VPN, tunnel interfaces, blocked countries"
echo "  sudo dezhban start   # arm the kill switch"
echo
echo "uninstall any time with:  sudo sh $SHARE_DIR/uninstall.sh"
