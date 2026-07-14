#!/bin/sh
# uninstall-local.sh — completely remove a local dezhban install: tear down its
# firewall rules, unregister the OS service, and delete the config it installed
# to the system path. Requires sudo.
#
# Built to survive a half-registered or already-gone service. On modern macOS the
# service manager's legacy `launchctl unload` can fail with "Input/output error"
# (5) or "No such process" when the daemon isn't cleanly loaded, which would abort
# an `set -e` script mid-teardown. So we:
#   1. always run `panic` first to force-remove the firewall rules even if `stop`
#      can't signal the daemon — connectivity is restored before anything else;
#   2. treat stop/unregister failures as non-fatal (|| true);
#   3. remove a leftover launchd plist the service manager may not have cleaned up.
#
# The installed system config (/etc/dezhban) is removed by default. Pass
# KEEP_CONFIG=1 to leave it in place.
set -eu
cd "$(dirname "$0")/.."

SYS_CONFIG_DIR=/etc/dezhban

go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o dezhban ./cmd/dezhban

echo "removing dezhban firewall rules (panic teardown) ..."
# panic force-removes the dezhban-tagged rules even with no daemon running — the
# reliable teardown when `stop` can't unload the service (macOS I/O error 5).
sudo ./dezhban panic || true

echo "stopping and unregistering the service ..."
sudo ./dezhban stop || true
sudo ./dezhban uninstall || true

# Belt-and-suspenders: drop a launchd plist the manager may have left behind when
# unload failed (macOS only; the path simply doesn't exist elsewhere).
sudo rm -f /Library/LaunchDaemons/dezhban.plist 2>/dev/null || true

if [ "${KEEP_CONFIG:-0}" = "1" ]; then
	echo "keeping system config at $SYS_CONFIG_DIR (KEEP_CONFIG=1)"
else
	echo "removing system config at $SYS_CONFIG_DIR ..."
	sudo rm -rf "$SYS_CONFIG_DIR"
fi

echo "dezhban uninstalled — rules removed, service unregistered."

# A copy installed via `go install` lives on $PATH and shadows this build; bare
# `dezhban ...` would keep running that older binary. Point it out, don't delete
# it (the user may have installed it deliberately).
gobin="$(go env GOBIN 2>/dev/null)"
[ -n "$gobin" ] || gobin="$(go env GOPATH 2>/dev/null)/bin"
if [ -x "$gobin/dezhban" ]; then
	echo "note: a 'go install' copy still on your PATH at $gobin/dezhban"
	echo "      remove it too with: rm -f \"$gobin/dezhban\""
fi
