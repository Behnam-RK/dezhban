# Usage

```
dezhban [-v] <command> [flags]

Commands:
  run          Run the monitor→decision→enforcement loop          (root)
  block        Manually block network egress                      (root)
  unblock      Remove dezhban's firewall rules                    (root)
  status       Show version, config, service, and block state (--json for tooling)
  validate     Load + validate a config file (no root, no effects)
  print-rules  Print the ruleset a block/guard would apply, without applying it
  doctor       Diagnose VPN guard config (tunnels, endpoints, lockout risks)
  monitor      Live read-only view: IP, country, tunnel state, endpoints, verdict
  panic        Force-remove dezhban's rules even with no daemon   (root)
  install      Register dezhban as a boot-persistent OS service   (root)
  uninstall    Remove the OS service                              (root)
  start        Start the installed service                        (root)
  stop         Stop the installed service (removes firewall rules) (root)
  restart      Restart the installed service — apply a config change (root)
  detect-vpn   Print detected VPN tunnel interfaces for config
  switch       Open a bounded window to connect a brand-new VPN    (root)
  pause        Open a bounded pause: real ISP IP for a while, then re-arms   (root)
  resume       End an open pause early                             (root)
  vpn          Manage VPN profiles and learned endpoints (list/add/remove/import/promote/forget)
  setup        Interactive wizard to create or update the config
  config       Inspect or change the config without hand-editing JSON
  token        Enroll/remove the control token authorising password-free config changes (root, except status)
  completion   Print a shell completion script (bash|zsh|fish)
  upgrade      Check/download/apply a newer release (check: no root; download/apply: root, macOS)
  version      Print the version
  help         Print usage (also: --help, -h)

Global: -v / --verbose   override the configured log level to debug
        --no-sudo        opt out of auto-elevation (or DEZHBAN_NO_SUDO=1)
        --no-daemon      skip the control socket, act on the firewall directly (or DEZHBAN_NO_DAEMON=1)
```

`--config` is **optional**: when omitted, dezhban resolves the config from
`$DEZHBAN_CONFIG`, then the canonical system path (`dezhban config path` prints
it), then built-in defaults. So `dezhban run` / `monitor` / `validate` normally
need no path at all.

## Do I need a password?

Mostly, no. Once the daemon is running, the commands you use day to day go **to the
daemon** over its control socket and need no password at all — provided
`control.group` is set, which it is by default on macOS but not on Linux; see
[passwordless.md](passwordless.md) to turn it on there:

| Command | Needs a password? |
|---|---|
| `block`, `unblock`, `switch`, `pause`, `resume` | **No** — the running daemon performs them (see [config.md](config.md#control-block)). Only if no daemon is listening do they fall back — `block`/`unblock` act on the firewall directly; `switch`/`pause`/`resume` write the root-owned command file, which itself needs a running daemon to consume it. Either way, root. |
| `status`, `validate`, `print-rules`, `doctor`, `monitor`, `detect-vpn` | **No** — read-only, no root, no firewall effects. |
| `install`, `uninstall`, `start`, `stop`, `restart` | Yes — a daemon can't install, start, or stop itself. Rare (install-time). |
| `panic` | Yes — deliberately independent of the daemon, so the lockout escape hatch works when nothing else does. |
| `run` | Yes — it *is* the daemon. |
| `setup`, `config set`/`edit`, `config preset apply` | Yes, but only for the config write itself. `preset apply` is a write like any other — see [Presets](#presets). |
| `config show`/`path`/`schema`, `config preset list`/`show`/`diff`, `setup --questions` | **No** — read-only; they report the config (or what the wizard would ask), they don't change it. |
| `token status` | **No** — reports whether a control token is enrolled; the answer is not itself a secret. |
| `token enroll`, `token forget` | Yes — the token's hash lives in the daemon's root-owned state dir, because anything that could rewrite it could nominate its own token. Once, at setup. |
| `upgrade check`, `upgrade can-activate` | **No** — read-only, no root. `can-activate` reports whether a restart could activate right now — the same gate `apply`'s activation step uses (see [upgrade.md](upgrade.md)); `scripts/install.sh` checks it before restarting a running daemon. |
| `upgrade download`, `upgrade apply` | Yes — root, macOS only. `download`'s staging directory is root-owned on purpose: a writable-by-anyone staging area would let a local user swap the verified `.pkg` before `apply` installs it. |

`dezhban status` prints a `control socket:` line saying which mode you're in.

### Touch ID

Touch ID for privileged ops — CLI and menubar app alike — comes from **`sudo` +
`pam_tid`**, which you enable once yourself (macOS 14+):

```sh
sudo sh -c 'echo "auth       sufficient     pam_tid.so" > /etc/pam.d/sudo_local'
```

That's a change to your system's `sudo` configuration, not to dezhban — it applies
to every `sudo` you run, and survives OS updates (unlike editing `/etc/pam.d/sudo`
directly). `dezhban doctor` reminds you when it isn't set up.

With it in place, the **CLI**'s auto-elevation (`dezhban start` and friends) shows
the Touch ID prompt in the terminal, and the **menubar app** authenticates its
privileged actions (start, stop, install/uninstall, panic, config writes) through
the same mechanism — the system Touch ID HUD — with `sudo`'s timestamp cache making
a second action a moment later silent.

Without `pam_tid`, the app falls back to **Authorization Services** (the API behind
the System Settings padlock; in practice its `system.privilege.admin` prompt is
password-only — SecurityAgent does not offer biometrics for that right, which is
why the app prefers the sudo path), caching the authorization for the life of the
app; and as a last resort, the legacy `osascript` dialog — also password-only. A
cancelled Touch ID (or a closed lid, where the sensor is unavailable) falls
through to the password dialog rather than dead-ending.

When a command does need root and you're on an interactive terminal on unix,
dezhban **auto-re-runs itself under `sudo`** — so you rarely type `sudo` yourself.
Pass `--no-sudo` (or `DEZHBAN_NO_SUDO=1`) to opt out and get the plain "must run as
root" error; on Windows, and when there's no terminal (CI/pipes), it never
auto-elevates. Pass `--no-daemon` (or `DEZHBAN_NO_DAEMON=1`) to skip the control
socket and act on the firewall directly — the escape hatch for a wedged daemon.
That escape hatch is exactly the case `run` guards against contending with a
still-running service: it takes an exclusive lock over the state directory for
its whole lifetime, so a second `run` — with or without `--no-daemon` — refuses
immediately ("another dezhban is already running") instead of racing the first
to apply firewall rules. `panic`, `unblock`, and the service-lifecycle commands
take no such lock; they are the recovery path and must stay usable with no
daemon running at all.

A manual `block` **holds**: the daemon suspends its geo state machine until you
`unblock`, so an allowed country won't quietly undo what you asked for.

`status --json` embeds the daemon's last published snapshot under `state`,
verbatim. **Check `stateStale` before trusting it.** A crashed or `SIGKILL`ed
daemon leaves its last posture on disk indefinitely, so `state.posture` alone
will report a host as guarded long after enforcement stopped; `stateStale` is
`true` once the snapshot ages past 3× the poll interval (floored at 90s), which
is the same threshold the prose `status` uses to print "Stopped" instead and the
menubar app uses to grey its icon. It is always present, so its absence means
you are reading something other than this CLI's output — never "the snapshot is
fresh".

`preset`/`presetExact` report the strictness preset the config matches
(`presetExact: true`), or — for a drifted, "Custom" config — the *nearest*
preset with `presetExact: false`, the same default target `config preset diff`
picks. `state.ipv6` (when present) is the last observed public IPv6 address
from a separate best-effort lookup; observational only, never used for country
decisions, and absent on v4-only hosts or from older daemons.

`state.redial` is present only when an automatic redial window was **refused**
for the drop currently being carried, and it is how a script tells "the VPN has
not come back yet" from "dezhban will not let it try again until 3:15PM". It
carries `reason` (`"cooldown"` while backing off after fast drops, `"exhausted"`
when the rolling budget is spent — stable identifiers, match on them rather than
displaying them), `nextEligible`, `remainingSeconds` of budget, and `fastDrops`.
An open window is reported by `state.switch` instead, never here. The sentence a
person should read is already composed in `state.display.detail`.

`nextEligible` is the earliest instant a window *could* open. dezhban re-takes
the decision when that instant arrives, so a refused drop gets its window once
the bound lifts without needing the tunnel to drop again — which matters most
when the tunnel cannot come back on its own. It is still a **bound, not a
promise**: the re-decision may refuse again (the budget is consulted afresh), and
the preconditions are re-checked, so a script should read it as "nothing before
this time, and an attempt at it", never as "a window at this time".
`state.display.detail` words it the same way ("dezhban tries again at 3:15PM —
no window opens before then").

It answers for **both** bounds, not just whichever refused first: a host that is
backing off *and* out of budget reports the later of the two, so the instant does
not move when the next drop arrives. The key is **omitted** when the writer had
no instant to give — never published as a zero timestamp, which every reader
would have to special-case. Treat absent as "no time known" and say nothing.

A `nextEligible` in the past means the re-decision has already run and refused
again without naming a new time, or that nothing could be scheduled; treat it as
"the bound has lifted, waiting for the VPN to try again", which is what
`state.display.detail` then says in place of a time that has gone by.

A refused drop still gets **at most one** automatic window: the re-decision stops
once a window is granted, and an expired window never re-opens.

`remainingSeconds` is **not** stale in that way: unlike `reason` and
`nextEligible`, which are the decision and stay as decided, it is re-read from
the ledger on every snapshot. Episodes roll out of the rolling period while the
cut lasts, so it grows back on its own and a script can watch it recover. What it
does not do is *cause* anything — the budget is still only consulted on a
tunnel-down edge, so watching it reach a full window tells you a window would be
granted, not that one is coming.

`state.verify` is present only when enforcement verification (`vpn.advanced.verifyInterval`)
last found something wrong — the firewall rules dezhban believes it installed
were missing, or the backend could not be read at all. `missing: true` means
they were found gone and have already been re-applied (`repairs` is the
cumulative count since startup — a number that keeps climbing means something on
this host is repeatedly removing dezhban's rules). An `err` string instead means
the backend itself could not be read; that is **not** treated as evidence the
rules are gone, so `missing` stays false and nothing is re-applied — the same
discipline as an undeterminable exit country holding the current posture. Absent
means the last check was clean, or verification is disabled (`"0"`) — the two
look the same here; `pollIntervalSeconds`-scale staleness rules apply the same
way they do to the rest of the snapshot.

`state.zombie` is present only while a run of exit-country lookups has failed
through a tunnel that still reports up — `checks` is the streak length, `since`
when it started. This is **diagnosis, not a leak**: the guard is holding exactly
as it would for any other unknown reading. Absent means either nothing is wrong
or the tunnel is plainly down instead (a different, already-explained state —
see `state.drop`). Whether this can also open an automatic redial window is
controlled by `vpn.advanced.livenessRedial` (default off); see
[ADR-0010](../adr/0010-tunnel-liveness.md) for why that default matters — an exit
that censors the geo lookup produces the identical symptom on a tunnel that was
never actually down.

`state.exitIpChangedAt` is set the first time the observed exit IP differs from
the previous successful reading, and stays set (it is not cleared by a later
unchanged reading). Purely observational: it never affects `blocked`,
`countryCode`, or `pending` — a failover between two servers in the same allowed
country changes nothing those fields report, but changes this. Absent means no
change has been observed since the daemon started, not that the exit has never
had an IP.

```sh
dezhban status                                    # config + service + block state
dezhban status --json                             # machine-readable (merges the state file)
dezhban run --dry-run                             # poll & print country, no firewall
sudo dezhban run --config /etc/dezhban/dezhban.json

# manual block / override
dezhban block        --config configs/dezhban.example.json
dezhban block        --force                      # cut ALL egress, ignore detection
dezhban unblock
sudo dezhban panic                                # standalone teardown, no daemon needed
```

## Key flags

- `run --dry-run` — poll and print the country without touching the firewall.
- `block --guard` — install the VPN interface guard (see [modes.md](../concepts/modes.md)).
- `block --force` — unconditional hard block of all egress (loopback + the
  geo-provider pass only), bypassing the VPN guard. The override when detection
  is wrong. The provider pass obeys `vpn.allowGeoProviders` and is scoped to the
  tunnel interface **and** the provider addresses, exactly as in the daemon's
  FULL BLOCK ([ADR-0006](../adr/0006-geo-providers-tunnel-scoped.md)) — so with
  the key off, or with no tunnel interface resolved to scope the rule to,
  `--force` cuts everything but loopback. It has no lift-and-probe fallback;
  recover with `unblock` or `panic`.
- `unblock --force` — accepted for symmetry (`unblock` is already unconditional).
- `--simulate-country IR` (on `monitor` and `run`) — force the verdict from
  anywhere, without a sanctioned IP.

## Diagnose & test safely (no root)

Inspect and validate before you risk a block — none of these touch the firewall:

```sh
dezhban validate    --config <config>                 # parse + validate, summarize
dezhban print-rules --mode guard --config <config>    # exact ruleset, not applied
dezhban doctor      --config <config>                 # tunnels, subnets, endpoint sanity
dezhban doctor --discover --config <config>           # macOS: find the VPN's real server IP
dezhban doctor --json --config <config>               # the same checks as structured JSON
dezhban monitor     --config <config>                 # live: IP, country, tunnels, endpoints, verdict
```

`monitor` streams the live state the decision rests on; add `--once` for a single
snapshot. `print-rules --mode` takes `guard`, `fullblock`, or `switch`. `doctor
--json` prints the identical findings `doctor` reports in prose — `{checks:
[{name, status, summary, details, fixes}], ok}` — for a consumer (the macOS
app's Diagnostics pane) that needs to render them itself rather than parse
text. See [config.md](config.md) for the full field reference and
[troubleshooting.md](troubleshooting.md) for the lockout-recovery runbook.

`detect-vpn --json` is the machine-readable VPN inventory the app's
Diagnostics pane renders: `{tunnels, connectedVPN, discoverySupported,
scanPrivileged, candidates: [{vpn, server, port, process}], discoveryErr,
supportedVPNs, tunnelPatterns: {prefixes, keywords}}`. `tunnels` is the same
interface scan the prose prints; `candidates`/`connectedVPN` come from the
macOS discovery layer (empty elsewhere — `discoverySupported: false` says why);
`supportedVPNs` and `tunnelPatterns` name the client-process and
interface-name patterns detection recognizes, so an *unrecognized* VPN can be
told apart from a *missing* one. A discovery failure degrades to
`discoveryErr` plus an empty `candidates` — the tunnel scan is still
delivered.

`scanPrivileged` says whether the scan could see the whole machine, and has
three states: **absent** (discovery did not run), **`false`** (partial), and
**`true`** (authoritative). Discovery shells out to `lsof`, which run as an
unprivileged user lists only *that user's* sockets — so a VPN whose transport
runs as root is invisible, and `candidates: []` from such a scan is not
evidence of absence. The app always runs unprivileged and therefore always sees
`false`; run `sudo dezhban detect-vpn --json` (or `sudo dezhban doctor
--discover`) for the authoritative answer.

Beyond the lockout checks, `doctor` answers **will dezhban need me again**:
whether a reboot brings the guard back (*boot service*, *arm at boot*) and
whether a VPN drop can redial on its own (*learned endpoints*). Those three are
informational — they never change the exit code, because none of them is a
guard about to fail closed — but they are where the "I have to turn it on
again" and "every drop needs a manual window" complaints get diagnosed. The
boot-service check reads the service unit rather than asking the service
manager, so it stays truthful without root: on macOS an unprivileged status
query cannot see the system domain and reports a running daemon as absent.

## Create & manage the config

You rarely need to touch JSON by hand. See [config.md](config.md#where-the-config-lives)
for where the file lives and the resolution order.

```sh
sudo dezhban setup                 # interactive wizard — builds/updates the config,
                                   # detects tunnels, previews the ruleset, then writes it
dezhban config path                # print the resolved config path
dezhban config show                # print the effective config as JSON
dezhban config schema              # describe every settable key (add --json for machine output)
dezhban config get blockedCountries
sudo dezhban config set blockedCountries IR,RU   # set, validate, save
sudo dezhban config reset vpn.switchWindow       # restore a shipped default (--all: every tunable)
sudo dezhban config set vpn.tunnelInterfaces=utun4 \
     vpn.autoDiscoverEndpoints=true                # several keys, one atomic write
sudo dezhban config edit           # open the config in $EDITOR, re-validated on save
```

`config set` takes either one `<key> <value>` pair or any number of `key=value`
pairs. The multi-pair form applies them all to one in-memory config, validates
**once**, and writes **once** — so there is no ordering to get right (a key whose
validity depends on another, like an endpoint alongside its profile, can come
first) and no half-applied config if one value is rejected. It is also one privileged write, i.e.
one password prompt instead of one per key; the macOS app's VPN Guard pane uses it
for exactly that reason.

After a successful write, `config set` (and `config reset`) asks a running daemon
to re-read its configuration, and reports what that achieved:

```
set pollInterval = 20s  (/etc/dezhban/config.json)
Saved and applied: pollInterval
Restart dezhban to apply: logLevel
```

Most keys take effect immediately. A few cannot, because the daemon built
something from them before its run loop started — the logger, the geo providers,
the control socket, the tunnel watcher, arm-at-boot. Those are named explicitly
rather than applied silently, so a setting is never reported as in force while
the old value is still being enforced. With no daemon running, the write still
succeeds and says so; the new values are read the next time it starts.

`setup` needs an interactive terminal and reuses the same tunnel detection,
validation, and ruleset preview as `detect-vpn`/`validate`/`print-rules`. Writes to
the system path need root (hence `sudo`); a permission error prints a `sudo` hint.

The wizard asks only what has no safe default: blocked countries (plus a
free-text field for other codes), whether to configure the VPN now, automatic
vs. manual detection, and — when configuring — tunnel interfaces (manual mode
only), self-hosted config files to import, and endpoints. Everything it used
to also ask (poll interval, log level, provider quorum, physical DNS,
auto-discovery) ships with a sane default and lives in the app's Settings or
`config set`; a wizard run leaves those keys untouched, so re-running setup
never clobbers a tuned value. The one silent defaulting decision it kept: a
brand-new macOS config gets live endpoint discovery turned on.

`setup --questions` is the exception: it prints what the wizard *would* ask —
each question, what it writes, its seeded answer, and which earlier answer
unlocks it — and asks nothing. Read-only, no root, no terminal needed.
`--json` is the machine form, and is how the macOS app's own first-run wizard
gets the question set instead of keeping a second copy of it.

```sh
dezhban setup --questions          # what would you ask me?
dezhban setup --questions --json   # the same, for another surface to render
```

### Asking what a key is

`config schema` describes the keys themselves rather than your values: for each
one, its default, what bounds it, whether `"0"` turns it off, whether a preset
writes it, whether the running daemon can adopt it without a restart, and which
part of [config.md](config.md) documents it.

```sh
dezhban config schema              # every key, explained
dezhban config schema --json       # the same, for tools
```

It reads no config file — the schema is what the keys *are*, not what this host
has set — so it answers the same on a machine that has never been configured.
That is what lets the macOS app label its settings, bound its sliders, and know
where an explicit **Off** is a real choice instead of hardcoding any of it. The
defaults it prints are derived from the shipped defaults themselves, so this
output cannot drift from what you actually get by setting nothing.

### Presets

A quick way to answer "how strict am I", without knowing which eight keys that
touches — see [config.md](config.md#presets) for exactly what each one sets and
what it costs you:

```sh
dezhban config preset list                  # strict/focused/balanced/relaxed, cost, and which matches now
dezhban config preset show strict           # one preset's key/value set
dezhban config preset diff                  # keys that differ from the matched-or-nearest preset
dezhban config preset diff relaxed          # keys that differ from a specific preset
sudo dezhban config preset apply strict     # write it — same validated path as `config set`
```

`preset apply` is not a new write mechanism — it builds the same `key=value`
pairs `config set` would take, then validates once and writes once, so it
applies live where it can and reports what needs a restart exactly like any
other write. It never touches identity (blocked countries, tunnel interfaces,
endpoints, profiles); add `--json` to `list`/`show`/`diff` for machine-readable
output.

A preset also never raises `vpn.advanced.switchWindowMax`/`redialWindowMax`, so
lowering one of those caps by hand can put a preset out of reach. `list` and
`diff` mark it `cannot apply:` with both values (a `conflicts` array under
`--json`), and `apply` refuses before writing anything rather than failing on a
validation rule mid-write. See
[config.md](config.md#presets) for the reasoning.

### Changing settings without a password

Settings changes can also go over the control socket, so the macOS app's Settings
pane doesn't ask for your password on every edit. That op is gated twice, and both
gates must pass:

- **Proof** — the client must present the enrolled **control token**. The socket's
  own gate is filesystem permissions (root-owned, mode `0660`, admin group), which
  is a fine bar for ops that only move between fail-closed postures but not for one
  that changes state outliving the daemon. The token raises that bar; it does not
  lower it.
- **Policy** — `control.allowConfigOps` must be true (it is, by default). Set it
  `false` and config writes are refused even from a client holding a valid token,
  which sends settings changes back to `sudo dezhban config set`.

```sh
dezhban token status               # is a token enrolled on this host?
sudo dezhban token enroll          # mint one, print it once, record only its hash
sudo dezhban token forget          # un-enroll; config writes fall back to sudo
```

Only the **hash** is stored, root-owned in the daemon's state directory. `enroll`
prints the token itself exactly once, on stdout — it is never recoverable
afterwards, and enrolling again replaces the previous one, which is how you revoke
a token that has leaked. Rationale:
[ADR 0003](../adr/0003-biometric-token-over-existing-daemon.md).

To use a token from a script, give `config set` the `--token-stdin` flag and pipe
the token in. It never goes in an argument or an environment variable, both of
which other processes can read:

```sh
printf '%s' "$DEZHBAN_TOKEN" | dezhban config set --token-stdin pollInterval=20s
```

If no daemon answers, the write falls back to the ordinary privileged path. If the
daemon **refuses**, that is reported and never routed around — a refusal is a
decision, and re-running it as root would make the gate advisory.

In the macOS app this is the **"Use Touch ID for settings changes"** toggle in
Settings. Turning it on enrolls a token (one password prompt, once) and stores it
in your login keychain. Saving a change then asks for a fingerprint instead of a
password. Turning the toggle off removes **both** copies, the keychain item and
the daemon's hash. Macs without Touch ID keep using the password path, which is
exactly what they had before.

**What the fingerprint check is, precisely.** Dezhban asks macOS for a
fingerprint and reads the token once macOS says yes. The keychain is not itself
withholding the token pending biometrics — that stronger arrangement needs a
code-signing entitlement an ad-hoc signature cannot carry, and every build this
project ships is ad-hoc signed. So the check raises the bar rather than making it
unforgeable: a modified copy of the app could skip it. Two things bound that. The
keychain item keeps its ordinary access control, tied to the app's code identity,
so another program reading it gets a keychain password prompt rather than silent
access; and modifying the app needs admin rights, which already allow
`sudo dezhban config set` and bypass the token outright. Rationale:
[ADR 0012](../adr/0012-app-checked-biometrics-on-unsigned-builds.md), and
[ADR 0011](../adr/0011-biometric-enrollment-requires-a-signed-build.md) for the
signing constraint behind it.

## Connect & switch VPNs

After a one-time `setup`, run dezhban (or install the service) and connect any
VPN. Known VPNs need no ceremony, and a drop or server rotation is covered by the
[automatic redial window](../concepts/modes.md#automatic-redial-window) with no
interaction; the manual switch window below is the fallback — e.g. for arming a
brand-new VPN while the guard is already holding the line.

```sh
# Known VPNs — register once, then just connect/switch in the VPN's own app:
dezhban vpn add proton --endpoint nl-01.protonvpn.net
dezhban vpn import ~/wg0.conf          # WireGuard .conf / OpenVPN .ovpn / V2Ray JSON
dezhban vpn list                        # profiles + learned endpoints + active state

# A brand-new VPN whose server dezhban has never seen:
dezhban switch                          # open a window (5s default); connect it in its app now
dezhban switch --for 90s --name windscribe        # custom duration + attribution
dezhban switch --cancel                 # close the window early
dezhban switch --status                 # is a window open?
sudo dezhban vpn promote <name>         # make a learned endpoint permanent (see: vpn list)
sudo dezhban vpn forget <name>          # drop a learned endpoint
```

`switch` writes a root-owned control file the daemon consumes, then narrates the
window from the state file until it closes. See [modes.md](../concepts/modes.md#switch-window--connecting-a-brand-new-vpn)
for the posture and the real-IP-exposure trade-off.

`switch --status` leads with a fixed token — `switch window: OPEN`,
`switch window: closed`, or `pause: OPEN` for a window opened by `dezhban pause`
— then the rendered window sentence, then an `until: <RFC3339>` line. The token
is what a script tests, and it does not move with the wording; the `until:` line
carries the exact deadline, since the sentence dates a window only to a
wall-clock time and a window can outlive both its date and its minute:

```
switch window: OPEN — Your real IP may be exposed until 3:04PM. The guard is relaxed so a new VPN can connect.
until: 2026-07-25T15:04:00Z
```

## Pause the guard temporarily

For the times the *correct* traffic is the one the guard blocks — a domestic-only
service that refuses a foreign VPN exit:

```sh
dezhban pause --list       # the offered lengths, and what each is for
dezhban pause 15m          # real IP for 15 minutes, capped by vpn.pauseMax
dezhban resume             # end it early
```

Unlike `switch`, this doesn't wait for a VPN — it just opens egress for the given
duration and re-arms the guard by itself at the deadline, so there's nothing to
remember to turn back on. See
[modes.md](../concepts/modes.md#pause--deliberately-using-your-real-ip).

`--list` offers a short set of realistic lengths — the question is never "how
many seconds" but "how long do I need my real IP for" — and any duration up to
the cap still works if none of them fit. Lengths above `vpn.pauseMax` are shown
as **unavailable with the cap as the reason**, not hidden: a cap you cannot see
is a cap you will keep bumping into.

A pause longer than the cap is **refused and explained, never shortened**. Asking
for an hour against a 30-minute cap fails with the cap named, rather than quietly
granting thirty minutes you did not ask for and cannot tell apart from the hour
you wanted.

## Keep a deliberate disconnect cut

dezhban cannot tell a VPN you turned off from a VPN that fell over, so by default
it treats every drop the same way: it opens a redial window so the client can get
back. When you are the one disconnecting, that is a relaxation you never asked
for. Arm **hold the line** first and the next drop stays cut:

```sh
dezhban hold               # the next VPN drop stays cut — no redial window
dezhban hold --status      # armed or not
dezhban hold --cancel      # back to the usual behaviour
```

It is the opposite of `pause` in both directions: pause says *let me use my real
IP*, hold the line says *keep me cut*. It only ever **removes** a relaxation, so
the three sanctioned triggers are unchanged and there is no fourth — which is
also why it needs no `control.allow*` gate of its own.

It is one-shot on purpose: spent by the drop it covers, disarmed as soon as a
tunnel is up again, and forgotten if the daemon restarts. A flag that survived a
reboot would eventually cut an *accidental* drop off from the redial help it
should have had.

Arming it *during* a cut works too — it suppresses the pending retry, so a drop
the budget already refused stays refused. Cancelling then puts you back exactly
where you were: dezhban re-decides straight away, and opens a window if the
budget now allows one. Changing your mind never costs you the recovery.

## Shell completion

```sh
source <(dezhban completion zsh)     # or bash; add to your ~/.zshrc / ~/.bashrc
dezhban completion fish | source     # fish
```

Completes subcommands, `--mode` values (`guard|fullblock|switch`), the `config`
subcommands, and file paths for `--config`.

## Run as a service

On macOS the [installer](../../README.md#install-macos) (`dezhban-<version>.pkg`) does all of
this for you — it installs the CLI + app and registers the service in one step, with
one password prompt. It deliberately leaves enforcement stopped; run
`sudo dezhban setup` then `sudo dezhban start`. Everything below is the manual
equivalent, and the only path on Linux/Windows.

dezhban can install itself as a boot-persistent background service using one
cross-platform API (launchd on macOS, systemd/upstart/sysv on Linux, the Windows
Service manager). The service wraps the `run` loop, restarts on crash, and routes
logs to the platform logger (syslog/journald/Event Log).

```sh
sudo dezhban install --config /etc/dezhban/dezhban.json   # register (default path if omitted)
sudo dezhban start                                        # start now; also auto-starts on boot
dezhban status                                            # → service: installed, running
sudo dezhban stop                                         # stops AND removes firewall rules
sudo dezhban uninstall
```

`stop` cancels the run loop so its deferred `Cleanup()` removes every rule —
stopping the service never leaves a block-all rule behind. If the service crashes
while blocked, the rules persist by design (a kill switch must not fail open); use
`sudo dezhban panic` to flush them even with no daemon running.

## Upgrade

```sh
dezhban upgrade check                    # no root — is a newer release out?
dezhban upgrade can-activate --json      # no root — could a restart activate right now?
sudo dezhban upgrade download       # macOS only — fetch + verify the .pkg
sudo dezhban upgrade apply           # macOS only — install it, then activate
sudo dezhban upgrade apply --no-activate   # install without restarting
```

`check` and `can-activate` work on every platform and are read-only.
`download`/`apply` are macOS only — Linux and Windows package managers own
their own upgrade path. `apply` installs the `.pkg` (zero enforcement gap)
and then, unless `--no-activate`, restarts into it — but only when the
daemon's posture makes that safe (healthy `guard` or `standby`; never
`full-block` or an open switch window). `can-activate` reports that same
verdict without applying anything — `scripts/install.sh`'s upgrade path
checks it before restarting a service that was already running, so it can
never lift a block by accident. See [docs/usage/upgrade.md](upgrade.md) for the
full design: why it's split this way, the activation gate, rollback, and the
menubar app's **About → Updates** panel.

## macOS app

On macOS an optional native app (`Dezhban.app`) shows the daemon's live posture
and offers click-to-control. It's a separate Swift target (AppKit shell, SwiftUI
main window), so the Go binary keeps its zero-dependency, `CGO_ENABLED=0`
promise. Build it with `task gui:build` (see [development.md](../contribute/development.md)).

Two surfaces, split by urgency:

- **Menubar dropdown — the safety/glance core.** One status line (posture, exit
  country/provider), **Open Dezhban…**, **Block now/Unblock**, the VPN switch
  window (Switching VPN… / Cancel with a live countdown) when in VPN mode,
  **Panic — force unblock…**, Quit. These are the time-critical and
  lockout-recovery actions; they never depend on the main window opening. Items
  enable/disable from the current state.
- **Main window — everything else**, opened from the dropdown or by clicking the
  Dock icon (never automatically at launch).

The main window's sidebar sections:

- **Overview** — live status hero (posture; public IPv4 — and IPv6 when the
  daemon has observed one — with the exit country named in full as e.g.
  `Kazakhstan (KZ)`; the strictness preset; tunnel, the connected VPN app when
  detection can name it, endpoints (collapsed past three), every configured
  VPN profile with the matched one marked, switch-window countdown) plus the
  daily controls, Pause, and a visually-separated Panic. Faults render as
  banners above the grid — enforcement problems in red, failing exit checks in
  orange, first line only with the full text behind a disclosure — while an
  *expected* unknown exit stays a plain row. With profiles configured,
  "Switching VPN…" becomes a menu so a switch window can target one by name.
  Degraded states are guided: CLI missing, service not installed, and daemon
  stopped each render an explanation with the one relevant action inline
  (Install service… / Guard up).
- **Settings** — startup ("Start the guard at boot" installs the launchd
  system service so enforcement survives reboots; "Open this app at login" via
  `SMAppService`; **"Open minimized"** — Never / Always / Only at login, an
  app-local preference that decides whether the main window opens when Dezhban
  starts, defaulting to "Only at login", which is what the app always did; the
  Dock icon and the menubar's "Open Dezhban…" open it regardless;
  **per-event notifications** — a master toggle plus a checkbox per essential
  event class), a **strictness preset
  picker** (Strict/Focused/Balanced/Relaxed, each showing its cost, or "Custom" with
  the keys that differ), tunnels/endpoints/autodetection, blocking (blocked
  countries, poll interval, the DNS and exit-check passes with their
  consequences stated inline), windows (switch/redial/endpoint grace) as
  **sliders** over each key's real range — Off detent only where "0" is a
  persisted choice, cap from the live config, Custom escape hatch — all
  applied through one validated `config set` batch, a **Developer**
  disclosure generated from the schema's advanced flag (every
  `vpn.advanced.*` key plus hysteresis/providerQuorum/logLevel), **"Use Touch
  ID for settings changes"** (see below), an explicit **Restart dezhban…**,
  and the raw config file escape hatch (control socket, geo providers,
  allowlist are JSON-only).
- **Diagnostics** — a **Your VPNs** inventory (`detect-vpn --json`: tunnels
  found, VPN apps detection can attribute, the connected one marked, an
  unrecognized client flagged) above `doctor`'s findings (`--json`), rendered
  as status rows with fixes inline instead of a text dump; an optional "Find
  my VPN's server" checkbox runs it with `--discover`. Read-only, same
  guarantee as running `dezhban doctor` in a terminal. When the last report
  found something to look at, the sidebar's Diagnostics row carries a yellow
  dot until a later run comes back clean.
- **Logs** — a scoped `log show --last 1h`, a live `log stream` with Stop
  (also opens Console.app), and the transcripts of window-triggered
  panic/install/uninstall/apply/restart runs.
- **About** — version, config/binary paths, posture, service state, and which
  elevation path (Touch ID-capable Authorization Services vs password-only
  fallback) privileged actions will take.

**Status icon** — full-color brand state icons (from `gui/artifacts/`), shown in both
the menu bar and the Dock tile: teal allow/guard, red block/full-block, amber
warning (switch window open or enforcement error), gray stopped or stale;
repainted about once a second. Outside the assembled `.app` bundle (e.g. a bare
`swift run`) the menu bar falls back to monochrome SF Symbol shields.

**Passwords** — Block, Unblock and the switch window go to the running daemon
over its control socket and raise **no prompt at all**. Only the service lifecycle
(Install/Uninstall/Start/Stop) and Panic raise the native admin prompt, because
neither can be daemon-mediated. Tooltips say which it will be before you click.

The app runs no IP/country poller of its own — it reads the daemon's state file
(see [architecture.md](../contribute/architecture.md#state-export--statejson)),
the single source of truth for what the daemon decided. It is unsigned; `curl`-installed
via `install.sh` it needs no Gatekeeper workaround at all (see [install.md](install.md)),
but a standalone double-click of the app bundle hits Gatekeeper — see
[releasing.md](../contribute/releasing.md#unsigned-artifacts-signed-checksums) for the
bypass. The app's own verification checklist lives in
[testing.md](../contribute/testing.md#macos-app).
