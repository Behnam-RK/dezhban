#!/usr/bin/env bash
# Build the DezhbanMenu executable with SwiftPM and assemble it into a
# self-contained Dezhban.app bundle under dist/. macOS only; needs the Swift
# toolchain (Command Line Tools are enough — no full Xcode required).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"   # gui/macos
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
OUT_DIR="${1:-$REPO_ROOT/dist}"
APP="$OUT_DIR/Dezhban.app"
CONFIG="${CONFIG:-release}"

# The .pkg ships a universal (arm64 + x86_64) app — one installer has to run on
# both Apple Silicon and Intel. Set DEZHBAN_APP_UNIVERSAL=1 for that; plain dev
# builds stay single-arch (much faster).
#
# Each slice is built separately and lipo'd together, rather than passing both
# --arch flags to one `swift build`: the multi-arch form needs xcbuild from a full
# Xcode, and this project builds with the Command Line Tools alone. lipo ships with
# the CLT, so this keeps that promise.
build_slice() {
	swift build --package-path "$HERE" -c "$CONFIG" --arch "$1" >&2
	echo "$(swift build --package-path "$HERE" -c "$CONFIG" --arch "$1" --show-bin-path)/DezhbanMenu"
}

BUILT=""       # temp universal binary, cleaned up on exit
ICONSET_DIR="" # temp iconset for icns generation, cleaned up on exit
# `return 0` is load-bearing: under `set -e`, a trap whose last command reports
# failure (which `[[ -n "" ]]` does on the non-universal path) can take the whole
# script's exit status down with it.
cleanup() {
	[[ -n "$BUILT" ]] && rm -f "$BUILT"
	[[ -n "$ICONSET_DIR" ]] && rm -rf "$ICONSET_DIR"
	return 0
}
trap cleanup EXIT

if [[ "${DEZHBAN_APP_UNIVERSAL:-}" == "1" ]]; then
	echo "==> swift build ($CONFIG, universal: arm64 + x86_64)"
	ARM_BIN="$(build_slice arm64)"
	X86_BIN="$(build_slice x86_64)"
	BUILT="$(mktemp -t DezhbanMenu)"
	lipo -create -output "$BUILT" "$ARM_BIN" "$X86_BIN"
	BIN="$BUILT"
else
	echo "==> swift build ($CONFIG)"
	swift build --package-path "$HERE" -c "$CONFIG"
	BIN="$(swift build --package-path "$HERE" -c "$CONFIG" --show-bin-path)/DezhbanMenu"
fi

if [[ ! -x "$BIN" ]]; then
	echo "error: built binary not found at $BIN" >&2
	exit 1
fi

echo "==> assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/DezhbanMenu"
cp "$HERE/Info.plist" "$APP/Contents/Info.plist"
# SUDO_ASKPASS helper. Lives in the (code-signed, read-only) bundle so sudo is
# never pointed at a path a local process could swap out from under it.
install -m 0755 "$HERE/askpass.sh" "$APP/Contents/Resources/askpass.sh"

# The license travels INSIDE the bundle, not beside it. Dezhban.app is published
# as a standalone release asset (Dezhban-macos.app.zip), and `ditto -xk` unpacks
# that archive straight into /Applications — so anything added at the archive's
# top level would land as /Applications/LICENSE, and anything left out of the
# bundle would mean that download hands somebody the Software with no License at
# all. Hippocratic 3.0 Core section 5.1 requires the opposite, and 7.2 ends the
# grant if it is not cured within 30 days of notice. Staged here, before the
# ad-hoc codesign below, so the seal covers it.
install -m 0644 "$REPO_ROOT/LICENSE" "$APP/Contents/Resources/LICENSE"

# Login-item LaunchAgent. SMAppService.agent(plistName:) reads it from exactly
# this directory, and it is the only thing that tells a login launch apart from
# a user launch: it passes --background, which LaunchVisibility keys on. A
# bundle assembled without it registers nothing and the app silently stops
# starting at login, so its absence is a build failure rather than a note.
# See docs/adr/0014-login-item-launch-marker.md.
AGENT_LABEL="com.behnam-rk.dezhban.app.login"
AGENT_PLIST="$APP/Contents/Library/LaunchAgents/$AGENT_LABEL.plist"
mkdir -p "$APP/Contents/Library/LaunchAgents"
install -m 0644 "$HERE/LoginAgent.plist" "$AGENT_PLIST"

# The two facts that make the feature work, asserted rather than commented.
# `install` under `set -e` already aborts if the file does not land, so its
# existence needs no test — but deleting --background from LoginAgent.plist, or
# renaming its Label, passes go test, swift test and this build while silently
# restoring the original bug (or killing login-at-launch outright, reported only
# as an SMAppService status nobody reads).
#
# 1. Label must equal the filename SMAppService.agent(plistName:) is given
#    (LoginItem.plistName) — launchd rejects a mismatch.
plist_label="$(plutil -extract Label raw -o - "$AGENT_PLIST")"
if [[ "$plist_label" != "$AGENT_LABEL" ]]; then
	echo "build-app.sh: LoginAgent.plist Label is '$plist_label', but it is installed as '$AGENT_LABEL.plist' — launchd would reject the job and the app would not start at login" >&2
	exit 1
fi
# 2. ProgramArguments must still carry the launch marker LaunchVisibility keys on
#    (LaunchVisibility.backgroundArgument), or every login looks like a user
#    launch and "Open minimized" silently stops working.
agent_argc="$(plutil -extract ProgramArguments raw -o - "$AGENT_PLIST")"
agent_marker=0
for ((i = 0; i < agent_argc; i++)); do
	if [[ "$(plutil -extract "ProgramArguments.$i" raw -o - "$AGENT_PLIST")" == "--background" ]]; then
		agent_marker=1
	fi
done
if [[ "$agent_marker" -ne 1 ]]; then
	echo "build-app.sh: LoginAgent.plist ProgramArguments no longer pass --background — a login launch would be indistinguishable from a user launch" >&2
	exit 1
fi
# 3. The label must be spelled the same in the three places that must agree:
#    the plist, LoginItem.plistName (what SMAppService.agent is given), and
#    uninstall.sh (what retracts it). Renaming it consistently in the plist AND
#    here would otherwise satisfy check 1 while SMAppService named a file that
#    does not exist — reported only as the .notFound status nobody reads.
for consumer in "$HERE/Sources/DezhbanMenu/LoginItem.swift" "$REPO_ROOT/packaging/macos/uninstall.sh"; do
	# -F: a fixed string, not a regex. Unanchored, `.` is a wildcard, so a label
	# renamed to "…app-login" in one consumer still matched the pattern "…app.login"
	# — the drift check passing over exactly the drift it exists to catch, leaving
	# SMAppService naming a plist that does not exist.
	if ! grep -qF "$AGENT_LABEL" "$consumer"; then
		echo "build-app.sh: $consumer does not mention '$AGENT_LABEL' — the label, LoginItem.plistName and the uninstaller have drifted apart, and login-at-launch would fail silently" >&2
		exit 1
	fi
done

# Documentation, rendered from the repo's own markdown into the bundle. Shipping
# it means the help pane works with every byte of egress cut — which is exactly
# when someone needs it — and that the docs always match the version they
# document. tools/helpgen is dev tooling (stdlib only, never installed); a
# missing or renamed page is an error there, not a silently thinner bundle.
if ! (cd "$REPO_ROOT" && go run ./tools/helpgen -docs docs -out "$APP/Contents/Resources/help"); then
	echo "build-app.sh: helpgen failed — the app would ship without its documentation" >&2
	exit 1
fi
# Brand artifacts (gui/artifacts/png): full-color menubar + Dock state
# icons. Optional — a checkout without gui/artifacts/ still builds, and AppDelegate
# falls back to SF Symbols / the static app icon when the PNGs are absent.
ASSETS="$REPO_ROOT/gui/artifacts/png"
if [[ -d "$ASSETS" ]]; then
	for state in on off blocked warning paused; do
		# Menubar: the asset pack ships dedicated colored menubar glyphs
		# (88px tall = 22pt @4x; AppDelegate scales to a 22pt pointing height,
		# preserving aspect). Older packs without them fall back to downscaling
		# the 512px state tile.
		if [[ -f "$ASSETS/menubar-$state-color-88px.png" ]]; then
			cp "$ASSETS/menubar-$state-color-88px.png" \
				"$APP/Contents/Resources/menubar-state-$state.png"
		elif [[ -f "$ASSETS/icon-$state-512.png" ]]; then
			sips -Z 44 "$ASSETS/icon-$state-512.png" \
				--out "$APP/Contents/Resources/menubar-state-$state.png" >/dev/null
		fi
		# Window hero: the full five-state tile set, deliberately NOT subject to
		# the Dock's coarsening below. The Overview's hero answers "what is the
		# guard doing right now?" in the brand's own state artwork, so off /
		# warning / paused each need their own file. Before this, three of the
		# five resolved to no file at all and the hero silently fell back to a
		# generic SF Symbol shield, which is not a dezhban artifact.
		if [[ -f "$ASSETS/icon-$state-512.png" ]]; then
			cp "$ASSETS/icon-$state-512.png" "$APP/Contents/Resources/state-tile-$state.png"
		fi
		# Dock tile: PostureUI.dockState coarsens every state down to "blocked" or
		# "on", so only those two are ever requested. "blocked" is the state tile,
		# because a cut is the one thing the Dock has to shout about. "on" is the
		# brand app icon rather than a state tile: the Dock answers "is dezhban
		# cutting my traffic right now?", and everything that is not a cut should
		# look like the app, not like a status light.
		if [[ "$state" == "blocked" && -f "$ASSETS/icon-blocked-512.png" ]]; then
			cp "$ASSETS/icon-blocked-512.png" "$APP/Contents/Resources/dock-state-blocked.png"
		fi
		if [[ "$state" == "on" && -f "$ASSETS/app-icon-1024.png" ]]; then
			sips -Z 512 "$ASSETS/app-icon-1024.png" \
				--out "$APP/Contents/Resources/dock-state-on.png" >/dev/null
		fi
	done
	# A missing hero tile is otherwise invisible until someone actually reaches
	# that state in a shipped build — which is exactly how the generic shield
	# shipped. A note, not a failure: the block above is explicitly optional.
	for state in on off blocked warning paused; do
		[[ -f "$APP/Contents/Resources/state-tile-$state.png" ]] \
			|| echo "build-app.sh: note: no state-tile-$state.png — the Overview hero will fall back to an SF Symbol for '$state'" >&2
	done
fi

# App icon: a hand-dropped gui/macos/AppIcon.icns wins; otherwise one is
# generated from the 1024px brand master so Finder / the installer / the Dock's
# default tile all show the brand icon.
if [[ -f "$HERE/AppIcon.icns" ]]; then
	cp "$HERE/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
elif [[ -f "$ASSETS/app-icon-1024.png" ]]; then
	ICONSET_DIR="$(mktemp -d -t AppIconset)"
	ICONSET="$ICONSET_DIR/AppIcon.iconset"
	mkdir -p "$ICONSET"
	for sz in 16 32 128 256 512; do
		sips -Z "$sz" "$ASSETS/app-icon-1024.png" --out "$ICONSET/icon_${sz}x${sz}.png" >/dev/null
		sips -Z "$((sz * 2))" "$ASSETS/app-icon-1024.png" --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
	done
	iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
fi
if [[ -f "$APP/Contents/Resources/AppIcon.icns" ]]; then
	/usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string AppIcon" \
		"$APP/Contents/Info.plist" 2>/dev/null || true
fi
# Stamped by `task gui:build` (DEZHBAN_VERSION from `git describe` or an explicit
# VERSION=vX.Y.Z). CFBundle version fields must be dotted numerics, so only a
# release version is stamped: X.Y.Z, or an rc reduced to its numeric core
# (0.2.0-rc.1 -> 0.2.0). A `git describe` value like 0.2.0-3-g<sha> or `dev` is a
# dev build and is left at Info.plist's checked-in 0.0.0, so it is visibly
# unstamped rather than masquerading as a release.
if [[ -n "${DEZHBAN_VERSION:-}" ]]; then
	ver="${DEZHBAN_VERSION#v}"
	ver="${ver%-rc.*}"
	if [[ "$ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $ver" \
			"$APP/Contents/Info.plist"
		/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $ver" \
			"$APP/Contents/Info.plist"
	else
		echo "    note: DEZHBAN_VERSION='$DEZHBAN_VERSION' is not a release version; leaving Info.plist at its dev default" >&2
	fi
fi

# Ad-hoc sign, LAST — after lipo and the PlistBuddy edits above, either of which
# invalidates a prior signature. Not a Gatekeeper measure (there is no Developer
# ID here — see packaging/macos/build-pkg.sh's INSTALLER_SIGN_IDENTITY /
# NOTARIZE_PROFILE seam for that, still dormant): Apple Silicon's kernel refuses
# to execute an unsigned arm64 binary at all, ad-hoc or not. Go's linker already
# ad-hoc-signs the CLI's own output; the assembled .app bundle needed the same
# for its seal to actually match its contents rather than by accident.
echo "==> codesign (ad-hoc)"
codesign -s - --force --deep "$APP"

echo "==> built $APP"
echo "    open it with:  open \"$APP\""
