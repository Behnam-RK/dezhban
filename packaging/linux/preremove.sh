#!/bin/sh
# Runs BEFORE the .deb/.rpm payload is deleted (dpkg prerm / rpm %preun) — this
# has to be preremove, not postremove: by the time postrm/%postun runs, dpkg/rpm
# have already deleted /usr/local/bin/dezhban, so there is nothing left to call
# panic/stop/uninstall on.
#
# Same ordering rationale as packaging/macos/uninstall.sh: panic first
# (force-removes firewall rules even with no daemon running — a kill switch
# half-removed while its block-all rule is still loaded is a locked-out
# machine), then unregister the service. Every step is non-fatal (|| true): a
# service that is already stopped or never registered must not abort the
# removal.
#
# /etc/dezhban and /var/db/dezhban are never touched here — this package's file
# list never claimed them, so dpkg/rpm were never going to remove them either.
# Config survives a plain removal by default, matching the curl-install
# uninstaller's KEEP_CONFIG default.
set -u

# Only on a REAL removal. Both package managers run this scriptlet during an
# UPGRADE too (dpkg prerm gets "upgrade <ver>"; rpm %preun gets "1", the number
# of instances that will remain), and tearing enforcement down there would be
# the worst possible behaviour for a kill switch: `apt upgrade dezhban` would
# silently panic-flush the rules, stop the daemon, and unregister the service —
# and postinstall.sh deliberately never starts anything, so the host would come
# out of a routine upgrade unprotected, with nothing saying so. On an upgrade
# the new payload simply replaces the binary underneath the running daemon,
# which is exactly the zero-gap swap `dezhban upgrade` relies on too.
case "${1:-}" in
	remove|purge|0) ;;      # dpkg removal / rpm final erase — go ahead
	*) exit 0 ;;            # upgrade, deconfigure, failed-upgrade — leave it armed
esac

BIN=/usr/local/bin/dezhban

if [ -x "$BIN" ]; then
	"$BIN" --no-sudo panic || true
	"$BIN" --no-sudo stop || true
	"$BIN" --no-sudo uninstall || true
fi

exit 0
