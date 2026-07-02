# Troubleshooting

## I'm locked out — no network after a block

dezhban is fail-closed: a crashed `run`, a misconfigured guard, or a stale VPN
endpoint leaves the block-all rule in place by design (the kill switch must not
fail open). The escape hatch removes dezhban's rules with no daemon involved:

```sh
sudo dezhban panic      # or: make panic
dezhban status
```

`panic` only touches rules tagged `dezhban` (the pf anchor / nft table / WFP
sublayer), so it is always safe and a no-op on a clean system. After it runs,
connectivity is restored. Then fix the cause below before re-enabling the guard.

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
  → tunnel drops, can't reconnect (its path to the server is cut)
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
sudo make reinstall                       # tear down + reinstall the service
```

### Note for NetworkExtension VPNs (macOS)

Some macOS VPN clients (Lightway/RocketTunnel, WireGuard-go, Xray/V2Box) run their
transport inside a system extension and bind it directly to the physical
interface. `route get <endpoint>` will show such an endpoint going via the tunnel
even when it's correct — that's why dezhban's check uses **subnet containment**,
not a route probe, and why `--discover` reads live sockets instead. The pf rule
still matches the provider's physical-side socket, so a correct **public**
endpoint works even though `route get` is misleading.

## Preview rules before applying them

Never find out what a block does by getting locked out. Render the exact ruleset
first, no root, no side effects:

```sh
dezhban print-rules --mode guard --config <config>     # or: make rules MODE=guard
dezhban print-rules --mode fullblock --config <config>
dezhban print-rules --mode legacy --config <config>
```

## Config won't load

```sh
dezhban validate --config <config>     # prints the precise validation error
```

See [config.md](config.md) for every field and its constraints.
