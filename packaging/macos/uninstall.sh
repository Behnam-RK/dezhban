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
LOGIN_ITEM_STUCK=none
# Separate from LOGIN_ITEM_STUCK, which holds one value: the two conditions are
# independent and both have to be reportable at once.
SUPPORT_DIR_KEPT=0

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
CONSOLE_HOME=""
if [ -n "$CONSOLE_USER" ] && [ "$CONSOLE_USER" != "root" ]; then
	CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || echo "")
	if [ -z "$CONSOLE_UID" ]; then
		# Somebody IS at this Mac; their uid just could not be looked up (an
		# unreachable network/AD directory, a damaged local record). Telling them
		# "nobody is logged in — re-run from a graphical session" is both false and
		# useless advice, since re-running fails the same way.
		LOGIN_ITEM_STUCK=no-console-uid
	fi
	# /Search, not the local node. `dscl .` reads only local records, so for a
	# network/LDAP/AD account it returns nothing — leaving the cleanup below to be
	# skipped for precisely the accounts this lookup exists to serve.
	#
	# And read as a plist, not scraped with sed. `dscl`'s plain output puts a value
	# containing a space on a *continuation* line ("NFSHomeDirectory:\n /Volumes/Home
	# Dirs/jsmith"), so the obvious `s/^NFSHomeDirectory: //p` came back empty — for
	# a home with a space in it, which is what network and relocated homes tend to
	# have.
	CONSOLE_HOME=$(dscl -plist /Search -read "/Users/$CONSOLE_USER" NFSHomeDirectory 2>/dev/null |
		plutil -extract 'dsAttrTypeStandard:NFSHomeDirectory.0' raw -o - -- - 2>/dev/null)
	# The lookup is best-effort — a directory record without NFSHomeDirectory, or any
	# change in dscl/plutil output shape, yields nothing — and every consumer below
	# degrades quietly: the ~/Applications half of the bundle search is skipped, and
	# so is the per-user cleanup, under a closing message that says "files deleted".
	# The conventional path is a better guess than no guess, and if even that is not
	# there the script says so rather than reporting a clean removal.
	if [ -z "$CONSOLE_HOME" ] && [ -d "/Users/$CONSOLE_USER" ]; then
		CONSOLE_HOME="/Users/$CONSOLE_USER"
	fi
fi

# The bundle is looked for, not assumed. The app is allowed to register the login
# agent from anywhere under /Applications or ~/Applications (LoginItem's
# isInStableInstallLocation), so filing it into /Applications/Utilities is a
# supported thing to have done — and with APP fixed at /Applications/Dezhban.app
# that install got "the app bundle was already gone", an unloaded-for-this-boot
# agent, an `rm -rf` that deleted nothing, and a bundle that kept launching at
# every subsequent login while the script said everything was removed.
# Unconditionally, and recording *every* match rather than stopping at the first.
# Gating this on /Applications/Dezhban.app being absent left a second install
# elsewhere untouched: `SessionLock` is path-keyed and `isInStableInstallLocation`
# accepts any Applications directory precisely so two can coexist, so the copy
# holding the agent registration may not be the one at the default path. The errand
# then ran from a bundle that had never registered — where the status is `.notFound`
# and `retractAll()` truthfully reports nothing to do — `rm -rf` deleted the wrong
# copy, and the script closed with an unqualified "files deleted" while the surviving
# bundle went on starting the app at every login.
if true; then
	# Root-owned, mode 700, for the same reason the errand's marker directory is:
	# this runs as root and a predictable name in a world-writable directory is a
	# symlink-plant away from a root write.
	search_dir=$(mktemp -d "${TMPDIR:-/tmp}/dezhban-search.XXXXXX") || search_dir=""
	# Every account's ~/Applications, not only the console user's. CONSOLE_HOME is
	# resolved only when somebody is logged in at the console, so run over ssh or at
	# the login window this searched /Applications alone — and an install in a
	# ~/Applications was never found, `rm -rf "$APP"` deleted nothing, and the script
	# still printed "files deleted" and, on the no-console-user branch, "Nothing will
	# start Dezhban (the app is gone)". The same false clean report the unbounded
	# search was added to prevent, reached from the other side.
	set -- /Applications
	case "$CONSOLE_HOME" in
	/Users/*) ;; # already covered by the glob below
	?*)
		[ -d "$CONSOLE_HOME/Applications" ] && set -- "$@" "$CONSOLE_HOME/Applications"
		;;
	esac
	for home_apps in /Users/*/Applications; do
		[ -d "$home_apps" ] || continue
		set -- "$@" "$home_apps"
	done
	for root in "$@"; do
		[ -n "$search_dir" ] || break
		[ -d "$root" ] || continue
		# Unbounded depth, to match LoginItem.isInStableInstallLocation, which accepts
		# anything *under* an Applications directory. A depth limit here meant an
		# install the app would happily register the login agent from — say
		# /Applications/Utilities/Network/Tools/Dezhban.app — was one the uninstaller
		# could not find, so it printed "Nothing will start Dezhban (the app is gone)"
		# over a bundle still sitting there launching at every login.
		# Written to a file by find itself, not piped through `head`. `$(… | head -1)`
		# truncates at the first newline, and a directory name may legally contain one
		# on macOS — so `/Applications/My<newline>Apps/Dezhban.app` yielded
		# APP=/Applications/My, which is then handed to `rm -rf` as root and used to
		# exec the retraction errand. `-quit` stops at the first match, and printf
		# without a trailing newline means the command substitution below reproduces
		# the path exactly, embedded newlines included.
		# One file per match, named by mktemp so nothing has to be counted, and each
		# path written with printf so `$(cat …)` reproduces it byte for byte. Piping
		# through `head -1` truncated at the first newline — legal in a macOS
		# directory name — and the result is handed to `rm -rf` as root.
		find "$root" -name Dezhban.app -type d -exec sh -c '
			out=$(mktemp "$2/bundle.XXXXXX") || exit 0
			printf "%s" "$1" >"$out"' _ {} "$search_dir" \; 2>/dev/null
	done
fi
# The default path counts as a candidate even if the search could not run.
if [ -n "$search_dir" ]; then
	for f in "$search_dir"/bundle.*; do
		[ -f "$f" ] || continue
		APP=$(cat "$f")
		echo "note: found the app at $APP" >&2
		break
	done
fi
# Every bundle found, each retracting its own registration: only the registering
# bundle can call SMAppService.unregister(), so a second copy's agent is retractable
# by nothing else.
retract_and_remove_bundle() {
	APP="$1"
	if [ -n "$CONSOLE_UID" ]; then
		run_retraction_errand
	fi
	rm -rf "$APP"
}

run_retraction_errand() {
	echo "unregistering the login agent for $CONSOLE_USER ..."
	if [ ! -x "$APP/Contents/MacOS/DezhbanMenu" ]; then
		# Only the app can call SMAppService.unregister(), so with the bundle already
		# gone — dragged to the Trash before running this — the registration cannot
		# be retracted by anything here, or ever again. Saying nothing would report a
		# clean uninstall over exactly the orphan this errand exists to remove.
		#
		# Always warned, but worded as a conditional. Gating this on
		# `launchctl print` was wrong in the direction that matters: it reports only
		# *loaded* jobs, and a registration sitting at `.requiresApproval` — the
		# state the app documents as "the user switched Dezhban off under Login
		# Items" — is registered without being loaded. So the one case this branch
		# exists for printed a clean "files deleted" over a surviving entry. There is
		# no root-side way to read SMAppService status, so the honest thing is to say
		# "if there is an entry, remove it" rather than to claim either way.
		LOGIN_ITEM_STUCK=no-app
	# The marker directory is created by this script, mode 700, owned by root. The
	# obvious "${TMPDIR:-/tmp}/name.$$" is a predictable path in a world-writable
	# directory, and under `sudo sh` TMPDIR is often unset — so an unprivileged
	# local user could pre-plant a symlink there and have the `echo` below write
	# through it AS ROOT. The sticky bit stops them deleting our file; it does not
	# stop them creating one first, and `rm -f` would only unlink their symlink and
	# leave the window open to try again.
	elif ! errand_dir=$(mktemp -d "${TMPDIR:-/tmp}/dezhban-uninstall.XXXXXX"); then
		# A real skip. Announcing one and falling through anyway left errand_done as
		# "/done" — on the sealed read-only system volume, so the marker could never
		# be written, the poll burned its full 15s, the reason was overwritten with
		# "timeout", and the kill -9 below murdered a retraction that had very likely
		# just succeeded.
		echo "note: could not create a private temp directory; skipping the login-item retraction" >&2
		# Its own state. Reporting this as "refused" told the user macOS would not
		# retract the item and sent them to clear it by hand, when in fact nothing
		# was ever attempted and simply running the uninstaller again would very
		# likely retract it cleanly.
		LOGIN_ITEM_STUCK=not-attempted
	else
		# Bounded. The errand talks to launchd over XPC, and this runs before
		# `rm -rf "$APP"` — an uninstaller that hangs here, silently (output is
		# discarded), leaves the machine mid-removal with no message on screen.
		#
		# Completion is signalled by a file rather than by watching the child: an
		# exited-but-unreaped child is a zombie, which `kill -0` still reports as
		# alive, so polling the pid would wait out the whole timeout on a perfectly
		# successful run. And no `( sleep N; kill ) &` watchdog — that leaks its
		# `sleep` past the end of the script and, if it ever fires late, aims a
		# `kill -9` at a pid the system may have recycled.
		errand_done="$errand_dir/done"
		(
			# Written to a side name and moved into place, so the marker only ever
			# appears complete. `echo failed >"$errand_done"` truncates before it
			# writes, and a poll landing in that gap saw the file and `cat`ed an
			# empty string — which is not "failed", so a retraction macOS had
			# refused was reported as a clean uninstall. One-directional, and
			# towards the silent orphan this block exists to catch.
			if launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" \
				"$APP/Contents/MacOS/DezhbanMenu" --unregister-login-item >/dev/null 2>&1; then
				echo ok >"$errand_done.partial"
			else
				echo failed >"$errand_done.partial"
			fi
			mv "$errand_done.partial" "$errand_done"
		) &
		errand=$!
		waited=0
		while [ ! -f "$errand_done" ] && [ "$waited" -lt 150 ]; do
			sleep 0.1
			waited=$((waited + 1))
		done
		if [ ! -f "$errand_done" ]; then
			echo "note: retracting the login item did not finish in 15s; continuing" >&2
			# Reported, not just survived. On this path no marker is ever written, so
			# the "failed" test below reads false and the script would print a clean
			# "files deleted" while the registration was still on file — the silent
			# orphan that test exists to prevent, on the one path the timeout is for.
			LOGIN_ITEM_STUCK=timeout
			# The subshell AND what it started. Killing only the subshell leaves the
			# DezhbanMenu it launched running, and the very next statement deletes the
			# bundle out from under it — the thing the `pkill` above exists to avoid,
			# reintroduced on the one path this timeout is here for.
			kill -9 "$errand" >/dev/null 2>&1 || true
			# Scoped to the user the errand ran as. Unscoped, this reached every
			# logged-in account's menubar app on a Mac using fast user switching —
			# other people's sessions, over a timeout in this one.
			pkill -x -U "$CONSOLE_UID" DezhbanMenu >/dev/null 2>&1 || true
		elif [ "$(cat "$errand_done" 2>/dev/null)" = "failed" ]; then
			# The status matters. The app only logs a refused unregister, and this
			# script discards its output — so without checking, a login item macOS
			# would not retract stayed behind, pointing at a bundle deleted moments
			# later and unreachable from then on, while the closing message claimed
			# everything was removed.
			LOGIN_ITEM_STUCK=refused
		fi
		rm -rf "$errand_dir"
		wait "$errand" >/dev/null 2>&1 || true
	fi
}

echo "removing the app bundle(s) ..."
bundles_handled=0
if [ -n "$search_dir" ]; then
	for f in "$search_dir"/bundle.*; do
		[ -f "$f" ] || continue
		retract_and_remove_bundle "$(cat "$f")"
		bundles_handled=$((bundles_handled + 1))
	done
	rm -rf "$search_dir"
fi
if [ "$bundles_handled" -eq 0 ]; then
	# Nothing found anywhere: still run the errand so the "the bundle is gone, and
	# only the app could have retracted this" warning is reached.
	retract_and_remove_bundle "$APP"
fi

if [ -n "$CONSOLE_UID" ]; then
	launchctl bootout "gui/$CONSOLE_UID/$LOGIN_AGENT" >/dev/null 2>&1 || true
	# The app's own per-user directory: the session lock the GUI takes at startup
	# to keep a second copy of itself from running, and the hand-off file beside
	# it. Machine-derived, none of it the user's — and files this version creates
	# that no earlier one did, so leaving them would make this script's own
	# promise false.
	#
	# CONSOLE_HOME is resolved up top, where the bundle search needs it too.
	if [ -n "$CONSOLE_HOME" ] && [ -d "$CONSOLE_HOME" ]; then
		rm -rf "$CONSOLE_HOME/Library/Application Support/$APP_BUNDLE_ID"
	else
		# Its own flag. Sharing LOGIN_ITEM_STUCK meant that whenever an earlier step
		# had already recorded timeout/refused/not-attempted, an unresolvable home
		# was swallowed — the session lock and any hand-off file surviving under a
		# closing report that mentioned only the other problem.
		SUPPORT_DIR_KEPT=1
	fi
	# The app's preferences, for the same reason. These are not cosmetic: the
	# migration that moves an old LaunchServices login item onto the login agent
	# records that it has run, so a surviving flag means a LATER install is never
	# migrated — silently restoring the "Open minimized" bug the agent exists to
	# fix. Written by the user's own cfprefsd, so it is deleted through defaults as
	# that user rather than by unlinking the plist under them.
	launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" \
		defaults delete "$APP_BUNDLE_ID" >/dev/null 2>&1 || true
else
	# No non-root console user: run at the login window, or over ssh on a Mac
	# nobody is logged into. Every step above needs that user's own launchd
	# session, so the whole per-user teardown is skipped — and because each
	# LOGIN_ITEM_STUCK assignment lives inside that block, the script used to print
	# an unqualified "files deleted" over an agent registration that survives, an
	# entry that fails to load at every subsequent login, and a migration flag that
	# makes a LATER reinstall skip the migration. The same silent-clean-report the
	# other states were introduced to end.
	#
	# Only if nothing more specific was recorded: a console user whose uid could not
	# be looked up also lands here, and "nobody is logged in" is the wrong thing to
	# tell somebody sitting at the machine.
	if [ "$LOGIN_ITEM_STUCK" = "none" ]; then
		LOGIN_ITEM_STUCK=no-console-user
	fi
fi

# The bundles are removed by `retract_and_remove_bundle` above, each after its own
# registration has been retracted — the removal cannot be hoisted here, because only
# the registering bundle can retract, so it has to still exist when its errand runs.

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
case "$LOGIN_ITEM_STUCK" in
none) ;;
*)
	echo
	case "$LOGIN_ITEM_STUCK" in
	refused) echo "warning: macOS would not retract the login item." ;;
	timeout) echo "warning: retracting the login item did not finish in time." ;;
	not-attempted)
		echo "warning: the login item was not retracted — the step was skipped."
		echo "         Running this uninstaller again will most likely clear it."
		;;
	no-app)
		echo "warning: the app bundle was already gone, so its login item could not"
		echo "         be retracted — only the app itself can do that."
		;;
	no-console-uid)
		echo "warning: could not look up $CONSOLE_USER's user id, so Dezhban's"
		echo "         per-user leftovers were not removed. This usually means the"
		echo "         directory service is unreachable; re-run once it is back."
		;;
	no-console-user)
		echo "warning: nobody is logged in, so Dezhban's per-user leftovers could not"
		echo "         be removed — its login item, and the saved preferences that"
		echo "         would make a later reinstall skip the login-item migration."
		echo "         Re-run this uninstaller from a logged-in graphical session."
		;;
	esac
	echo "         Nothing will start Dezhban (the app is gone). If a \"Dezhban\""
	echo "         entry remains under System Settings > General > Login Items,"
	echo "         remove it there."
	;;
esac

if [ "$SUPPORT_DIR_KEPT" = "1" ]; then
	echo
	echo "warning: $CONSOLE_USER's home directory could not be resolved, so"
	echo "         ~/Library/Application Support/$APP_BUNDLE_ID was left behind."
	echo "         It holds only Dezhban's session lock. (The saved preferences"
	echo "         were removed.)"
fi
echo
echo "If any OTHER account on this Mac ran the app, its login agent is still"
echo "registered there — root cannot reach another user's launchd session. Nothing"
echo "will start Dezhban (the bundle is gone), but the entry lingers under System"
echo "Settings → General → Login Items until that user removes it there."
