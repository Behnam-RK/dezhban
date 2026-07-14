#!/bin/sh
# install-local.sh — build, install the config to the system path, register the
# service, and start it. Requires sudo (service registration is privileged).
#
# Override the config with CONFIG=path; defaults to the local config, falling
# back to the example. The system config path is /etc/dezhban/dezhban.json.
set -eu
cd "$(dirname "$0")/.."

CONFIG="${CONFIG:-configs/dezhban.local.json}"
[ -f "$CONFIG" ] || CONFIG="configs/dezhban.example.json"
SYS_CONFIG=/etc/dezhban/dezhban.json

echo "validating $CONFIG ..."
go run ./cmd/dezhban validate --config "$CONFIG"

go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o dezhban ./cmd/dezhban

echo "installing config -> $SYS_CONFIG"
sudo mkdir -p /etc/dezhban
# 0644 (world-readable) to match config.Save: the file holds no secrets, and the
# unprivileged inspect commands (e.g. `dezhban status` below) must be able to read
# the config the root daemon uses. Installing 0600 root-only breaks those reads.
sudo install -m 644 "$CONFIG" "$SYS_CONFIG"

echo "registering and starting service ..."
sudo ./dezhban install --config "$SYS_CONFIG"
sudo ./dezhban start
./dezhban status || true

# Warn if a different `dezhban` shadows this build on $PATH (e.g. a leftover
# `go install` in ~/go/bin). The service loads this build, but a bare `dezhban`
# in your shell would run the shadowing copy instead — a common source of
# "why is an old version installed?" confusion. Scripts here use ./dezhban.
onpath="$(command -v dezhban 2>/dev/null || true)"
if [ -n "$onpath" ] && [ "$onpath" != "$(pwd)/dezhban" ]; then
	echo "note: a different 'dezhban' is on your PATH at $onpath"
	echo "      bare 'dezhban ...' runs that copy — use ./dezhban for this build"
fi
