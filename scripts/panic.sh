#!/bin/sh
# panic.sh — the lockout escape hatch. Force-removes dezhban's firewall rules
# even if no daemon is running, then prints status. Requires sudo. Use this when
# a misconfigured guard or a crashed `run` has cut your connectivity.
set -eu
cd "$(dirname "$0")/.."

make build
sudo ./dezhban panic
./dezhban status || true
