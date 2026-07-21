#!/bin/sh
# Removes everything scripts/install.sh installed on Linux. NOT used by a
# .deb/.rpm — a system package's own postrm handles that removal through the
# package manager instead. This is for the curl-installed path only.
#
# See packaging/macos/uninstall.sh for the macOS counterpart: same ordering
# rationale, different platform specifics (no app bundle, no pkgutil receipts,
# no osascript — the service here is whatever kardianos/service registered,
# systemd on a normal distro, and `dezhban uninstall` is what unregisters it,
# same as macOS never hand-touches the launchd plist).
#
#   sudo sh /usr/local/share/dezhban/uninstall.sh
#   sudo KEEP_CONFIG=1 sh /usr/local/share/dezhban/uninstall.sh   # keep /etc/dezhban
#
# Ordering is the whole point of this script. `panic` runs FIRST and force-removes
# the firewall rules even with no daemon running: a kill switch that is half-removed
# while its block-all rule is still loaded is a locked-out machine. Only once
# connectivity is guaranteed do we unregister the service and delete files. Every
# teardown step is non-fatal (|| true) — a service that is already gone must not
# abort the removal and strand the rest.
set -u

BIN=/usr/local/bin/dezhban
CONFIG_DIR=/etc/dezhban
STATE_DIR=/var/db/dezhban
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

# The binary and this script go LAST: everything above needs the binary, and `sh`
# has already read this file, so deleting it mid-run is safe.
echo "removing the CLI ..."
rm -f "$BIN"
rm -rf "$SHARE_DIR"

echo
echo "dezhban uninstalled — rules removed, service unregistered, files deleted."
