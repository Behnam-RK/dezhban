# Changelog

All notable changes to **dezhban** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are cut with the manually-dispatched `release` workflow, which rewrites
the `## [Unreleased]` section below into a versioned entry — see
[docs/contribute/releasing.md](docs/contribute/releasing.md). Keep `## [Unreleased]`
current as you land changes.

## [Unreleased]

### Added

- **Countries are named, not just coded.** `Iran (IR)` rather than `IR`,
  everywhere a person reads one: `dezhban status`, `validate`, `monitor`
  (including its `--simulate-country` banner), the rendered posture sentence
  (`Full block — Iran (IR)`), the macOS menubar dropdown, and the app's
  Overview. The code stays visible on purpose — it is the token you type back
  into `blockedCountries` or pass to `--simulate-country`. Machine-readable
  output is deliberately unchanged: `config get blockedCountries` still prints
  `IR,RU,KP`, `config show` still prints the file as written, and
  `status --json` keeps `countryCode`/`blockedCountries` as they were, with the
  names carried alongside in the new `countryName` and `blockedCountryNames`
  snapshot fields. Names come from one table in the daemon
  (`internal/country`, CLDR English short names, stdlib only — no new
  dependency), so the CLI, menubar and window cannot disagree; a code the
  table does not know displays as the bare code, and validation is unchanged
  (still length-only, so an unrecognised code still loads). An older daemon
  omits the new fields and every reader falls back to the code.
- **macOS app: "Open minimized" (Never / Always / Only at login).** Decides
  whether the main window opens when Dezhban starts. Defaults to "Only at
  login", which is exactly what the app already did, so an upgrade changes
  nothing for anyone who leaves it alone. It is an app preference on this Mac
  (like the notification and update-check toggles), not a daemon config key —
  so it needs no password and never touches `/etc/dezhban`. The Dock icon and
  the menubar's "Open Dezhban…" open the window in every mode.

- **Enforcement verification** (`vpn.advanced.verifyInterval`, default `1m`).
  The run loop now periodically confirms the firewall rules it believes are
  installed are actually still there AND still enforcing — not just present
  but disconnected from what makes them bite (the pf main ruleset no longer
  referencing our anchor, an nft chain's policy rewritten off `drop` in
  place, a Windows profile's outbound default flipped back to Allow while
  our rules sit untouched) — re-applying whatever posture is currently in
  force (the standing guard, a full block, or an open switch/redial window
  or pause) the instant either check fails. Every other rule change was
  already triggered by something dezhban itself did — a tunnel change, an
  endpoint refresh, a posture flip; this is the only one that notices a
  ruleset (or the switch that makes it matter) disturbed from OUTSIDE the
  daemon (another firewall tool, `pfctl -F all`, `nft flush ruleset`, an OS
  ruleset reload). Reported in `state.verify`
  (`status --json`), the plain-text `dezhban status`/menubar posture sentence,
  and turns the menubar icon amber, only while something is wrong;
  disablable (`"0"`) from the CLI or the macOS app's Settings pane, and an
  unreadable backend is never treated as evidence the rules are gone. See
  [docs/usage/config.md](docs/usage/config.md#advanced-tunables-vpnadvanced).
- **Zombie-tunnel detection.** A tunnel interface that reports up while a run
  of exit-country lookups through it has failed is now diagnosed as such —
  reported in `state.zombie`, `dezhban doctor`'s new "enforcement liveness"
  check, and the rendered posture sentence (which now also turns the menubar
  icon amber for the duration of the streak) — instead of sitting correctly
  cut with no signal to anyone. Detection is always on; letting a confirmed
  streak open an automatic redial window is a separate, off-by-default key
  (`vpn.advanced.livenessRedial`, settable from the CLI or the macOS app's
  Settings pane), because an exit that censors the geo providers produces the
  identical symptom on a tunnel that was never actually down. See
  [ADR-0010](docs/adr/0010-tunnel-liveness.md).
- **A single-instance guard on `run`.** A second `dezhban run` — with or
  without `--no-daemon` — started alongside an already-running daemon now
  refuses immediately instead of racing it to apply firewall rules. The lock
  is released by the OS the moment the holding process ends, by any means, so
  a killed daemon never wedges the next start. `panic`, `unblock`, and the
  service-lifecycle commands deliberately take no such lock — they remain the
  escape hatch, usable with no daemon running at all.
- **Exit-IP change observation.** The daemon now logs and publishes
  (`state.exitIpChangedAt`) when the observed exit IP differs from the
  previous successful reading — purely informational, like the exit-country
  check it sits beside: it never affects `blocked`, `countryCode`, or the
  hysteresis streak. A failover between two servers in the same allowed
  country changes nothing those fields report, but changes this. Now also
  reported in `dezhban doctor`'s "enforcement liveness" check (text and
  `--json`) and the macOS app's Diagnostics pane — previously the field was
  published but rendered nowhere.
- **A startup self-test log line.** `dezhban run` now logs one summary at
  startup — firewall backend reachable, state directory writable, tunnels
  configured/detected, endpoints known, whether this host has ever observed a
  tunnel up — diagnostic only, never blocking startup.
- **Panic-disarm status visibility.** `dezhban panic` tearing down the
  firewall while a daemon keeps running (and every automatic Apply path
  standing down because of it) is now reported to the operator instead of
  being silent — `state.panicDisarmed` (`status --json`) and a "Panic
  disarmed" headline that wins over the plain-text `dezhban status`/menubar
  posture sentence, so `status`/the menubar can no longer say "Guarding" or
  "Full block" while enforcement is actually torn down and waiting on
  `dezhban unblock` (or a daemon restart).

### Changed

- The automatic-redial-refusal log lines no longer hardcode "vpn tunnel
  down", since a refusal can now also come from the zombie-tunnel liveness
  trigger, whose interface never goes down: `"vpn tunnel down — no redial
  window (...)"` is now `"no automatic redial window (...)"`, and
  `"vpn tunnel down — redial window suppressed"` is now `"redial window
  suppressed"`. Anyone grepping or alerting on the old strings should update
  the pattern.

- **macOS app: the Overview hero now shows the brand state tile for the
  healthy state too**, instead of the crimson app icon. The hero is a status
  readout — colour is the state — and the menubar has always shown the teal
  guardian for a healthy guard, so the two surfaces disagreed about the same
  snapshot. The Dock tile is unchanged and still shows the app icon.

### Fixed

- **STANDBY no longer full-blocks the host at startup when the physical
  connection's country is in `vpn.blockedCountries`.** With `vpn.autoArm` on
  and no tunnel interface present yet, the daemon entered standby — installing
  no rules, by design ([ADR-0002](docs/adr/0002-standby-no-tunnel-posture.md))
  — but the one-off exit-country observation at startup ran anyway, unlike the
  periodic one in the run loop, which has always skipped standby. With no
  tunnel up that lookup leaves over the physical link and reports the user's
  own ISP country, so anyone blocking their real location (the primary use for
  this tool) got a FULL BLOCK applied on top of standby on any boot that beat
  the VPN client to it — the lockout standby exists to prevent. The startup
  observation now skips standby, matching the loop.
- **macOS app: the main window's sidebar and titlebar are now real macOS
  chrome.** The sidebar rendered as a floating, bordered card inset from the
  window edges instead of a full-height source list, and the sidebar-toggle
  button — together with an orphaned separator hairline — jumped from the
  middle of the titlebar to the far right whenever the sidebar collapsed. The
  window sets `toolbarStyle = .unified` but never actually installed a
  toolbar, which left the unified titlebar treatment inert and let SwiftUI
  supply one containing nothing but a toggle and a tracking separator with no
  trailing content. The window now owns an `NSToolbar` and an
  `NSSplitViewController` whose sidebar item is a real AppKit source list, so
  the sidebar runs edge to edge and up behind the titlebar, the toggle stays
  put, and the separator tracks the actual split divider. The section name
  moved from the titlebar into the toolbar beside it, and each pane's
  now-inert `.navigationTitle` was removed in favour of one binding on the
  sidebar selection. Adds a **View ▸ Toggle Sidebar (⌃⌘S)** menu item, which
  the app never had. The window's minimum width also rises to 820pt: the Help
  pane's inner split needs 620pt and could not fit at the old 640pt minimum.
- **macOS app: the Overview's buttons no longer truncate, and its content no
  longer sprawls.** At a narrow window every label in the action row shortened
  at once — "Block n…", "Switchin…", "Guard…" — because an `HStack` given less
  width than its children need compresses all of them rather than admitting it
  does not fit; the row's natural width exceeds the pane at the minimum window
  size, so this was reachable by simply resizing. The row now wraps onto as
  many lines as it needs, keeping the trailing lifecycle action ("Guard down",
  "Apply…") flush right, and the Panic caption wraps instead of truncating.
  At the other end, nothing capped the content width, so the divider ran the
  full width of a large display and the trailing button was flung far from the
  rest; content is now capped at a readable column. The Settings footer had
  the identical defect and gets the identical fix.
- **macOS app: the "off" states showed a generic SF Symbol shield instead of a
  dezhban artifact.** `build-app.sh` ships only two Dock state images, because
  the Dock deliberately coarsens every posture to "blocked" or "on" — but the
  Overview asked that same two-file set for the full five-state key, so `off`,
  `warning` and `paused` resolved to no file at all and fell back to a plain
  system shield. The three degraded pages (CLI missing, service not installed,
  daemon stopped) hit this every single time. All five official state tiles
  from `gui/artifacts/png` now ship as their own resource family, and the
  build warns if one is missing rather than letting it go unnoticed until
  someone reaches that state.

- **`dezhban panic`'s teardown now stays effective across every automatic
  enforcement path, not just periodic verification.** Previously the
  panic-disarm marker (which tells enforcement verification to stand down
  after a deliberate `panic` teardown) was consulted only by the
  `verifyInterval` tick — the automatic redial window, the geo-provider
  GUARD/FULL BLOCK state machine, tunnel/endpoint-change re-applies, and
  auto-arm from standby could all still silently reinstall rules within
  moments, turning the documented lockout escape hatch into a brief flicker.
  Every automatic path now stands down while the marker is set; every
  explicit operator command (`block`, `unblock`, `switch`, `pause`/`resume`)
  clears it unconditionally instead, exactly as `unblock` already did — an
  explicit command is never blocked by the marker.
- **A hung tunnel `vpn.advanced.livenessRedial` refused to redial could stay
  refused forever**, even once it had genuinely been up long enough to pass
  `redialMinUptime`'s anti-flap check. The re-decision that fires once that
  bound lifts was reusing the uptime captured at the very first widening
  attempt — correct for an ordinary drop (the tunnel is down, so uptime is a
  frozen historical fact) but wrong for a zombie streak, where the tunnel
  never goes down and its uptime keeps growing for real. It now re-derives
  the uptime for a standing zombie streak instead of reusing the stale value.
- **A manual `block` no longer leaves a stale "tunnel reports up but exit
  checks failing" warning on screen.** If a zombie-tunnel streak was showing
  when the operator ran `dezhban block` (or the control-socket equivalent),
  the warning previously lingered until the next periodic geo check cleared
  it; it is now cleared in the same publish as the block itself.
- **Exit-IP change observation now also covers the window between daemon
  startup and the first periodic geo check.** A VPN failover landing in that
  window was previously invisible to `state.exitIpChangedAt` because only the
  periodic check recorded the last-seen exit IP; the startup reading now
  records it too.
- **Windows enforcement verification (`vpn.advanced.verifyInterval`) no
  longer needs two sequential PowerShell calls per tick.** The group-existence
  check and the per-profile default-action query are now one invocation, so a
  slow PowerShell/WMI response can no longer nearly double the time a verify
  tick can hold up the run loop.
- **macOS enforcement verification's three `pfctl` reads now share one
  deadline instead of each getting its own.** Under `pf` lock contention a
  verify tick could previously stall the run loop's single goroutine — window
  timers, geo ticks, control-socket replies — for up to 3x `pfctl`'s 10s
  timeout; it is now bounded to that timeout once, the same fix already
  applied to the Windows backend above.
- **A live daemon could briefly reinstate rules `dezhban panic` had just torn
  down.** `panic` now records the panic-disarm marker BEFORE tearing down the
  firewall rather than after — every automatic Apply path a running daemon
  might take checks the marker first and only then reads the firewall, so the
  old ordering left a window where such a check could land between the two
  calls, see the rules already gone, find no marker yet, and silently
  reinstate them.
- **Windows enforcement verification no longer reports false rule drift when
  a PowerShell/WMI hiccup drops one firewall profile's line from the query
  output.** A profile missing from the parsed result used to read as the Go
  zero value, which never matched the wanted action and triggered an
  unwarranted repair; it is now treated as an unreadable check, the same
  discipline already applied to a fully failed query.
- **A local, unprivileged process could block the Windows kill switch from
  ever starting.** The single-instance lock's mutex lived under a predictable
  `Global\` name, so anyone could pre-create it first and either get treated
  as the legitimate "already running" holder or deny the daemon's own
  `CreateMutexW` with a hostile DACL. It now lives inside a boundary-restricted
  private namespace that only `LocalSystem` or `BUILTIN\Administrators` can
  create or open objects in, falling back to the old name only if that setup
  fails.
- **`dezhban panic`'s teardown could be silently undone by a transient read
  error.** The running daemon checked whether its panic marker was still
  present with a bare stat that treated ANY error — not just a missing file —
  as "gone", so a momentary I/O or permission hiccup could make enforcement
  verification re-apply the very posture the operator just tore down. Only a
  definite "not found" now counts as absent.
- **`dezhban doctor`'s liveness check now reports a suspended `dezhban panic`
  teardown**, and no longer claims "enforcement is holding" when it genuinely
  can't confirm that (an unreadable firewall, or a repair attempt that itself
  failed) — both previously fell through to the same reassuring summary a
  routine, already-repaired finding gets.
- **Enforcement verification's repair counter and log line no longer claim
  success before the re-apply actually lands.** A repair whose
  `Backend.Apply` call itself failed was previously counted and reported as
  completed; it is now counted only once the re-apply confirms, and a failed
  attempt surfaces through the daemon's normal enforcement-error reporting
  instead.
- **Live-disabling `vpn.advanced.verifyInterval` while `dezhban panic` had
  suspended verification could silently swallow the next "verification
  suspended" warning** if the interval was later re-enabled. The suspended
  flag is now reset when the interval is turned off, so the warning fires
  again on the next suspend.
- **Live-disabling `vpn.advanced.livenessRedial` no longer leaves a standing
  zombie-tunnel redial refusal free to open a window anyway** once its
  budget/cooldown lifts — the retry now re-checks the live setting, not just
  whether the streak is still standing.
- **Exit-IP change observation now also covers readings that land FULL
  BLOCK**, not only allowed ones — a failover between two servers in the same
  forbidden country previously went unrecorded, as did the very first
  reading whenever startup itself observed a blocked country.
- **`dezhban run`'s startup self-test no longer delays the boot-time firewall
  apply.** Its firewall-reachability check (and, with auto-discovery on, its
  endpoint resolve) now run off the startup path instead of ahead of the very
  `Apply(guard)` call ADR-0008's arm-at-boot promise depends on landing
  immediately. The self-test's "state directory writable" and "endpoints
  known" fields also now probe a real write and a real resolve instead of
  inferring success from configuration alone.
- **Windows enforcement verification no longer misreports drift after its own
  bookkeeping write fails.** The write that records what `Apply` last set is
  atomic, so a failure left the PREVIOUS `Apply`'s value on disk; `IsBlocked`'s
  drift check then compared a live, correctly re-applied profile against that
  stale value and could report a false repair. The stale record is now
  cleared on a failed write instead of left in place.
- Config/state writes made through the shared `atomicfile` helper now also
  fsync the containing directory after the rename, hardening durability
  across a crash on filesystems that require it (covers the panic marker and
  the arm-at-boot record, among others).
- **A failed enforcement-verification repair's error could be silently
  overwritten by the very next ordinary geo-poll tick**, replacing it with a
  falsely reassuring "rules re-applied" message even though the repair
  actually failed and the firewall was still unenforced. The geo-poll step
  now only overwrites the enforcement-error surface on a tick that actually
  touched the backend, never on an uneventful no-op reading.
- **A manual `block`/`unblock` over the control socket no longer leaves a
  stale "rules missing, re-applied N times" message on screen** after
  installing fresh rules — enforcement-verification state is now reset in
  the same command, the same fix already applied to the zombie-tunnel
  warning.
- **`dezhban hold` armed during a hung-tunnel (zombie) streak no longer
  permanently forfeits that streak's one-shot `vpn.advanced.livenessRedial`
  attempt.** Because that attempt is suppressed without being "spent" by
  hold (a later real disconnect still needs its own hold), cancelling hold
  previously had nothing to act on and the attempt was lost for the rest of
  the streak. Cancelling hold now restores it, the same way it already
  restores a refused-and-waiting ordinary drop's retry.
- **Windows enforcement verification could misreport rules as missing when
  PowerShell wrote incidental output ahead of the group-existence check's
  own marker line.** The check required that marker on the exact first line
  of output; it now scans for it instead, still requiring an exact line
  match (never a substring) so unrelated text can't be mistaken for it
  either.
- **Linux (`nft`) enforcement verification's drift check is now scoped to
  the `output` chain specifically**, instead of searching the whole `nft
  list table` output for the text "policy drop" anywhere. A table with more
  than one chain could previously have its `output` chain's policy drift to
  `accept` while another chain's unrelated "policy drop" text kept the check
  reporting enforcement as healthy.
- **The menubar app no longer crashes when a settings field's "?" opens Help at
  a heading.** Every contextual help link took the app down. `Bundle.main
  .resourceURL` returns a URL *relative to* a base (`file:///Applications/
  Dezhban.app/`), and the helper that appended the heading anchor rebuilt the
  URL from `URLComponents(url:resolvingAgainstBaseURL: false)` — which sees only
  the relative half, with no scheme. `WKWebView.loadFileURL` raises on a
  non-file URL, and an ObjC exception thrown inside a SwiftUI layout pass cannot
  be caught. Opening Help from the menu was unaffected, because only the
  anchored path appended a fragment at all. The same base also made a URL built
  from the bundle never compare equal to the same URL handed back by WebKit, so
  a plain in-page heading link was misread as a jump to another page and routed
  into that identical crash — clicking a table-of-contents entry inside Help was
  enough, with no settings field involved. The base is now folded in once where
  the bundle is opened, the fragment helpers moved to `DezhbanCore` so they are
  covered by tests, and the loader refuses to hand WebKit anything that is not a
  file URL — a bad anchor now lands the reader at the top of the right page,
  matching what `HelpBundle.resolve` already did with a stale one.
- **"Use Touch ID for settings changes" now works — it never did.** The token was
  stored under a `SecAccessControl` with `.biometryCurrentSet`, which macOS keeps
  only in a keychain reachable with a code-signing entitlement that the
  ad-hoc-signed builds this project ships cannot carry, so `SecItemAdd` failed
  with `-34018` (`errSecMissingEntitlement`) on every Mac, every time. Worse, the
  app checked only whether the Mac *had* Touch ID, so it took a root password,
  minted a token and wrote the daemon's hash **before** discovering the keychain
  would refuse it — leaving an enrollment no client could present and a manual
  `sudo dezhban token forget` to clean up, while the About pane went on inviting
  you to try again. The token is now an ordinary keychain item read after an
  explicit `LAContext` fingerprint check, which needs no entitlement and no Apple
  Developer account, so the daily path costs one fingerprint and no password.
  **The check is performed by the app rather than enforced by the keychain**, and
  the toggle's help text and
  [the CLI reference](docs/usage/cli.md#changing-settings-without-a-password) now
  say so — a gate that reads stronger than it is would be worse than none. See
  [ADR-0012](docs/adr/0012-app-checked-biometrics-on-unsigned-builds.md), and
  [ADR-0011](docs/adr/0011-biometric-enrollment-requires-a-signed-build.md) for
  the signing constraint (adding the entitlement was tested: an ad-hoc signature
  declaring `keychain-access-groups` is SIGKILLed at launch).
- **Enrollment can no longer half-succeed.** The app probes the keychain before
  spending anything privileged, so an enrollment that cannot complete costs no
  password and enrolls nothing; if a store fails anyway, the daemon's hash is
  rolled back automatically instead of being left for you to clear.
- **The About pane no longer advertises "turn on Touch ID in Settings" when that
  cannot work.** It distinguishes a Mac with no Touch ID from a keychain that
  refused, so it stops sending people to a toggle that could only fail.
- **A cancelled fingerprint falls back to `sudo`, never to your login password.**
  The check uses `.deviceOwnerAuthenticationWithBiometrics` deliberately: a login
  password that unlocks a settings change is the thing the token exists to avoid.
- **The app no longer touches your keychain unless you open its window.** The
  check for whether this Mac can hold the secret is a keychain write, and doing
  it at launch meant every session paid for a feature you may never open — on a
  Mac whose login keychain password has drifted from your account password, that
  is an unexplained unlock prompt at every login, from a menubar app. It now runs
  when the main window first appears, so a session spent entirely in the menubar
  never asks.
- **A half-completed "turn Touch ID off" no longer leaves the app unable to save
  anything.** Removing the enrollment clears the daemon's half first, since that
  is the half that can be declined — but if the keychain removal then fails, what
  is left is a secret the daemon has already forgotten. Offering it draws a
  refusal, and a refusal is deliberately never retried with your password, so
  every settings save would have failed until the item was cleared from the
  command line. The app now notices and stops offering it, falling back to the
  password path, which is where turning the toggle off was heading anyway. The
  same applies to a "turn Touch ID on" whose keychain write failed with an older
  secret still in place — that secret no longer matches what dezhban expects,
  whether or not the rollback got through — and the About pane says "Password"
  for a stale secret rather than claiming a Touch ID it will not actually ask
  you for.
- **Re-enrolling works after an app update.** A login-keychain item's access
  control is bound to the exact binary that created it, and an ad-hoc signature's
  identity is its cdhash — so every rebuild produced an app the keychain treated
  as a stranger to its own stored secret. `SecItemDelete` was refused with
  `-25244` and the following add collided with `-25299`, which broke both the
  documented recovery and the revocation path for a leaked token. Removal now
  goes through `SecKeychainItemDelete`, which is not subject to that check.
  Reading a secret stored by a previous version may ask you to approve keychain
  access once; approving keeps the enrollment, and turning the toggle off and on
  is always a clean escape. The keychain probe that guards enrollment removes its
  own leftovers the same way, so a probe stranded by one build cannot make every
  later build believe the keychain refuses it.
- **The Touch ID toggle keeps re-checking the sensor.** The new capability check
  could easily have been cached whole for the life of the app; only its keychain
  half, which genuinely cannot change while a process runs, is remembered. A
  MacBook in clamshell mode — or one in Touch ID lockout after repeated failures
  — reports no usable sensor, and a menubar app runs for weeks, so a frozen
  verdict would have left the toggle greyed out with "this Mac has no Touch ID"
  until you quit and relaunched. Re-checking has a cost the Settings pane now
  pays properly: the keychain half is established once, in the background, when
  the window opens — but that warm-up is skipped when no sensor is available
  *then*, so opening a clamshelled Mac and going straight to Settings used to
  leave the pane to run its first keychain write on the main thread, behind a system
  dialog if your login keychain was locked. The pane now shows "Checking…"
  for the moment that takes instead of freezing.
- **The About pane says "Password" while the sensor is unusable, even with Touch
  ID enrolled.** A shut lid or a Touch ID lockout makes the read fall back to the
  ordinary password path with the enrollment perfectly intact — so "Touch ID
  (control token enrolled)" named a cost you were not about to pay, in the one
  row that exists to answer "why did I get a password dialog?". It now says which
  it is, and that nothing is wrong with the enrollment. The row also fills in as
  soon as the answer is known rather than waiting for the version and config-path
  lookups beside it.

## [0.9.0] - 2026-07-29

### Added

- **A checked-in test-quality gate.** `task test:cover` (and CI, once per run)
  now enforces `.testcoverage.yml`'s per-package coverage floors via
  `go-test-coverage`, pinned to today's real measured coverage rather than an
  aspirational number — the point is to stop regressions, not to pretend every
  package is already where it should be. See docs/contribute/testing.md's new
  "Unit test policy" section for the rules a new test is expected to follow.
- `cmd/dezhban/harness_test.go` exercises the CLI's read-only command surface
  in-process (`run()` itself was previously untested), and `blockPlan`/
  `parseOverrides` pull pure decision logic out of `cmdBlock`/`cmdRun` so it is
  testable without root or a firewall backend.
- **`scripts/install.sh` is now interactive at a real terminal.** Piped
  (`curl | sudo bash`) it is unchanged — no prompt, today's exact defaults.
  Run directly instead, it asks: which components to install on a fresh
  machine (menubar app, service registration), and on a machine that already
  has dezhban, upgrade / reinstall / **uninstall** (typed confirmation,
  keep-config prompt, defaults to keeping `/etc/dezhban`). Progress now shows
  `[n/N]` step counters, with curl's own progress bar on a real terminal.
- **`dezhban upgrade can-activate [--json]`** — a read-only, no-root
  subcommand reporting whether a restart could activate right now (the same
  gate `upgrade apply` uses). See "Fixed" below for why `install.sh` needed it.
- **[docs/usage/passwordless.md](docs/usage/passwordless.md)** — a task-oriented
  tutorial for pointing `control.group` at your distro's existing admin group
  (`sudo`/`wheel`/`admin`) so day-to-day ops (`block`, `unblock`, `switch`,
  `pause`, `resume`, `hold`) no longer prompt for a password. Grants no new
  authority — if you can already `sudo`, you're already a member. Bundled into
  the macOS app's Help pane.
- `dezhban doctor` gained a **`control:`** check: socket reachability, the
  configured group, whether the caller is in it, and which ops
  `allowSwitchOps`/`allowPauseOps`/`allowConfigOps` force back to `sudo`.

### Fixed

- Replaced six blind `time.Sleep`-and-hope waits in `internal/runner` and
  `internal/control`'s tests with bounded polling (or, in one case, an
  explicitly documented exception where the sleep IS the scenario under test).
- **`scripts/install.sh`'s upgrade path could restart a running daemon through
  an unsafe posture.** It stopped and restarted the service unconditionally,
  with no regard for ADR-0007's activation gate — including through FULL
  BLOCK, which would have briefly lifted a block on a forbidden-country exit,
  the one thing this tool exists to prevent. It now checks `dezhban upgrade
  can-activate` before stopping anything: the new binary always installs (safe
  — a running process keeps its old inode), but the stop/restart is skipped on
  refusal, leaving the old build enforcing until `sudo dezhban restart`
  succeeds on its own. No override, matching `upgrade apply`'s own rule.
- **`scripts/install.sh` could finish with the kill switch disarmed and say
  nothing.** When an upgrade restarted a running daemon, the `start` was
  unguarded: under `set -e` a failure aborted the script with no message at
  all — after the `stop` had already run, so every firewall rule was gone.
  The install looked like it had merely "failed" while the host was in fact
  left unprotected. It now stops with an explicit warning that names the
  exposure and the command that ends it. Relatedly, cancelling the optional
  setup wizard on a fresh install no longer swallows the "next steps" footer.
- **Docs and `doctor`/`setup` hints no longer model `sudo` on commands that
  don't need it.** `status` never needed root — its `doctor` fix hint wrongly
  said otherwise. `block`, `unblock`, `switch`, `pause`, `resume` route over
  the control socket before ever needing root; `sudo` in front of them was
  actively counterproductive; it re-execs as root, which then pays the very
  password prompt the socket exists to avoid. Trimmed from the user-facing
  `docs/usage/` pages; ADRs, contributor docs, and every `curl | sudo bash`
  install line are untouched by design, and the `sudo` still shown on `panic`,
  `setup`, `run`, and the service lifecycle is correct — those genuinely need
  root.

## [0.8.0] - 2026-07-28

### Changed

- **A refused redial is re-decided when its bound lifts, instead of waiting for
  another drop.** `nextEligible` named an instant nothing acted on: the decision
  was retaken only on the next tunnel-down edge, so a tunnel that could not come
  back on its own — a rotated server address the endpoint pass does not cover,
  which is exactly the case the automatic window exists for — produced no further
  edge, the refusal stood indefinitely, and the budget refilling changed nothing.
  The stated time now has a timer behind it.

  Still trigger two, and still no fourth trigger: the drop qualified at its own
  edge and only the budget or cooldown said no, so re-asking when that expires
  completes a decision already earned. Every rail holds — one automatic window
  per drop, the same cap, the same ledger, all preconditions re-checked, and
  `dezhban hold` suppresses the re-decision without being spent by it. See
  [ADR-0009](docs/adr/0009-redial-budget.md).

### Fixed

- **A refusal states its time as an attempt, not an outcome.** "It can relax
  again at 3:15PM" read as a promise the guard would be open then, which the
  re-decision does not make: it consults the budget afresh and re-checks every
  precondition, so it may refuse again. Both surfaces now read *"dezhban tries
  again at 3:15PM — no window opens before then. Your VPN can still reconnect on
  its own, and you can open a window yourself at any time"*: when dezhban itself
  acts, the bound that goes with it, the fact that a held guard still passes
  known server addresses so the VPN's own redial is unaffected meanwhile, and the
  way out that always works. Once that instant has passed the sentence drops it
  rather than reprinting a moment that came and went. `docs/usage/cli.md` states
  the same caveat for scripts; a person deserves it more, not less.

- **`doctor` no longer reports an unreadable boot service as a missing one.** Any
  failure to read the unit — a permission problem on the launchd plist or on
  `/Library/LaunchDaemons`, on the systemd unit or on `/etc/systemd/system` — was
  reported as "not registered to start at boot", telling a user whose service is
  installed and enforcing to reinstall it. That is the same false negative the
  check exists to avoid, reached from the other side. Only "the file is absent"
  now means absent on either platform; anything else reports that the question
  cannot be answered without asking the service manager. On Linux the rule also
  covers the enablement symlink, where it matters more: an unreadable one used to
  read as "installed, but not set to start at boot" for a service that *is*
  enabled, sending the user to fix something that was not broken.

- **Cancelling `dezhban hold` mid-cut no longer strands the drop.** Arming hold
  while already cut correctly suppresses the pending re-decision — but cancelling
  it did not give that re-decision back. The retry fires once, the hold consumes
  it, the timer disarms itself, and nothing re-armed it, so the drop stayed cut
  until the next tunnel-down edge — which cannot arrive while the tunnel is
  already down. Changing your mind cost you the automatic recovery entirely, and
  both surfaces went on saying dezhban re-checks on its own. Cancelling now
  re-asks immediately, through the same path the timer would have used, so every
  rail still applies. Hold only ever *subtracts* a relaxation; cancelling it is
  that subtraction being taken back, not a fourth trigger.

- **A refusal no longer outlives the setting that justified it.** Reloading
  `vpn.redialWindow` to `"0"` during a cut left the standing refusal published:
  `status --json` kept reporting `redial.reason` with a `nextEligible` nothing
  would ever act on, and both surfaces kept naming a time for a window that had
  been switched off entirely — a promise nothing would keep, which is the exact
  failure publishing the refusal exists to prevent. Turning the automatic window
  off now drops the refusal and the timer behind it.

- **A cooldown refusal now answers for the budget too.** `nextEligible` reported
  only the cooldown deadline, so a host that was backing off *and* out of budget
  was told 3:00PM, waited, and was told 3:15PM instead — the moving deadline the
  published refusal exists to avoid. It reports the later of the two bounds now.

- **The budget's rolling period is honoured on its boundary.** An episode exactly
  one period old was still counted, so the instant `nextEligible` published was
  one tick before the ledger could actually afford a window: the drop that
  arrived at the promised time was refused and handed a new one.

- **`state.redial.nextEligible` is omitted rather than published as a zero
  timestamp.** `omitempty` does not omit a zero `time.Time`, so a writer without
  an instant would have emitted `"0001-01-01T00:00:00Z"` — which the macOS app's
  ISO8601 decoder refuses, failing the *whole* snapshot decode and reading as
  "stopped" while dezhban was enforcing. The field is `omitzero`, and the app
  decodes it as optional.

- **Four control-socket refusals said "daemon" or "egress" to the user.**
  `dezhban pause` in standby answered *"standby — egress is already open"*, and a
  runner without a reload hook answered *"this daemon cannot reload its
  configuration"*. These reach the user verbatim after `dezhban refused:`, so
  they are copy — `internal/runner` is now in the vocabulary lint's Go scope, and
  the startup lockout refusal no longer says "egress" either.

- **Three glossary rows enforced nothing, and the app's alert copy was
  unreadable to the lint.** Any banned phrase ending in punctuation — every row
  naming a config key, e.g. `"Enable VPN guard (vpn.enabled)"` — compiled to a
  regex with a trailing `\b` that cannot match, so the row parsed, counted, and
  checked nothing. Word-boundary anchors are now applied only where there is a
  word to anchor against, and `TestEveryTermMatchesItself` fails any row that
  cannot find itself. Separately, the Swift scanner could not see multi-line
  (`"""`) literals at all — the fences carry no content and the content lines
  carry no quotes — so two user-facing alerts told people about "the daemon"
  while the lint reported the file clean. Both are fixed; the alerts now say
  "dezhban".

- **A refusal no longer names a time it has already gone past.** The "it can
  relax again at 3:15PM" clause is decided when the tunnel drops and re-decided
  only when it drops again, so a tunnel that stays down carried the old instant
  indefinitely — `status` and the menubar app would still promise 3:15PM at
  3:44PM, for a moment that had come and gone with nothing happening. Once the
  stated time has passed, both surfaces now say the guard can relax again *the
  next time your VPN tries to reconnect*, which is the thing actually being
  waited for. A time in the past is worse than no time: it reads as a
  commitment that was broken.

- **The redial backoff no longer deepens on drops it refused.** A drop turned
  away because the budget was spent still counted toward the consecutive-fast-drop
  streak and pushed the cooldown out, so refusals compounded into a wait longer
  than either bound had asked for — the guard kept holding after the budget had
  already rolled over. Only a drop that actually receives a window advances the
  backoff now, which is the rule the cooldown path already followed.

- **`vpn.advanced.redialBudget` and `redialBudgetWindow` refuse a `"0"` written
  in the config file**, not just one typed at `dezhban config set`. The file
  previously accepted it and normalised it back to the default, so the same
  value was a named error one way in and a silent discard the other. Both paths
  now say the same thing: a limit has no "off" — raise it, or set
  `vpn.redialWindow` to `"0"` to turn the automatic window off outright.

- **A redial window whose firewall rules failed to install no longer costs the
  budget anything.** The grant is debited before the rules are applied — the
  decision has to come first — so an `Apply` that errored left the debit standing
  with no window to close it, and the ledger deliberately never ages out an open
  episode. A single failed open could therefore spend the whole budget and refuse
  every later drop, for exposure that never happened. The grant is credited back
  in full when the open fails, which is what "the budget measures exposure taken,
  not exposure offered" was supposed to mean all along.

- **`status --json` no longer reports an open window and a standing refusal at
  the same time.** Opening a manual `switch` or a `pause` over a refused drop
  left `state.redial` published beside `state.switch`, so a script matching on
  `.redial.reason` — which [the CLI reference](docs/usage/cli.md) tells it to do
  — saw the guard holding until 3:15PM while the guard was in fact relaxed. Any
  window opening now clears the refusal, whatever its trigger. The sentence a
  person reads was never affected.

- **`vpn.advanced.redialBudget` below `5s` is refused** while the automatic
  window is enabled, instead of validating clean. `5s` is the shortest window
  dezhban will open, so a smaller budget can never afford one: the automatic
  redial window was off permanently while the config still read as though it were
  on, and `status` compounded it by reporting that the guard could relax again
  "the next time your VPN tries to reconnect" — a promise nothing would ever
  keep. Turning the window off stays available and explicit
  (`vpn.redialWindow: "0"`); turning it off by arithmetic does not. The floor
  follows a `vpn.redialWindow` set deliberately shorter than `5s`, which dezhban
  honours as written — a budget that affords that shorter window is accepted
  rather than refused against a minimum that does not apply to it.

- **A tunnel that recovers is no longer held by the redial backoff's wait.** The
  cooldown armed by a fast drop was checked before any evidence about the current
  drop, so a tunnel that redialed, carried a confirmed exit, stayed up past
  `vpn.advanced.redialMinUptime` and then dropped again was still refused a
  window — with budget to spare. The retry above would eventually re-ask, but not
  before the whole remaining cooldown elapsed: a wait a recovered link never
  earned, and on a slow flap long enough to send the user to `dezhban switch` by
  hand, which is the manual interaction the redial budget exists to remove. A
  confirmed exit or a healthy uptime now clears the cooldown
  outright, and the rolling budget — the bound that actually matters — is
  unchanged.

- **`state.redial.remainingSeconds` is now read from the live ledger** on every
  snapshot instead of being frozen at the instant of the refusal. Episodes roll
  out of the budget period while the cut lasts, so the published number stayed
  behind the real one for as long as the tunnel was down and a script watching
  it could not see the budget recover. `reason` and `nextEligible` are the
  decision and still stand as decided.

### Changed

- **The glossary is now checked, not just written down.** It has always claimed
  to be the authority — "when user-facing copy and this page disagree, the copy
  is wrong" — but nothing verified that, and the copy had drifted back to
  "protection", "egress" and "daemon" in about forty places while the page said
  not to.

  `internal/vocab` parses the banned-word table out of
  [docs/concepts/glossary.md](docs/concepts/glossary.md) itself and fails the
  build, so there is one list and it is the one a human reads. Editing a row
  changes what the build enforces. The Go side uses `go/parser` to tell a string
  reaching stdout from one reaching a log, because those registers differ:
  "daemon" is wrong on a button and exactly right in a log line. Two markers in
  the table say where each row applies, and an exemption requires a written
  reason, so an exception is a recorded decision rather than a silent dodge.

  User-visible wording changed accordingly. `status` prints **`control socket:`**
  instead of `daemon control:`; `block`/`unblock` no longer print "(via
  daemon)"; refusals read `dezhban refused:`; "is the daemon running?" became
  "is dezhban running?"; and the app's panic tooltip and block hint dropped
  "daemon" and "egress". `status --json` keys are unchanged — they are stable
  identifiers, and the lint does not touch them.

  The settings copy changed too. Every setting's one-line hint — the text
  `dezhban config schema` prints and the macOS Settings pane shows beside each
  row — lives in a table the lint had not been pointed at, because the `Printf`
  that writes it is in a different package from the sentence itself. Six hints
  said "daemon" or "relaxation" and the `strict` preset's summary said "zero
  relaxation"; all seven now read in the same voice as the rest of the app.

### Added

- **The automatic redial window now spends from a bounded budget**
  (`vpn.advanced.redialBudget`, default `2m`, per
  `vpn.advanced.redialBudgetWindow`, default `15m`). Two problems, opposite in
  direction, went away together.

  Total exposure across drops had no ceiling at all: every drop earned a fresh
  30s, so a link dropping once a minute produced 30s of relaxed guard every
  minute, indefinitely. It is now bounded, and the bound is measured in the
  thing that matters — time actually relaxed. A window that closes early on a
  successful redial only spends what it used, so a healthy link that drops
  occasionally and reconnects in seconds will never approach the budget. The
  limit bites a connection that is genuinely failing, which is when it should.

  And a flapping tunnel used to get **no window at all**
  (`vpn.advanced.redialMinUptime` suppressed it outright), which pushed exactly
  the users with the worst connections onto `dezhban switch` by hand — a product
  failure in a tool whose promise is minimum interaction. That setting now seeds
  a *backoff* instead: a fast drop still gets a window, shorter for each
  consecutive fast drop and with a growing wait between them, until the budget
  runs out and the guard holds. Refusals say which bound refused and when a
  window can next open, in the logs and in `status`.

  A refusal is published, not only logged: `status --json` gains `state.redial`
  (the reason, when a window can next open, and what is left of the budget) for
  as long as the refusal stands, and `status` and the menubar app both read
  *"Your VPN has dropped often enough to use up its redial budget, so the guard
  is holding and traffic stays cut. dezhban tries again at 3:15PM — no window
  opens before then. Your VPN can still reconnect on its own, and you can open a
  window yourself at any time."* — the same sentence, composed once. Without a
  time, "the guard is holding" leaves a wait indistinguishable from a wall.

  Still trigger two, not a fourth trigger. `vpn.redialWindow: "0"` remains the
  one way to turn the automatic window off; `dezhban hold` still suppresses a
  single drop and spends nothing; `redialWindowMax` still caps any single
  window. Rationale, and the four alternatives rejected:
  [ADR-0009](docs/adr/0009-redial-budget.md).

- **`dezhban doctor` answers "will dezhban need me again".** Three new checks,
  for the two complaints that are really the same complaint — being asked to do
  by hand what the guard exists to do for you.

  *Boot service* says whether a reboot brings the guard back at all, separating
  "nothing is registered", "registered but not set to start at boot", and "both
  fine, and enforcing right now" — the last one matters because it rules
  enforcement out and leaves the menubar app's login item, which has an entirely
  different fix. It reads the service unit rather than asking the service
  manager, so it stays truthful for a normal user: on macOS an unprivileged
  status query cannot see the system domain and reports a running daemon as
  absent.

  *Arm at boot* says whether the next reboot arms the guard immediately or opens
  into standby. `vpn.armAtBoot` may only arm when a tunnel has been observed up
  at least once on this host, and that half fails silently — the setting reads
  "on" the whole time — so the check names which half is missing.

  *Learned endpoints* reads the store the guard redials through and tells apart
  the two opposite reasons a drop keeps needing a window by hand: addresses that
  were learned and then aged out (retain them longer), or a VPN that rotates its
  server address (retaining more only delays it — a hostname re-resolves and
  follows the rotation). All three are informational and never change the exit
  code; the macOS Diagnostics pane shows them alongside the rest.

- **`dezhban config schema` describes every setting**, so you can ask what a key
  is instead of reading source. For each one it prints the label, its default,
  what bounds it, whether `"0"` turns it off, whether a strictness preset writes
  it, whether a running daemon adopts it without a restart, and where it is
  documented. `--json` for tools; read-only, no root, and it reads no config file
  — the schema is what the keys *are*, not what this host has set, so it answers
  the same on a machine that has never been configured.

  Behind it, defaults are now data rather than prose. They used to be written
  down in four places — the Go constants, the macOS app's placeholder hints, the
  documentation tables, and the example configs — and they had already come
  apart: the app advertised a 30s endpoint refresh and a 5s tunnel watch while
  the shipped defaults were 1m and 1s. Every surface now derives them from one
  table, which itself derives the values from the shipped defaults, so the same
  drift cannot recur.

- **`dezhban hold` keeps a deliberate disconnect cut.** dezhban cannot tell a VPN
  you turned off from a VPN that fell over, so it treats every drop the same way
  and opens a redial window — a relaxation you never asked for when you are the
  one disconnecting. Arm hold the line first and the next drop stays cut, with
  the icon red because traffic really is cut. `hold --status` reports it,
  `hold --cancel` disarms it.

  It only ever **removes** a relaxation, so the three sanctioned triggers are
  unchanged and there is no fourth — and it carries no `control.allow*` gate,
  because there is no authority to withhold. One-shot on purpose: spent by the
  drop it covers, disarmed once a tunnel is back, and forgotten if the daemon
  restarts, so a flag left armed can never cost a later *accidental* drop the
  redial help it should have had. `status --json` gains a `hold` object.

- **A relaxed guard says so first, and names when your VPN dropped.** Every
  window sentence now leads with the exposure and when it ends, instead of
  opening with the machinery and leaving the consequence trailing after a dash:
  *"Your real IP may be exposed until 3:04PM. Your VPN dropped at 3:03PM and the
  guard relaxed so it can redial."* A guard holding a downed tunnel names the
  drop time too. Both surfaces change together, because both display the same
  rendered strings.

- **The state file records when your VPN dropped.** `status --json` gains a
  `drop` object (`at`) present from a tunnel drop until a tunnel is up
  again. Until now the moment the guard cut traffic was unobservable on the
  common path: the automatic redial window opens on the same edge, so the cut
  snapshot was replaced within microseconds while observers read the file about
  once a second — leaving both surfaces able to say only "a window is open".

- **The macOS Settings pane is reorganised around what you are deciding**, not
  around the shape of the config file. Sections are ordered which VPN to trust →
  what gets blocked → when the guard may relax → local network → how closely it
  watches → startup, each with a line saying why you would touch it. Headings say
  the thing rather than the config block: "When the guard relaxes", not
  "Windows". Autodetection is folded into "Your VPN", where those toggles were
  always about the same decision.

- **Duration settings in the macOS app are a menu of real choices**, not a text
  field demanding Go's duration syntax. Each offers lengths derived from that
  key's own default and its live cap, marks the recommended value, and provides
  a Custom entry with immediate validity feedback instead of a modal alert after
  Apply. Lowering a cap by hand narrows the menu, because the ceiling is read
  from your config rather than a constant.

  Where `"0"` is a real, persisted opt-out — the switch window, the redial
  window, the pause cap, and the anti-flap gate — an explicit **Off** is offered
  and states its consequence in words. It is offered *only* for those keys: for
  every other duration a `0` is coerced back to the default, so an Off there
  would be a security choice that silently did nothing.

- **`vpn.pauseMax` has a control in the macOS app**, under Windows alongside the
  switch and redial windows. It was settable from the CLI and reachable by
  editing the file, but the app offered no way to see or change how long a pause
  may last.

- **Pause offers realistic lengths, in both surfaces.** `dezhban pause --list`
  prints them with what each one is for, and the macOS app's Pause item gains a
  submenu of the same choices. They are defined once in the config core and read
  from the daemon, so the two cannot drift apart. Lengths above `vpn.pauseMax` are listed as unavailable with the
  cap as the reason rather than hidden — a cap you cannot see is one you keep
  bumping into. Any duration up to the cap still works.

- **The macOS app carries dezhban's documentation inside it.** A new Help pane
  in the main window shows the same pages as the repository's `docs/` — quick
  start, how the guard works, the postures, troubleshooting, and the full
  configuration and command references — with a search box and a first-time
  reading order.

  It reads them from inside the app bundle and never touches the network, which
  is the entire point: the moment you most need to know what the guard is doing
  to your traffic is often the moment it has cut all of it, and a help pane that
  needed a working connection would be blank exactly then. The pages are
  rendered from the repository's markdown when the app is built, so they always
  match the version that documents them, and the pane refuses to follow any link
  that leaves the bundle.

  Every setting in the Settings pane also carries a **?** button that opens Help
  at that setting's own section. A tooltip has room for one sentence; why a
  setting exists, what it costs, and what turning it off actually does often
  needs a page — and each link lands on the heading, not the top of a long
  reference.

- **`dezhban setup --questions` says what the wizard would ask** — each question,
  what it writes, its seeded answer, and which earlier answer unlocks it —
  without asking anything. Read-only, no root, no terminal needed; `--json` is
  the machine form.

  Behind it, the wizard's decisions moved out of the CLI into `internal/setup`,
  which now owns the question set, the branching, and how answers become a
  config. The CLI keeps only the presentation. A second wizard therefore cannot
  ask different questions or apply the same answer differently — the same reason
  the settings schema lives in one place.

- **The macOS app has a setup wizard.** Launch it with nothing configured and it
  walks the same questions `dezhban setup` asks — which countries to refuse, how
  to find your VPN, whether to pin an interface — seeded from whatever config
  you already have, with no terminal involved. It reads the question set from
  the daemon rather than keeping its own, and saves through the same batched,
  validated `config set` every other pane uses.

  It is offered only when dezhban does not know a VPN yet: if you set it up from
  the CLI, the app does not ask you to do it again. Settings → **Run Setup
  Again…** reopens it whenever you want, which is the guided way through those
  decisions when you change VPN.

### Changed

- **The macOS menubar is a glance and the actions that are urgent when you look
  at it** — the posture line, the switch or pause countdown, hold the line, and
  Open Dezhban. **Block now and Unblock have moved into the window's Overview**:
  someone who wants to cut their own internet can turn off Wi-Fi, so blocking by
  hand is a power-user and debugging affordance rather than part of the routine
  flow.

  **Panic now sits behind the Option key**, as the alternate to "Open Dezhban…" —
  hold ⌥ and the item becomes "Panic — force unblock…", or press ⌘⌥O. It stays
  in the menubar on purpose, because the moment it is needed is the moment the
  main window may not open; it is one keystroke away rather than one slip away
  in a menu people open to check a countdown.

- **A pause longer than `vpn.pauseMax` is now refused and explained, instead of
  silently shortened to the cap.** Asking for an hour against a 30-minute cap
  used to grant thirty minutes and report success, which is indistinguishable
  from having got what you asked for. It now fails, names the cap, and says how
  to raise it. Every path refuses — the CLI, the control socket, and the
  root-owned command file — so no client can get the old behaviour, and the
  command file (the one that still works with `control.allowPauseOps: false`)
  logs the same reason the socket would have replied with. A pause with *no*
  duration given is unchanged: nobody asked for a particular length, so the
  built-in default is still clamped to the cap.

### Fixed

- **Re-running `dezhban setup` no longer deletes your saved VPN profiles.** The
  wizard collects profiles by importing config files you name, and it wrote that
  list over the configured one — so running setup again to change, say, your log
  level, and not naming those files a second time, silently dropped every
  profile you had imported. Imported profiles are now merged into the saved
  ones, replacing by name.

- **The macOS app's Settings pane no longer advertises wrong defaults.** Its
  field hints were literal strings that had drifted from the shipped values — it
  suggested a 30s endpoint refresh and a 5s tunnel watch when the defaults are
  1m and 1s. Labels, hints, and help text now come from the daemon's own schema,
  so the pane says what dezhban actually does. Against a CLI too old to report a
  schema the pane falls back to a plainer label rather than a stale value: less
  helpful, never wrong.

- **The documentation bundled into the app now renders as written.** The subset
  renderer degraded silently instead of refusing, so pages shipped wrong while
  every test passed: Quick start — the first page of the guided track — opened
  with three lines of raw HTML source shown as text, bold that the author had
  wrapped across two lines left literal `**` in eight of the nine pages, nested
  bullets flattened into their own parents, and an asterisk inside `` `code` ``
  paired with an unrelated one to open emphasis that closed outside the tag.
  Soft-wrapped lines are now joined before inline markup is read, a list item's
  continuation stays inside the item, emphasis cannot reach into a code span,
  and anything the renderer still cannot represent **fails the build** rather
  than shipping — which is what the design claimed all along.

- **A settings value can no longer be written under the wrong key.** The pane
  staged its twenty-five values as an array destructured by position, with only
  a count check — so inserting or reordering a key would have silently applied
  one field's value to another setting. Values are now keyed throughout.

- **`dezhban setup` no longer clears `vpn.autoDiscoverEndpoints` on Linux and
  Windows.** Endpoint discovery is macOS-only, so the wizard does not ask about
  it elsewhere — and an unasked question was read as "no" and written to the
  config, quietly turning off a setting the user had set. The same class of bug
  as the deleted profiles above: a question the wizard never put on screen now
  leaves its key alone.

- **A drop that did not happen today now says which day it was.** The drop
  record is carried until a tunnel returns, so through an overnight outage or a
  long FULL BLOCK "Your VPN dropped at 3:04PM" read as *a few minutes ago* and
  understated how long the host had been cut. It now reads "at 3:04PM on Jul 26"
  once the drop is no longer on the snapshot's own day.

- **Links in the app's Help pane go somewhere.** The bundled pages cross-reference
  the ADRs and the contributor docs thirty times over, and those deliberately do
  not ship — so each of those links resolved to a file beside the bundle that does
  not exist, and clicking one reported *"That link points outside the app:
  file:///…/Contents/Resources/adr/0008-arm-at-boot.md"* with a Copy button that
  copied exactly that. Every bundled page had them. A link to a document that is
  not bundled now points at the repository, so the pane names a URL that works in
  a browser; a link to a page that *is* bundled still opens it in place. The pane
  still reaches the network for nothing.

- **`dezhban hold` no longer reports success for a hold the daemon discards.**
  Arming through the root-owned command file was a silent no-op when
  `vpn.redialWindow` was `"0"` — nothing to suppress, nothing logged. The CLI
  checks the config first, but skips that check when the file cannot be read, and
  then printed "hold the line armed". The daemon now says why it ignored the
  command, the same way the pause path does.

- **An over-cap pause refusal now says how to fix it.** The daemon's log named the
  cap but not the command to raise it, so the CLI and the daemon explained the
  same refusal differently.

### Changed — BREAKING

- **"reconnect" is now "redial" everywhere.** The codebase used both words for the
  same thing, and the macOS app already said "redial now" inside a window it
  called a "reconnect window". One word, chosen for being unambiguous about what
  the VPN client is doing, now runs through the config keys, the Go and Swift
  identifiers, the CLI output, and the documentation.

  Three config keys are renamed with **no aliases**, so an existing config must
  be updated by hand:

  | Old | New |
  |---|---|
  | `vpn.reconnectWindow` | `vpn.redialWindow` |
  | `vpn.advanced.reconnectWindowMax` | `vpn.advanced.redialWindowMax` |
  | `vpn.advanced.reconnectMinUptime` | `vpn.advanced.redialMinUptime` |

  Missing one is loud rather than silent — the old names are recognised as
  renamed and reported with their replacement by `dezhban validate` and at daemon
  start (see below) — but the old key genuinely stops taking effect. Nothing else
  about the behaviour changes: the automatic window, its cap, and its anti-flap
  gate work exactly as before under the new names.

- **Two config keys are renamed for casing/vocabulary consistency**, and both
  are reported by name — with what to change them to — by `dezhban validate`
  and at daemon start. Only one of them stops taking effect; see below, because
  the difference matters.

  | Old | New |
  |---|---|
  | `vpn.autodetect` | `vpn.autoDetect` |
  | `vpn.profiles[].ifaceHint` | `vpn.profiles[].tunnelHint` |

  `vpn.autoDetect` matches the casing of every sibling `auto*` key
  (`autoDiscoverEndpoints`, `autoArm`). `tunnelHint` drops the word
  *interface* per the glossary's "keep tunnel, drop interface" rule, matching
  `vpn.tunnelInterfaces`. `dezhban vpn add`'s `--iface-hint` flag is renamed to
  `--tunnel-hint` the same way (a CLI flag, so this one has no validate-time
  report — an old invocation gets a plain "flag provided but not defined").

  Unlike the redial rename, `vpn.autodetect` (the old, unqualified casing)
  **keeps working with no deprecation window at all**, and needs no alias to:
  JSON key matching ignores case, so the old spelling has always landed in the
  same field, and an explicit `"autodetect": false` still narrows the guard as
  written. It is reported at `validate`/daemon start as a misspelling that
  *took effect* — see the report fix under Fixed — rather than as a key with
  none. `vpn.profiles[].ifaceHint` → `tunnelHint` is a real word change, so
  that one does stop taking effect and is reported as renamed.

### Added

- **Recovery after a VPN redial is now fast and visible.** Leaving FULL BLOCK used
  to wait for the next ordinary poll — up to `pollInterval` × `hysteresis`, ~30s at
  defaults — with nothing to show a recovery was even under way, so a normal wait
  was indistinguishable from a stuck daemon. A tunnel coming back up now triggers
  re-checks every couple of seconds until the outcome is decided, and the state
  file, `status`, and the app all report progress ("restoring the guard — 1 of 2
  confirming checks").

  This changes **cadence only**: hysteresis still gates the change, an
  undeterminable country still holds the posture, and no new relaxation of the
  guard exists. Two rails keep it that way — it is skipped entirely when checking
  would require lifting the guard (which would multiply real-IP exposure instead of
  merely speeding things up), and it is time-bounded, so an exit that stays
  forbidden falls back to the normal cadence rather than polling the geo providers
  indefinitely.

- **Settings can now be changed without a password, and the daemon adopts them
  immediately.** A new `config-write` control-socket op takes a set of config
  keys, writes them through exactly the validation `dezhban config set` uses, and
  reloads in the same request — so a client never has to choose between
  elevating and leaving a saved setting inert. It is the one op that changes
  state outliving the daemon, so it is gated twice and **both** gates must pass:
  the client must present the enrolled control token, and `control.allowConfigOps`
  (new, default `true`) must permit it. Setting that key `false` refuses config
  writes even from a client holding a valid token.

- **The macOS app can change settings with a Touch ID tap instead of a password.**
  A new "Use Touch ID for settings changes" toggle in Settings enrolls a control
  token and keeps it in the login keychain under `.biometryCurrentSet`, so
  *reading* the token is the biometric prompt — there is no separate "is this
  allowed?" question for a tampered app to answer for itself. Changing your
  fingerprints invalidates the stored token by design; re-enrolling from the same
  toggle restores it, and turning the toggle off removes both the keychain item
  and the daemon's hash. Macs without Touch ID keep the password path unchanged.
  A cancelled prompt or a stopped daemon falls back to the password path; a
  daemon *refusal* is reported and never retried with elevation.

- **`dezhban config set --token-stdin`** does the same thing from a script: the
  token is read from stdin — never an argument or an environment variable, both
  of which other local processes can read — and the daemon performs the write.

- **`dezhban token`** manages that enrollment: `token status` (no root — whether
  the feature is set up is not itself a secret), `sudo dezhban token enroll`
  (mints a token, records only its hash root-only, prints the token once), and
  `sudo dezhban token forget` (un-enrolls, so a host whose token was lost falls
  back to `sudo` rather than becoming impossible to configure). Enrolling again
  replaces the previous token, which is the revocation path for one that leaked.

- **Unrecognised config keys are now reported instead of ignored.** Go's JSON
  decoder drops fields it does not know, so a typo — or a key renamed by an
  upgrade — silently reverted that setting to its default. Someone who wrote
  `"redialWindow": "0"` to forbid every automatic relaxation would have got the
  30s default back without a word. Every key the schema does not define is now
  listed by `dezhban validate` and warned about at daemon start, with renamed
  keys naming their replacement. It stays a report rather than a hard failure:
  refusing to start would leave the machine with no kill switch at all, which is
  worse than running with one setting at its default and saying so.

- **Config changes now apply without restarting the daemon.** The daemon read its
  configuration exactly once, at startup, and nothing told it to look again — no
  watcher, no signal, no control op — so `config set` wrote the file and notified
  nobody, and declining the macOS app's restart prompt left the new settings inert
  on disk. A new `reload` control op makes the daemon re-read the same file and
  adopt what it can, and `config set`/`config reset` trigger it automatically.

  The reply names both halves: keys that took effect, and keys still being
  enforced at their old values because the daemon built something from them
  before its run loop started (the logger, geo providers, the control socket, the
  tunnel watcher, arm-at-boot). Those are reported by name rather than applied
  silently — a setting is never claimed to be in force when it is not. A
  malformed edit changes nothing and keeps the kill switch enforcing the
  configuration already in force, and a reload can never lengthen a switch,
  redial, or pause window that is already open.

- **A dedicated PAUSED icon.** A pause used to draw the amber warning icon it
  shared with switch and redial windows, which read as "something went wrong"
  for the one relaxation the user deliberately asked for. It now has its own
  brand state icon and its own wording on every surface. It is still not the
  calm guard icon — the guard is relaxed and the real IP is in use either way.

- **Main-thread stall detection in the menubar app.** A background watchdog pings
  the main queue twice a second and records any stall past one second, plus how
  long it lasted, to unified logging under the `sh.dezhban.menu` subsystem
  (`log show --last 1h --predicate 'subsystem == "sh.dezhban.menu"'`). The
  beachballs have no cause visible in the source — every subprocess and
  elevation call is already dispatched off-main — so they have to be caught in
  the act. Diagnostic only: it observes the main thread and never blocks it, and
  it touches neither the daemon nor the firewall.

- **A single renderer for posture prose.** Posture sentences used to be composed
  independently in at least eight Go call sites and five Swift ones, with the
  same fact worded differently every time — including three separate spellings
  of the hysteresis-streak sentence ("agreeing readings" / "confirming checks" /
  "good readings"). `internal/render` now composes the headline, the detail
  sentence, and a stable machine key (`on`/`off`/`blocked`/`warning`/`paused`)
  from a `state.Snapshot`; `status` prints it, the state file (and therefore
  `status --json`) carries it, and the macOS app displays it instead of writing
  its own copy.
- **`status --json` gains `controlReachable`**, the machine-readable form of the
  `daemon control:` line, so a consumer no longer has to scrape a human sentence
  for a substring to learn whether routine ops need a password.

- **`status --json` gains `stateStale`**, so its answer cannot contradict the
  prose one. A crashed or `SIGKILL`ed daemon leaves its last posture on disk
  forever; the prose `status` substitutes "Stopped" once a snapshot ages past
  the staleness threshold, but the JSON passed the snapshot through verbatim —
  including its rendered `display.headline: "Guarding"` — so a script branching
  on `state.posture` alone would report a host as protected indefinitely after
  enforcement stopped. The snapshot is still passed through unchanged (it is a
  stable contract, and the last known posture is worth having); `stateStale`
  is how a consumer knows not to trust it. Emitted unconditionally, like
  `controlReachable` and `pauseEnabled` — `omitempty` on a safety flag would
  make "the snapshot is fresh" and "this CLI predates the field" the same
  absence on the wire.

- **`DezhbanCore`, a new Swift Package Manager library target** holding the
  macOS app's testable logic layer — Snapshot decoding, posture→icon
  derivation, and settings-field batching — split out of the `DezhbanMenu`
  executable specifically so it can be unit-tested (an `.executableTarget`
  cannot be `@testable import`ed). `DezhbanMenu` is unchanged behaviourally;
  it now imports `DezhbanCore` instead of defining these types itself.
- **The macOS app's first automated tests**, in a new `DezhbanCoreTests`
  target: Snapshot decoding (both RFC3339 date forms, an old daemon's
  `display`-less snapshot, corrupt data), every posture→icon mapping including
  the guard-holds-a-downed-tunnel case with an empty tunnel list (previously
  uncovered), and the settings-field seed/pairs round trip. CI gains a `gui`
  job (`macos-latest`) running `swift build` and `swift test`.
- **Pause and Resume in the macOS app**, using the same `control.allowPauseOps`
  socket op the CLI already had — no new daemon behaviour. Overview's action
  row gains a **Pause — use my real IP** button (disabled with a reason when
  `vpn.pauseMax: "0"`), and the menubar dropdown gets the same pair. While a
  pause is open both surfaces show **Resume now (m:ss left)** instead of the
  switch-window Cancel item — `switch --cancel` refuses to touch a pause by
  design — and the countdown banner reads "Guard re-arms in …" in blue rather
  than the switch window's amber "Closes in …", since a pause is deliberate,
  not a warning. `status --json` gains `pauseEnabled` so the app doesn't have
  to shell out separately to read `vpn.pauseMax`.
- **An explicit "Restart dezhban…" control in Settings**, beside Apply. Restart
  was previously only reachable as a side effect of applying settings that
  needed one; this makes it a direct, one-prompt action with its own
  confirmation, and the confirmation gets noticeably stronger — a `.critical`
  alert naming the exposure — when restarting would lift enforcement during
  FULL BLOCK or an open window, since that gap is the one thing this tool
  exists to prevent.
- **`dezhban doctor --json`.** `doctor`'s checks (config, tunnels, endpoints,
  the lockout-risk warning, Touch ID discoverability, `--discover`) are now
  built as structured data (`runDoctor`) and rendered two ways: the existing
  prose (`printDoctor`, byte-identical to before this change) or
  `{checks: [{name, status, summary, details, fixes}], ok}` via `--json` —
  for a consumer that needs to render findings itself instead of parsing
  text, which the macOS app's new Diagnostics pane (below) does.
- **A Diagnostics pane in the macOS app**, replacing the Logs pane's old "Run
  diagnostics" button (which dumped `doctor`'s raw text into the transcript
  view). Renders `doctor --json`'s checks as status rows — config, tunnels,
  endpoints, lockout risk, Touch ID, and an optional "Find my VPN's server"
  (`--discover`) — with fix text inline, read-only and root-free like the CLI
  command it wraps. The sidebar's "Logs & Diagnostics" section is renamed
  **Logs**; it keeps its transcript job (last-hour/live logs, and the output
  of panic/install/config-apply/restart) but no longer runs `doctor` itself.
- **VPN profile visibility in the macOS app.** Overview's details grid lists
  every configured profile (`vpn.profiles`) and marks the one the daemon last
  matched (`activeProfile`), reading `config show` the same way `dezhban vpn
  list` does. "Switching VPN…" becomes a menu — "Any known VPN" plus one item
  per profile — so a switch window can be opened targeted at a specific VPN
  (`switch --no-wait --name <profile>`), the same profile-attribution flag the
  CLI already had.
- **Every `vpn.advanced.*` key is now settable** with `dezhban config set` —
  previously the whole block was hand-edit-only. `switchWindowMax`,
  `redialWindowMax`, `redialMinUptime`, `windowDiscoveryInterval`,
  `commandFreshness`, `tunnelPruneAfter`, `learnedEndpointTTL`,
  `learnedMaxPerProfile`, `promoteAfterRefreshes`, `endpointWarnThreshold`,
  `windowProtocols`, and `windowPorts` all go through the same validated
  write-and-reload path, and reset/round-trip the same way every other key
  does; `redialMinUptime=0` persists as the same explicit-disable sentinel the
  three windows already use, not a silent reset to its 15s default.
- **Strict/Balanced/Relaxed presets, defined once in the config core**
  (`internal/config.Presets`). A preset is a write-time macro over the eight
  keys that answer "how strict am I" (the three relaxation windows, poll
  cadence and hysteresis, the two firewall-pass toggles, arm-at-boot) — never
  runtime state, and never identity (blocked countries, tunnel interfaces,
  endpoints, profiles), the same carve-out `config reset --all` already uses.
  `Balanced` is exactly `config.Default()`, so the shipped defaults and the
  middle preset can never disagree. `PresetDrift`/`MatchPreset` compare a
  config against a preset (or report "Custom") using the same `config.Change`
  vocabulary `config set`'s reload report already uses. Each preset states its
  cost in plain words — see [config.md](docs/usage/config.md#presets). No CLI
  or GUI surface yet; that's the next two changes.
- **`dezhban config preset list/show/diff/apply`.** `list` shows all three
  presets, their cost, and which (if any) matches the current config (or
  `Custom (N key(s) differ from …)`); `show <name>` prints one preset's
  key/value set; `diff [<name>]` shows the divergent keys (defaulting to the
  matched-or-nearest preset); `apply <name>` writes a preset's values through
  the exact same validated path `config set` uses — one write, live where it
  can, `Restart dezhban to apply: …` where it can't — and warns before
  applying Strict if any configured VPN endpoint is a hostname (Strict turns
  off `vpn.allowPhysicalDNS`, so it couldn't re-resolve while the tunnel is
  down). `list`/`show`/`diff` take `--json`.
- **A preset picker and an Advanced pane in the macOS app.** Settings gains a
  strictness-preset row — one button per preset, checked when it matches the
  live config, with its summary and cost shown below (or "Custom" plus a
  disclosure of exactly which keys differ from the nearest preset, matching
  `dezhban config preset diff`). Choosing one names the cost in the
  confirmation, then applies through `dezhban config preset apply` via the
  same batched write/reload/restart-prompt path Apply already uses. A
  collapsed **Advanced** disclosure exposes all twelve now-settable
  `vpn.advanced.*` keys, staged into the same batch (`SettingsFields` grows
  from 13 to 25 fields).

### Changed

- **`doctor --discover`'s failure line is indented like its siblings.** It was
  printed with three spaces where every other line in the block uses two — an
  artifact of `Println`'s operand separator, not a layout — and it only appeared
  on a host where endpoint discovery errors outright, which is why the
  before/after comparison across the shipped configs never surfaced it.

- **The macOS app no longer restarts the daemon to apply settings.** Saving now
  applies the change outright, and the restart prompt appears only when the
  daemon reports keys it could not adopt live — naming them, so the choice is
  informed rather than a blanket warning on every edit. Declining says exactly
  which settings are still on their old values instead of the previous vague
  "restart later to apply". The pane also re-reads the config file whenever you
  return to the app, so an edit made in a terminal no longer leaves it showing
  values the daemon has stopped using; unsaved edits suppress the re-read rather
  than being discarded.
- **The app's main window opens when you launch it.** Reaching it only through the
  menubar dropdown made opening the app a two-step discovery problem. Launches
  macOS performs on your behalf — a login item, state restoration, opening a file —
  still open nothing, because a window appearing unbidden at every boot is exactly
  the noise a menubar app should not make. The menubar item is unchanged either way.
- **`gui/assets/` is now `gui/artifacts/`**, refreshed with a new brand set that
  adds the paused state across every size and variant. Documentation, the app
  build script, and source comments follow the new path; historical changelog
  entries keep the old one, because that is where the files were at the time.
- **The Dock tile shows the brand app icon** when dezhban is not cutting traffic,
  instead of the "on" state tile. The Dock answers one question — is my traffic
  being cut right now — so only a real cut earns a distinct tile; everything else
  should look like the app.
- **`status` now leads with the rendered posture.** It previously never worded
  the posture at all — printing a config dump plus three narrow live lines — so
  guard, full block, standby, stopped, a lookup failure, and an enforcement
  error were simply absent from its output. `vpn switch --status`, the switch
  wait loop, and `vpn list`'s window line now render the same sentence instead
  of composing three more variants of it (previously with two different clock
  formats between them). `vpn switch --status` additionally prints an
  `until: <RFC3339>` line: the rendered sentence dates a window to a bare
  wall-clock time, which is the right register for prose and the wrong one for
  the command whose entire job is "when does my exposure end" — it carries no
  date and no seconds, and a window can outlive both (`vpn.pauseMax` has no cap
  floor).
- **The macOS app displays the daemon's rendered posture instead of composing
  its own.** `PostureUI.humanPosture`, the icon's inline help text,
  `OverviewView.postureBlurb`, `Snapshot.pendingSummary`, and the menubar's
  notification-title map are gone; all five read `snapshot.display` now. The
  notification classifier no longer parses the rendered sentence to tell
  standby from stopped (both draw the same grey icon) — it reads the
  snapshot's posture and liveness directly, so a wording change can no longer
  misclassify a notification.
- **One vocabulary, per [docs/concepts/glossary.md](docs/concepts/glossary.md).**
  The glossary already existed and already ruled on most of this drift; this
  release implements it: "Stop/Start kill switch" buttons become **Guard
  down** / **Guard up**; "Not protecting" / "Protection stopped" become
  "Standby — nothing is being blocked" / "Stopped"; "Autodetect tunnel
  interface (vpn.autodetect)" becomes "Find my VPN tunnel automatically";
  "Tunnel interfaces (comma-sep)" becomes "Your VPN tunnel (comma-sep)"; and
  "the daemon" is gone from every user-facing sentence in the app and CLI
  (technical register — logs, `--json`, docs — keeps it). The glossary also
  gains an **Egress** entry: technical register only, never user-facing —
  "Egress blocked" becomes "Traffic cut" everywhere, including the icon help
  text and the getting-started guide's image alt text.

### Fixed

- **A retired config key misspelled in letter case is no longer reported as
  having taken effect.** `validate` and the daemon's startup report tell you
  when a key differs from the schema only in case, because `encoding/json`
  honors it and the value really is live. But three keys the schema still parses
  are *retired* and read by nothing (`failClosed`, `allowlist`, `vpn.enabled`),
  so a file containing `"FailClosed": true` got both "`failClosed` has no
  effect" and "`FailClosed` … TOOK EFFECT" in the same output — self-
  contradicting, and the half that claimed a discarded security setting was
  running is the exact failure the whole unknown-key report exists to prevent.
  Such a key is now reported as retired, naming the schema's spelling and why
  that key is dead.

- **`switch --status`, `vpn list`, and `switch`'s live progress no longer print
  an unrelated enforcement error where the window sentence belongs.** All three
  append their own clause to the rendered sentence — `(profile "work")`, `—
  connect now…` — and were taking it from the full renderer, which puts a
  firewall-action failure ahead of the posture. With a window open and a failed
  `pfctl` action, `switch --status` read `pfctl: /dev/pf: Device busy (profile
  "work")`. They now use the posture-only renderer; `status` still reports the
  enforcement failure, which is where a general readout belongs.

- **`upgrade apply` no longer activates while the guard is holding a downed
  tunnel.** Activation is gated on a healthy posture, but the check only
  compared the posture *string*: `guard` with no tunnel up carries that same
  string while being the exact opposite case — the standing guard rule is then
  the only thing keeping traffic off the physical link, which is why `status`
  renders it "VPN down — traffic cut". Restarting through it removed every rule
  for the length of the swap with nothing carrying traffic through a tunnel, and
  with `vpn.armAtBoot: false` (the Relaxed preset) the host stayed open
  afterwards instead of re-arming. The gate now refuses, naming the reason, and
  the payload stays staged for a later `sudo dezhban restart` exactly as it does
  during FULL BLOCK.

  The staleness budget on the same gate is tightened to the one the renderer
  uses (3× the poll interval, floored at 90s) instead of its own 5-minute
  fallback — a safety gate must not call a snapshot fresh that `status` and the
  menubar app already show as "Stopped".

- **A config key typed in the wrong letter case is no longer reported as having
  no effect — because it has one.** Go's JSON decoder matches keys
  case-insensitively, so `"pollinterval": "1h"` or `"vpn": {"pausemax": "2h"}`
  is honored in full, while the schema check that produces the
  `validate`/daemon-start report compared spellings exactly and therefore
  called them unrecognised, adding "it has no effect". That is the same failure
  this project treats as its worst — a security setting's true state
  misreported — pointed the other way: someone reading that a 2-hour pause
  window "has no effect" stops looking while it is in force. Such keys are now
  reported as taking effect, with the spelling to change them to, and are kept
  distinct in the report from keys that really are inert (retired, renamed, or
  simply unrecognised). A block spelled in the wrong case is still walked into,
  so a genuine typo nested under it is still caught.

- **Lowering a window cap now binds the very next window.** A reload adopted the
  new `vpn.pauseMax` for the "is pausing available at all" check but left the
  clamp reading the value the daemon started with, so `vpn.pauseMax` reduced from
  30m to 1m was reported as applied while a 9-minute pause was still granted —
  a security setting accepted and silently discarded, which is the failure this
  project treats as the worst one it can have.

- **Five more settings now really do apply live, instead of only reporting that
  they did.** The reload path adopted them into the daemon's own view while the
  code that acts on them kept using the value captured at startup, so `Saved and
  applied` was true of the bookkeeping and false of the enforcement:

  - `vpn.endpointGrace` and `vpn.advanced.windowDiscoveryInterval` were read once
    at boot; both are now read at each use, so a shortened grace binds the very
    next endpoint reconciliation.
  - `vpn.advanced.switchWindowMax` bounded the *episode* at its new value but left
    `switch --for` clamped against the cap the daemon started with.
  - `vpn.switchWindow` and `vpn.pauseMax` could be turned off live but not back
    on: a daemon that started with both disabled never wired the command-file
    poll, so re-enabling a window left the root-owned path deaf until a restart.
    The poll is now always wired and every command re-checks the live value, so
    disabling still disables — a `"0"` window cannot be opened by any trigger.
  - `vpn.autoDetect` was never live at all (what autodetects is the tunnel
    watcher, built at startup) and is now reported as needing a restart, which is
    what it always needed.

- **A tightening now lands even while traffic is cut.** FULL BLOCK carries the
  `vpn.allowLocalNetwork` and `vpn.allowPhysicalDNS` passes, but a reload only
  reinstalled the *standing* rules — which it deliberately skips while blocked. So
  closing one of those passes during a block was reported as applied while the pass
  stayed in the firewall until the posture next changed. The current posture is now
  re-applied in place.

- **An unrelated config change no longer restarts the agreement streak.** Every
  reload handed the run loop a freshly built country decider, which resets the
  in-progress hysteresis count — so a settings save while a forbidden exit was
  being confirmed cancelled the escalation, and a client writing settings once per
  poll interval could defer FULL BLOCK indefinitely. The decider is now rebuilt only
  when `blockedCountries` or `hysteresis` actually changed, which is the only case
  where discarding counted readings is the right answer.

- **The config file is now written atomically.** It was written in place, so a
  save interrupted partway could leave a truncated file. That is not merely a
  lost setting: this config is what arms the guard at boot, so an unparseable one
  would leave the host unprotected at the next start. It is now staged, flushed
  and renamed, the same convention the daemon's other on-disk records already use.
  The control token's hash is written the same way, so a power loss cannot leave a
  zero-length hash that reads as "not enrolled" and locks out a valid token.

- **A failed Touch ID no longer strands you on a password-only prompt.** The app
  ran `sudo` with stdin on `/dev/null`, so the moment a biometric read missed —
  a closed lid, a wet finger, a couple of bad reads — PAM's password fallback hit
  EOF, failed instantly, and the whole attempt dropped to an Authorization
  Services dialog that cannot offer Touch ID at all. There was no retry, which is
  why `pam_tid` looked broken when it was configured correctly all along. The app
  now runs `sudo -A` with a bundled `SUDO_ASKPASS` helper, so a miss becomes the
  ordinary "enter your password" continuation that macOS's own biometric prompts
  offer. The helper lives inside the code-signed bundle — sudo executes whatever
  `SUDO_ASKPASS` names, so it must never be a path a local process can rewrite.
  Machines without `pam_tid` are unaffected and still use the system dialog.

- **The menubar app no longer reads the daemon's state file on the main thread.**
  The 1-second poll stat'd and decoded `state.json` inline, so any stall in that
  filesystem call froze the whole UI — clicks included. The read now runs on a
  background queue and publishes its result back on the main thread, and a tick
  is skipped while a previous read is still outstanding so slow reads cannot
  stack up. This is the leading suspect for the reported random beachballs; it
  is a hazard worth removing either way.

- **A typo or a renamed key inside `vpn.profiles` was silently dropped.** The
  unknown-key scanner only recursed into JSON objects, so `vpn.profiles` (a
  JSON array) was never walked — exactly the failure this mechanism exists to
  catch, just one container type away from where it was already caught for
  `vpn`/`vpn.advanced`/`control`. An unrecognised or renamed key inside any
  profile is now reported with its index (e.g. `vpn.profiles[1].ifaceHint`),
  and this is what makes the `ifaceHint` → `tunnelHint` rename above reportable
  at all.

- **`status` no longer reports a crashed or SIGKILLed daemon's last posture as
  still in force.** It only checked whether the state file was readable, not
  whether it was fresh, so a dead daemon's last snapshot (`guard`, say) kept
  printing "Guarding — Traffic leaves only through your VPN tunnel" — the most
  prominent line in the output — indefinitely, contradicted only by the
  `service:` line a few rows down. `status` now applies the same staleness rule
  (`internal/render.IsStale`) the macOS app's icon already used, so a stale
  snapshot reads as Stopped on both surfaces.

- **`config preset apply --json` now fails loudly instead of silently ignoring
  `--json`.** `apply`'s output is the ordinary `set k = v` lines `config set`
  already prints, not a JSON-able report; a script that asked for
  machine-readable output and got prose back now gets a clear error instead.

- **The macOS app's standalone "Restart dezhban…" button no longer claims a
  save that never happened.** A failed restart from that control reported
  "Saved, but the restart failed", which is only true when the restart is a
  step inside Apply — the standalone button writes nothing first. It now says
  "Restart failed" when invoked on its own.

- **Applying a preset in the macOS app now warns about unsaved edits it would
  discard.** The confirmation named the preset's own cost but not the pane's:
  applying writes the preset to disk and re-seeds every field from what
  landed, silently overwriting anything typed but not yet saved. The
  confirmation now says so when there is something to lose.

- **A preset that your own advanced caps forbid is now refused by name, before
  anything is written.** `vpn.advanced.switchWindowMax`/`redialWindowMax` are
  deliberately not preset keys — a strictness macro must never raise a ceiling
  you set by hand — so lowering one can put a preset out of reach. `config
  preset apply relaxed` under a `10s` `switchWindowMax` used to fail with
  `vpn.switchWindow 30s exceeds vpn.advanced.switchWindowMax 10s`, which names
  the validation rule rather than the conflict, while `preset list` and `preset
  diff` went on offering and diffing a preset that could never apply. All three
  now say `cannot apply:` with both values and the way forward (`--json` gets a
  `conflicts` array); the write is refused up front rather than part-way
  through validation. Nothing was ever persisted in either case. The macOS
  app's preset picker reads the same field and greys such a preset out with
  the reason beside it, instead of offering a button that fails when pressed.

- **A renamed config key nested under a miscased parent block keeps its rename
  hint.** The report shows a key with the spelling your file uses, so it can be
  found and fixed — but that meant `{"VPN": {"profiles": [{"ifaceHint": …}]}}`
  reached the rename table as `VPN.profiles[0].ifaceHint` and missed, degrading
  "renamed to `vpn.profiles[].tunnelHint`" to a bare "not a recognised config
  key". Both were truthful that the value is dead; only one told you what to
  change it to. The lookup now ignores letter case.

- **`dezhban switch --status` leads with a stable token again.** Growing a
  rendered prose sentence left the open branch with no fixed string, while the
  closed branch still printed `switch window: closed` — so a script testing for
  `OPEN` silently stopped matching and read an open exposure window as closed.
  Output is now `switch window: OPEN — <sentence>` (or `pause: OPEN — …`),
  keeping the sentence and the `until: <RFC3339>` line.

- **`config set` now says when it stored something other than what you typed.**
  Nine of the twelve `vpn.advanced.*` keys have no disabled state, so a `0`
  meant as "off" is replaced by the shipped default. The echoed value was
  already honest, but silently so; a `note: <key> was normalised on write:
  <typed> → <stored>` line now appears whenever the two differ. The three
  windows and `redialMinUptime`, whose `0` is a real opt-out, are unaffected
  and draw no note.

- **`dezhban doctor` can no longer drop a check on the floor.** The text layout
  indexed checks by name, so two checks sharing a name — or one whose name the
  layout has no section for — never printed, and a missing check reads exactly
  like a check that passed. Both cases now print; the shipped checks are also
  pinned unique by a test.

- **A retired config key nested under a miscased block is no longer reported as
  having taken effect** — and the spelling the report tells you to change to is
  now one the schema actually has. Reported key paths deliberately keep your
  file's own casing so the line can be found, but the *canonical* path was being
  built from that same prefix, so `{"VPN": {"Enabled": true}}` reached the
  retirement table as `VPN.enabled`, missed, and printed `"VPN.Enabled" … TOOK
  EFFECT` directly beneath the correct `"vpn.enabled" has no effect` — the
  discarded-setting-looks-live lie the whole report exists to prevent, and only
  the parent block's casing separated the two outcomes. The same prefix made
  `the schema spells it "VPN.profiles"` name a path that exists nowhere, leaving
  the one actionable part of the line wrong. The canonical path is now the
  schema's own name at every level.

- **`config set --token-stdin` now says when it stored something other than what
  you typed.** The `note: <key> was normalised on write: …` line only appeared
  on the privileged path: the daemon performs the token/socket write, so the CLI
  never held the normalised config and reported just `Saved and applied: <key>`
  — a true statement about a value the operator did not type, on the path the
  macOS app and every script prefer. The CLI now takes both readings itself and
  the note appears on either path.

- **The menubar's Pause item no longer blames `vpn.pauseMax` when dezhban simply
  isn't running.** Two independent reasons greyed it out and one tooltip covered
  both, so a stopped daemon with a perfectly ordinary `vpn.pauseMax: "30m"` read
  `Disabled — vpn.pauseMax is "0" in your config.` and sent you to fix a key that
  was already right (`status --json` is read-only, so it answers correctly with
  the daemon down — which is exactly why the two causes were indistinguishable).
  The tooltip now names the reason that applies.

- **A refused preset no longer announces that it was applied.** `config preset
  apply` printed its `applying <name>: …` banner and the preset's full cost
  paragraph before the check that refuses a preset your own advanced caps
  forbid, so the refusal arrived under a heading claiming the write had started
  and a description of a trade you never made. The check now runs first.

## [0.7.0] - 2026-07-22

### Added

- **`vpn.armAtBoot`** (default **true**): arms the guard directly at startup,
  even before the VPN's tunnel interface exists, on any host that has
  connected successfully at least once and has a known endpoint. Closes a real
  gap — `internal/runner` decided STANDBY from a live interface probe taken
  fresh on every start, so a normal boot (this daemon starts before the VPN
  client) opened the network for however long the VPN took to reconnect, on
  every reboot, even on hosts that had run the guard for months. A fresh
  install, or a host whose VPN has never come up, still starts in STANDBY —
  this cannot turn a misconfiguration into a lockout. See
  [ADR-0008](docs/adr/0008-arm-at-boot.md).
- **`dezhban pause [duration]` / `dezhban resume`**: a bounded, deliberate drop
  to the real ISP IP (e.g. to reach a domestic-only service a VPN exit can't
  reach), auto-reverting at the deadline with no further action. A third
  sanctioned relaxation of the guard alongside the switch window and the
  automatic reconnect window, with its own cap (`vpn.pauseMax`, default 30m,
  `"0"` disables) and its own control-socket gate (`control.allowPauseOps`,
  default true, independent of `control.allowSwitchOps`).

### Changed

- **README now leads with the macOS app**, with the CLI presented as the
  headless option for Linux, servers, and terminal users — it previously
  mentioned the app only twice, both in passing, despite the app covering
  every everyday operation. Adds a platform-support table marking Windows
  **experimental** (no passwordless control path yet) rather than a peer
  install target.
- **`docs/` reorganized from a flat 17-file list into `usage/`, `concepts/`,
  and `contribute/`**, grouped by audience; `docs/adr/` is unchanged.
  Duplicated explanations of the same concepts (the country-check hold
  behavior, the switch/reconnect/pause windows) are consolidated into single
  canonical homes in `docs/concepts/modes.md`, which also gains two ASCII
  diagrams (the posture state machine, the window-trigger comparison).
- Five of seven ADRs were marked `implementation pending` in the decision-log
  index despite having shipped; statuses now match reality (`docs/adr/README.md`).
- `CLAUDE.md` corrected against a full verification pass: the dependency count
  (four third-party modules, not three), the read-only/root command split, the
  global-flags list, and the subcommand list, plus a new doc-maintenance
  convention routing config/CLI/behavior changes to their canonical doc.

### Fixed

- Removed `legacy` as an offered `--mode` value from the Taskfile description
  and all three shell completions (bash/zsh/fish) — `print-rules --mode legacy`
  has errored by name since ADR-0001, but the completions still suggested it.
  Also added the `--no-sudo`/`--no-daemon` global flags to all three
  completions, which were missing entirely.
- Retired "legacy direct model" language from `internal/firewall` comments
  (`backend.go`, `policyset.go`, the three per-OS renderers): the code path
  they describe is live — it's what `block --force` renders — not a leftover
  of the country-blocklist model ADR-0001 removed. One dead `docs/plans`
  reference in `backend.go` is also fixed. The same sweep now covers
  `cmd/dezhban/main.go`, whose comments still called the watcher's job "the
  legacy kill switch" and described `print-rules` populating a legacy
  allowlist it no longer builds.
- `print-rules --mode` help text still offered `legacy` alongside the live
  modes; it now lists `guard, fullblock, or switch` — matching what the
  command actually accepts and what the completions suggest.
- Every `docs/*.md` path cited from Go/Swift source and shell scripts now
  points at the reorganized hierarchy — including the user-visible ones:
  the paths `dezhban upgrade` prints in its guidance and the one shown in
  the app's **About → Updates** pane.
- **`dezhban config set vpn.switchWindow 0` now disables manual switch windows**
  instead of being silently coerced back to the 5s default by `Normalize` — the
  same explicit-opt-out sentinel `vpn.reconnectWindow` already used. `config get`
  now reports the disabled state as `0s` rather than a negative duration, and
  `dezhban status` prints `switch window: off` instead of the raw sentinel.

## [0.6.0] - 2026-07-22

### Fixed

- **GUI main-thread crash on launch/settings.** `DezhbanCLI.exec` spawned a
  `Process` and blocked on `waitUntilExit`, which spins the calling thread's
  run loop instead of simply blocking; on the main thread this re-entered
  AppKit's display cycle mid-SwiftUI-body-evaluation and corrupted state,
  crashing on a null PC. The CLI's config-path resolution is now memoized
  behind a lock and split into a non-blocking `displayConfigPath` (safe in a
  `body`) and a blocking, background-only `resolvedConfigPath()`; every
  remaining main-thread call site was moved off-main or reads the memoized
  value, and `exec` now asserts `!Thread.isMainThread` so a regression fails
  loudly in development.
- Sidebar toggle misalignment/relocation-on-click in the macOS app: the
  window hosted its SwiftUI root via a bare `NSHostingView` instead of an
  `NSHostingController`, so `NavigationSplitView`'s toolbar-based sidebar
  toggle couldn't install into the titlebar and SwiftUI drew its own inline
  toggle instead.
- Dock icon states now match what `PostureUI.dockState` actually reports
  (`on`/`blocked`); the unused intermediate asset states were removed rather
  than left to silently bit-rot.

## [0.5.0] - 2026-07-22

### Added

- **`dezhban upgrade check|download|apply`** — self-update for macOS. `check`
  is the only network call anywhere in the upgrade path and never runs in the
  root daemon (GUI on launch/~24h, or CLI on demand); `download` fetches and
  verifies the `.pkg` into a root-owned staging directory so a local user can't
  swap it before `apply` installs it. Installing opens no enforcement gap (the
  running daemon keeps enforcing on its old inode while the files land) —
  only *activating* (the restart) is the exposure, and it's gated on
  `internal/update.CanActivate` (healthy `guard`/`standby` only, re-checked at
  the instant of restart). The menubar app surfaces the same flow under
  **About → Updates**, with one confirmation and a self-relaunch. See
  [docs/upgrade.md](docs/usage/upgrade.md) and
  [ADR-0007](docs/adr/0007-upgrade-disclosed-window-not-holding-block.md).
- curl/PowerShell installers plus `.deb`/`.rpm` packaging, wired into CI
  alongside the existing macOS `.pkg`.
- `vpn.advanced.reconnectWindowMax` (default `10m`) — an independent hard cap
  for the automatic reconnect window, kept separate from
  `vpn.advanced.switchWindowMax` (see Changed below).
- The macOS app's Settings pane now also exposes `pollInterval` and
  `vpn.reconnectWindow`, previously config-file-only.
- **Reset to Defaults** in the macOS app's Settings pane. Runs
  `dezhban config reset --all` rather than carrying a second copy of the
  defaults in Swift, so `config.Default()` stays the only place they live, and
  inherits that command's identity carve-out: blocked countries, tunnel
  interfaces, endpoints, and saved profiles are preserved, so a reset can never
  silently unblock a country or forget your VPN.

### Changed

- **The curl/PowerShell installers now tell a first-time install from an
  upgrade.** Both read the outgoing version before anything is overwritten and
  branch their closing guidance on it: a fresh install still gets the
  `setup` + `start` walkthrough, while an upgrade or same-version reinstall
  drops `setup` entirely — it would have walked an existing user through
  replacing a config they already had — and instead reports `old -> new`,
  states that config and learned state were left untouched, and says whether
  the service was restarted or left stopped. A prior binary too old or broken
  to report its version classifies as an upgrade, never as fresh.

- **The VPN Guard and Settings sections of the macOS app are merged into one
  Settings pane.** The two sections split VPN keys along a seam that didn't
  match how they relate (`switchWindow`/`endpointGrace` lived in Settings,
  `endpointRefresh`/`tunnelWatch` in VPN Guard); merging removes it. The
  combined pane now awaits the restarted daemon's posture on Apply (as VPN
  Guard always did), since it carries guard-affecting keys.
- **Defaults retuned for a safer, less configuration-dependent out-of-box
  posture** (2026-07-22 defaults review):
  - `vpn.autodetect` and `vpn.autoDiscoverEndpoints` now default `true`
    (previously `false`, with `autodetect` only implied when no
    `tunnelInterfaces` were pinned). Explicit `tunnelInterfaces` still win;
    set either to `false` explicitly to opt out.
  - `blockedCountries` now defaults to `IR,RU,KP` **when the key is absent**.
    An explicit `blockedCountries: []` is a deliberate "block nothing" and is
    never overridden.
  - `vpn.switchWindow` default drops `15s` → `5s`, and its floor (`10s`) is
    removed entirely — any positive duration up to the cap now validates.
    `vpn.advanced.switchWindowMax` drops `5m` → `3m`.
  - `vpn.reconnectWindow`'s floor (`5s`) is also removed. It now has its own
    independent cap, `vpn.advanced.reconnectWindowMax` (default `10m`), no
    longer sharing `switchWindowMax` — sharing one cap between the two
    triggers would have silently truncated whichever trigger has the larger
    intended budget.
  - `vpn.advanced.windowDiscoveryInterval` drops `2s` → `1s`, so the new
    shorter `switchWindow` default still gets several discovery ticks.
- Upgrade stash lifecycle hardening: the upgrade stash is now classified
  against the *running* version rather than the one on disk, closing a gap
  where a stash could be misjudged after a partial or interrupted upgrade.
  Checksum/verify hardening and a CI seal check were added alongside it.

## [0.4.0] - 2026-07-21

### Removed

- **BREAKING: the country-blocklist fallback mode is gone.** dezhban now has a
  single enforcement model — the always-on interface guard. The
  `vpn.enabled: false` mode watched your public IP and cut egress by destination;
  it applied **no rules at rest**, so it was "best-effort, not a zero-leak
  guarantee" by its own documentation, and it was only meaningful when the
  country you blocked was your *real physical location*. The guard already
  contains the country check — that is what FULL BLOCK is. See
  [ADR-0001](docs/adr/0001-single-guard-mode.md).
- `print-rules --mode legacy` now errors by name instead of rendering a posture
  that no longer exists. `guard` / `fullblock` / `switch` are unchanged.
- `status --json` drops `mode` (`"vpn"`/`"legacy"`) and `vpnEnabled`. Both had
  exactly one possible value after the merge; a constant field is noise, not
  compatibility. `posture` is unchanged and remains the field to read.
- There is no `failClosed` setting. Under the guard, the standing rules **are**
  the fail-closed block, so an undeterminable country holds the current posture
  rather than escalating — escalating would cut the tunnel's own egress and
  livelock the reconnect that could fix the lookup.

### Added

- **`vpn.allowLocalNetwork` (default `true`) — LAN devices keep working while the
  guard is armed.** There was previously **no local-network handling anywhere**:
  none of the three backends contained a single reference to RFC1918, link-local
  or multicast, so arming the guard made printers, NAS, the router's admin page,
  AirPlay/Chromecast and local dev servers unreachable with no setting to get
  them back. The passes are **destination-scoped**, never interface-scoped, so
  they cannot become an internet path — packets to public addresses stay blocked
  whatever the next hop is — and they cost nothing against the threat model,
  since this traffic never leaves the building. Multicast is included because
  mDNS/SSDP is what actually makes discovery work; unicast alone would leave
  devices visible but undiscoverable. Set `false` on untrusted networks, where
  the real cost is that other devices there can reach you.
  See [ADR-0005](docs/adr/0005-allow-local-network-by-default.md).
- `dezhban status` now prints an `also reachable:` line naming exactly what stays
  open on the physical link (local network, DNS, or neither). These are the only
  standing exceptions to "only the tunnel may egress", so they should not have to
  be inferred from the config file.
- **`vpn.switchWindow: "0"` now disables manual switch windows.** Previously it
  was silently coerced back to the 15s default, so the setting was accepted and
  discarded — the worst failure mode a security tool has. It now uses the same
  explicit-opt-out sentinel `vpn.reconnectWindow` already had. Setting both to
  `"0"` is the strict zero-leak posture in which nothing can relax the guard.
  Disabling one never disables the other. `dezhban switch` refuses by name,
  telling you which setting is responsible. See
  [ADR-0004](docs/adr/0004-switch-window-fully-disableable.md).
- **Retired keys are reported, not ignored.** `vpn.enabled`, `failClosed` and
  `allowlist` still parse without error, do nothing, and are named — with a
  reason — by `dezhban validate` and once at daemon start. They are never
  written back when dezhban saves your config.
- **Architecture decision records** under [`docs/adr/`](docs/adr/), plus a
  [glossary](docs/concepts/glossary.md) fixing the "guard"/"protection"/"kill switch"
  vocabulary drift. GUARD is the canonical term.

### Fixed

- **The recovery probe no longer lifts the guard — a recurring leak is gone.**
  While in FULL BLOCK, observing the exit country meant applying the **GUARD**
  ruleset (full tunnel egress) for up to 8 seconds on *every* probe tick, just to
  make one HTTP request, for as long as a forbidden exit persisted. FULL BLOCK
  now carries a standing pass scoped to the tunnel interface **and** the geo
  providers' addresses, so the lookup completes with no rule change and no leak.
  The double scoping is load-bearing: with the tunnel down the lookup fails and
  the posture holds — correct, since there is no exit to measure — whereas a pass
  on the *physical* link would succeed and report the ISP's country, silently
  defeating the check. Provider IPs refresh on the endpoint cadence, since
  CDN-fronted providers rotate. If none resolve, recovery falls back to the old
  lift-and-probe rather than losing the ability to recover at all.
  The provider rule deliberately carries **no DNS pass**: a tunnel-scoped but
  destination-unscoped `port 53` rule would send *every application's* DNS
  through the tunnel to the forbidden exit's resolver for as long as FULL BLOCK
  lasted, handing the exit whose country we are refusing a running log of every
  hostname the host looks up. The set is refreshed while the guard is healthy,
  and a mid-block rotation falls back to lift-and-probe, which heals it.
  See [ADR-0006](docs/adr/0006-geo-providers-tunnel-scoped.md).
- **The local-network pass no longer includes globally-routable multicast.**
  `224.0.0.0/4` and `ff00::/8` were shorthand for "multicast", but they contain
  scopes designed to cross the internet (`232/8` SSM, `233/8` GLOP, `ff0e::/16`
  global) — which a pass justified by "this traffic never leaves the building"
  must not contain. Narrowed to the local and administratively-scoped ranges
  that discovery actually uses: `224.0.0.0/24`, `239.0.0.0/8`, `ff02::/16`,
  `ff05::/16`. mDNS, Bonjour, SSDP, AirPlay and Chromecast are unaffected.
- Invalid addresses dropped at the policy seam are now logged. Dropping is
  correct — one `invalid IP` entry would make pf reject the whole ruleset — but
  a silent drop of a VPN endpoint presents as a tunnel that will not handshake,
  with nothing connecting the two.
- FULL BLOCK now carries tunnel **groups** as well as concrete interfaces, so a
  host that names only an interface class (`utun`) gets a scoped provider pass
  instead of silently degrading to lift-and-probe.
- **Failed exit-country lookups are now classified instead of all being reported
  as errors.** Three causes collapsed into one alarming message, and the most
  common was not a fault at all: during a switch or reconnect window the tunnel
  is *supposed* to be down — that is why the window exists — so there is no VPN
  exit to measure and the lookup failing is correct behaviour. That is now
  reported as a state (`exitUnknown`: "no tunnel is up, so there is no VPN exit
  to check") rather than an error. `lookupErr` is reserved for a failure with a
  tunnel **up**, where there really was an exit to measure and something went
  wrong — which may mean the exit itself is censoring the geo providers. The two
  fields are mutually exclusive; the macOS app and `status --json` render them
  differently. This is what made the geo providers look broken during every
  window.
- **IPv4-in-IPv6 addresses are now unmapped at the policy seam — a silent lockout.**
  pf does *not* reject `::ffff:1.2.3.4`; verified with `pfctl -nvf`, it accepts
  the rule and expands it to `pass out quick inet6 … to ::ffff:1.2.3.4`. Real
  IPv4 traffic never matches that, so the pass is effectively absent while
  looking perfectly present in `pfctl -sr` — and when the address is a VPN
  endpoint, the tunnel's own handshake is blocked and the VPN can never connect.
  Callers each remembered `.Unmap()` individually except the learned-endpoint
  reload, which is exactly how a per-caller convention fails. Now normalised once
  in `firewall.PolicyInput` so no backend or caller has to defend itself.
  Invalid addresses are dropped rather than rendered, since the zero
  `netip.Addr` stringifies to `invalid IP` — a ruleset pf genuinely does reject,
  turning one bad entry into a total failure to install any rules.

### Changed

- **Upgrade note — no config migration is required.** A pre-merge config loads,
  validates, and enforces identically; the three retired keys are reported and
  ignored. Two behavior changes to know about:
  - A config that had `vpn.enabled: false` was running the fallback mode. It now
    runs the guard, which means it rests in **STANDBY** (no rules, network fully
    open) until a tunnel is configured *and* observed up — rather than watching
    your public IP. If you were relying on the fallback, there is no longer an
    equivalent; the guard requires a VPN.
  - `vpn.endpoints` is no longer required at load time. A config with none is
    valid and rests in STANDBY. The check moved to where it can tell the
    difference: the runner refuses to *arm* a guard that has tunnels but no
    endpoints, and `doctor` reports it as a lockout risk beforehand.
- **STANDBY is a first-class posture**, not an emergent property of `autoArm`.
  It is the resting state before any tunnel has been observed: no rules, network
  fully open, and the UI says so — grey icon, never red. This is the job
  `vpn.enabled: false` was quietly doing as a safety opt-in, now done properly.
  See [ADR-0002](docs/adr/0002-standby-no-tunnel-posture.md).
- **One constructor builds every posture** (`firewall.PolicyInput`). The run loop
  and `print-rules` previously built them separately, and had already drifted —
  the preview dropped `TunnelGroups` entirely and degraded a zero-tunnel guard on
  a different condition than the daemon. A preview that can lie about what the
  daemon would install is a correctness bug, not untidiness.

## [0.3.0] - 2026-07-20

### Changed

- **Upgrade note — posture defaults change for existing configs.** The defaults
  review below makes `vpn.autoArm` and `vpn.allowPhysicalDNS` default **on**. A
  config that never set `vpn.autoArm` (the previous default was off, and it was
  omitted when false) will **arm on VPN connect / park in standby instead of
  arming from boot** under this release — a real posture change, not a no-op.
  Every tunnel drop now also opens a **30s reconnect window** by default (real IP
  may be exposed while the client redials) unless you set `vpn.reconnectWindow:
  "0"`. To keep the pre-upgrade strict posture, set `vpn.autoArm: false`,
  `vpn.allowPhysicalDNS: false`, and `vpn.reconnectWindow: "0"`. (An explicit
  `allowPhysicalDNS: false` already on disk is preserved; only omitted keys pick
  up the new default.)
- **Defaults review (2026-07-19)** — the shipped defaults now favor the
  smooth-operation posture; every previous value remains one config line away:
  `vpn.autoArm` **on** (standby until a VPN connects — no more mystery blackout
  when starting without a VPN), `vpn.allowPhysicalDNS` **on** (hostname redials
  work while the tunnel is down; explicit `false` still closes the DNS-metadata
  leak), `pollInterval` **15s** + `hysteresis` **2** (~30s worst-case
  forbidden-exit confirmation), `vpn.switchWindow` **15s** (windows close early
  on success; `--for` extends a one-off), `vpn.endpointRefresh` **1m**.
  `autoArm`/`allowPhysicalDNS` became pointer fields on disk so an explicit
  `false` survives normalization.
- **Five new geo-IP providers** (geojs.io, country.is, ipwho.is,
  freeipapi.com, ipapi.co) join the existing three, and the default provider
  list is now ordered by rate-limit headroom — the first reachable provider
  absorbs nearly all poll traffic, so unmetered endpoints go first and
  quota-limited ones (ipinfo, ipapi.co) become deep fallbacks. Provider-side
  failure shapes (ipwho.is `success:false`, ipapi.co `error:true`) fail closed.

### Added

- **Automatic reconnect window** (`vpn.reconnectWindow`, default `30s`, on by
  default; `"0"` disables): a tunnel drop from healthy GUARD now opens the
  bounded switch-window relaxation automatically, so a VPN client can redial
  *any* server — including a never-seen one (rotating-pool / 443-fronted
  anti-censorship VPNs) — or a different VPN app entirely, with zero operator
  interaction. Closes early and learns the new endpoint on a confirmed good
  exit; fail-closes and stays closed on expiry. Guarded by an anti-flap gate
  (`vpn.advanced.reconnectMinUptime`, default `15s`) and never opens from
  standby, FULL BLOCK, or for a tunnel never observed up. The manual
  `dezhban switch` window remains as the fallback for edge cases.
  `status --json` labels an open window's origin via the additive
  `switch.trigger` field (`"manual"`/`"auto"`); the menubar app shows a
  distinct "VPN dropped — reconnect window open" banner and notification.

- **`dezhban config reset <key> [key ...]` / `config reset --all`** — restore
  shipped defaults from the CLI. `--all` resets every tunable while preserving
  identity data (blockedCountries, allowlist, vpn.enabled / tunnelInterfaces /
  endpoints / profiles); deleting the config file remains the true wipe.
- **Persistent log capture, always on**: every daemon run — interactive or
  under the service manager — now also appends to
  `<state dir>/logs/dezhban.log` (0644, size-rotated at 5 MiB with two
  archives), so history survives shell exits and is readable without root;
  stderr and the platform logger keep working exactly as before.
- **Touch ID for the menubar app's privileged prompts**: elevation now prefers
  `sudo` + `pam_tid` (the system Touch ID HUD) when Touch ID for sudo is
  configured, falling back to Authorization Services and then the legacy
  osascript dialog — in practice the `system.privilege.admin` SecurityAgent
  prompt never offers biometrics, so the sudo path is the one that actually
  delivers them. `dezhban doctor` now surfaces the one-line
  `/etc/pam.d/sudo_local` opt-in when it is missing.

### Fixed

- Saving a config (`config set`, the setup wizard, GUI settings) silently
  dropped `vpn.endpointGrace` and `vpn.autoArm` — both now round-trip, and an
  absent `endpointGrace` normalizes to its effective `15m` default so
  observers see the real value instead of `0`.
- The menubar/Dock icon now shows the blocked (red) state in the zero-tunnel
  standing posture — VPN guard armed with no tunnel present is a total egress
  cut and no longer renders the calm green shield.

## [0.2.0] - 2026-07-18

### Added

- **`vpn.autoArm`** (default off): the daemon starts PASSIVE (new posture
  `standby`, nothing enforced) when no tunnel interface is present, and arms
  the guard automatically the moment a VPN connects — no more choosing between
  "always blocked without a VPN" and "kill switch off". Arming is one-way on
  tunnel loss (a drop is exactly the leak the kill switch exists for); an
  explicit `unblock` with the tunnel down releases back to standby. Arm-time
  endpoint checks preserve the no-endpoint blackout refusal, and switch
  windows are refused in standby (egress is already open). Toggle in the
  macOS app's VPN Guard pane.
- **macOS: notifications for essential events** — guard armed / auto-armed,
  egress blocked, warnings (enforcement error, switch window open), standby,
  stopped. Posted by the menubar app on posture transitions only (never at
  launch, never for routine updates); toggle in Settings.

- **Brand assets wired in end-to-end** (`gui/assets/`): full-color menubar and Dock
  state icons (teal on / gray off / red blocked / amber warning), a generated
  `AppIcon.icns`, and the README banner. The Dock tile mirrors the enforcement
  posture (the app is no longer an `LSUIElement` agent).
- **`vpn.endpointGrace`** (default `15m`): an autodiscovered endpoint now stays
  in the allowed set for a grace period after a refresh stops reporting it, so
  a dropped VPN can redial the same server without needing a switch window.
  Discovery could only see an endpoint while its socket lived — and the socket
  dies with the tunnel, which walled off exactly the reconnect the guard keeps
  endpoints open for.
- **macOS: Settings hub in the app**: startup controls (install/uninstall the
  boot service, open the app at login), blocked countries, switch-window
  duration, endpoint grace, and the config-file escape hatch. Replaces the
  scattered "VPN guard mode" / "Open config file…" / "Launch at login" menu
  items. About now also reports which elevation path privileged actions take
  (Authorization Services with Touch ID vs. the password-only fallback).

### Changed

- **Dev-task vocabulary overhauled** (developer-facing only; no runtime change).
  The Taskfile shrinks to ~15 intent-named commands in four groups — everyday
  (`build`, `check`, `dev`, `clean`), safe loop (`monitor`, `validate`, `rules`,
  `doctor`, `status`), real install (`pkg`, `install`, `uninstall`, `panic`),
  and the unchanged release trio. Renames: `dev:all`→`dev`, `pkg:build`→`pkg`,
  `pkg:cycle`→`install` (`pkg:fresh` is now `install FRESH=1`),
  `pkg:uninstall`→`uninstall`, `run-dry`→`monitor`. The source-install wrappers
  (`install-local`, `reinstall`, `uninstall-local`) are gone — the scripts
  remain standalone (`sh scripts/install-local.sh`). Bare `task` on a TTY now
  opens an interactive picker (`tools/taskmenu`, built on the huh dependency
  the setup wizard already uses); non-TTY prints a grouped menu. Privileged
  flows ask for sudo up front, destructive ones confirm first, and behavior
  vars are asked on-demand: unset on a TTY, `install` asks "wipe first?" and
  `uninstall` asks "keep config?", and the release tasks ask for the
  bump/version spec — passing `FRESH=`, `KEEP_CONFIG=`, `VERSION=`/`BUMP=`
  explicitly (or having no TTY) skips the question. Plumbing is hidden behind
  `task --list-all`.
- **macOS app overhauled: one main window, minimal menubar.** A SwiftUI main
  window (sidebar: Overview / VPN Guard / Settings / Logs & Diagnostics /
  About) is now the primary surface — opened via "Open Dezhban…" (⌘O) or the
  Dock icon, never at launch; closing it keeps the app running. The menubar
  dropdown shrinks to the safety core: status line, Block/Unblock, the switch
  window, Panic, Quit. Panic from the menubar still shows its transcript in a
  plain alert, so the escape hatch never depends on the window opening. The
  three separate panel windows (Settings, VPN guard, output) are gone; long
  command output lands in the Logs & Diagnostics pane. Repo layout moved with
  it: `macos-gui/` → `gui/macos/`, `assets/` → `gui/assets/`.

### Fixed

- **A daemon whose run loop ended on its own (startup refusal, run failure)
  lingered as a zombie**: the service manager still counted the process as
  running, so `start` was a silent no-op and only a kill recovered. The
  process now exits when the loop ends by itself, and `stop`'s teardown wait
  is bounded (30s, with a loud log pointing at `dezhban panic`).
- **`switch --cancel` could die with "daemon busy" while a window was open
  with a VPN mid-connect.** The early-close verification probe ran inline on
  the run loop (8s budget vs. the control socket's 2s hand-off), and the CLI
  treated the busy reply as a daemon refusal — which callers rightly never
  escalate. The probe now runs off-loop (verdict and every firewall Apply
  still on the loop), and transient server errors fall back to the durable
  root command-file path.
- **`stop` on a crash-looping (loaded-but-not-running) service reported
  "already stopped" without unloading it**, so KeepAlive kept respawning the
  daemon. The idempotence guard now consults the loaded state, not just
  running. (Post-merge review finding on #21.)
- **macOS: a guard posture with every tunnel down now shows the BLOCKED state
  icon** (menu bar + Dock) instead of the calm "on" — the guard cutting
  physical egress is a blocked state visually, even though the posture string
  legitimately stays `guard`.

- **macOS: start/stop/restart from the menubar app failed with "Expecting a
  LaunchAgents path … Load failed: 5".** The app's admin prompt runs commands as
  root but *inside the GUI login session*, and the legacy `launchctl load`/
  `unload`/`list` used by the service library infer the launchd domain from the
  session, not the uid — so loading the LaunchDaemons plist was rejected, and
  the service was misreported as stopped while running. Service start/stop and
  the root status query on macOS now use the domain-explicit subcommands
  (`launchctl bootstrap system …` / `bootout system/…` / `print system/…`),
  which behave identically under a terminal `sudo` and the app's elevation.
  (`uninstall` also boots the job out first, so it can no longer remove the
  plist while leaving the daemon resident.)
- **A startup refusal is now visible, not just logged.** When the run loop
  refuses to arm (e.g. the VPN guard's "refusing to start: the tunnel is up but
  no server address is known") or fails, the reason is published into the final
  `posture: "stopped"` snapshot as `enforcementErr` — so `status --json` and the
  menubar app can say *why* the daemon is down instead of showing a bare
  "stopped" indistinguishable from a deliberate shutdown.

## [0.1.0] - 2026-07-14

### Added

- **A release is now one command.** `task release BUMP=minor` (or
  `VERSION=0.2.0`) runs a preflight — on `main`, clean tree, synced with origin,
  `[Unreleased]` non-empty, CI green — prints what it is about to do, asks you to
  type the tag to confirm, then dispatches and streams the workflow.
  `task release:preview` shows the resolved version, the rendered notes and the
  CHANGELOG diff without touching anything, and the workflow's `dry_run` input
  does the same at full fidelity on a real runner: it cross-compiles everything
  and install/uninstall-tests the `.pkg`, then publishes nothing. All of it goes
  through one `scripts/release.sh`, which is the same code the workflow runs, so a
  local preview cannot drift from what CI does.
- **Release candidates** (`X.Y.Z-rc.N`). An rc is a pure snapshot: it tags only —
  no CHANGELOG roll — and publishes as a GitHub pre-release, so it never becomes
  "latest" and an abandoned rc line costs nothing to walk away from.
  `bump: patch|minor|major` always counts from the last *final* tag; `bump: rc`
  advances an open rc line.
- The release **never pushes to `main`**. It tags the exact commit it built and
  tested, publishes, and then opens a `chore(release)` PR carrying the rolled
  CHANGELOG — because `main`'s ruleset requires a pull request and the Actions bot
  cannot bypass it (GitHub only permits that on org-owned repos). The ruleset is
  left intact, and no long-lived admin token goes anywhere near CI.
- `dezhban -v version` now reports the commit, build date and Go version
  alongside the version, and `status --json` gained `commit` and `buildDate`. A
  binary built without the Taskfile (a plain `go build`) no longer reports itself
  as an anonymous `dev`: it falls back to the VCS stamps the Go toolchain embeds,
  so it still names the commit it came from and whether the tree was dirty.
- **Standalone macOS installer** (`dezhban-<version>.pkg`, `task pkg:build`):
  installs the CLI, the menubar app, and the launchd service in one step with a
  single password prompt. It registers the service but deliberately does **not**
  start enforcement — configure with `sudo dezhban setup`, then `sudo dezhban start`.
  Ships its own uninstaller (`sudo sh /usr/local/share/dezhban/uninstall.sh`), and
  the release workflow installs + uninstalls it on a runner before publishing.
  Unsigned (no Apple Developer certificate); `build-pkg.sh` has the signing seams.
- **Control socket** (`internal/control`, config `control` block): the daemon
  listens on a root-owned, admin-group unix socket, so `block`, `unblock` and
  `switch` are performed BY the running daemon and **need no password**. Both the
  CLI and the menubar app go through it, falling back to the previous root path when
  no daemon is listening. `panic` and the service lifecycle deliberately stay
  root-only. Tighten with `control.allowSwitchOps: false`, `control.group: ""`, or
  `control.enabled: false`; `dezhban status` reports which mode you're in.
- A manual `block` now **holds**: the geo state machine is suspended until you
  `unblock`, so an allowed reading can't quietly undo an operator's block.
- `config set` accepts several `key=value` pairs in one validated, atomic write
  (`dezhban config set vpn.enabled=true vpn.tunnelInterfaces=utun4`). One prompt,
  one write, and no ordering constraints between interdependent keys.

- `dezhban restart` — stop + start as one command, for applying a config change
  (there is no live reload). `start` and `stop` are now idempotent.

- **Touch ID for the menubar app's admin prompts.** It now elevates through
  Authorization Services (the API behind the System Settings padlock), whose prompt
  offers "Touch ID or password" — and caches the authorization, so a second privileged
  action a moment later is usually silent. The old `osascript` dialog was password-only
  and always had been; it remains as a fallback. For the CLI, enable Touch ID for
  `sudo` (`pam_tid`) — see [docs/usage.md](docs/usage/cli.md#touch-id).

### Changed

- **Makefile replaced by a [Taskfile](https://taskfile.dev)** (`task` lists everything).
  All targets carried over 1:1, plus two new update-roll loops for testing:
  `task dev:all` (fast: rebuild + swap CLI and app in place, restart daemon, relaunch)
  and `task pkg:cycle` (full: cross-compile, build the `.pkg`, install it, open the
  app), with `pkg:fresh`/`pkg:install`/`pkg:uninstall` piecewise variants. The
  `scripts/*.sh` escape hatches still run standalone without `task`. See
  [docs/development.md](docs/contribute/development.md).

### Fixed

- **A failed release used to strand a tag.** The release tagged and pushed
  *before* it built anything, so a broken build or a failed installer smoke-test
  left a pushed tag and a `chore(release)` commit with no release behind them —
  and the workflow's own "tag already exists" guard then refused the retry. The
  order is now resolve → build → smoke-test → *only then* tag and publish, so a
  failed release leaves the repository untouched and re-dispatching is free.
  `publish` additionally refuses to run if `main` moved after the commit it built
  from, rather than tag a tree that was never tested.
- **The release never checked whether the code it was shipping worked.** It ran no
  tests and never looked at CI, so a red `main` released fine. It now requires
  `ci.yml` to be green on the exact commit being released, waiting out an in-flight
  run and aborting on a red or missing one. `force: true` overrides it for an
  emergency, loudly.
- **`task pkg:install` / `pkg:cycle` / `pkg:fresh` could never find the installer
  they had just built.** The Taskfile looked for `dezhban-v0.1-…​.pkg` while
  `build-pkg.sh` writes `dezhban-0.1-…​.pkg` — it strips the tag's leading `v` and
  the Taskfile did not. Every invocation failed the precondition with a misleading
  "run `task pkg:build` first". The `v` is now normalised in one place.
- **Every dev build of the menubar app claimed to be version `0.1.0`**, the
  hardcoded fallback in `Info.plist`, which only a tagged CI build ever overwrote.
  An unstamped build is now a visible `0.0.0`. A release candidate stamps its
  numeric core (`0.2.0-rc.1` → `0.2.0`) into the pkg receipt and bundle rather than
  collapsing to `0.0.0`, since those fields must be dotted numerics.
- **Endpoint auto-discovery reported unrelated hosts as VPN endpoints.** It accepted any
  socket bound to a physical interface IP with a public peer, on the premise that a
  full-tunnel VPN routes everything else through the tunnel. That premise is false: apps
  bind to the physical link all the time. In the wild it returned GitHub, Cloudflare and
  Google — and those addresses went straight into the guard's pass list, so the kill
  switch punched **permanent holes to arbitrary hosts** (a leak) while still blocking the
  real VPN server (a blackout). Discovery now requires the socket to be owned by a
  process that is plausibly a VPN transport; an unattributable socket is not an endpoint.
- **The guard could be armed in a state that cut the tunnel's own transport.** With a
  VPN connected but no known server address, the guard's `block drop out all` covers
  the physical interface — which is exactly what carries the VPN's encrypted transport
  — so arming it killed the tunnel and every packet with it, unrecoverably (the socket
  discovery would have learned the server from died too). `vpn.autodetect` was wrongly
  excusing this; that allowance exists for the *zero-tunnel* case, where a total cut is
  correct and a switch window recovers it. The daemon now refuses to start with a live
  tunnel and no endpoint, and says how to fix it. `doctor` reports it as a LOCKOUT RISK
  and exits non-zero (it also now exits non-zero on tunnel-internal endpoints, which it
  previously reported and then exited 0 on).
  Note: endpoint auto-discovery reads *connected* sockets, and WireGuard (like other
  NetworkExtension clients) sends from an *unconnected* UDP socket — it cannot be
  discovered, and must be named via `vpn import` / `vpn add` / `vpn.endpoints`.
- **The menubar icon is no longer tinted at all.** Both the stopped (gray) and the
  enforcing (green) shields were unreadable on a dark menu bar. It is now a plain
  template image drawn in the menu bar's own color, with the posture carried by the
  symbol — hollow shield (stopped), check shield (enforcing), slashed shield (blocked),
  exclamation shield (switch window open).
- **`stop` failed on a service that wasn't running**, because launchd's
  `launchctl unload` is an edge trigger and errors with a bare "Input/output error"
  when the job was never loaded. Being asked to reach a state you are already in is
  not an error; `start`/`stop` now report it and exit 0. This is what made the GUI's
  config-apply abort halfway — a failed `stop` (on an installed-but-stopped daemon)
  took the following `start` down with it.
- **The daemon's state directory (`/var/db/dezhban`) was created `0700`** by the
  macOS pf backend, which silently broke everything that reads out of it as the
  logged-in user: the menubar app could not read `state.json` (so it showed "Kill
  switch stopped" and "no posture reported" while the daemon was enforcing
  perfectly), and the control socket was unreachable through the directory (so every
  routine `block`/`unblock` fell back to a password prompt — the very thing the
  socket exists to prevent). The directory is now `0755` and `state.EnsureDir`
  repairs an existing too-tight one at daemon startup. Confidentiality was never in
  the directory bit: the sensitive files inside it are `0600`.
- **The menubar app asked for a password once per config field.** Applying the VPN
  panel meant seven separate elevations, plus two more for the restart. The panel now
  sends the whole change as one batched, privileged invocation — **one prompt**. The
  same batching makes "Install service…" one prompt instead of two and "Uninstall
  service…" one instead of three.
- **The menubar icon was invisible on a dark menu bar** when stopped: it was tinted a
  fixed gray. Resting states now draw in the menu bar's own adaptive color; only the
  states that carry a warning keep an explicit color.
- Always-on **VPN interface guard** (`vpn.enabled: true`): egress is allowed only
  through the tunnel, cutting a tunnel drop with a zero leak window, with a bounded
  **switch window** as the only sanctioned relaxation.
- **Country-blocklist fallback** (`vpn.enabled: false`): polls the public IP and
  cuts traffic by destination country for hosts not behind a VPN.
- Cross-platform `FirewallBackend` seam with build-tagged backends: `pfctl`
  (macOS), `nftables` (Linux), WFP/`netsh` (Windows).
- CLI subcommands: `run`, `block`, `unblock`, `status`, `panic`, `install`,
  `uninstall`, `start`, `stop`, `restart`, `detect-vpn`, `validate`, `print-rules`,
  `doctor`, `monitor`, `switch`, `vpn`, `setup`, `config`, `completion`, `version`,
  plus a global `-v`/`--verbose`.
- Read-only diagnostics that need no root: `validate`, `print-rules`, `doctor`,
  `monitor`.
- macOS **menubar GUI** (`Dezhban.app`, `task gui:build`): a standalone Swift
  client that reads the daemon state file and drives the CLI.
- Cross-platform release build matrix (`task build:all`) producing five binaries:
  darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64.

[Unreleased]: https://github.com/Behnam-RK/dezhban/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.9.0
[0.8.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.8.0
[0.7.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.7.0
[0.6.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.6.0
[0.5.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.5.0
[0.4.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.4.0
[0.3.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.3.0
[0.2.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.2.0
[0.1.0]: https://github.com/Behnam-RK/dezhban/releases/tag/v0.1.0
