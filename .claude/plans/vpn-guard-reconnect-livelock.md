# Fix VPN-guard reconnect livelock (undeterminable country → FULL BLOCK)

## Context

A live recovery test (config: `vpn.enabled=true`, `blockedCountries=[IR]`,
`failClosed=true`, `hysteresis=3`, `autoDiscoverEndpoints=true`) failed: after
disconnecting and reconnecting RocketTunnel, **neither the VPN nor the internet
recovered until `Ctrl+C`** stopped the daemon (which ran `Cleanup()` and removed
all rules). This is a reconnect **livelock**, not an endpoint misconfig — the
`utun4` interface *does* come back up during the episode.

### Root-cause chain (confirmed from the code)

1. `isTunnelIface` (`internal/netdetect/netdetect.go:92`) / `liveSample`
   (`watch.go:123`) report `utun4` **"up"** as soon as it exists + has a
   global-unicast address — *before* RocketTunnel's session is actually routing
   / DNS-ready.
2. Because the watcher says "up", the tunnel-down geo-skip in `runVPN`
   (`runner.go:397`) is bypassed and the geo step runs during warmup.
3. DNS through the not-yet-healthy tunnel fails (`"no such host"`). With
   `failClosed=true` + `hysteresis=3`, `Decider.raw` maps each lookup error →
   `Block` (`decision.go:86-92`) and after 3 in a row commits **FULL BLOCK
   country=""** (22:27:45 in the log).
4. FULL BLOCK drops the `pass out on { utun4 }` rule (`pf_darwin.go:217-223`),
   cutting the tunnel's own egress → RocketTunnel can't pass keepalives → it
   tears the tunnel down (22:27:48).
5. `blocked=true` → each tick runs the recovery **probe** (`runner.go:480`):
   briefly lifts the guard (~8s), one failing lookup, re-cut. The brief window
   flickers `utun4` up (22:27:55) but the re-cut kills it → **livelock** until
   `Ctrl+C`.

The core defect: **in guard mode an *undeterminable* country escalates to FULL
BLOCK, which cuts tunnel egress and prevents the very reconnect it is waiting
for.** FULL BLOCK's real job is to cut a *confirmed* forbidden exit (IR), and
confirming that requires a *successful* lookup.

### Decisions taken (user-confirmed)

- **Hold GUARD on unknown.** In VPN guard mode a lookup failure holds the current
  posture; only a *successful* reading of a blocked country (IR) triggers FULL
  BLOCK. Accepted tradeoff: if the tunnel exit really is IR *and* every geo
  provider is unreachable through it, traffic keeps flowing through the tunnel
  exit (the guard still blocks all physical leaks). This intentionally disables
  fail-closed→FULL-BLOCK *in guard mode only* (the guard itself is the
  fail-closed mechanism for physical leaks).
- **Open physical DNS (opt-in).** Add a guard/full-block `pass out … port 53`
  rule, behind a new `vpn.allowPhysicalDNS` flag (default **false** = strict), so
  a VPN client that re-resolves its server hostname on reconnect can do so while
  the tunnel is down. Accepted tradeoff: DNS-query metadata leaks to the physical
  path while the tunnel is down.

---

## Fix 1 — Hold GUARD on a lookup error in VPN mode (primary; breaks the livelock)

**File:** `internal/runner/runner.go`, `vpnGeoStep` (line ~423).

After observing (`probe` while blocked, else `Monitor.Once`), if `res.Err != nil`
log the existing `"country lookup failed"` warning and **return early without
calling `Decider.Evaluate`** — hold the current posture. Only successful readings
drive the GUARD↔FULL BLOCK state machine.

```go
if res.Err != nil {
    o.Log.Warn("country lookup failed", "err", res.Err)
    // Undeterminable country: hold the current posture. The standing guard
    // already blocks physical leaks, so an unknown must not escalate
    // GUARD→FULL BLOCK (which cuts tunnel egress and livelocks the reconnect)
    // nor lift an active FULL BLOCK on a blip. Only a *successful* reading
    // moves the state machine. (This makes failClosed a no-op in VPN mode —
    // the guard is the fail-closed block; documented in decision.go / docs.)
    return res, enfErr
}
switch o.Decider.Evaluate(res) { /* unchanged */ }
```

Notes:
- Keep the observe/probe call *before* the early return so the probe still
  performs its lift → re-cut each blocked tick (recovery keeps trying) and a
  failed re-cut is still surfaced via `enfErr`.
- The startup observation (`runner.go:349`) already tolerates an error → stays in
  GUARD; unchanged.
- `Decider` itself is **not** modified — no `decision.New` signature change (it
  has ~15 call sites), so legacy/fallback fail-closed behavior is untouched.

**Tests** (`internal/runner/runner_test.go`): add `TestVPNHoldsGuardOnLookupError`
— script `Once` to return errors while guarding; assert **no** `apply-fullblock`
(guard held). Add a blocked-state variant: errors during probe hold FULL BLOCK
(don't lift). Existing VPN tests (`TestVPNGuardFullBlockAndProbeRecovery:359`,
`TestVPNProbeRespectsHysteresis:414`) drive only successful `IR`/`US` readings, so
they remain valid — confirmed forbidden readings still escalate.

---

## Fix 2 — Optional physical DNS egress in guard / VPN full-block (recovery aid)

New opt-in flag `vpn.allowPhysicalDNS` (default `false`). When set, the guard and
VPN-full-block rulesets add a DNS pass so hostname resolution works on the
physical link while the tunnel is down.

Plumbing (mirrors the existing `autoDiscoverEndpoints` / `tunnelWatch` fields):

- `internal/config/config.go`
  - `fileVPN` (line ~97): add `AllowPhysicalDNS bool `json:"allowPhysicalDNS"``.
  - `VPN` struct (line ~30): add `AllowPhysicalDNS bool` with doc comment.
  - fileVPN→VPN conversion (line ~186): `AllowPhysicalDNS: fc.VPN.AllowPhysicalDNS`.
  - VPN→fileVPN round-trip (line ~230): mirror the field.
- `internal/firewall/backend.go`: add `Policy.AllowPhysicalDNS bool` (line ~42).
- `internal/runner/runner.go`: add `Options.AllowPhysicalDNS bool`; set it on
  both policies in `vpnPolicies` (line ~410).
- `cmd/dezhban/main.go`: set `AllowPhysicalDNS: cfg.VPN.AllowPhysicalDNS` in
  `assembleOptions` (line ~328) **and** in `policyForMode` (line ~950) so
  `print-rules` / `doctor` reflect it.
- Renderers — emit the rule in `ModeGuard` and the VPN `ModeFullBlock` branch when
  `p.AllowPhysicalDNS`:
  - `internal/firewall/pf_darwin.go` `renderRuleset` (line ~216):
    `pass out quick proto { udp tcp } to any port 53 no state`
  - `internal/firewall/render_linux.go` (nft) and `render_windows.go` (WFP) —
    equivalent port-53 allow, for cross-platform parity.

Scope choice: **`to any` port 53** (not scoped to `allowlist.dns`) so resolution
works regardless of which resolver the VPN/system uses on reconnect; the residual
leak is DNS-metadata-only and gated behind the default-off flag.

**Tests:** extend `pf_darwin_test.go` (+ `nft_linux_test.go`, `wfp_windows_test.go`)
to assert the port-53 pass appears in guard/full-block **only** when
`AllowPhysicalDNS` is set, and is absent otherwise.

---

## Docs

- `docs/troubleshooting.md` — under the existing "tunnel dies, DNS fails" section,
  add the warmup-livelock case and note the new hold-on-unknown behavior + the
  `vpn.allowPhysicalDNS` recovery aid.
- `docs/config.md` — document `vpn.allowPhysicalDNS`.
- `docs/modes.md` — note guard mode no longer FULL-BLOCKs on an undeterminable
  country (only on a confirmed blocked exit); show the optional DNS line.
- `configs/dezhban.vpn-guard.json` — add `"allowPhysicalDNS": false` with a
  comment, so the sample documents the knob.
- Update the `CLAUDE.md` "fail-closed" invariant wording to scope it: fail-closed
  applies to the **fallback** model; in **guard** mode the standing guard is the
  fail-closed block and an undeterminable country holds GUARD.

---

## Verification

1. `go build ./...` && `go vet ./...` && `go test ./...` (new + existing pass).
2. `make rules MODE=guard CONFIG=configs/dezhban.vpn-guard.json` — confirm the
   `port 53` pass appears with `allowPhysicalDNS:true`, absent with `false`.
3. **Live repro (the original test), macOS, real RocketTunnel:**
   `sudo dezhban run` with the vpn-guard config → disconnect RocketTunnel →
   reconnect. Expect: tunnel **and** internet recover **without** `Ctrl+C`, and
   the log shows GUARD held during warmup — **no** `FULL BLOCK country=""`. A
   genuine forbidden exit (simulate via `--sim-country IR` or an IR exit node)
   must still produce `FULL BLOCK country=IR`.
4. Confirm `dezhban panic` / `Cleanup()` still fully restore connectivity.
