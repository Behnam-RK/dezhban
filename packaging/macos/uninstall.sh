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
LOGIN_AGENT=com.behnam-rk.dezhban.app.login
APP_BUNDLE_ID=com.behnam-rk.dezhban.app
LOGIN_ITEM_STUCK=0

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

# The login item is a per-user launchd agent (SMAppService.agent), NOT a
# LaunchServices entry: deleting the bundle does not retract it. Left registered it
# fails to load at every subsequent login and lingers in System Settings → General
# → Login Items as an orphan the user has to hunt down.
#
# Only SMAppService.unregister() actually retracts the registration, and only the
# app can call it — `launchctl bootout` unloads the job for THIS boot and leaves
# the record that recreates it at the next login, pointing at a plist inside the
# bundle we are about to delete. So the app is run one last time, as the console
# user inside their GUI session, purely to retract itself. bootout follows as a
# belt-and-braces unload of the job it just retracted.
#
# All of it needs the user's own launchd session, so root can only reach the
# console user; other accounts get the one command they need, printed at the end.
CONSOLE_USER=$(stat -f %Su /dev/console 2>/dev/null || echo "")
CONSOLE_UID=""
if [ -n "$CONSOLE_USER" ] && [ "$CONSOLE_USER" != "root" ]; then
	CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || echo "")
fi
if [ -n "$CONSOLE_UID" ]; then
	echo "unregistering the login agent for $CONSOLE_USER ..."
	if [ -x "$APP/Contents/MacOS/DezhbanMenu" ]; then
		# Bounded. The errand talks to launchd over XPC, and this runs before
		# `rm -rf "$APP"` — an uninstaller that hangs here, silently (output is
		# discarded), leaves the machine mid-removal with no message on screen.
		#
		# Completion is signalled by a file rather than by watching the child:
		# an exited-but-unreaped child is a zombie, which `kill -0` still reports
		# as alive, so polling the pid would wait out the whole timeout on a
		# perfectly successful run. And no `( sleep N; kill ) &` watchdog — that
		# leaks its `sleep` past the end of the script and, if it ever fires late,
		# aims a `kill -9` at a pid the system may have recycled.
		errand_done="${TMPDIR:-/tmp}/dezhban-uninstall-errand.$$"
		rm -f "$errand_done"
		(
			if launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" \
				"$APP/Contents/MacOS/DezhbanMenu" --unregister-login-item >/dev/null 2>&1; then
				echo ok >"$errand_done"
			else
				echo failed >"$errand_done"
			fi
		) &
		errand=$!
		waited=0
		while [ ! -f "$errand_done" ] && [ "$waited" -lt 150 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		if [ ! -f "$errand_done" ]; then
			echo "note: retracting the login item did not finish in 15s; continuing" >&2
			# The subshell AND what it started. Killing only the subshell leaves the
			# DezhbanMenu it launched running, and the very next statement deletes
			# the bundle out from under it — the thing the `pkill` above exists to
			# avoid, reintroduced on the one path this timeout is here for.
			kill -9 "$errand" >/dev/null 2>&1 || true
			pkill -x DezhbanMenu >/dev/null 2>&1 || true
		fi
		# The status matters. The app only logs a refused unregister, and this
		# script discards its output — so without checking, a login item macOS
		# would not retract stayed behind, pointing at a bundle deleted two lines
		# later and unreachable from then on, while the closing message claimed
		# everything was removed.
		if [ "$(cat "$errand_done" 2>/dev/null)" = "failed" ]; then
			LOGIN_ITEM_STUCK=1
		fi
		rm -f "$errand_done"
		wait "$errand" >/dev/null 2>&1 || true
	fi
	launchctl bootout "gui/$CONSOLE_UID/$LOGIN_AGENT" >/dev/null 2>&1 || true
	# The app's own per-user directory: the instance lock the GUI takes at startup
	# to keep a second copy of itself from running, and the hand-off file beside
	# it. Machine-derived, none of it the user's — and files this version creates
	# that no earlier one did, so leaving them would make this script's own
	# promise false.
	#
	# The home directory is asked for, not assumed: a network or mobile account,
	# or a relocated home, is not under /Users, and hardcoding that path made the
	# closing "files deleted" line untrue for exactly those users.
	CONSOLE_HOME=$(dscl . -read "/Users/$CONSOLE_USER" NFSHomeDirectory 2>/dev/null |
		sed -n 's/^NFSHomeDirectory: //p')
	if [ -n "$CONSOLE_HOME" ] && [ -d "$CONSOLE_HOME" ]; then
		rm -rf "$CONSOLE_HOME/Library/Application Support/$APP_BUNDLE_ID"
	fi
	# The app's preferences, for the same reason. These are not cosmetic: the
	# migration that moves an old LaunchServices login item onto the login agent
	# records that it has run, so a surviving flag means a LATER install is never
	# migrated — silently restoring the "Open minimized" bug the agent exists to
	# fix. Written by the user's own cfprefsd, so it is deleted through defaults as
	# that user rather than by unlinking the plist under them.
	launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" \
		defaults delete "$APP_BUNDLE_ID" >/dev/null 2>&1 || true
fi

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
if [ "$LOGIN_ITEM_STUCK" = "1" ]; then
	echo
	echo "warning: macOS would not retract the login item. Nothing will start"
	echo "         Dezhban (the app is gone), but the entry remains — remove"
	echo "         \"Dezhban\" under System Settings > General > Login Items."
fi
echo
echo "If any OTHER account on this Mac ran the app, its login agent is still"
echo "registered there — root cannot reach another user's launchd session. Nothing"
echo "will start Dezhban (the bundle is gone), but the entry lingers under System"
echo "Settings → General → Login Items until that user removes it there."
