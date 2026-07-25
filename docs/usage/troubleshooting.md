# Troubleshooting

## I'm locked out — no network after a block

dezhban is fail-closed: a crashed `run`, a misconfigured guard, or a stale VPN
endpoint leaves the block-all rule in place by design (the kill switch must not
fail open). The escape hatch removes dezhban's rules with no daemon involved:

```sh
sudo dezhban panic      # or: task panic (or: sh scripts/panic.sh)
dezhban status
```

`panic` only touches rules tagged `dezhban` (the pf anchor / nft table / WFP
sublayer), so it is always safe and a no-op on a clean system. After it runs,
connectivity is restored. Then fix the cause below before re-enabling the guard.

**No `dezhban` binary at all (e.g. a dev build gone missing).** Tear the
platform rules down directly:

```sh
# macOS
sudo pfctl -a dezhban -F all                 # flush our anchor → network back
sudo cp /etc/pf.conf.dezhban.bak /etc/pf.conf # restore the saved ruleset, if present
sudo pfctl -f /etc/pf.conf                    # reload it
sudo pfctl -d                                 # last resort: disable pf entirely
sudo rm -f /var/db/dezhban/pf.state           # clear the stale state marker

# Linux
sudo nft delete table inet dezhban            # or: nft delete table ip dezhban

# Windows (PowerShell, as Administrator)
Remove-NetFirewallRule -Group dezhban
```

## I rebooted and my VPN never came up — no network at all

This is `vpn.armAtBoot` (on by default) working as designed, not a bug: it arms
the guard at startup on a host that has connected successfully before, so the
network stays blocked from boot until the VPN dials — but if the VPN never
manages to connect (a changed server, a client that failed to start, an
endpoint that moved), the guard just holds, the same way it would after any
tunnel drop. `panic` clears it with no daemon involved:

```sh
sudo dezhban panic
dezhban status                     # confirm: tunnel down, no rules
```

Then fix whatever kept the VPN from connecting (see the endpoint-routing checks
below), or temporarily disable arm-at-boot while you sort it out:

```sh
sudo dezhban config set vpn.armAtBoot=false
```

If you need the real ISP IP for a domestic-only site rather than turning
anything off, use a bounded [`pause`](cli.md#pause-protection-temporarily)
instead — it re-arms itself, so there's nothing to remember to undo.

## VPN guard: tunnel dies, DNS fails ("no such host"), country lookups time out

Symptom (from the daemon log):

```
msg="vpn guard active (startup)" tunnels=[utun4] endpoints=1
msg="country lookup failed" err="... dial tcp: lookup ip-api.com: no such host"
```

**Cause.** In guard mode dezhban blocks the physical interface except egress to
`vpn.endpoints`, keeping the VPN's encrypted transport alive so the tunnel can
stay up. If `vpn.endpoints` is **wrong** (a stale server IP) or **internal to the
tunnel** (an address like `10.0.0.x` that only exists inside the tunnel), the
real transport is blocked → the tunnel drops → all traffic (DNS included) routed
over the dead tunnel fails → the host locks itself out.

The failure chain:

```
wrong/internal vpn.endpoints
  → physical-side `pass to <endpoint>` matches nothing real
  → VPN transport blocked on the physical link
  → tunnel drops, can't redial (its path to the server is cut)
  → DNS + everything over the tunnel fails → lockout
```

**Recover, then diagnose:**

```sh
sudo dezhban panic            # restore connectivity
dezhban doctor                # tunnels, subnets, endpoint sanity
dezhban doctor --discover     # macOS: find the VPN's REAL server IP
```

`doctor` flags any endpoint that sits inside a tunnel's own subnet (a guaranteed
lockout). `--discover` (macOS) inspects the connected VPN's live sockets and
prints the actual server IP:port it talks to on the physical link — compare that
against `vpn.endpoints`.

**Fix.** Set `vpn.endpoints` to the VPN server's **public IP** — the address the
client sends encrypted packets to on the physical interface. Get it from your VPN
client's config, or from `dezhban doctor --discover`. Then:

```sh
dezhban validate --config <your-config>   # confirm it parses
task install FRESH=1                      # tear down + reinstall (or: sh scripts/reinstall.sh)
```

### Redial livelock during tunnel warmup (fixed)

Symptom: after disconnecting and redialing your VPN, **neither the VPN nor the
internet recovered until you stopped the daemon** (`Ctrl+C`), even though the
tunnel interface came back up. The log showed `FULL BLOCK country=""` during the
redial.

**Cause (historical).** A freshly redialed tunnel reports "up" before it is
actually routing/DNS-ready. Guard mode used to run the geo lookup during that
warmup; the lookup failed (`no such host`), and the then-current fail-closed
behavior escalated a run of failures to FULL BLOCK with an *empty* country — which cut the tunnel's
own egress and prevented the very redial it was waiting for (a livelock).

**Fix (current behavior).** An **undeterminable** country now
*holds* the current posture instead of escalating — only a *successful* reading
of a blocked country produces FULL BLOCK. See
[modes.md](../concepts/modes.md#an-unknown-country-holds-it-never-escalates).
If your endpoints are
hostnames, keep `vpn.allowPhysicalDNS` on (the default) so the client can re-resolve its
server on the physical link while the tunnel is down. The residual leak is
DNS-query metadata only; your traffic stays blocked.

### My VPN can't redial after a drop (rotating-server VPNs)

Symptom: the guard cuts egress on a VPN drop (correct), but hitting the client's
redial button does nothing — the VPN never comes back without a manual
`dezhban switch`.

**Cause.** Rotating-pool and anti-censorship VPNs (NordVPN, ProtonVPN,
RocketTunnel, …) pick a **fresh server IP on almost every connect**. The guard
only passes *known* endpoints on the physical link, so the redial targets an
address dezhban has never seen and is dropped — `endpointGrace` only covers
redials to the *same* server.

**Fix (current behavior).** The [automatic redial
window](../concepts/modes.md#automatic-redial-window) (`vpn.redialWindow`, default
`30s`) opens on the drop so the client can redial anywhere; the new server is
learned and the guard snaps back on a confirmed good exit. If you disabled it
(`"0"`), redials to fresh servers need `dezhban switch` — that is the
configured strict behavior, not a bug. If the window keeps getting suppressed in
the logs, your tunnel is flapping faster than
`vpn.advanced.redialMinUptime` (default `15s`) — fix the VPN, or lower/zero
the gate if the flapping is expected.

### Note for NetworkExtension VPNs (macOS)

Some macOS VPN clients (Lightway/RocketTunnel, WireGuard-go, Xray/V2Box) run their
transport inside a system extension and bind it directly to the physical
interface. `route get <endpoint>` will show such an endpoint going via the tunnel
even when it's correct — that's why dezhban's check uses **subnet containment**,
not a route probe, and why `--discover` reads live sockets instead. The pf rule
still matches the provider's physical-side socket, so a correct **public**
endpoint works even though `route get` is misleading.

## I started the kill switch with the VPN connected and lost ALL internet

Even though your IP was in an allowed country. Symptom: `ping: cannot resolve
google.com: Unknown host` — DNS and everything else, gone.

Check what dezhban actually knows:

```sh
dezhban doctor --config /etc/dezhban/dezhban.json
```

If it says `endpoints … (none resolved)`, that is the whole story. The guard's
standing rule is:

```
pass quick on lo0 all
pass out quick on { utun4 } all      # tunnel traffic
block drop out all                   # everything else — INCLUDING en0
```

That last line blocks the physical interface, and the physical interface is what
carries your VPN's own encrypted transport. With no endpoint allowed through it, the
guard cuts the tunnel's handshake and keepalives: the VPN dies, so nothing flows at
all. It is not a leak-proof guard, it is a total blackout — and it can't recover,
because the socket discovery would have learned the server from is now dead too.

dezhban now **refuses to start** in this state and tells you so; `doctor` exits
non-zero on it.

**Why wasn't the server auto-discovered?** Endpoint discovery reads *connected*
sockets out of `netstat`. WireGuard — and other NetworkExtension clients — send from
an **unconnected** UDP socket, so they never appear as a connected flow and have no
foreign address to read. No amount of retrying will find them. Name the server:

```sh
dezhban vpn import ~/wg0.conf                  # reads Endpoint= from the VPN's own config
dezhban vpn add home --endpoint vpn.example.com
sudo dezhban config set vpn.endpoints=203.0.113.7
dezhban doctor                                  # confirm it resolves, then start
```

## The menubar app says "stopped" — or routine ops started asking for a password again

Both symptoms have one cause: the daemon's state directory `/var/db/dezhban` is not
traversable by the logged-in user. The daemon runs as root, but everything it
publishes is read from outside it — `state.json` (0644) by the menubar app and
`status --json`, and `control.sock` (0660 `root:admin`) by every `block`/`unblock`.
A `0700` directory silently severs both: the app sees no snapshot and reports
"stopped" while the daemon is enforcing perfectly, and the control socket can't be
reached, so routine ops fall back to the root path and prompt for a password.

```sh
stat -f "%Sp %Su %Sg %N" /var/db/dezhban    # want: drwxr-xr-x root wheel
dezhban status | grep "daemon control"      # want: reachable — routine ops need no password
```

Starting the daemon repairs the mode automatically (`state.EnsureDir`). To fix it
without a restart:

```sh
sudo chmod 755 /var/db/dezhban
```

The open directory leaks nothing: the sensitive files inside it (`command.json`,
`pf.state`) are `0600`.

## Preview rules before applying them

Never find out what a block does by getting locked out — render the exact
ruleset first, no root, no side effects:
[modes.md § Preview any ruleset](../concepts/modes.md#preview-any-ruleset-without-applying-it).

## Config won't load

```sh
dezhban validate --config <config>     # prints the precise validation error
```

See [config.md](config.md) for every field and its constraints.
