#!/bin/sh
# reinstall.sh — tear down any existing service/rules, then install fresh. The
# one-shot for iterating on config or a new build. Requires sudo.
set -eu
cd "$(dirname "$0")/.."

go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o dezhban ./cmd/dezhban
echo "removing any existing install ..."
sudo ./dezhban stop || true
sudo ./dezhban uninstall || true

exec sh scripts/install-local.sh
