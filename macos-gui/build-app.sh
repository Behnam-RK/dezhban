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

echo "==> built $APP"
echo "    open it with:  open \"$APP\""
