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

make build

echo "installing config -> $SYS_CONFIG"
sudo mkdir -p /etc/dezhban
sudo install -m 600 "$CONFIG" "$SYS_CONFIG"

echo "registering and starting service ..."
sudo ./dezhban install --config "$SYS_CONFIG"
sudo ./dezhban start
./dezhban status || true
