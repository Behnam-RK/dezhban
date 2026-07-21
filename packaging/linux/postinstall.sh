#!/bin/sh
# Runs after the .deb/.rpm payload lands (dpkg postinst / rpm %post).
#
# Registers the systemd service but deliberately does NOT start enforcement —
# same reasoning as packaging/macos/scripts/postinstall: a kill switch must not
# arm itself against a config the user has not chosen yet. `dezhban setup` (or
# the menubar app, macOS-only) is the explicit configuration step; starting it
# is another, separate step.
#
# No systemd unit file ships in this package on purpose: `dezhban install`
# renders it (via kardianos/service), the same as it renders the launchd plist
# on macOS. One code path owns that shape everywhere instead of a shipped unit
# file that could drift from what internal/svc/program.go actually configures.
set -u

BIN=/usr/local/bin/dezhban
CONFIG_DIR=/etc/dezhban
CONFIG="$CONFIG_DIR/dezhban.json"

mkdir -p "$CONFIG_DIR"

"$BIN" --no-sudo install --config "$CONFIG" || {
	echo "dezhban: could not register the systemd service; run 'sudo dezhban install' to retry" >&2
}

# Never fail the package install for a recoverable condition — the binary is on
# disk and the step above is re-runnable by hand.
exit 0
