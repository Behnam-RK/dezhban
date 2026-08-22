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

So a label never shows a config key (`vpn.autoDetect`), a serialised form
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
the machinery rather than the action; say "Guard down" (the app) or "Turn off the guard"
(prose).

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
state before a tunnel has ever been observed. User-facing: "Standby — nothing is being
blocked." Icon is **grey** — nothing is being cut, so nothing is red. See
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

**Redial budget** — the rolling allowance of total redial-window time
(`vpn.advanced.redialBudget`, per `vpn.advanced.redialBudgetWindow`) that
trigger two spends from. It bounds how much a *series* of drops can cost, which
the per-window cap alone cannot: `redialWindowMax` bounds one window, the budget
bounds all of them. Debited when a window opens and credited back when it closes
early, so it measures exposure taken rather than exposure offered. When it is
spent the guard simply holds. Belongs to trigger two alone — never shared with
the manual window or pause, for the same reason their caps are not shared. See
[ADR-0009](../adr/0009-redial-budget.md). User-facing: "the redial budget is
spent". Say **budget**, not "quota" or "allowance", and never "rate limit".

**Backing off** — shortening each successive redial window, and waiting longer
between them, while a tunnel keeps dropping faster than
`vpn.advanced.redialMinUptime`. It **shortens**; it does not suppress. Say
"backing off" or "a shorter window", never "the flap guard" or "suppressed" —
those name the behaviour ADR-0009 replaced, in which a struggling VPN got no
automatic help at all.

**Pause** — a switch window opened by an explicit operator command
(`dezhban pause`, or the app) to deliberately use the real ISP IP for a domestic-
only service, not to connect a VPN. Trigger three, capped by its own
`vpn.pauseMax` — never `switchWindowMax`. A genuinely distinct trigger, not
just different copy on trigger one: its own config key, its own control-socket
gate (`control.allowPauseOps`), and `switch --cancel` refuses to touch it (use
`resume`). See [ADR-0008](../adr/0008-arm-at-boot.md). User-facing: "Paused —
the guard re-arms automatically at «time»."

**Hold the line** — an armed intent that the NEXT tunnel drop stays cut: no
redial window opens, so a deliberate disconnect does not get a relaxation nobody
asked for. **Not a fourth trigger.** It is the only thing in this section that
*removes* a relaxation rather than granting one, which is also why it has no
`control.allow*` gate — there is no authority to withhold. One-shot: spent by the
drop it covers, disarmed when a tunnel returns, and gone on restart, so a
forgotten flag can never leave a later accidental drop without redial help.
`dezhban hold` / `hold --cancel` / `hold --status`. User-facing: "The next VPN
drop stays cut." Contrast with Pause, which is its opposite in both directions:
pause means "let me use my real IP", hold the line means "keep me cut".

## Network concepts

**Tunnel interface** — the virtual network interface the VPN creates (`utun4`, `tun0`).
User-facing: "your VPN tunnel" — keep the word *tunnel*, drop the word *interface*. Never
make a user type an interface name to get started; offer Detect.

**Physical interface** — the real network link (`en0`, Wi-Fi, Ethernet). User-facing:
"your normal connection" or "your real connection".

**Egress** — traffic leaving the machine. Technical register only (logs, `--json`, code
comments, these docs) — **never in user-facing copy**. Say what actually happened
instead: "cut" (FULL BLOCK, or a guard holding a downed tunnel), "cut until you unblock"
(a manual block). A menubar title or icon tooltip reading "Egress blocked" is exactly the
drift this page exists to end — say "Traffic cut".

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

**Enforcement verification** — a periodic check (`vpn.advanced.verifyInterval`,
default `1m`) that the firewall rules dezhban believes it installed are still
installed AND still actually enforcing — not just present but disconnected
from what makes them bite (the pf main ruleset no longer referencing our
anchor, an nft chain's policy rewritten off `drop` in place, a Windows profile's
outbound default flipped back to Allow while our rules sit untouched) —
re-applying whatever posture is currently in force — the standing guard, a
full block, or an open switch/redial window or pause — the instant it is not.
Every other rule change is triggered by something dezhban itself did; this is
the only one that notices a ruleset (or the switch that makes it matter)
disturbed from OUTSIDE it — another firewall tool, `pfctl -F all`,
`nft flush ruleset`, an OS ruleset reload — the one failure mode that used to be
completely silent. Reported in `state.verify` (`status --json`) only while
something is wrong. Disablable (`"0"`); an unreadable backend is never treated
as evidence the rules are gone, the same discipline **fail closed** already
applies to an undeterminable country.

**Zombie tunnel** — a tunnel interface that reports up while a run of
exit-country lookups through it has failed. Diagnosis, not a leak: the guard is
already holding exactly as it would for any other unknown reading (see **Fail
closed**). Detection reuses **Hysteresis**'s streak length and is always on,
reported in `state.zombie`. Acting on it — letting a confirmed streak open an
automatic redial window — is a separate, off-by-default key
(`vpn.advanced.livenessRedial`): an exit that censors the geo providers produces
the identical symptom on a tunnel that was never actually down, so relaxing the
guard on this signal is opt-in. See [ADR-0010](../adr/0010-tunnel-liveness.md).

**Preset** — a named bundle of values for the keys that answer "how strict am I"
(the three relaxation windows, poll cadence and hysteresis, the two firewall-pass
toggles, arm-at-boot): **Strict**, **Focused** (redial-only), **Balanced** (the
shipped defaults), **Relaxed**.
Applying one is a write-time macro — it writes those keys through the ordinary
`config set` path, same validation and same live-reload/restart reporting as a hand
edit. The daemon never knows a preset was applied; a config that has since drifted
from all four shows as **Custom**. Presets never touch identity (blocked countries,
tunnel interfaces, endpoints, profiles) — same carve-out as `config reset --all`.
Each preset states its **cost** in plain words beside its summary — see
["Safe"/"Secure" as a preset name](#words-we-do-not-use).

**Policy** — the internal description of what should be enforced. Rendered by a backend
into an actual **ruleset**.

**Ruleset** — the concrete firewall rules for a posture. Preview any of them with
`dezhban print-rules`, which needs no root and changes nothing.

**Backend** — the per-OS firewall implementation (pf on macOS, nft on Linux, WFP on
Windows) behind the `FirewallBackend` interface. Nothing outside that interface may touch
the firewall.

**Control socket** — the unix socket carrying routine commands into the running daemon
without a password prompt.

**Control token** — the secret a client presents to prove a settings change over the
[control socket](#control-socket) is authorised. The daemon keeps only its hash, root-owned;
the holder keeps the token. It raises the socket's bar above filesystem permissions for the
one operation that writes state outliving the daemon. Enrolling again replaces it, which is
how a leaked token is revoked.

**Command file** — the root-owned file carrying operator commands into the daemon. Always
available, root-only, and independent of the socket.

**Panic** — the lockout escape hatch: remove every dezhban firewall rule immediately, as
root, **with no daemon running**. Deliberately not a socket operation, because the escape
hatch must never depend on the thing it is escaping from.

**Launch marker** — the `--background` argument the macOS app's login LaunchAgent
passes and nothing else does, so the app knows macOS started it at login rather
than the user starting it. What the "Open minimized" setting decides on; before
it, the app inferred the launch kind from an AppKit key that read wrong in both
directions ([ADR-0014](../adr/0014-login-item-launch-marker.md)).

**Login agent** — `Contents/Library/LaunchAgents/com.behnam-rk.dezhban.app.login.plist`
inside `Dezhban.app`, registered with `SMAppService.agent(plistName:)`. It is what
starts the app at login, and it exists in place of `SMAppService.mainApp` solely
because a LaunchAgent can pass the **launch marker**. Unlike the login item it
replaced it does not disappear with the bundle, so `uninstall.sh` has the app
retract it.

**Session lock** — an exclusive `flock` the **macOS app** holds for its lifetime,
one per install, so a second copy of the same bundle exits at startup instead of
running a second menubar item, Dock tile and state-file timer. Needed because
registering the login agent starts the app immediately and launchd, unlike
LaunchServices, does not care that it is already running. Distinct from the
**single-instance lock** below, which is the daemon's and guards `Backend.Apply`;
this one guards nothing but the app's own uniqueness.

**Hand-off request** — a file beside the **session lock** by which a copy of the
app that is exiting asks the copy that owns the session to show its window, so a
launch the user performed is never a silent no-op. A file rather than only a
notification because the notification is never queued and the owner may not be
observing yet.

**Single-instance lock** — an exclusive lock `run` holds over the state directory
for its entire lifetime, so a second `run` — with or without `--no-daemon` —
refuses outright instead of racing the first to call `Backend.Apply`. Released
by the OS the moment the process ends, by any means, so a killed daemon never
wedges the next start. `panic`, `unblock`, and the service-lifecycle commands
take no such lock — they are the escape hatch and must stay usable with no
daemon running at all.

## Words we do not use

**This table is machine-read.** `internal/vocab` parses it and fails the build,
so it is not only prose: every double-quoted phrase in the **Don't say** column
becomes a check. Editing a phrase changes what the build enforces. Two markers
say where each row applies, because [the rule](#the-rule) is that the registers
differ in notation — a word banned from a button is often exactly right in a log:

| Marker | Where it is enforced |
|---|---|
| *(none)* | Everywhere the lint looks: user-facing copy **and** the prose in these docs. The word is wrong in both registers. |
| ‡ | **User-facing copy only** — the macOS app's strings, the CLI's human output, `internal/render`. Correct in the technical register, which includes these docs, logs, `--json`, and config keys. |
| † | **Not linted.** The ban needs judgement no string match can supply, so the row is guidance for a reviewer. |

Comments, tests, `--json` field values and struct tags are exempt everywhere:
they are identifiers or notes to developers, not copy.

| Don't say | Say | Why |
|---|---|---|
| "Legacy mode", "country-blocklist mode", "VPN guard mode" | *(nothing)* | There is one mode. See [ADR-0001](../adr/0001-single-guard-mode.md). |
| "Protection" / "protected" / "protecting" / "secured" | "the guard" / "guard active" | One word for one concept. The drift this page ends — wrong in both registers, which is why it carries no marker. |
| ‡ "Stop kill switch" | "Guard down" (app) / "Turn off the guard" (prose) | Name the action, not the machinery. Fine in prose *about* the product. |
| ‡ "Daemon" | "dezhban" / "the background service" | Users do not have daemons. They have a guard. Correct in logs, `--json`, and these docs — never in copy a user reads. |
| "Enable VPN guard (vpn.enabled)" | "Turn on the guard" | Drop the config key, keep the domain word. |
| † "Blocked" for STANDBY | "Standby — nothing is being blocked" | Nothing is blocked in standby, and the icon must agree — but "blocked" is correct in FULL BLOCK, so no matcher can call it. |
| † "Safe" / "Secure" as a preset name | Name the trade | A security tool states costs beside benefits. The words are fine in a sentence and wrong as a label; only a reader can tell which. |
| "Autodetect tunnel interface (vpn.autoDetect)" | "Find my VPN tunnel automatically" | Drop the key and the word *interface*; keep *tunnel*. |
| "Tunnel interfaces (comma-sep)" | "Your VPN tunnel" + token field | Serialised forms are not a UI. |
| ‡ "Egress" | "traffic" / "traffic cut" | *Egress* is a technical word; copy should read to someone who just wants their real IP hidden. It is the right word in these docs and in the code. |
| ‡ "Relaxation" | "window" — "switch window", "redial window" | The mechanism's name, not the user's. A user is told a *window* is open and when it closes; ADRs and architecture docs say "relaxation" freely. |
| ‡ "guard is disarmed", "not enforcing" | "standby" | There is a named posture for this; two more phrases for it is two more things to learn. Phrased narrowly on purpose: *disarm* is the right verb for hold the line, which really is an armed flag — what is wrong is using it for the guard's resting state. |
| † "Peer", "server" (for the address) | "endpoint" / "VPN server address" | One word for the thing the guard must pass. Not linted, and it cannot be: the replacement wording contains "server" itself — what is banned is *server* standing alone for an address, which only a reader can judge. |
| † "utun", "interface" (for the tunnel) | "tunnel" / "your VPN tunnel" | `utun4` is an implementation detail and *interface* is the word this page dropped. Not linted: "physical interface" is correct, and a bare `utun` appears legitimately in examples of what Detect finds. |
