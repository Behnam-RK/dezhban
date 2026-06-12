#!/bin/sh
# dev.sh — build, then run the monitor in dry-run (no firewall touch, no root).
# Polls country and logs each reading so you can watch detection without risk.
#
# Override the config with CONFIG=path; defaults to the dev template.
set -eu
cd "$(dirname "$0")/.."

CONFIG="${CONFIG:-configs/dezhban.dev.json}"

make build
echo "running dry-run with $CONFIG (Ctrl-C to stop) ..."
exec ./dezhban -v run --dry-run --config "$CONFIG"
