# Glossary

One term per concept, defined once. dezhban currently uses "kill switch", "protection",
"guard", and "daemon" for overlapping ideas across the GUI, the CLI, and these docs,
which makes the product harder to learn than the product actually is. **"Guard" wins** —
see [the rule](#the-rule).

**This file is the authority.** When user-facing copy and this page disagree, the copy is
wrong.

## The rule

**"Guard" is dezhban's own word, and it is shared vocabulary — user-facing copy, config,
JSON, logs, and docs all use it.** It is not jargon to be translated away for beginners:
it is the name of the thing dezhban does, it is already the stable posture identifier,
and a user who learns it once can read the app, the CLI, the state file, the logs, and
these docs without a second dictionary. Teaching one word beats maintaining two
vocabularies that drift apart.

What separates the registers is **notation, not concepts**:

- **User-facing** (GUI labels, CLI human output, notifications, first-run): domain words
  in plain sentences. "Guard active", "your VPN tunnel", "everything is cut".
- **Technical** (config keys, `--json`, state files, logs): the same concepts as exact
  identifiers. `guard`, `tunnelInterfaces`, `posture`.

So a label never shows a config key (`vpn.autodetect`), a serialised form
(`comma-sep`), or a posture constant in shouty caps — but it freely says "guard",
"tunnel", and "endpoint", because those are what the things are called.

## Core terms

**dezhban** — the product. Persian for "gatekeeper". Lowercase in prose, including at the
start of a sentence. Not "Dezhban" except as the macOS app bundle name.

**Guard** — dezhban enforcing. The central term, used everywhere: "the guard is on",
"turn off the guard", "Guard active". Prefer it over "protection" in all copy — see
[the rule](#the-rule). Capitalised as **GUARD** only when naming the posture in a
technical context (`status --json`, state file, logs).

**Protection** — avoid. It was an earlier attempt at a friendlier synonym for "guard",
and having both is exactly the drift this page exists to end. "Protection is stopped"
becomes "The guard is off".

**Kill switch** — what dezhban *is*, in prose. Correct in the README, marketing copy, and
introductory documentation. **Avoid as a UI control label** — "Stop kill switch" names
the machinery rather than the action; say "Turn off the guard".

**Daemon** — the background process (`dezhban run` under launchd/systemd). A technical
term: correct in these docs, logs, and `--json`. **Never in user-facing copy** — say
"dezhban" or "the background service". A user should not need to know a daemon exists to
understand whether the guard is on.

**Menubar app** — the macOS status-bar UI. It displays and commands; it never enforces.
Call it "the app" in user-facing copy. Not "the GUI" outside developer docs.

## Postures

A **posture** is the enforcement state dezhban is in right now — one of the values below.
Appears in `status --json` and the state file. User-facing copy **names the posture and
then explains it in a sentence**, rather than replacing the name with a euphemism.

**STANDBY** — no rules installed, network fully open, the guard is **off**. The resting
state before a tunnel has ever been observed. User-facing: "Guard off — standby. Nothing
is being blocked." Icon is **grey** — nothing is being cut, so nothing is red. See
[ADR-0002](../adr/0002-standby-no-tunnel-posture.md).

**GUARD** — the healthy enforcing state. Only the tunnel may carry traffic off this
machine; everything else on the physical interface is blocked. User-facing: "Guard
active", plus what that means right now. Icon is green.

**FULL BLOCK** — all user traffic is cut because the VPN's exit landed in a blocked
country. GUARD minus the tunnel-egress pass, keeping the endpoint handshake open so the
tunnel can survive and recover. User-facing: "Full block", with the reason. Icon is red.

**SWITCH WINDOW** — the bounded, temporary relaxation. See below.

## Windows

**Switch window** — the bounded period during which the guard is relaxed so a VPN
handshake can complete, or (see Pause below) so the operator can deliberately use
the real IP. **The sanctioned relaxation of the guard**, with exactly three
triggers, each with its own cap. Always bounded, always auto-reverting to the
prior fail-closed posture.

**Manual switch** — a switch window opened by an explicit operator command
(`dezhban switch`, or the app), to connect a brand-new VPN. Trigger one, capped
by `advanced.switchWindowMax`.

**Redial window** — a switch window opened *automatically* when the tunnel drops from
healthy GUARD, so the VPN can redial. Trigger two, capped by
`advanced.redialWindowMax`. Same machinery, same rails; only the trigger and
the cap differ. User-facing: "Your VPN dropped — redialing".

**Pause** — a switch window opened by an explicit operator command
(`dezhban pause`, or the app) to deliberately use the real ISP IP for a domestic-
only service, not to connect a VPN. Trigger three, capped by its own
`vpn.pauseMax` — never `switchWindowMax`. A genuinely distinct trigger, not
just different copy on trigger one: its own config key, its own control-socket
gate (`control.allowPauseOps`), and `switch --cancel` refuses to touch it (use
`resume`). See [ADR-0008](../adr/0008-arm-at-boot.md). User-facing: "Paused —
protection resumes automatically at «time»."

## Network concepts

**Tunnel interface** — the virtual network interface the VPN creates (`utun4`, `tun0`).
User-facing: "your VPN tunnel" — keep the word *tunnel*, drop the word *interface*. Never
make a user type an interface name to get started; offer Detect.

**Physical interface** — the real network link (`en0`, Wi-Fi, Ethernet). User-facing:
"your normal connection" or "your real connection".

**Endpoint** — the VPN server's public address, reached across the physical interface.
The guard must pass it or the tunnel can never connect. User-facing: "VPN server address".
A wrong endpoint is the single most common lockout cause.

**Learned endpoint** — an endpoint dezhban discovered itself during a window, rather than
one you configured. Stored separately in `learned.json` and never written into your
config file.

**Profile** — a named set of VPN settings, so several VPNs can coexist. The guard passes
the union of every profile's endpoints, so switching between known VPNs just works.

**Exit country** — the country your traffic *appears to come from* while the VPN is up,
i.e. where the VPN server is. This is what dezhban checks. Distinct from your real
physical country, which dezhban does not care about.

**Blocked country** — a country you have listed as unacceptable for your VPN to exit
through. User-facing: "Countries your VPN must not exit through". A blocked exit produces
FULL BLOCK, not a warning.

**Geo provider** — a public API dezhban queries to learn the exit country. Passes to
providers are **tunnel-scoped only** — see [ADR-0006](../adr/0006-geo-providers-tunnel-scoped.md),
which explains why the alternative silently breaks the check.

## Mechanism

**Arm / disarm** — the transition between STANDBY and an enforcing posture. dezhban arms
when a tunnel is configured *and* has been observed up at least once.

**Fail closed** — when something is undeterminable, choose the blocking answer. Scoped
carefully in guard mode: the standing GUARD rule is *itself* the fail-closed block, so an
undeterminable country **holds** the current posture. Only a *successful* reading of a
blocked country escalates to FULL BLOCK — escalating on an unknown would cut the tunnel's
own egress and livelock recovery.

**Hysteresis** — the number of consecutive agreeing exit-country readings required before
the posture actually changes (`hysteresis`, default 2). It is what stops one odd reading
flapping the firewall. An undeterminable reading neither commits a change nor cancels one
in progress.

**Confirming checks** — how a hysteresis streak is described to users: "restoring the
guard — 1 of 2 confirming checks". Published in the state file and `status --json` as
`pending`, so `status` and the app say the same thing. Informational only; observing
progress never alters it.

**Accelerated recovery** — after a tunnel comes back up during FULL BLOCK, the exit
country is re-checked every few seconds instead of once per `pollInterval`, until the
streak resolves or a bounded budget runs out. It changes **cadence only** — hysteresis
still gates the change, and it is skipped entirely when checking would require lifting
the guard.

**Policy** — the internal description of what should be enforced. Rendered by a backend
into an actual **ruleset**.

**Ruleset** — the concrete firewall rules for a posture. Preview any of them with
`dezhban print-rules`, which needs no root and changes nothing.

**Backend** — the per-OS firewall implementation (pf on macOS, nft on Linux, WFP on
Windows) behind the `FirewallBackend` interface. Nothing outside that interface may touch
the firewall.

**Control socket** — the unix socket carrying routine commands into the running daemon
without a password prompt.

**Command file** — the root-owned file carrying operator commands into the daemon. Always
available, root-only, and independent of the socket.

**Panic** — the lockout escape hatch: remove every dezhban firewall rule immediately, as
root, **with no daemon running**. Deliberately not a socket operation, because the escape
hatch must never depend on the thing it is escaping from.

## Words we do not use

| Don't say | Say | Why |
|---|---|---|
| "Legacy mode", "country-blocklist mode", "VPN guard mode" | *(nothing)* | There is one mode. See [ADR-0001](../adr/0001-single-guard-mode.md). |
| "Protection" / "protected" / "secured" | "the guard" / "guard active" | One word for one concept. The drift this page ends. |
| "Stop kill switch" | "Turn off the guard" | Name the action, not the machinery. |
| "The daemon isn't running" (in the app) | "The guard is off" | Users do not have daemons. They have a guard. |
| "Enable VPN guard (vpn.enabled)" | "Turn on the guard" | Drop the config key, keep the domain word. |
| "Blocked" for STANDBY | "Guard off — standby" | Nothing is blocked in standby. The icon must agree. |
| "Safe" / "Secure" as a preset name | Name the trade | A security tool states costs beside benefits. |
| "Autodetect tunnel interface (vpn.autodetect)" | "Find my VPN tunnel automatically" | Drop the key and the word *interface*; keep *tunnel*. |
| "Tunnel interfaces (comma-sep)" | "Your VPN tunnel" + token field | Serialised forms are not a UI. |
