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

BIN=/usr/local/bin/dezhban

if [ -x "$BIN" ]; then
	"$BIN" --no-sudo panic || true
	"$BIN" --no-sudo stop || true
	"$BIN" --no-sudo uninstall || true
fi

exit 0
