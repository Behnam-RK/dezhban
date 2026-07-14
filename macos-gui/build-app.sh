#!/usr/bin/env bash
# Build the DezhbanMenu executable with SwiftPM and assemble it into a
# self-contained Dezhban.app bundle under dist/. macOS only; needs the Swift
# toolchain (Command Line Tools are enough — no full Xcode required).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
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

BUILT="" # temp universal binary, cleaned up on exit
# `return 0` is load-bearing: under `set -e`, a trap whose last command reports
# failure (which `[[ -n "" ]]` does on the non-universal path) can take the whole
# script's exit status down with it.
cleanup() {
	[[ -n "$BUILT" ]] && rm -f "$BUILT"
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
# Optional icon: drop AppIcon.icns in macos-gui/ to have it bundled.
if [[ -f "$HERE/AppIcon.icns" ]]; then
	cp "$HERE/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
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

echo "==> built $APP"
echo "    open it with:  open \"$APP\""
