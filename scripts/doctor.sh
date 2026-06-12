#!/bin/sh
# doctor.sh [extra args] — diagnose the VPN guard config (tunnels, endpoints,
# lockout risks). No root. Pass --discover to hunt the connected VPN's real
# server IP on macOS, e.g.: scripts/doctor.sh --discover
#
# Override the config with CONFIG=path; defaults to the local config.
set -eu
cd "$(dirname "$0")/.."

CONFIG="${CONFIG:-configs/dezhban.local.json}"
[ -f "$CONFIG" ] || CONFIG="configs/dezhban.vpn-guard.json"

exec go run ./cmd/dezhban doctor --config "$CONFIG" "$@"
