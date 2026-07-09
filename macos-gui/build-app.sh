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

echo "==> swift build ($CONFIG)"
swift build --package-path "$HERE" -c "$CONFIG"

BIN="$(swift build --package-path "$HERE" -c "$CONFIG" --show-bin-path)/DezhbanMenu"
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
# Stamped by `make gui-macos` (DEZHBAN_VERSION=$(VERSION), from `git describe`
# or an explicit VERSION=vX.Y.Z). Only a strict X.Y.Z is stamped — CFBundle
# version fields must be dotted numerics, so a `git describe` value like
# 0.2.0-3-g<sha> or `dev` is left alone, keeping Info.plist's checked-in 0.1.0.
if [[ -n "${DEZHBAN_VERSION:-}" ]]; then
	ver="${DEZHBAN_VERSION#v}"
	if [[ "$ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $ver" \
			"$APP/Contents/Info.plist"
		/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $ver" \
			"$APP/Contents/Info.plist"
	else
		echo "    note: DEZHBAN_VERSION='$DEZHBAN_VERSION' is not X.Y.Z; leaving Info.plist version unchanged" >&2
	fi
fi

echo "==> built $APP"
echo "    open it with:  open \"$APP\""
