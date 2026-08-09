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
anything off, use a bounded [`pause`](cli.md#pause-the-guard-temporarily)
instead — it re-arms itself, so there's nothing to remember to undo.

## I have to turn dezhban on again after every reboot

The opposite complaint to the one above, and it has three different causes that
look identical from the outside. `doctor` tells them apart — no root needed:

```sh
dezhban doctor
```

**"boot service: not registered to start at boot."** Nothing is asking the OS to
launch dezhban, so a reboot leaves the host unguarded until you start it by
hand. `sudo dezhban install` registers it. The variant *"installed, but not set
to start at boot"* means the service unit exists with the wrong options —
`sudo dezhban install` rewrites it.

**"arm at boot: … it cannot arm."** The boot service is fine, but
[`vpn.armAtBoot`](config.md) cannot take effect. It may only arm the guard at
startup when a configured tunnel has been observed up at least once on this host
([ADR-0008](../adr/0008-arm-at-boot.md)) — arming without that would fail closed
on a machine whose VPN has never worked, which is a lockout by design. Connect
your VPN once with dezhban running and the observation persists from then on.
If the check instead reports the record could not be read, the daemon rewrites
it the next time a tunnel comes up.

**Both are healthy, but you still see nothing after logging in.** Then the guard
*is* up and what is missing is the menubar app, which is a login-item question,
not an enforcement one — add Dezhban.app under System Settings → General →
Login Items. `dezhban status` from a terminal confirms the guard is enforcing
without it.

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
configured strict behavior, not a bug.

If the logs say **`redial budget spent`** or **`backing off after consecutive
fast drops`**, the windows are being rationed rather than refused outright: your
tunnel is dropping again within `vpn.advanced.redialMinUptime` (default `15s`),
so each window is shorter than the last, and the rolling
`vpn.advanced.redialBudget` (default `2m` per `15m`) has run out. The log line
carries `nextEligible` — the instant a window can open again. Fix the VPN if you
can; if the flapping is expected, raise the budget or set `redialMinUptime` to
`"0"` so every drop gets a full-length window until the budget runs out. Note
that successful redials cost almost nothing (a window that closes early only
spends what it used), so reaching the limit means the redials themselves are
failing.

You do not have to sit out a `backing off` wait: one reconnection that holds —
long enough to clear `redialMinUptime`, or long enough for dezhban to confirm the
exit — clears it, and the next drop starts from a full window again. A
`redial budget spent` wait is the one that has to elapse, because the budget is
the actual bound.

**You do not have to do anything when it elapses, either.** dezhban re-takes the
decision at the `nextEligible` instant it reported, so a drop that was refused
gets its window as soon as the bound lifts — the tunnel does not have to drop
again first. That matters when the tunnel cannot come back on its own, which is
the rotation case below. The re-decision may refuse again if the budget is still
short, and it is skipped entirely while `dezhban hold` is armed, which is the
point of arming it. One drop still earns at most one automatic window.

**Confirming it is rotation.** `dezhban doctor`'s *learned endpoints* check reads
the store and says which of the two opposite problems you have. "Every learned
address … has aged out" means the addresses were learned and then discarded, and
retaining them longer (`vpn.advanced.learnedEndpointTTL`) removes the
interaction. "… looks like it rotates its server address" means the store is
full of addresses seen for the first time recently, so retaining more only
delays the problem — name the server by **hostname** instead, which dezhban
re-resolves on `vpn.endpointRefresh` and follows the rotation rather than
chasing it:

```sh
dezhban vpn add work --endpoint vpn.example.net
```

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
dezhban status | grep "control socket"      # want: reachable — routine ops need no password
```

Starting the daemon repairs the mode automatically (`state.EnsureDir`). To fix it
without a restart:

```sh
sudo chmod 755 /var/db/dezhban
```

The open directory leaks nothing: the sensitive files inside it (`command.json`,
`pf.state`) are `0600`.

## The installer upgraded the binary but the service is still running the old version

Expected when the daemon's posture wasn't safe to restart through at the
moment `scripts/install.sh` ran: FULL BLOCK, a guard holding a downed tunnel,
or an open switch/redial/pause window. The new binary is on disk and installed
either way — only the *restart* was skipped, so the old build keeps enforcing
uninterrupted rather than the install racing a restart through an unsafe
posture (see [upgrade.md](upgrade.md) for why that specific gap matters).

```sh
dezhban upgrade can-activate     # names the current refusal reason, if any
dezhban status                   # confirm the posture that's blocking it
sudo dezhban restart             # once the posture clears
```

There is no override — this is the same rule `dezhban upgrade apply` enforces
for its own restart, and `sudo dezhban restart` is already the deliberate,
by-name escape hatch for an operator who wants to force it anyway.

## dezhban says its firewall rules went missing and were re-applied

Symptom (from the daemon log):

```
msg="dezhban's firewall rules are MISSING — something removed them; re-applying now" posture=guard repairs=1
```

**Cause.** Every other rule change dezhban makes is triggered by something it
itself did — a tunnel change, an endpoint refresh, a posture flip. This message
means something else removed the rules: another firewall tool, `pfctl -F all`
/ `nft flush ruleset` run by hand, an OS-level firewall reset, or a
misbehaving script. Enforcement verification (`vpn.advanced.verifyInterval`,
default `1m`) noticed the gap on its next check and closed it immediately.

**This is not a leak that happened** — verification found the gap and repaired
it before you saw this message, not after. What it tells you is that *something
on this host keeps removing dezhban's rules*, which is worth tracking down
regardless: a `repairs` count that keeps climbing across restarts means it is
recurring, not a one-off.

```sh
dezhban status --json     # state.verify — At, Missing/Err, Repairs
dezhban doctor            # the "enforcement liveness" section
```

**Fix.** Find and stop whatever else is touching the firewall — a competing
security tool, a system firewall reset on network change, a cron job. If you
need to intentionally flush rules for testing, expect dezhban to notice and
repair within one `verifyInterval` — that is the feature working, not a bug to
route around. Setting `vpn.advanced.verifyInterval: "0"` disables the check
entirely; do this only if you understand you are giving up the one signal that
would otherwise catch a rules-removed-from-outside gap.

**`sudo dezhban panic` is the one deliberate exception.** Tearing down the
rules on purpose is still "the rules going missing" from a running daemon's
point of view, so `panic` leaves behind a marker telling that daemon's
enforcement verification to stand down instead of re-applying the posture you
just asked it to remove — otherwise the two features would fight, and
verification would win within a minute of every `panic`. The marker clears
automatically the next time you `dezhban unblock` (or restart the daemon,
which re-applies the initial posture on its own anyway), so verification
never stays suspended longer than it takes to explicitly resume enforcement.

## dezhban says my VPN might be hung (zombie tunnel)

Symptom (from the daemon log, or in `dezhban doctor`):

```
msg="tunnel interface reports up, but exit lookups through it keep failing — it may need reconnecting; guard holds either way" checks=2
```

**Cause.** The tunnel interface still looks up to the OS, but a run of
exit-country lookups through it have failed — the same signal count as
`hysteresis`. dezhban's posture never escalates on a lookup failure alone (an
unknown country **holds**, it never flips — see
[glossary § Fail closed](../concepts/glossary.md#mechanism)), so a hung tunnel
stays correctly cut, but until this check existed it explained itself to no
one and recovered only if a person noticed and ran `dezhban switch` by hand.

This has one important false-positive case: an exit that **censors the geo
providers** produces the identical symptom — interface up, lookups failing —
on a tunnel that is working perfectly. That is why this diagnosis alone never
opens a redial window; see below.

```sh
dezhban status --json     # state.zombie — Since, Checks
dezhban doctor            # the "enforcement liveness" section
```

**Fix.** Reconnect your VPN client. If it happens repeatedly with the same VPN,
consider `vpn.advanced.livenessRedial: true` to let a confirmed streak open an
automatic redial window through the same budget/backoff machinery an ordinary
drop uses (off by default — see
[ADR-0010](../adr/0010-tunnel-liveness.md) for the censoring-exit trade-off
before turning it on).

## A second `dezhban run` refuses to start

```
another dezhban is already running (holds /var/db/dezhban/dezhban.lock) — see `dezhban status`
```

**Cause.** `run` takes an exclusive lock over the state directory for its
entire lifetime, so a second daemon process — started by hand, via
`--no-daemon`, or by accident alongside the service — cannot start and race the
first to apply firewall rules. This is expected and correct: only one process
may own `Backend.Apply` at a time.

```sh
dezhban status               # confirm the already-running daemon's posture
sudo dezhban restart         # restart the ONE daemon, rather than starting a second
```

The lock is released by the OS the moment the holding process ends, by any
means — a crash, `SIGKILL`, a clean stop — so it never survives past the
process it belonged to; there is no stale-lock case to clean up by hand. The
lock file itself is root-owned and `0600`, so an unprivileged local user
cannot hold it open to keep `run` from starting. If this refuses and
`dezhban status`/your process manager show nothing running, look for the
daemon in a genuinely stuck (not dead) state rather than assuming a leftover
lock file — `sudo dezhban panic` removes firewall rules without needing this
lock at all, regardless of what is holding it.

This lock is a safety net around a race, not part of the kill switch itself:
if acquiring it fails for any reason OTHER than the contention above — an
unwritable or missing state directory, for example — `run` logs a warning and
starts anyway, without the single-instance guard for that run, rather than
refusing to enforce. A lock that cannot be established must never become a
reason the guard does not arm.

## Preview rules before applying them

Never find out what a block does by getting locked out — render the exact
ruleset first, no root, no side effects:
[modes.md § Preview any ruleset](../concepts/modes.md#preview-any-ruleset-without-applying-it).

## Config won't load

```sh
dezhban validate --config <config>     # prints the precise validation error
```

See [config.md](config.md) for every field and its constraints.
