# Safety

dezhban is a kill switch; treat every `block` as potentially self-inflicting until
teardown is proven on your machine. For recovering from an actual lockout, see the
[troubleshooting.md](troubleshooting.md) runbook — this page is the principles.

- **Test on the local console**, not over SSH/VPN — a lock-out shouldn't also kill
  your way back in.
- **Verify teardown *first*:** `block` then immediately `unblock` (or `panic`), and
  confirm rules are gone (macOS: `sudo pfctl -a dezhban -s rules`).
- **`sudo dezhban panic` is the always-available escape hatch** — a standalone
  teardown that removes dezhban's tagged rules and restores saved prior state,
  whether or not a daemon owns them. Idempotent; a no-op on a clean system.
- **The allowlist must include** loopback (implicit), DNS, and the geo-API egress
  IPs, or recovery can never fire and the block becomes permanent.
- **Allowlist IPs are pinned at block time.** A provider behind a rotating CDN may
  resolve to a different IP later that isn't allowed, breaking recovery. Prefer
  providers with stable IPs, or pin a wide-enough `allowlist.hosts` range. (The
  `run` loop refreshes the allowlist live; a manual `block` is static.)
- **VPN guard:** a wrong or tunnel-internal `vpn.endpoints` value is the #1 lockout
  cause — it blocks the real transport and drops the tunnel. Verify with
  `dezhban doctor` / `doctor --discover` before enabling. See [modes.md](modes.md)
  and [troubleshooting.md](troubleshooting.md).

## Teardown mechanics

On macOS, `block` appends one `anchor "dezhban"` line to `/etc/pf.conf` (backed up
to `/etc/pf.conf.dezhban.bak` first) and loads rules into the kernel `dezhban`
anchor; `unblock`/`panic` flush the anchor and restore the backup. Because the
rules live in the kernel anchor, teardown works even if the blocking process was
killed. Linux and Windows teardown is equivalently surgical: the whole `dezhban`
nftables table / WFP sublayer is removed, nothing else touched.
