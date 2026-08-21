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
# The preference domain from a superseded bundle identifier. Still on disk on any
# Mac that ran an early build, and inert — but a purge that leaves it behind is
# not a purge, and it is the pair of domains ADR-0015 is about.
LEGACY_APP_BUNDLE_ID=com.dezhban.DezhbanMenu
# Whether each per-user path ran the preference-domain deletion, and for WHOM.
# Two flags, not one: the closing warnings ask two different questions — did the
# console user's preferences get cleared, and did the invoking user's — and a
# single flag answered both with whichever ran. Bob SSHing in to uninstall while
# Alice is at the console then silently suppressed the warning about ALICE's
# surviving preferences, which is the leftover this whole change is about.
#
# Deliberately "ran", not "succeeded": every `defaults delete` here is `|| true`,
# and deleting a domain that does not exist also exits non-zero, so success is
# not something this script can observe. Whether the pass happened at all is.
CONSOLE_PREFS_RAN=0
INVOKER_PREFS_RAN=0
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
		# Any depth under an Applications directory, to match
		# LoginItem.isInStableInstallLocation — a depth limit meant an install the app
		# would happily register the login agent from, say
		# /Applications/Utilities/Network/Tools/Dezhban.app, was one the uninstaller
		# could not find, so it printed "Nothing will start Dezhban (the app is gone)"
		# over a bundle still launching at every login.
		#
		# But pruned at every .app boundary. Unpruned, this walked *inside* every
		# installed application — millions of inodes on a Mac with Xcode, repeated for
		# each home directory, any of which may be a network mount that blocks — while
		# the script sat silent, after `panic` had already torn the rules down and
		# before anything was deleted. Nothing useful lives inside another app bundle.
		#
		# One file per match, named by mktemp so nothing has to be counted, and each
		# path written with printf so `$(cat …)` reproduces it byte for byte. Piping
		# through `head -1` truncated at the first newline — legal in a macOS directory
		# name — and the result is handed to `rm -rf` as root.
		# Two pruned branches, not one chain: `expr -a \( … \) -prune` evaluates the
		# prune only when the whole conjunction is true, so a non-matching bundle fell
		# through and was descended into anyway — verified by finding a Dezhban.app
		# nested inside an Xcode.app fixture.
		find "$root" \
			\( -name Dezhban.app -type d -exec sh -c '
				out=$(mktemp "$2/bundle.XXXXXX") || exit 0
				printf "%s" "$1" >"$out"' _ {} "$search_dir" \; -prune \) \
			-o \( -name '*.app' -type d -prune \) 2>/dev/null
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
		# Both domains, and both directories. This branch exists because a
		# network or relocated home is not under /Users, so the sweep below
		# never sees it.
		for id in "$APP_BUNDLE_ID" "$LEGACY_APP_BUNDLE_ID"; do
			rm -rf "$CONSOLE_HOME/Library/Application Support/$id"
			rm -rf "$CONSOLE_HOME/Library/Saved Application State/$id.savedState"
		done
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
	#
	# Both domains: the legacy one carries the same migration flag, and clearing
	# only the current one left a reinstall reading state from a bundle identifier
	# nothing has used for versions.
	#
	# ONE `launchctl asuser`, not one per domain. Each is an unbounded XPC hop into
	# a session that may be tearing down (the errand above is wrapped in a 15s
	# timeout for exactly that reason), and this file's rule is not to multiply
	# them — so the two deletes ride the same invocation.
	launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" sh -c \
		'defaults delete "$1" >/dev/null 2>&1; defaults delete "$2" >/dev/null 2>&1' \
		sh "$APP_BUNDLE_ID" "$LEGACY_APP_BUNDLE_ID" >/dev/null 2>&1 || true
	CONSOLE_PREFS_RAN=1
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
# Every account's support directory, not only the console user's. The search above
# deliberately finds and deletes bundles in any account's ~/Applications, so scoping
# the cleanup to the console user left those users' session lock and hand-off file
# behind — under a closing message that says everything was removed. Root can reach
# these; the preferences and the login item cannot be, and are named in the warning.
for home in /Users/*; do
	# Guarded on the home itself rather than on each path below: the loop now
	# removes three things, and an unmatched glob leaves `$home` as the literal
	# "/Users/*", which is not a directory.
	[ -d "$home" ] || continue
	# Saved window state alongside the support directory, and both preference
	# domains for each. Not because the app's own deletion loses a race — its
	# window opts out of AppKit restoration, so nothing rewrites this — but
	# because the app only ever speaks for the account running it, and root is
	# the only thing that can reach these for an account nobody is logged into.
	for id in "$APP_BUNDLE_ID" "$LEGACY_APP_BUNDLE_ID"; do
		rm -rf "$home/Library/Application Support/$id"
		rm -rf "$home/Library/Saved Application State/$id.savedState"
	done
done

echo "removing daemon state at $STATE_DIR ..."
rm -rf "$STATE_DIR"

if [ "${KEEP_CONFIG:-0}" = "1" ]; then
	echo "keeping config at $CONFIG_DIR (KEEP_CONFIG=1)"
else
	echo "removing config at $CONFIG_DIR ..."
	rm -rf "$CONFIG_DIR"
fi

# Per-user preferences for an invoking user the console-user block above did not
# already cover — someone who sudo'd from ssh, or from a second account.
#
# This is the half a root script cannot properly reach, and the half whose
# survival used to make a reinstall look like an already-configured machine to
# the app's first-run wizard: the preference domain outlived every uninstall, so
# `dezhban.firstRunCompleted` stayed set while /etc/dezhban was empty.
#
# Only $SUDO_USER, never a loop over /Users: this script speaks for the person
# running it. Other accounts are named in the closing summary rather than touched.
#
# It also covers the case where $SUDO_USER *is* the console user but CONSOLE_UID
# could not be looked up. The whole console block above is gated on that uid, so a
# Mac with somebody at the keyboard and an unreachable directory server — the
# script's own `no-console-uid` case — skipped the preference deletion entirely,
# leaving `dezhban.firstRunCompleted` set. The bare `sudo -u` fallback below needs
# no uid and works there.
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] &&
	{ [ "$SUDO_USER" != "${CONSOLE_USER:-}" ] || [ -z "$CONSOLE_UID" ]; }; then
	echo "removing $SUDO_USER's app preferences ..."
	# Through that user's own launchd session when they have one, exactly as the
	# console-user path above does. A bare `sudo -u` from a root shell lands in
	# root's bootstrap namespace and talks to the wrong cfprefsd, so over ssh the
	# delete silently no-opped and `|| true` hid it. The bare form is still right
	# for a user with no session at all — there is no cfprefsd to reach, and
	# defaults then works on the file.
	#
	# Which one is chosen by probing the session with `launchctl asuser … true`,
	# not by the exit status of the delete itself: `defaults delete` of a domain
	# that does not exist also exits non-zero, so keying off it ran the fallback
	# on every Mac for the legacy domain — a second unbounded hop bought by a
	# result that said nothing about whether a session existed.
	sudo_uid=$(id -u "$SUDO_USER" 2>/dev/null || echo "")
	if [ -n "$sudo_uid" ] && launchctl asuser "$sudo_uid" true >/dev/null 2>&1; then
		launchctl asuser "$sudo_uid" sudo -u "$SUDO_USER" sh -c \
			'defaults delete "$1" >/dev/null 2>&1; defaults delete "$2" >/dev/null 2>&1' \
			sh "$APP_BUNDLE_ID" "$LEGACY_APP_BUNDLE_ID" >/dev/null 2>&1 || true
	else
		sudo -u "$SUDO_USER" sh -c \
			'defaults delete "$1" >/dev/null 2>&1; defaults delete "$2" >/dev/null 2>&1' \
			sh "$APP_BUNDLE_ID" "$LEGACY_APP_BUNDLE_ID" >/dev/null 2>&1 || true
	fi
	# Same gate as CONSOLE_PREFS_RAN below, for the same reason: both limbs above
	# resolve $SUDO_USER through the name service, so an empty sudo_uid means that
	# lookup failed and the deletes plausibly never ran. Claiming the pass then
	# would suppress the one warning that names the leftover.
	[ -n "$sudo_uid" ] && INVOKER_PREFS_RAN=1
	# The fallthrough case: SUDO_USER *is* the console user, whose uid could not
	# be looked up. Then this pass did cover them, and the console warning below
	# must stop claiming their preferences were left behind.
	#
	# But only if `id -u` worked HERE. The reason CONSOLE_UID is empty is that the
	# same lookup failed moments ago, and both branches above resolve the name
	# through that same name service — so on an unreachable directory server the
	# deletes plausibly never ran at all. Claiming the pass then would suppress
	# the one warning that names the leftover this whole change is about.
	[ -n "$sudo_uid" ] && [ "$SUDO_USER" = "${CONSOLE_USER:-}" ] && CONSOLE_PREFS_RAN=1
	# The login item, unlike the preferences, is NOT attempted for this user: the
	# only thing that retracts it is the app's own --unregister-login-item errand,
	# and that needs the account's launchd session. The console-user block runs it
	# where there is one; here there is not.
	echo "note: $SUDO_USER's \"open at login\" registration is not removed here — retracting" >&2
	echo "      it needs Dezhban.app running in that account's own session. Untick Dezhban" >&2
	echo "      in System Settings > General > Login Items." >&2
elif [ -z "${SUDO_USER:-}" ] || [ "$SUDO_USER" = root ]; then
	echo "note: no invoking user to clean up after (running as root directly)." >&2
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
		echo "warning: could not look up $CONSOLE_USER's user id, so Dezhban's login"
		echo "         item was not retracted — only the app, running inside that"
		echo "         account's own session, can do that. This usually means the"
		echo "         directory service is unreachable; re-run once it is back, or"
		echo "         untick Dezhban in System Settings > General > Login Items."
		if [ "$CONSOLE_PREFS_RAN" != "1" ]; then
			echo "         Its saved preferences were not removed either, so a later"
			echo "         reinstall would skip the login-item migration."
		fi
		# A home outside /Users is reachable only through the block this path
		# skipped: the sweep below globs /Users. Named rather than left out —
		# the old wording said "per-user leftovers" and covered this by being
		# vague, and a closing report that stops naming what survived is the
		# false clean report this file keeps guarding against.
		case "${CONSOLE_HOME:-}" in
		/Users/* | "") ;;
		*)
			echo "         Its session lock and saved window state under $CONSOLE_HOME"
			echo "         were not removed either — that home is outside /Users, which"
			echo "         is the only place root can sweep without that user's session."
			;;
		esac
		;;
	no-console-user)
		echo "warning: nobody is logged in, so Dezhban's login item could not be"
		echo "         retracted — only the app, running inside that account's own"
		echo "         session, can do that."
		if [ "$INVOKER_PREFS_RAN" != "1" ]; then
			echo "         Nor could the saved preferences that would make a later"
			echo "         reinstall skip the login-item migration."
		fi
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
	echo "         ~/Library/Application Support/$APP_BUNDLE_ID and its saved window"
	echo "         state were left behind. They hold only Dezhban's session lock and"
	echo "         window positions. (The saved preferences were removed.)"
fi
# The login keychain, unconditionally: no branch above can reach it, and unlike
# the login item there is no errand that can. A login keychain item's ACL is bound
# to the unlocked session that owns it, so root has no route to this one even for
# the console user (ADR-0015). Here rather than mid-transcript, because this is
# where the things left for the user to do are collected.
#
# Worded as a condition, not as a fact. On the flow ADR-0015 makes the recommended
# one the app has ALREADY cleared these items and then launched this script, so
# "your Touch ID key stays in your login keychain" would close that transcript with
# a statement about a key that is gone, telling the user to go and do the thing
# they just did. This script cannot check — the keychain is the one thing it cannot
# read — so it says which case it is talking about instead of guessing.
echo
echo "If you did NOT remove Dezhban from the app's Settings > Remove Dezhban, its"
echo "Touch ID key is still in your login keychain. Root cannot remove it; clear it"
echo "by running this as yourself:"
# A loop, not one invocation, and no -a: the service holds TWO items (the token
# and enrollment's capability probe) and each delete removes one. Naming the
# accounts here would be a second copy of strings that live in ControlToken.swift;
# deleting by service until none is left needs neither.
echo "    while security delete-generic-password -s sh.dezhban.menu >/dev/null 2>&1; do :; done"

echo
echo "If any OTHER account on this Mac ran the app, two things remain there that"
echo "root cannot reach: its login agent registration — nothing will start Dezhban,"
echo "since the bundle is gone, but the entry lingers under System Settings >"
echo "General > Login Items until that user removes it — and Dezhban's saved"
echo "preferences, which record that the login-item migration has run, so a later"
echo "reinstall for that account would skip it. From that account:"
echo "    defaults delete $APP_BUNDLE_ID"
echo "    defaults delete $LEGACY_APP_BUNDLE_ID"
echo "(the second is a superseded bundle identifier; it carries the same flag, and"
echo "reports \"domain does not exist\" if that account never ran an early build.)"
