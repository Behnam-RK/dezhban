#!/bin/sh
# Removes everything the dezhban .pkg installed. Shipped inside the payload,
# because a .pkg has no native uninstaller.
#
#   sudo sh /usr/local/share/dezhban/uninstall.sh
#   sudo KEEP_CONFIG=1 sh /usr/local/share/dezhban/uninstall.sh   # keep /etc/dezhban
#
# Ordering is the whole point of this script. `panic` runs FIRST and force-removes
# the firewall rules even with no daemon running: a kill switch that is half-removed
# while its block-all rule is still loaded is a locked-out machine. Only once
# connectivity is guaranteed do we unregister the service and delete files. Every
# teardown step is non-fatal (|| true) — a service that is already gone, or was never
# cleanly loaded (launchctl unload can fail with I/O error 5 on modern macOS), must
# not abort the removal and strand the rest.
set -u

BIN=/usr/local/bin/dezhban
APP=/Applications/Dezhban.app
CONFIG_DIR=/etc/dezhban
STATE_DIR=/var/db/dezhban
PLIST=/Library/LaunchDaemons/dezhban.plist
SHARE_DIR=/usr/local/share/dezhban

if [ "$(id -u)" -ne 0 ]; then
	echo "error: run as root — sudo sh $0" >&2
	exit 1
fi

if [ -x "$BIN" ]; then
	echo "removing firewall rules (panic teardown) ..."
	"$BIN" --no-sudo panic || true

	echo "stopping and unregistering the service ..."
	"$BIN" --no-sudo stop || true
	"$BIN" --no-sudo uninstall || true
else
	echo "note: $BIN is already gone; skipping rule teardown" >&2
	echo "      if the network is still cut, reinstall and run 'sudo dezhban panic'" >&2
fi

# Belt-and-suspenders: drop a plist launchd's unload may have left behind.
rm -f "$PLIST" 2>/dev/null || true

echo "removing the menubar app ..."
# The app may be running (it's a login item). Ask it to quit so we don't delete the
# bundle out from under a live process.
osascript -e 'tell application "Dezhban" to quit' >/dev/null 2>&1 || true
pkill -x DezhbanMenu >/dev/null 2>&1 || true
rm -rf "$APP"

# The daemon's own directory: state.json, learned.json, the command file and the
# control socket. All machine-derived and safe to discard — none of it is the user's.
echo "removing daemon state at $STATE_DIR ..."
rm -rf "$STATE_DIR"

if [ "${KEEP_CONFIG:-0}" = "1" ]; then
	echo "keeping config at $CONFIG_DIR (KEEP_CONFIG=1)"
else
	echo "removing config at $CONFIG_DIR ..."
	rm -rf "$CONFIG_DIR"
fi

# Forget the receipts, or macOS still believes dezhban is installed (and a later
# install of an older version would be refused as a downgrade).
pkgutil --forget com.behnam-rk.dezhban.cli >/dev/null 2>&1 || true
pkgutil --forget com.behnam-rk.dezhban.app >/dev/null 2>&1 || true

# The binary and this script go LAST: everything above needs the binary, and `sh`
# has already read this file, so deleting it mid-run is safe.
echo "removing the CLI ..."
rm -f "$BIN"
rm -rf "$SHARE_DIR"

echo
echo "dezhban uninstalled — rules removed, service unregistered, files deleted."
