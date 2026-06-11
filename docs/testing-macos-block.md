# Live block test — macOS (acceptance steps 1–5)

Manually verify the macOS `pfctl` backend end-to-end. Unit tests can't cover
this: `block` drives the live firewall and can cut your own network.

> [!WARNING]
> **Run on the local console, not over SSH/VPN/remote.** A bad allowlist or a
> crash mid-block can lock you out. Keep the escape hatch below open in a second
> terminal before you start.

> [!CAUTION]
> **This test is invalid behind a full-tunnel VPN.** The Phase-2 backend uses a
> destination-IP allowlist over `block drop out all`. Under a full tunnel the
> default route is the tunnel (e.g. `utun4`) and pf on the physical interface
> sees only the **encrypted outer packets to the VPN endpoint** — never the inner
> DNS/geo-API destinations. So `block` cuts the tunnel's transport and **kills
> everything**, including the "allowed" DNS (`dig` → 8.8.8.8 times out) and the
> geo-API providers. That is expected here, not a bug — it's why the per-IP
> allowlist is the wrong primitive for a VPN host.
>
> To exercise this dst-IP backend, **disconnect the VPN first** so `en0` carries
> the real traffic the allowlist names. The VPN-aware **interface guard** (GUARD /
> FULL-BLOCK states) is the path for VPN hosts — see
> [VPN / full-tunnel mode](./plans/README.md#vpn--full-tunnel-mode-primary-use-case);
> test it once that lands.

## Before you start

- Be at the physical machine (or a console that does **not** go over the network
  you're about to cut).
- Open a **second terminal** with the manual escape ready:
  ```bash
  sudo pfctl -a dezhban -F all   # flush our anchor (frees the network)
  sudo pfctl -d                  # disable pf entirely, only if we enabled it
  ```
- Build and use a config that lists a DNS resolver and (optionally) provider
  IPs. The provider hostnames are resolved and added to the allowlist
  automatically at block time:
  ```bash
  go build -o /tmp/dezhban ./cmd/dezhban
  ```
  Use `configs/dezhban.example.json` or your own.

## Step 0 — prove teardown first

Confirm `unblock` works *before* trusting `block`. Apply, then immediately tear
down, and check the anchor is empty:

```bash
sudo /tmp/dezhban block --config configs/dezhban.example.json
sudo /tmp/dezhban unblock
sudo pfctl -a dezhban -s rules     # expect: no output (empty anchor)
```

If that round-trips cleanly, proceed.

## Step 1 — block cuts general egress, allowlist stays open

```bash
sudo /tmp/dezhban block --config configs/dezhban.example.json
```

Expect, in another shell:

| Check | Command | Expected |
|-------|---------|----------|
| General egress dies | `curl -m 5 https://example.com` | hangs / fails |
| Loopback works | `curl -m 5 http://127.0.0.1` (or `ping 127.0.0.1`) | works |
| DNS resolves | `dscacheutil -q host -a name ipinfo.io` or `dig ipinfo.io` | resolves |
| Geo-API reachable | `curl -m 5 https://ipinfo.io/json` | returns JSON |

If general egress does **not** die, stop and inspect: `sudo pfctl -a dezhban -s rules`.

## Step 2 — status reports blocked

```bash
sudo /tmp/dezhban status        # needs root to read pf rules
```

Expect: `blocked:          true`.

## Step 3 — block is idempotent

```bash
sudo pfctl -a dezhban -s rules > /tmp/rules.before
sudo /tmp/dezhban block --config configs/dezhban.example.json
sudo pfctl -a dezhban -s rules > /tmp/rules.after
diff /tmp/rules.before /tmp/rules.after   # expect: no difference
```

Re-blocking must not stack or duplicate rules.

## Step 4 — unblock restores everything

```bash
sudo /tmp/dezhban unblock
curl -m 5 https://example.com       # expect: full connectivity back
sudo pfctl -a dezhban -s rules      # expect: empty
cat /etc/pf.conf                    # expect: no leftover `anchor "dezhban"` line
```

Behind the scenes `unblock` flushes the anchor, restores the saved
`/etc/pf.conf` from `/etc/pf.conf.dezhban.bak`, and (if we enabled pf) returns
pf to its prior on/off state.

## Step 5 — cleanup survives a killed process

Simulate a crash mid-block: kill the process, then unblock from a fresh one.

```bash
sudo /tmp/dezhban block --config configs/dezhban.example.json
# (no long-running process in Phase 2; the kill case matters once `run` lands.
#  The invariant under test: state lives in the kernel anchor + on disk, not in
#  process memory, so a separate invocation can still tear down.)
sudo /tmp/dezhban unblock            # different process — must still clean up
sudo pfctl -a dezhban -s rules       # expect: empty
```

State the backend relies on: the kernel `dezhban` anchor (rules) and
`/var/db/dezhban/pf.state` + `/etc/pf.conf.dezhban.bak` (prior pf state). None of
it is held in process memory, so teardown works after a crash.

## If something goes wrong

```bash
sudo pfctl -a dezhban -F all                 # flush our anchor → network back
sudo cp /etc/pf.conf.dezhban.bak /etc/pf.conf # restore the saved ruleset (if present)
sudo pfctl -f /etc/pf.conf                    # reload it
sudo pfctl -d                                 # last resort: disable pf entirely
sudo rm -f /var/db/dezhban/pf.state           # clear stale state marker
```

The Phase 7 `panic` command will automate this recovery; until then, the above
is the manual path.
