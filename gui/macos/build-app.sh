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
# Brand assets (gui/assets/png): full-color menubar + Dock state
# icons. Optional — a checkout without gui/assets/ still builds, and AppDelegate
# falls back to SF Symbols / the static app icon when the PNGs are absent.
ASSETS="$REPO_ROOT/gui/assets/png"
if [[ -d "$ASSETS" ]]; then
	for state in on off blocked warning; do
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
		# Dock tile: the 512px state tiles.
		if [[ -f "$ASSETS/icon-$state-512.png" ]]; then
			cp "$ASSETS/icon-$state-512.png" "$APP/Contents/Resources/dock-state-$state.png"
		fi
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
