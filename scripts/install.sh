#!/usr/bin/env bash
# Installs dezhban from a GitHub release. This is the primary distribution
# channel (see docs/usage/install.md for why): curl deliberately does NOT set
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
# On a real terminal (curl saved to a file, then run — NOT piped, since piping
# makes stdin the script text itself) this asks a few questions: which
# components to install on a fresh machine, and upgrade/reinstall/uninstall on
# a machine that already has dezhban. Piped or non-interactive, it takes
# today's exact defaults with no prompt at all — DEZHBAN_ASSUME_YES=1 forces
# that behavior even on a real terminal.
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

# --- interactive discipline -----------------------------------------------
# stdin IS the script text itself when piped (`curl | sudo bash`), so any
# prompt reads /dev/tty directly. But the DECISION to prompt at all keys on
# `-t 0`, not `-t 1`: a piped install still has this terminal as its stdout,
# so testing stdout would make `curl | sudo bash` interactive — the exact
# thing every comment, doc, and changelog entry here promises it is not.
# stdin is the only stream that actually distinguishes the two invocations.
# /dev/tty must also be readable and the caller must not have opted out.
# Every function below degrades silently with no prompt at all when this is
# 0 — that is the load-bearing property: a provisioner, CI job, or
# `curl | sudo bash` must see byte-for-byte today's non-interactive behavior.
interactive=0
if [ -t 0 ] && [ -r /dev/tty ] && [ "${DEZHBAN_ASSUME_YES:-0}" != "1" ]; then
	interactive=1
fi

# ask VAR PROMPT DEFAULT — sets the (bash 3.2 has no namerefs, hence eval)
# variable named VAR to a line read from /dev/tty, or DEFAULT verbatim when
# not interactive or the line was empty. Always succeeds.
ask() {
	if [ "$interactive" != 1 ]; then
		eval "$1=\$3"
		return 0
	fi
	printf '%s' "$2" > /dev/tty
	IFS= read -r _ask_reply < /dev/tty || _ask_reply=""
	[ -n "$_ask_reply" ] || _ask_reply="$3"
	eval "$1=\$_ask_reply"
}

# confirm PROMPT DEFAULT("Y"|"N") — prints PROMPT with a [Y/n]/[y/N] suffix on
# /dev/tty and returns 0 for yes, 1 for no. Not interactive: DEFAULT wins with
# no prompt shown at all.
confirm() {
	suffix="[Y/n]"
	[ "$2" = N ] && suffix="[y/N]"
	ask _confirm_reply "$1 $suffix " "$2"
	# shellcheck disable=SC2154 # ask() assigns this indirectly via eval "$1=..."
	case "$_confirm_reply" in
		[Yy]*) return 0 ;;
		[Nn]*) return 1 ;;
		*) [ "$2" != N ] ;;
	esac
}

# menu PROMPT VAR OPTION... — prints PROMPT and a numbered OPTION list to
# /dev/tty, reads a choice into VAR (1-based). Not interactive: VAR is always
# 1 — every menu below puts the today-equivalent action first, so this is the
# same rule confirm/ask follow, just for a multi-way choice.
#
# Unrecognised input is NOT re-prompted: each caller's `case` simply falls
# through to option 1. That is safe by construction because option 1 is always
# the non-destructive install/upgrade — a typo can never land on "uninstall",
# which additionally requires typing the word out in full.
menu() {
	_menu_prompt="$1"; _menu_var="$2"; shift 2
	if [ "$interactive" != 1 ]; then
		eval "$_menu_var=1"
		return 0
	fi
	printf '%s\n' "$_menu_prompt" > /dev/tty
	_menu_i=1
	for _menu_opt in "$@"; do
		printf '  %d) %s\n' "$_menu_i" "$_menu_opt" > /dev/tty
		_menu_i=$((_menu_i + 1))
	done
	printf 'choice [1]: ' > /dev/tty
	IFS= read -r _menu_reply < /dev/tty || _menu_reply=""
	[ -n "$_menu_reply" ] || _menu_reply=1
	eval "$_menu_var=\$_menu_reply"
}

# --- step counter -----------------------------------------------------------
# [n/N] progress lines. total is computed once every conditional step is known
# (component choice, whether a service is already running) and before any of
# them run — see "compute step total" below.
step_n=0
step_total=0
step() {
	step_n=$((step_n + 1))
	note "[$step_n/$step_total] $*"
}

# dl NAME — fetches release asset NAME into $tmp/NAME. Shows curl's own
# progress bar on a real terminal; quiet everywhere else (unchanged from
# before — a piped install must not spew a progress bar into a log).
dl() {
	if [ "$interactive" = 1 ]; then
		curl -fL --progress-bar -o "$tmp/$1" "$GH/releases/download/$tag/$1"
	else
		curl -fsSL -o "$tmp/$1" "$GH/releases/download/$tag/$1"
	fi
}

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

# --- 2b. classify: fresh install vs upgrade ------------------------------------
# Read the OUTGOING version before anything is overwritten, so the run can tell
# the user what actually changed and — more importantly — so the "next steps"
# footer never tells an existing user to run `setup`, which would walk them
# through re-answering a config they already have.
#
# `dezhban version` prints "dezhban <version>". A binary too old or too broken
# to answer still counts as an existing install: the safe classification of an
# unreadable prior install is "upgrade", never "fresh".

mode=fresh
prev_version=""
if [ -x /usr/local/bin/dezhban ]; then
	prev_version="$(/usr/local/bin/dezhban version 2>/dev/null | awk 'NR==1 {print $2}')"
	[ -n "$prev_version" ] || prev_version="unknown"
	prev_version="${prev_version#v}"
	if [ "$prev_version" = "$version" ]; then
		mode=reinstall
		note "dezhban $version is already installed — reinstalling the same version"
	else
		mode=upgrade
		note "existing installation found: $prev_version → $version"
	fi
else
	note "no existing installation found — this is a first-time install"
fi

# Whether a service is already running, checked here (read-only — this just
# asks the OLD binary) so both the step total below and the stop/restart
# sequence later can rely on one answer. A FRESH install never touches this:
# was_running stays 0, so enforcement is never armed here. That is the same
# invariant the .pkg's postinstall holds: a kill switch must not arm itself
# during install.
was_running=0
if [ -x /usr/local/bin/dezhban ] \
	&& /usr/local/bin/dezhban status --json 2>/dev/null | grep -q '"service": *"installed, running"'; then
	was_running=1
fi

# --- 2c. menu: what to do -------------------------------------------------
# Every branch's option 1 is exactly what a non-interactive run already does —
# menu()'s own non-interactive default is "1", so this whole block is a no-op
# in effect when interactive=0.

install_app=0
[ "$goos" = darwin ] && install_app=1
install_service=1

if [ "$mode" = fresh ]; then
	fresh_desc="CLI + register the service, not started"
	[ "$goos" = darwin ] && fresh_desc="CLI + menubar app + register the service, not started"
	fresh_choice=1
	menu "Install dezhban $version:" fresh_choice \
		"Install now ($fresh_desc)" \
		"Choose components" \
		"Cancel"
	case "$fresh_choice" in
		3) note "cancelled — nothing changed"; exit 0 ;;
		2)
			if [ "$goos" = darwin ]; then
				confirm "Install the menubar app (Dezhban.app)?" Y && install_app=1 || install_app=0
			fi
			confirm "Register dezhban as a system service now? (installed, not started)" Y && install_service=1 || install_service=0
			;;
	esac
else
	if [ "$mode" = upgrade ]; then
		upgrade_choice=1
		menu "dezhban $prev_version is installed; $version is available." upgrade_choice \
			"Upgrade to $version" \
			"Uninstall dezhban" \
			"Cancel"
	else
		upgrade_choice=1
		menu "dezhban $version is already installed." upgrade_choice \
			"Reinstall $version" \
			"Uninstall dezhban" \
			"Cancel"
	fi
	case "$upgrade_choice" in
		2) do_uninstall=1 ;;
		3) note "cancelled — nothing changed"; exit 0 ;;
	esac
fi

if [ "${do_uninstall:-0}" = 1 ]; then
	SHARE_DIR=/usr/local/share/dezhban
	uninstaller="$SHARE_DIR/uninstall.sh"
	if [ ! -x "$uninstaller" ]; then
		# Fetch the uninstaller matching what's actually installed (prev_version),
		# never the newer $tag just resolved above — this run may not even proceed
		# with that version. "unknown" (an unreadable prior binary) has no real tag
		# to ask for, so fall back to the version this run resolved.
		fetch_tag="v$prev_version"
		[ "$prev_version" = "unknown" ] && fetch_tag="$tag"
		note "no installed uninstaller found — fetching the one matching $fetch_tag"
		uninstall_src="packaging/macos/uninstall.sh"
		[ "$goos" = linux ] && uninstall_src="packaging/linux/uninstall.sh"
		mkdir -p "$SHARE_DIR"
		curl -fsSL -o "$uninstaller" "https://raw.githubusercontent.com/$REPO/$fetch_tag/$uninstall_src" \
			|| die "could not fetch the uninstaller for $fetch_tag"
		chmod +x "$uninstaller"
	fi

	echo
	echo "This will remove:"
	echo "  - dezhban's firewall rules (panic teardown)"
	echo "  - the CLI, and the menubar app if installed"
	echo "  - the system service"
	echo "  - daemon state (learned VPN endpoints, live posture)"
	echo

	keep_config=1
	confirm "Keep your config in /etc/dezhban?" Y && keep_config=1 || keep_config=0

	echo
	ask confirm_word "Type \"uninstall\" to confirm: " ""
	# shellcheck disable=SC2154 # ask() assigns this indirectly via eval "$1=..."
	[ "$confirm_word" = "uninstall" ] || die "confirmation did not match \"uninstall\" — nothing was removed"

	if [ "$keep_config" = 1 ]; then
		KEEP_CONFIG=1 sh "$uninstaller"
	else
		sh "$uninstaller"
	fi
	exit 0
fi

# --- 2d. compute step total ------------------------------------------------
# Every conditional below mirrors a real step further down: keep this in sync
# with the sequence, or the [n/N] count will drift from what's actually
# happening.
# Base, unconditional steps: download CLI, verify checksums, install CLI,
# fetch uninstaller. Platform/mode detection and version resolution print
# their own "==>" lines but aren't part of this count.
step_total=4
[ "$install_app" = 1 ] && step_total=$((step_total + 2))     # download + install app
[ "$install_service" = 1 ] && step_total=$((step_total + 1)) # register service
# Upper bound: was_running, not restart_after — the activation gate (checked
# later, right before the stop) may still refuse and skip both, in which case
# the printed count simply falls short of this total. That's the honest
# tradeoff: the gate must be checked as late as possible, not this early.
[ "$was_running" = 1 ] && step_total=$((step_total + 2))

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

step "downloading $asset $tag"
dl "$asset"
# Not dl(): SHA256SUMS is a few hundred bytes, and dl's interactive branch would
# flash a progress bar that finishes before it can be read. Same URL shape,
# always quiet.
curl -fsSL -o "$tmp/SHA256SUMS" "$GH/releases/download/$tag/SHA256SUMS"
if [ "$install_app" = 1 ]; then
	step "downloading Dezhban-macos.app.zip"
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

step "verifying checksums"
verify "$asset"
[ "$install_app" = 1 ] && verify "Dezhban-macos.app.zip"

# --- 4. install -----------------------------------------------------------
# The CLI lands FIRST, before anything is stopped. If a service is already
# running (an upgrade over a live install) that is safe: overwriting a running
# executable's file is safe on unix, and the old daemon keeps enforcing on its
# old inode until it actually restarts.
step "installing the CLI to /usr/local/bin/dezhban"
install -m 0755 "$tmp/$asset" /usr/local/bin/dezhban.new
mv -f /usr/local/bin/dezhban.new /usr/local/bin/dezhban

# Same enforcement as the .app below, and it matters just as much here: a
# quarantined bare executable is refused on exec too (not only bundles), so a
# flagged binary would fail as a launchd-started daemon — i.e. the kill switch
# silently never comes up. Cheap no-op when the flag was never set.
[ "$goos" = darwin ] && { xattr -d com.apple.quarantine /usr/local/bin/dezhban 2>/dev/null || true; }

# STOPPING/RESTARTING a live daemon, unlike installing the file, is gated on
# the same activation rule `dezhban upgrade apply` honours (docs/adr/0007): a
# restart must never happen through FULL BLOCK or an open switch window, since
# that would lift a block on a forbidden-country exit — the one thing this tool
# exists to prevent. No override: an operator who wants to force it already has
# `sudo dezhban restart`, typed by name.
#
# Asked as late as possible — the posture can change during the download above,
# so this must not reuse the answer from when was_running was first read. It is
# asked of the NEWLY INSTALLED binary, deliberately: `upgrade can-activate`
# ships for the first time with this very change, so the previously-installed
# binary would answer "unknown subcommand" on every upgrade from an older
# release and no live daemon would ever be restarted again. The new binary
# reads the same daemon-written state snapshot the old one does, so asking it
# is both correct and the only version-independent option.
restart_after=0
if [ "$was_running" = 1 ]; then
	if gate_msg="$(/usr/local/bin/dezhban upgrade can-activate 2>&1)"; then
		restart_after=1
	else
		note "not restarting the running service: $gate_msg"
		note "the currently running dezhban keeps enforcing on the old build; retry later with: sudo dezhban restart"
	fi
fi

if [ "$restart_after" = 1 ]; then
	step "stopping the running service for the upgrade"
	/usr/local/bin/dezhban --no-sudo stop || true
fi

if [ "$install_app" = 1 ]; then
	step "installing the menubar app to /Applications/Dezhban.app"
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
if [ "$install_service" = 1 ]; then
	step "registering the service (not starting it — see 'next steps' below)"
	# Absolute path, never a bare `dezhban`: /usr/local/bin is not necessarily first
	# on root's PATH — on Apple Silicon, Homebrew's /opt/homebrew/bin usually is,
	# and this repo now ships a Homebrew formula that puts a dezhban there. Resolving
	# through PATH could register the service using a DIFFERENT build than the one
	# just installed two lines above.
	/usr/local/bin/dezhban --no-sudo install --config "$CONFIG_DIR/dezhban.json" \
		|| die "could not register the service; the CLI is installed at /usr/local/bin/dezhban — retry with 'sudo dezhban install'"
fi

if [ "$restart_after" = 1 ]; then
	step "restarting the service"
	/usr/local/bin/dezhban --no-sudo start
fi

# The uninstaller comes from the SAME tag being installed — same guarantee the
# .pkg gives (it bakes in whichever uninstall.sh existed when that tag was
# built), just fetched instead of embedded in a payload.
step "fetching the uninstaller"
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
if [ "$mode" = fresh ]; then
	echo "dezhban $version installed."
	# Explicitly gated on interactive, not just confirm()'s own default: a
	# piped/non-interactive run must never launch an interactive wizard, no
	# matter what a "default yes" would otherwise imply.
	setup_now=0
	if [ "$interactive" = 1 ]; then
		confirm "Run the setup wizard now? (VPN, tunnel interfaces, blocked countries)" Y && setup_now=1
	fi
	# The wizard runs BEFORE the "next steps" heading, never under it: it is
	# pages of its own interactive output, so a heading printed first has
	# scrolled off by the time it exits, and the steps that follow it would
	# read as part of the wizard.
	if [ "$setup_now" = 1 ]; then
		/usr/local/bin/dezhban setup < /dev/tty
		echo
	fi
	echo "next steps:"
	[ "$setup_now" = 1 ] || echo "  dezhban setup   # configure: VPN, tunnel interfaces, blocked countries"
	if [ "$install_service" = 1 ]; then
		echo "  sudo dezhban start   # arm the kill switch"
	else
		echo "  sudo dezhban install   # register the service (skipped above)"
		echo "  sudo dezhban start     # then arm the kill switch"
	fi
else
	if [ "$mode" = upgrade ]; then
		echo "dezhban upgraded: $prev_version -> $version."
	else
		echo "dezhban $version reinstalled."
	fi
	echo "Your config in $CONFIG_DIR and any learned VPN state were left untouched."
	echo
	# Deliberately no `setup` here: an existing user has a config already, and
	# re-running the wizard would walk them through replacing it.
	if [ "$restart_after" = 1 ]; then
		echo "The service was running and has been restarted on the new build."
		echo "  dezhban status   # confirm the posture came back as expected"
	elif [ "$was_running" = 1 ]; then
		echo "The new build is installed, but the running service was NOT restarted"
		echo "(see the activation-gate note above) — it is still enforcing on the old build."
		echo "  sudo dezhban restart   # once the posture clears"
	else
		echo "The service was not running, so it was left stopped."
		echo "  sudo dezhban start    # arm the kill switch"
	fi
fi
echo
echo "uninstall any time with:  sudo sh $SHARE_DIR/uninstall.sh"
