#!/bin/sh
# rules.sh [mode] — print the firewall ruleset a policy would apply, WITHOUT
# applying it. No root. With no mode argument, prints all three modes.
#
# Override the config with CONFIG=path; defaults to the local config.
set -eu
cd "$(dirname "$0")/.."

CONFIG="${CONFIG:-configs/dezhban.local.json}"
[ -f "$CONFIG" ] || CONFIG="configs/dezhban.vpn-guard.json"

print_mode() {
	echo "===== mode: $1 ====="
	go run ./cmd/dezhban print-rules --mode "$1" --config "$CONFIG"
	echo
}

if [ "$#" -ge 1 ]; then
	print_mode "$1"
else
	for m in guard fullblock legacy; do print_mode "$m"; done
fi
