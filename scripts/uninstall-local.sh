#!/bin/sh
# uninstall-local.sh — stop and unregister the service. Requires sudo. Stopping
# the service removes dezhban's firewall rules; if anything lingers, run panic.sh.
set -eu
cd "$(dirname "$0")/.."

make build
sudo ./dezhban stop || true
sudo ./dezhban uninstall
echo "uninstalled. if rules persist, run: scripts/panic.sh"
