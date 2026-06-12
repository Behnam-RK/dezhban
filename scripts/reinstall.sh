#!/bin/sh
# reinstall.sh — tear down any existing service/rules, then install fresh. The
# one-shot for iterating on config or a new build. Requires sudo.
set -eu
cd "$(dirname "$0")/.."

make build
echo "removing any existing install ..."
sudo ./dezhban stop || true
sudo ./dezhban uninstall || true

exec sh scripts/install-local.sh
