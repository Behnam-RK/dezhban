#!/usr/bin/env bash
# Build the standalone macOS installer: dist/dezhban-<version>.pkg
#
# The .pkg installs everything dezhban needs in one double-click, with exactly one
# admin prompt — the Installer's own. Afterwards, routine operations (block,
# unblock, switch) need no password at all: they go to the daemon over its control
# socket. See docs/architecture.md.
#
# Payload:
#   /usr/local/bin/dezhban                       universal CLI (arm64 + x86_64)
#   /usr/local/share/dezhban/uninstall.sh        the uninstaller (no native pkg one)
#   /Applications/Dezhban.app                    menubar app (universal)
#   postinstall: registers the LaunchDaemon, but does NOT start enforcement.
#
# Unsigned: there is no Apple Developer certificate in this project. Set
# INSTALLER_SIGN_IDENTITY (and NOTARIZE_PROFILE) to sign/notarize once one exists —
# the seams are here, so that becomes a two-variable change, not a rewrite.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
OUT_DIR="${1:-$REPO_ROOT/dist}"
VERSION="${VERSION:-$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"

# pkg version fields must be dotted numerics; a `git describe` like 0.2.0-3-gabc or
# "dev" is not. Fall back to 0.0.0 for the metadata while keeping the real version
# in the filename, so a dev build is obviously a dev build and never masquerades as
# a release in the receipt database.
PKG_VERSION="${VERSION#v}"
if ! [[ "$PKG_VERSION" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
	echo "    note: version '$VERSION' is not dotted-numeric; using 0.0.0 in pkg metadata" >&2
	PKG_VERSION="0.0.0"
fi

CLI_ID="com.behnam-rk.dezhban.cli"
APP_ID="com.behnam-rk.dezhban.app"
BUILD="$OUT_DIR/pkgbuild"
STAGE="$BUILD/root"
PKG="$OUT_DIR/dezhban-${VERSION#v}.pkg"

CLI_ARM="$OUT_DIR/dezhban-darwin-arm64"
CLI_X86="$OUT_DIR/dezhban-darwin-amd64"
APP="$OUT_DIR/Dezhban.app"

for f in "$CLI_ARM" "$CLI_X86"; do
	[[ -f "$f" ]] || { echo "error: missing $f — run: task build:all VERSION=$VERSION" >&2; exit 1; }
done
[[ -d "$APP" ]] || { echo "error: missing $APP — run: task gui:build UNIVERSAL=1 VERSION=$VERSION" >&2; exit 1; }

echo "==> staging payload ($VERSION)"
rm -rf "$BUILD"
mkdir -p "$STAGE/usr/local/bin" "$STAGE/usr/local/share/dezhban"

# One binary for both architectures: the installer can't know which Mac it lands on.
lipo -create -output "$STAGE/usr/local/bin/dezhban" "$CLI_ARM" "$CLI_X86"
chmod 0755 "$STAGE/usr/local/bin/dezhban"

install -m 0755 "$HERE/uninstall.sh" "$STAGE/usr/local/share/dezhban/uninstall.sh"

SIGN_ARGS=()
if [[ -n "${INSTALLER_SIGN_IDENTITY:-}" ]]; then
	SIGN_ARGS=(--sign "$INSTALLER_SIGN_IDENTITY")
	echo "==> signing as: $INSTALLER_SIGN_IDENTITY"
fi

echo "==> building component packages"
# The postinstall rides the CLI component: it invokes the freshly-installed binary,
# and component scripts run after that component's payload lands.
pkgbuild \
	--root "$STAGE" \
	--identifier "$CLI_ID" \
	--version "$PKG_VERSION" \
	--install-location / \
	--scripts "$HERE/scripts" \
	"$BUILD/dezhban-cli.pkg" >/dev/null

# The app gets its own payload root (--component-plist is only accepted alongside
# --root). BundleIsRelocatable=false in that plist is load-bearing: without it the
# Installer would "update" any stray copy of Dezhban.app it finds elsewhere on the
# disk instead of installing to /Applications.
APP_ROOT="$BUILD/approot"
mkdir -p "$APP_ROOT"
cp -R "$APP" "$APP_ROOT/Dezhban.app"

pkgbuild \
	--root "$APP_ROOT" \
	--component-plist "$HERE/app-component.plist" \
	--identifier "$APP_ID" \
	--version "$PKG_VERSION" \
	--install-location /Applications \
	"$BUILD/dezhban-app.pkg" >/dev/null

echo "==> building product archive"
mkdir -p "$OUT_DIR"
sed "s/@VERSION@/$PKG_VERSION/g" "$HERE/distribution.xml" > "$BUILD/distribution.xml"
# "${arr[@]+...}" guards the expansion: under `set -u`, bash 3.2 (what /bin/sh is on
# macOS) errors on an empty array's [@] rather than expanding to nothing.
productbuild \
	--distribution "$BUILD/distribution.xml" \
	--package-path "$BUILD" \
	--resources "$HERE/resources" \
	${SIGN_ARGS[@]+"${SIGN_ARGS[@]}"} \
	"$PKG" >/dev/null

# Notarization needs a signed package, so it is gated on both being configured.
if [[ -n "${NOTARIZE_PROFILE:-}" && -n "${INSTALLER_SIGN_IDENTITY:-}" ]]; then
	echo "==> notarizing (keychain profile: $NOTARIZE_PROFILE)"
	xcrun notarytool submit "$PKG" --keychain-profile "$NOTARIZE_PROFILE" --wait
	xcrun stapler staple "$PKG"
fi

rm -rf "$BUILD"
echo "==> built $PKG"
if [[ -z "${INSTALLER_SIGN_IDENTITY:-}" ]]; then
	echo "    UNSIGNED — Gatekeeper will block a double-click. Install with:"
	echo "      sudo installer -pkg \"$PKG\" -target /"
	echo "    (or right-click → Open; on macOS 15+, System Settings → Privacy & Security → Open Anyway)"
fi
