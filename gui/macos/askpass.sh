#!/bin/sh
# SUDO_ASKPASS helper for the menubar app.
#
# sudo runs this (as the logged-in user, before any elevation) whenever PAM asks
# for a password. It must print the password on stdout and nothing else.
#
# Why it exists: with Touch ID configured, pam_tid handles authentication and
# this is never invoked. But a Touch ID attempt can legitimately fail — a closed
# lid, a wet finger, a few bad reads — and PAM then falls through to a password.
# Without an askpass helper sudo has nowhere to ask from in a GUI app, so that
# first miss killed the whole attempt and the app fell back to a password-only
# dialog with no way back to biometrics. With this, a miss becomes a prompt.
#
# Cancelling makes osascript exit non-zero and print nothing, so sudo sees an
# empty password and refuses — which is the correct outcome for "the user said
# no", and distinct from never having asked.
osascript \
	-e 'display dialog "dezhban needs your administrator password to change the kill switch." with title "dezhban" default answer "" with hidden answer with icon caution' \
	-e 'text returned of result' \
	2>/dev/null
