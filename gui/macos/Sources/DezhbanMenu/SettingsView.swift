import AppKit
import SwiftUI
import DezhbanCore

/// The Settings pane — SwiftUI port of the retired SettingsPanel, merged with the
/// former VPN Guard pane (VPNGuardView, retired 2026-07-22: the two sections split
/// VPN keys along an arbitrary seam — switchWindow/endpointGrace lived here while
/// endpointRefresh/tunnelWatch lived there). Two kinds of controls, two behaviors,
/// stated in the UI:
///   - Startup toggles act IMMEDIATELY (they are service/login-item actions, not
///     config values — there is nothing to batch or restart).
///   - Every other field is staged and written by Apply through one batched
///     `config set` (single validation, write, and admin prompt). The write also
///     applies the change: the CLI asks the running daemon to reload, so a
///     restart is offered only for the few keys the daemon reports it could not
///     adopt live, and only when it says so.
struct SettingsView: View {
    @EnvironmentObject var state: AppState

    @State private var loginEnabled = false
    /// Bumped by every login-item click, so an asynchronous status read that was
    /// already in flight cannot land afterwards and overwrite the result.
    ///
    /// Both readers of `LoginItem` are off the main thread now, and `seed()` runs
    /// on every `didBecomeActiveNotification` — which macOS delivers *during* a
    /// login-item change, since it surfaces System Settings or an approval prompt.
    /// So the ordering was really available: click off, `seed()` starts a read that
    /// still sees the registration, the click's own completion writes `false`, then
    /// the stale read writes `true`. The switch then reads ON with nothing starting
    /// the app at login until the next activation.
    @State private var loginRevision = 0
    /// True while a login-item change is in flight.
    ///
    /// The revision alone was not enough: `seed()` captures the current revision
    /// without bumping it, so a status read *started after* a click shares that
    /// click's revision and passes the equality check. This is the other half —
    /// while a mutation is outstanding, no read may write the switch, whichever
    /// order the two happen to complete in.
    @State private var loginPending = false
    /// The login item's own result line.
    ///
    /// Separate from the pane's shared `status` because the two are different facts
    /// with different lifetimes: `seed()` rewrites `status` on every
    /// `didBecomeActiveNotification` — which macOS delivers *during* a login-item
    /// change, since it surfaces System Settings or an approval prompt — and the
    /// service toggle owns it for the length of a privileged sequence. Sharing the
    /// line meant the `awaitingApproval` and `legacyStuck` guidance, the entire
    /// reason `Outcome` carries a message, could be wiped before it was read; and
    /// guarding it in one direction only moved the loss to the other.
    @State private var loginMessage: String?
    /// True while `loginMessage` explains a refusal the user has to act on.
    ///
    /// A refresh may clear a message about a moment that has passed; it must not
    /// clear one that is the only account of why a click did not take. See
    /// `LoginItem.Outcome.isTransient`.
    @State private var loginMessageIsTransient = true
    /// What the switch read when `loginMessage` was written.
    ///
    /// A message that explains a refusal has to survive a refresh, but not forever:
    /// once the user clears the condition in System Settings and comes back, the
    /// switch moves and the refusal is false. Without this, that text stayed on
    /// screen contradicting the switch, clearable only by another click.
    @State private var loginMessageForEnabled: Bool?
    @State private var notifyPrefs = NotificationManager.prefs
    @State private var checkUpdatesEnabled = true
    @State private var launchVisibility: LaunchVisibility = .bootOnly

    /// Displayed in the Config file row and used by "Open Config File…". Seeded from
    /// the memoized value (warmed at launch) and refreshed off the main thread by
    /// `seed()`, so neither the body nor a button action ever shells out.
    @State private var configPath = DezhbanCLI.displayConfigPath

    @State private var fields = SettingsFields()

    /// The values this pane was last seeded with, keyed by config key. Comparing
    /// the live fields against these is how an unsaved edit is told from a pane
    /// that is merely displaying what is on disk — which decides whether it is
    /// safe to re-read the file underneath the user.
    @State private var seededValues: [String: String] = [:]

    /// What every key IS, read once from `config schema --json`. Nil until it
    /// loads, and nil forever against a CLI too old to know the subcommand —
    /// every use falls back to a plainer label rather than a hardcoded value,
    /// because a wrong hint is worse than a terse one.
    @State private var schema: ConfigSchema?

    private var hasUnsavedEdits: Bool {
        !seededValues.isEmpty && fields.currentValues != seededValues
    }

    @State private var status = ""
    @State private var canApply = false
    @State private var bootBusy = false
    @State private var restartBusy = false
    @State private var presets: [PresetSummary] = []
    @State private var presetDrift: PresetDiff?
    @State private var presetBusy = false
    @State private var tokenBusy = false
    @State private var tokenEnrolled = ControlToken.isStored
    /// Seeded at init with the answer only when giving it costs nothing, and
    /// resolved in the background otherwise — nil means "not known yet".
    ///
    /// Deliberately NOT cached for the process's lifetime. The keychain half of
    /// `capability` is memoized inside `ControlToken`; the biometry half is
    /// re-asked on every `.onAppear`, because it really does change (clamshell
    /// mode, Touch ID lockout) and a menubar app that froze it once per process
    /// would leave the toggle greyed out until the user quit.
    ///
    /// **`refreshTokenCapability()` — called from `seed()` — is what re-asks it,
    /// and that is not an optimisation, it is the only thing that does.** The
    /// initialiser below runs exactly once per view identity: SwiftUI installs
    /// `@State` storage the first time and DISCARDS the initial value on every
    /// later re-creation of the struct, so re-creating `SettingsView` (which
    /// `DetailHostView.body` does on every `AppState` publish) does *not* re-run this.
    /// The predecessor of this property was a plain `let`, which did refresh that
    /// way; converting it to `@State` moved the responsibility to `seed()`.
    /// Deleting that call would silently freeze the verdict for the life of the
    /// window. `tokenEnrolled` above is re-read there for exactly the same reason.
    ///
    /// `capabilityIfKnown`, never `capability`: the latter may run the keychain
    /// probe, and a stored-property initialiser runs on the main thread. That is
    /// not merely slow — a locked login keychain answers a write with a system
    /// dialog, so it is a frozen window. `warmCapability()` covers this when the
    /// main window first appears, but cannot cover a sensor that was unavailable
    /// *then* and is available now, which is exactly the clamshell case the
    /// paragraph above describes.
    @State private var tokenCapability: TokenCapability? = ControlToken.capabilityIfKnown

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("Strictness preset") {
                    presetPicker
                }
                // Ordered by what a person is actually deciding: which VPN to
                // trust, what to block, when the guard may relax, and only then
                // the machinery. Headings say the thing rather than the config
                // block it lives in, per docs/concepts/glossary.md.
                Section {
                    schemaField("vpn.tunnelInterfaces", "Your VPN tunnel (comma-separated)",
                                text: $fields.tunnelInterfaces)
                    schemaField("vpn.endpoints", "VPN server addresses (comma-separated)", text: $fields.endpoints)
                    schemaToggle("vpn.autoDetect", "Find my VPN tunnel automatically", isOn: $fields.autoDetect)
                    schemaToggle("vpn.autoDiscoverEndpoints", "Find the VPN server address automatically",
                                 isOn: $fields.autoDiscover)
                    schemaToggle("vpn.autoArm", "Arm the guard when a VPN connects", isOn: $fields.autoArm)
                } header: {
                    sectionHeader("Your VPN",
                                  "Which tunnel the guard trusts. Leave the two fields empty and let "
                                      + "dezhban find them — that is what most setups want.")
                }

                Section {
                    schemaField("blockedCountries", "Blocked countries (comma-separated)",
                                text: $fields.blockedCountries)
                    durationField("pollInterval", "Exit country check interval", text: $fields.pollInterval)
                    // Visible captions, not hover help: these two decide what a
                    // FULL BLOCK still lets out, and a consequence nobody hovers
                    // over is a consequence nobody chose. Copy comes from the
                    // schema (the daemon owns behavioral claims).
                    schemaToggleWithCaption("vpn.allowPhysicalDNS", "Keep DNS working while the tunnel is down",
                                            isOn: $fields.allowPhysicalDNS)
                    if schema?["vpn.allowGeoProviders"] != nil {
                        // Rendered only when this CLI knows the key: a toggle
                        // that writes a key the daemon rejects can't "degrade
                        // to a plainer control" — omission is the honest degrade.
                        schemaToggleWithCaption("vpn.allowGeoProviders", "Keep exit checks running when blocked",
                                                isOn: $fields.allowGeoProviders)
                    }
                } header: {
                    sectionHeader("What gets blocked",
                                  "If your VPN surfaces in one of these countries, everything is cut "
                                      + "until it moves.")
                }

                Section {
                    durationField("vpn.switchWindow", "Switch window", text: $fields.switchWindow)
                    durationField("vpn.redialWindow", "Redial window", text: $fields.redialWindow)
                    durationField("vpn.pauseMax", "Longest pause", text: $fields.pauseMax)
                } header: {
                    sectionHeader("When the guard relaxes",
                                  "The only three ways traffic is ever let out around the guard. Each is "
                                      + "bounded, re-arms itself, and can be turned Off entirely.")
                }

                Section {
                    schemaToggle("vpn.allowLocalNetwork", "Keep local devices reachable",
                                 isOn: $fields.allowLocalNetwork)
                } header: {
                    sectionHeader("Local network",
                                  "Printers, NAS, your router — reachable while the guard is armed. Local "
                                      + "destinations only, so nothing on the internet is opened.")
                }

                Section {
                    durationField("vpn.endpointRefresh", "VPN server address refresh", text: $fields.endpointRefresh)
                    durationField("vpn.endpointGrace", "VPN server address grace", text: $fields.endpointGrace)
                    durationField("vpn.tunnelWatch", "Tunnel check interval", text: $fields.tunnelWatch)
                } header: {
                    sectionHeader("How closely dezhban watches",
                                  "How quickly a dropped tunnel is noticed, and how long a known VPN "
                                      + "server stays reachable so it can redial.")
                }

                Section {
                    Toggle("Start the guard at boot (install the system service)", isOn: bootBinding)
                        .disabled(bootBusy || !state.cliFound)
                        .help("Installs dezhban as a background system service: the guard starts at boot — "
                            + "before any login — and survives restarts and crashes. Unchecking uninstalls the "
                            + "service (rules are torn down first so nothing is left blocking).")
                    Toggle("Open this app at login", isOn: loginBinding)
                        .help("Registers the app as a login item (System Settings → General → Login Items). "
                            + "This is only the status display — the guard itself is the system service above.")
                    // Its own line, not the pane's shared `status`. The login item is
                    // the one control here whose result can be a several-second
                    // round-trip AND can need the user to go and do something in
                    // System Settings, so it was competing with the service toggle's
                    // progress message for a single line: each clobbered the other,
                    // and a guard against one direction only moved the loss to the
                    // other. Two facts, two lines.
                    if let loginMessage {
                        Text(loginMessage)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    Picker("Open minimized", selection: launchVisibilityBinding) {
                        ForEach(LaunchVisibility.allCases) { choice in
                            Text(choice.label).tag(choice)
                        }
                    }
                    .help("Whether the main window opens when Dezhban starts. The Dock icon and the "
                        + "menubar's \"Open Dezhban…\" always open it, whichever you pick — this only "
                        + "governs startup.")
                    Toggle("Notify on essential events", isOn: notifyBinding)
                        .help("macOS notifications for the transitions that matter: guard armed, traffic "
                            + "cut, warnings (enforcement error / window open), standby, stopped. "
                            + "Nothing else. Pick individual events below.")
                    DisclosureGroup("Which events") {
                        ForEach(NotificationPrefs.EventClass.allCases, id: \.rawValue) { eventClass in
                            Toggle(eventClass.label, isOn: eventClassBinding(eventClass))
                                .toggleStyle(.checkbox)
                        }
                        Text(notifyPrefs.summary)
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                    Toggle("Check for updates automatically", isOn: checkUpdatesBinding)
                        .help("Checks GitHub for a newer release at launch and every ~24h — never from the "
                            + "background service, only here, in this app, on this schedule. Turn off to stop this "
                            + "host contacting GitHub about updates entirely; \"Check Now\" in About still "
                            + "works either way.")
                } header: {
                    sectionHeader("Startup and updates",
                                  "These take effect immediately — they are actions, not settings, so "
                                      + "Apply does not touch them.")
                }

                Section("Authorization") {
                    Toggle("Use Touch ID for settings changes", isOn: tokenBinding)
                        // Unknown is treated as unavailable, never as available:
                        // enabling the toggle before the answer is in would let
                        // someone start an enrollment the probe is about to refuse,
                        // which is the whole failure this design exists to prevent.
                        //
                        // But an EXISTING enrollment must always be removable. The
                        // capability gate is about whether enrolling can succeed,
                        // and applying it to a host that is already enrolled leaves
                        // the only in-app way to revoke a token greyed out — a Mac
                        // put in clamshell mode, or one whose probe hit a transient
                        // keychain refusal, would otherwise have no way to turn it
                        // off at all.
                        .disabled(tokenBusy || !(tokenEnrolled || (tokenCapability?.isAvailable ?? false)))
                        .help("Applying a change asks dezhban to make it, authorised by a "
                            + "secret kept in your login keychain — so saving costs a fingerprint "
                            + "instead of your password. Dezhban checks your fingerprint and then "
                            + "reads the secret; the keychain is not itself holding it back, so this "
                            + "raises the bar rather than making it unforgeable. Turning this on "
                            + "stores the secret (one password prompt, now); turning it off removes "
                            + "it from both the keychain and dezhban. Nothing else about what dezhban "
                            + "enforces changes.")
                    // Says WHICH of the reasons applies. A disabled toggle with no
                    // explanation reads as a bug; "no Touch ID on this Mac" and
                    // "this build can't reach the keychain" send you to different
                    // places, and only the second is fixable by us.
                    //
                    // The nil case says so rather than borrowing one of the real
                    // reasons: "not known yet" is a fourth state, and showing a
                    // verdict we do not have would be a guess the user cannot tell
                    // from an answer. It is normally invisible — `warmCapability()`
                    // resolves this as soon as the main window appears, so nil
                    // survives only when the sensor was unavailable then and is
                    // available now.
                    //
                    // An ENROLLED host is answered without the verdict at all, the
                    // same rule `AboutView.describeSettingsAuthIfKnown` follows: the
                    // capability is about whether *enrolling* could succeed, and a
                    // transient probe refusal (a cancelled keychain-unlock dialog
                    // answers -25293) would otherwise print "the keychain refused to
                    // hold the secret, so settings changes ask for your password"
                    // underneath a working Touch ID enrollment — a flat lie about
                    // what the next save will cost. Clamshell is the same story with
                    // a different sentence: the sensor is back the moment the lid is.
                    if !tokenEnrolled {
                        if let tokenCapability {
                            if !tokenCapability.isAvailable {
                                Text(tokenCapability.toggleExplanation)
                                    .font(.callout)
                                    .foregroundStyle(.secondary)
                            }
                        } else {
                            Text("Checking whether this Mac can hold the secret…")
                                .font(.callout)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                Section { advancedGroup }
                Section {
                    LabeledContent("Config file") {
                        // `configPath`, never DezhbanCLI.resolvedConfigPath(): a body
                        // getter must not spawn a process. See DezhbanCLI.exec.
                        Text(configPath)
                            .textSelection(.enabled)
                            .foregroundStyle(.secondary)
                            .truncationMode(.middle)
                            .lineLimit(1)
                    }
                    Button("Open Config File…") {
                        NSWorkspace.shared.open(URL(fileURLWithPath: configPath))
                    }
                    // The wizard is not first-run-only: it is the guided way
                    // back through the same decisions, for someone changing VPN
                    // rather than someone starting out.
                    Button("Run Setup Again…") { state.showFirstRun = true }
                        .help("Walk through the setup questions again, seeded with your current settings.")
                } footer: {
                    Text("Some advanced options (control socket, geo providers, allowlist) live only in the config file.")
                        .foregroundStyle(.secondary)
                }
            }
            .formStyle(.grouped)

            Divider()
            footer
        }
        .onAppear(perform: seed)
        // The config file is not owned by this pane: `dezhban config set` in a
        // terminal, another admin, or a hand edit can all change it while the
        // window sits open, and the pane would go on showing values the daemon
        // stopped using. Re-read whenever the user comes back to the app — unless
        // they have typed something, since re-reading would then throw their work
        // away to fix a much smaller problem.
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            guard !hasUnsavedEdits else { return }
            seed()
        }
    }

    private var footer: some View {
        // Same shape, and same defect, as the Overview's action row: an HStack
        // whose natural width (~450pt) exceeds the pane at a narrow window
        // compresses every button's label. The .lineLimit(1) below only ever
        // masked it for the status text; the buttons truncated regardless.
        ActionRow(trailingCount: 4) {
            Text(status)
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
                // ActionRow places every subview at its ideal size, and a status
                // line's ideal width is however long the message is. Bound it,
                // or one long message pushes all four buttons onto a second row.
                .frame(maxWidth: PaneMetrics.statusColumn, alignment: .leading)
            Button("Restart dezhban…", action: restartNow)
                .disabled(restartBusy || !state.cliFound)
            Button("Reset to Defaults…", action: resetToDefaults)
                .disabled(!canApply)
            Button("Reload", action: seed)
            Button("Apply…", action: apply)
                .keyboardShortcut(.defaultAction)
                .disabled(!canApply)
        }
        .padding(PaneMetrics.footerPadding)
    }

    // MARK: - explicit restart

    /// Restart is a lifecycle action about the running daemon, not a settings
    /// change — no keys are written here — so it lives beside Apply rather than
    /// inside it, and it is the one place a restart can be asked for with
    /// nothing pending.
    private func restartNow() {
        let exposedNow = state.snapshot?.posture == "full-block" || (state.snapshot?.switch?.open ?? false)
        guard AppActions.confirmRestart(exposedNow: exposedNow, unsavedEdits: hasUnsavedEdits) else { return }
        restartBusy = true
        status = "Restarting…"
        ConfigApply.restartNow(awaitPosture: true, title: "Restart") { outcome in
            restartBusy = false
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
        }
    }

    // MARK: - preset picker

    /// One button per preset (a segmented control would apply on selection with
    /// no chance to confirm the cost first) plus a summary/cost line for
    /// whichever one is current, or the drift from the nearest one when the
    /// config is Custom.
    private var presetPicker: some View {
        VStack(alignment: .leading, spacing: 8) {
            // ActionRow (trailingCount 0: nothing pinned), not an HStack: four
            // preset buttons overflow a narrow pane, and wrapping beats
            // truncating a choice out of sight.
            ActionRow(trailingCount: 0) {
                ForEach(presets) { p in
                    Button {
                        confirmAndApplyPreset(p)
                    } label: {
                        Label(p.name.capitalized, systemImage: (p.matched ?? false) ? "checkmark.circle.fill" : "circle")
                    }
                    .buttonStyle(.bordered)
                    .tint((p.matched ?? false) ? Color.accentColor : Color.secondary)
                    // A preset the CLI would refuse (its window exceeds a cap
                    // lowered in Advanced) is offered as unavailable, with the
                    // reason on hover — letting it be clicked would trade a
                    // greyed button for an error sheet after the fact.
                    .disabled(presetBusy || !canApply || !p.isAppliable)
                    .help(p.isAppliable ? p.summary : (p.conflicts ?? []).joined(separator: "\n"))
                }
            }
            // Said in the pane, not only on hover: the reason names a value the
            // user set themselves in Advanced, and raising it is the fix.
            ForEach(presets.filter { !$0.isAppliable }) { p in
                ForEach(p.conflicts ?? [], id: \.self) { c in
                    Label("Can't apply \(c)", systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
            }
            if let matched = presets.first(where: { $0.matched ?? false }) {
                Text(matched.summary).font(.callout).foregroundStyle(.secondary)
                Text("Cost: \(matched.cost)").font(.callout).foregroundStyle(.secondary)
            } else if !presets.isEmpty {
                Text("Custom — doesn't match any preset.").font(.callout).foregroundStyle(.secondary)
                if let drift = presetDrift, !drift.changes.isEmpty {
                    DisclosureGroup("\(drift.changes.count) key(s) differ from \(drift.preset.capitalized)") {
                        ForEach(drift.changes) { c in
                            Text("\(c.key): \(c.from) → \(c.to)")
                                .font(.callout)
                                .foregroundStyle(.secondary)
                                .textSelection(.enabled)
                        }
                    }
                }
            }
        }
    }

    /// Names the cost before writing, then applies through the same batched
    /// write/reload/restart path Apply and Reset to Defaults use — a preset is
    /// a write-time macro over ordinary keys, not a separate kind of change.
    private func confirmAndApplyPreset(_ p: PresetSummary) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Apply the \(p.name.capitalized) preset?"
        var text = "\(p.summary)\n\nCost: \(p.cost)"
        if hasUnsavedEdits {
            // Applying writes the preset's values to disk and then re-seeds
            // from what actually landed (see below) — silently discarding
            // whatever the user had typed into the fields but not yet saved.
            // Name that loss here rather than only in the cost line, which
            // talks about the preset's trade-offs, not the pane's own state.
            text += "\n\nYou have unsaved changes in this pane — applying a preset will discard them."
        }
        alert.informativeText = text
        alert.addButton(withTitle: "Apply")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }

        presetBusy = true
        status = "Applying \(p.name)…"
        ConfigApply.applyPreset(p.name, awaitPosture: true, title: "Preset") { outcome in
            presetBusy = false
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
            // Re-seed so the preset picker (and every field) reflects what
            // actually landed, including the daemon's own normalisation.
            if outcome.ok { seed() }
        }
    }

    /// Reads presets and (only when the config is Custom) the drift from the
    /// nearest one, off the main thread — both shell out.
    private func refreshPresets() {
        DispatchQueue.global(qos: .userInitiated).async {
            let list = DezhbanCLI.readPresets() ?? []
            let diff = list.contains { $0.matched ?? false } ? nil : DezhbanCLI.readPresetDiff()
            DispatchQueue.main.async {
                presets = list
                presetDrift = diff
            }
        }
    }

    /// Reads the key schema once per pane opening. Off the main thread because
    /// it shells out, like every other CLI read here.
    ///
    /// It is only re-read on open rather than watched: the schema is a property
    /// of the installed binary, so the one thing that can change it — an upgrade
    /// — also relaunches the app.
    private func refreshSchema(then completion: @escaping (ConfigSchema?) -> Void = { _ in }) {
        DispatchQueue.global(qos: .userInitiated).async {
            let loaded = DezhbanCLI.readSchema()
            DispatchQueue.main.async {
                schema = loaded
                completion(loaded)
            }
        }
    }

    /// Placeholder for a text field: the concept's name and its real default,
    /// from the daemon. `fallback` is used only when the schema is unavailable,
    /// and deliberately states no value — a stale hardcoded default is exactly
    /// what this replaces.
    private func hint(_ key: String, _ fallback: String) -> String {
        schema?.placeholder(for: key, fallback: fallback) ?? fallback
    }

    /// Help text for a field, from the daemon's schema.
    private func helpText(_ key: String) -> String? { schema?.help(for: key) }

    /// A section heading with a line saying what the section is for.
    ///
    /// The headings name the thing rather than the config block it lives in —
    /// "When the guard relaxes", not "Windows" — following
    /// docs/concepts/glossary.md, which is the authority for user-facing words.
    /// The description carries the part a tooltip cannot: why you would touch
    /// this section at all.
    private func sectionHeader(_ title: String, _ description: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
            Text(description)
                .font(.callout)
                .foregroundStyle(.secondary)
                .textCase(nil)
        }
        .padding(.bottom, 2)
    }

    /// The Developer disclosure — the power-user tier, generated from the
    /// schema's `advanced` flag rather than a hand-kept row list, so a key
    /// reaches this section by being marked advanced in internal/config and
    /// nowhere else. Kept out of `body` deliberately: every row is a call the
    /// type-checker has to solve, and inlining the lot pushed `body` past the
    /// solver's budget — the compiler said so by name.
    ///
    /// With no schema (an older CLI) it falls back to the previous hand-listed
    /// vpn.advanced.* rows: hardcoded LABELS for keys every CLI has, never
    /// hardcoded values — the same degrade rule as everywhere else.
    @ViewBuilder private var advancedGroup: some View {
        DisclosureGroup("Developer") {
            Text("Touch only if you know why. These override recommended defaults. The caps "
                + "and budgets below bound how much exposure the settings above can ever "
                + "cause — lowering one narrows the choices they offer.")
                .font(.callout)
                .foregroundStyle(.secondary)
            if let schema {
                // Schema order — the daemon's presentation order. Keys the pane
                // has no storage for (e.g. `providers`, deliberately unexposed)
                // are skipped by the same rule everywhere: no storage, no row.
                ForEach(schema.tunables.filter { $0.advanced && fields.stagedKeys.contains($0.key) }) { tunable in
                    developerRow(tunable)
                }
            } else {
                legacyAdvancedRows
            }
        }
    }

    /// One schema-driven Developer row, picked by kind: durations get the
    /// slider control, bools a toggle, ints a stepper, everything else text.
    /// Bindings are key-based views onto SettingsFields' dictionary — the named
    /// accessors stay for the curated sections above, where a binding's
    /// spelling is part of the section's readability.
    @ViewBuilder
    private func developerRow(_ tunable: ConfigTunable) -> some View {
        switch tunable.kind {
        case "duration":
            durationField(tunable.key, tunable.label, text: fieldBinding(tunable.key))
        case "bool":
            schemaToggle(tunable.key, tunable.label, isOn: boolBinding(tunable.key))
        case "int":
            intField(tunable, text: fieldBinding(tunable.key))
        default:
            schemaField(tunable.key, tunable.label, text: fieldBinding(tunable.key))
        }
    }

    private func fieldBinding(_ key: String) -> Binding<String> {
        Binding(get: { fields.value(for: key) }, set: { fields.setValue($0, for: key) })
    }

    private func boolBinding(_ key: String) -> Binding<Bool> {
        Binding(get: { fields.value(for: key) == "true" },
                set: { fields.setValue(String($0), for: key) })
    }

    /// An integer key as a text field with a stepper: the field still displays
    /// (and round-trips) a nonnumeric seeded value rather than eating it, the
    /// stepper edits what parses. No hardcoded ranges — the schema carries
    /// none, and the daemon remains the validator.
    private func intField(_ tunable: ConfigTunable, text: Binding<String>) -> some View {
        HStack(spacing: 6) {
            TextField(tunable.label, text: text)
                .disabled(!canApply)
                .help(tunable.help)
            Stepper(tunable.unit ?? "") {
                if let n = Int(text.wrappedValue.trimmingCharacters(in: .whitespaces)) {
                    text.wrappedValue = String(n + 1)
                }
            } onDecrement: {
                if let n = Int(text.wrappedValue.trimmingCharacters(in: .whitespaces)), n > 0 {
                    text.wrappedValue = String(n - 1)
                }
            }
            .disabled(!canApply)
            .font(.callout)
            .foregroundStyle(.secondary)
            docLink(tunable.key)
        }
    }

    /// The pre-schema hand list (labels only), kept verbatim as the no-schema
    /// fallback for older CLIs.
    @ViewBuilder private var legacyAdvancedRows: some View {
        durationField("vpn.advanced.switchWindowMax", "Switch window cap", text: $fields.advSwitchWindowMax)
        durationField("vpn.advanced.redialWindowMax", "Redial window cap", text: $fields.advRedialWindowMax)
        durationField("vpn.advanced.redialMinUptime", "Redial backoff threshold", text: $fields.advRedialMinUptime)
        durationField("vpn.advanced.redialBudget", "Redial budget", text: $fields.advRedialBudget)
        durationField("vpn.advanced.redialBudgetWindow", "Redial budget period",
                      text: $fields.advRedialBudgetWindow)
        durationField("vpn.advanced.commandFreshness", "Command freshness", text: $fields.advCommandFreshness)
        durationField("vpn.advanced.windowDiscoveryInterval", "Window discovery interval",
                      text: $fields.advWindowDiscoveryInterval)
        durationField("vpn.advanced.tunnelPruneAfter", "Tunnel prune delay", text: $fields.advTunnelPruneAfter)
        durationField("vpn.advanced.learnedEndpointTTL", "Learned address lifetime",
                      text: $fields.advLearnedEndpointTTL)
        schemaField("vpn.advanced.learnedMaxPerProfile", "Learned addresses per profile",
                    text: $fields.advLearnedMaxPerProfile)
        schemaField("vpn.advanced.promoteAfterRefreshes", "Sightings before an address is learned",
                    text: $fields.advPromoteAfterRefreshes)
        schemaField("vpn.advanced.endpointWarnThreshold", "Address-bloat warning threshold",
                    text: $fields.advEndpointWarnThreshold)
        schemaField("vpn.advanced.windowProtocols", "Window protocols (comma-sep)",
                    text: $fields.advWindowProtocols)
        schemaField("vpn.advanced.windowPorts", "Window ports (comma-sep)",
                    text: $fields.advWindowPorts)
        durationField("vpn.advanced.verifyInterval", "Enforcement verification interval",
                      text: $fields.advVerifyInterval)
        schemaToggle("vpn.advanced.livenessRedial", "Redial on a hung tunnel",
                     isOn: $fields.advLivenessRedial)
    }

    /// A duration setting as a menu of real choices rather than a text field
    /// that demands Go's duration syntax. Bounds and Off-availability come from
    /// the schema, and the cap is resolved against the values this pane is
    /// actually holding, so lowering a cap by hand narrows the menu.
    private func durationField(_ key: String, _ fallback: String, text: Binding<String>) -> some View {
        HStack(spacing: 6) {
            DurationField(key: key, fallbackLabel: fallback, schema: schema,
                          values: fields.currentValues, text: text, enabled: canApply)
            docLink(key)
        }
    }

    /// A text field for one config key, labelled and explained from the schema.
    ///
    /// `fallback` is the label to use when the schema is unavailable. It names
    /// the concept but states no default, because the whole point of this change
    /// is that the app no longer holds an opinion about what a default is.
    @ViewBuilder
    private func schemaField(_ key: String, _ fallback: String, text: Binding<String>) -> some View {
        let field = TextField(hint(key, fallback), text: text).disabled(!canApply)
        HStack(spacing: 6) {
            if let help = helpText(key) {
                field.help(help)
            } else {
                field
            }
            docLink(key)
        }
    }

    /// A toggle for one boolean config key, labelled and explained from the schema.
    @ViewBuilder
    private func schemaToggle(_ key: String, _ fallback: String, isOn: Binding<Bool>) -> some View {
        let toggle = Toggle(schema?[key]?.label ?? fallback, isOn: isOn).disabled(!canApply)
        HStack(spacing: 6) {
            if let help = helpText(key) {
                toggle.help(help)
            } else {
                toggle
            }
            docLink(key)
        }
    }

    /// schemaToggle with the schema's Help rendered as a VISIBLE caption under
    /// the control instead of hover-only — for the consequence-bearing toggles,
    /// where the trade must be read before it is made. The Swift fallback label
    /// names the concept and makes no behavioral claim; the claim is the
    /// daemon's (schema Help), and with no schema there is simply no caption.
    @ViewBuilder
    private func schemaToggleWithCaption(_ key: String, _ fallback: String, isOn: Binding<Bool>) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Toggle(schema?[key]?.label ?? fallback, isOn: isOn).disabled(!canApply)
                docLink(key)
            }
            if let help = helpText(key) {
                Text(help)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    /// Opens the Help pane at the section of the documentation that describes
    /// this key.
    ///
    /// A tooltip has room for one sentence; the reason a setting exists, what it
    /// costs, and what happens when it is off often needs a page. This is the
    /// bridge between the two — and it lands on the *heading*, from the key's
    /// own `docAnchor`, so the answer is on screen rather than somewhere in a
    /// long reference page.
    ///
    /// Absent when the schema is unavailable (a CLI too old to know
    /// `config schema`): a button that could only apologise is worse than none.
    @ViewBuilder
    private func docLink(_ key: String) -> some View {
        if let tunable = schema?[key], !tunable.docAnchor.isEmpty {
            Button {
                state.openHelp(docAnchor: tunable.docAnchor)
            } label: {
                Image(systemName: "questionmark.circle")
            }
            .buttonStyle(.borderless)
            .foregroundStyle(.secondary)
            .help("Read about \(tunable.label) in the documentation")
            .accessibilityLabel("Documentation for \(tunable.label)")
        }
    }

    // MARK: - reset to defaults

    /// Restores every tunable to its shipped default via `config reset --all`,
    /// then re-seeds so the form shows what actually landed rather than what was
    /// requested. Confirmed first: this discards staged edits AND rewrites the
    /// on-disk config. What it deliberately does NOT touch is identity —
    /// blockedCountries, tunnel interfaces, endpoints, profiles — so a reset can
    /// never silently unblock a country or forget the user's VPN; that carve-out
    /// lives in `configReset` (Go), and the wording below must keep matching it.
    private func resetToDefaults() {
        let alert = NSAlert()
        alert.messageText = "Reset settings to defaults?"
        alert.informativeText = """
            Every tunable on this pane returns to its shipped default, and any \
            unapplied edits here are discarded.

            Your blocked countries, tunnel interfaces, endpoints, and saved VPN \
            profiles are kept.
            """
        alert.alertStyle = .warning
        alert.addButton(withTitle: "Reset")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }

        canApply = false
        status = "Resetting…"
        ConfigApply.resetAll(awaitPosture: true, title: "Reset to defaults") { outcome in
            canApply = true
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
            // Re-seed on success so the fields show the defaults that actually
            // landed on disk, not the values the user was looking at.
            if outcome.ok { seed() }
        }
    }

    // MARK: - startup toggles (immediate)

    /// Install-and-start or tear-down-and-uninstall, one admin prompt each. The
    /// binding reads AppState's cache and never latches intent: the toggle
    /// reflects reality, re-synced by refreshServiceState after the sequence.
    private var bootBinding: Binding<Bool> {
        Binding(
            get: { state.serviceIsInstalled },
            set: { want in bootToggled(want) })
    }

    private func bootToggled(_ wantInstalled: Bool) {
        if !wantInstalled {
            let alert = NSAlert()
            alert.alertStyle = .warning
            alert.messageText = "Uninstall the dezhban service?"
            alert.informativeText = "The guard will stop and will no longer start at boot. "
                + "All dezhban firewall rules are removed first, so nothing is left blocking."
            alert.addButton(withTitle: "Uninstall")
            alert.addButton(withTitle: "Cancel")
            guard alert.runModal() == .alertFirstButtonReturn else { return }
        }
        bootBusy = true
        status = wantInstalled ? "Installing service…" : "Uninstalling service…"
        let title = wantInstalled ? "dezhban — install service" : "dezhban — uninstall service"
        // Passed unevaluated (autoclosure): installCommands resolves the config path.
        AppActions.capturedSequence(wantInstalled ? AppActions.installCommands
                                                  : AppActions.uninstallCommands) { result in
            bootBusy = false
            status = ""
            if !result.ok {
                state.showInLogs(title: "\(title) — failed", text: result.output)
            }
        }
    }

    private var loginBinding: Binding<Bool> {
        Binding(
            get: { loginEnabled },
            set: { wanted in
                // `wanted`, not a re-read of live state: the switch can be stale
                // (the login item is also removable in System Settings), and
                // deciding from a fresh read then inverted the click.
                //
                // The outcome, not a bool: macOS can accept the registration and
                // still hold it for the user's approval, and there is one path
                // where only they can clear the old login item. A switch that
                // snaps back with no explanation reads as a bug.
                //
                // Off the main thread: `set(enabled:)` is six to eight blocking
                // SMAppService round-trips over XPC plus an unregister, and this
                // runs from a SwiftUI setter, so doing it inline beachballs the
                // Settings window. It is the same cost that had the migration moved
                // off-main in AppDelegate. The switch moves immediately to where the
                // user put it and is corrected from the outcome when it lands — and
                // on the disable path launchd may terminate the app partway
                // through (ADR-0014's known risk), which is one more reason not to
                // be holding the main thread while it happens.
                loginRevision += 1
                let revision = loginRevision
                loginPending = true
                loginEnabled = wanted
                loginMessage = wanted ? "Registering the login item…" : "Removing the login item…"
                // A progress line is transient by nature. Left inheriting the
                // previous outcome's value, a refusal's `false` made it un-clearable
                // if this click's completion was then superseded.
                loginMessageIsTransient = true
                loginMessageForEnabled = wanted
                // The enqueueing form, so two quick clicks are applied in the order
                // they were made — dispatching each to a concurrent queue let them
                // race into LoginItem's serial queue and land out of order.
                LoginItem.set(enabled: wanted) { outcome in
                    // A newer click supersedes this one's result.
                    guard revision == loginRevision else { return }
                    loginPending = false
                    loginEnabled = outcome.isOn
                    // Written unconditionally: this is the login item's own line,
                    // so there is nothing else to collide with. That is the point of
                    // having split it from the pane's shared status.
                    loginMessage = outcome.message
                    loginMessageIsTransient = outcome.isTransient
                    loginMessageForEnabled = outcome.isOn
                    // And bumped, so any status read that was already in flight
                    // cannot land afterwards and clear this. `loginPending` cannot
                    // cover it: seed()'s read is a queue.sync behind this very
                    // mutation, so it is *guaranteed* to complete after it, by which
                    // time pending is already false — and it then nils the one line
                    // that tells the user to go to System Settings, a moment after it
                    // appeared.
                    loginRevision += 1
                }
            })
    }

    /// Turning this on enrolls a control token; turning it off removes it from
    /// both the keychain and the daemon. The displayed state comes from whether
    /// the keychain item EXISTS, which is checked without reading it — so merely
    /// opening this pane never triggers a biometric prompt.
    private var tokenBinding: Binding<Bool> {
        Binding(
            get: { tokenEnrolled },
            set: { want in tokenToggled(want) })
    }

    private func tokenToggled(_ wantEnrolled: Bool) {
        tokenBusy = true
        status = wantEnrolled ? "Setting up Touch ID…" : "Removing the stored secret…"
        let done: (ConfigApply.Outcome) -> Void = { outcome in
            tokenBusy = false
            tokenEnrolled = ControlToken.isStored
            // A forget that succeeded turns the verdict back into something the
            // pane shows, and nothing else would ask for it before the next
            // `.onAppear` — leaving "Checking…" under a toggle that is finished.
            refreshTokenCapability()
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
        }
        if wantEnrolled {
            ConfigApply.enrollToken(completion: done)
        } else {
            ConfigApply.forgetToken(completion: done)
        }
    }

    /// Re-asks the capability, and only when the answer is going to be used.
    ///
    /// An enrolled host is skipped entirely — the verdict says whether *enrolling*
    /// could succeed, the toggle is enabled by `tokenEnrolled` regardless, and the
    /// explanation line is suppressed above. Asking anyway would run the probe's
    /// keychain ADD (a modal unlock dialog on a locked login keychain) on every
    /// entry to this pane, for a value nothing renders. Same rule, and the same
    /// reason, as `AboutView.describeSettingsAuthIfKnown`.
    ///
    /// `capabilityIfKnown` first, never `capability`: this runs on the main thread,
    /// and the probe may block. Re-asked rather than cached because the biometry
    /// half genuinely changes (clamshell, Touch ID lockout).
    private func refreshTokenCapability() {
        guard !tokenEnrolled else { return }
        tokenCapability = ControlToken.capabilityIfKnown
        if tokenCapability == nil {
            ControlToken.resolveCapability { tokenCapability = $0 }
        }
    }

    /// Like the notify/update toggles beside it: an app preference that takes
    /// effect immediately and is deliberately untouched by Apply, because Apply
    /// writes the daemon's config and this never goes there.
    private var launchVisibilityBinding: Binding<LaunchVisibility> {
        Binding(
            get: { launchVisibility },
            set: { choice in
                LaunchPreference.current = choice
                launchVisibility = LaunchPreference.current
                status = choice.detail
            })
    }

    /// The master toggle: off zeroes every class, on restores every class —
    /// all-or-nothing on purpose. The per-class checkboxes in the disclosure
    /// are where partial selections are made, so the common "mute everything"
    /// stays one click.
    private var notifyBinding: Binding<Bool> {
        Binding(
            get: { notifyPrefs.anyEnabled },
            set: { on in
                var prefs = notifyPrefs
                prefs.setAll(on)
                NotificationManager.prefs = prefs
                notifyPrefs = NotificationManager.prefs
                status = on ? "Notifications on for essential events." : "Notifications off."
            })
    }

    private func eventClassBinding(_ eventClass: NotificationPrefs.EventClass) -> Binding<Bool> {
        Binding(
            get: { notifyPrefs.isEnabled(eventClass) },
            set: { on in
                var prefs = notifyPrefs
                prefs.set(eventClass, enabled: on)
                NotificationManager.prefs = prefs
                notifyPrefs = NotificationManager.prefs
                status = notifyPrefs.summary
            })
    }

    private var checkUpdatesBinding: Binding<Bool> {
        Binding(
            get: { checkUpdatesEnabled },
            set: { on in
                UpdateChecker.isEnabled = on
                checkUpdatesEnabled = UpdateChecker.isEnabled
                status = on ? "Automatic update checks on." : "Automatic update checks off."
                if on { state.checkForUpdates() }
            })
    }

    // MARK: - seeding

    /// Re-reads everything the pane shows: config fields via `config get`
    /// (short-circuiting on the first failure so an error string can never be
    /// written back as a value), service state via AppState, login-item state
    /// from SMAppService.
    private func seed() {
        // Re-read for the same reason `tokenCapability` is: the `@State`
        // initialiser above runs once per view identity and SwiftUI discards its
        // value on every later re-creation, so without this the toggle would show
        // whatever was true when the window first opened — a token forgotten from
        // the CLI would leave it reading "on" for the life of the window, and that
        // stale `true` is also what keeps the control enabled.
        tokenEnrolled = ControlToken.isStored
        refreshTokenCapability()
        status = "Loading…"
        canApply = false
        // Off-main for the same reason `set(enabled:)` is: this is two blocking
        // SMAppService status reads over XPC (it was one before the agent), and
        // opening the Settings pane should not hitch on them. Stamped with the
        // click revision so a read started before a click cannot land after it —
        // see `loginRevision`.
        let revision = loginRevision
        DispatchQueue.global(qos: .userInitiated).async {
            let enabled = LoginItem.isEnabled
            DispatchQueue.main.async {
                guard revision == loginRevision, !loginPending else { return }
                loginEnabled = enabled
                // Clear only a message about a moment that has passed. "macOS is
                // holding this for your approval" outlived the approval — the user
                // went to System Settings, granted it, came back, which is what
                // fires this seed — and a fresh status read makes the switch speak
                // for itself there.
                //
                // But not the ones explaining a refusal. Clicking away from the app
                // and back is enough to fire this, so clearing unconditionally erased
                // "Dezhban has to live in Applications to open at login" a moment
                // after the switch snapped back, leaving exactly the unexplained
                // snap-back this message exists to prevent.
                if loginMessageIsTransient || loginMessageForEnabled != enabled {
                    loginMessage = nil
                    loginMessageForEnabled = nil
                }
            }
        }
        notifyPrefs = NotificationManager.prefs
        checkUpdatesEnabled = UpdateChecker.isEnabled
        launchVisibility = LaunchPreference.current
        fields = SettingsFields()
        state.refreshServiceState()
        refreshPresets()
        // The schema loads BEFORE the field seed, because it decides which
        // optional keys exist on this CLI at all. Seeding an optional key an
        // old CLI doesn't know would fail its `config get` and sink
        // the WHOLE seed (ConfigApply.seed reports the first failure and
        // delivers no values at all) — the
        // pane would brick against every older install. Schema-known keys
        // only, or none when there is no schema to ask.
        refreshSchema { loadedSchema in
            let extraKeys = SettingsFields.optionalKeys.filter { loadedSchema?[$0] != nil }
            // `path` is the same resolution ConfigApply.seed already did for the
            // `config get` calls — reusing it here means configPath never needs its
            // own second background resolve, so there's nothing to race.
            ConfigApply.seed(keys: SettingsFields.keys + extraKeys) { path, values, error in
                configPath = path
                if let error = error {
                    status = error
                    return
                }
                guard let v = values else { return }
                fields = SettingsFields(seeded: v, extraKeys: extraKeys)
                // Recorded AFTER the fields are populated, so `currentValues` and the
                // seeded snapshot are the same thing at this instant and the pane
                // starts out clean.
                seededValues = fields.currentValues
                status = "Seeded from \(path)"
                canApply = true
            }
        }
    }

    // MARK: - apply (staged fields)

    private func apply() {
        // Which fields are durations, and what they are called, both come from
        // the daemon's schema. With no schema there is nothing to pre-validate
        // against, and the daemon still rejects a malformed duration and says
        // so — better than guessing the field set or inventing labels that
        // disagree with the ones on screen.
        if let schema {
            for (label, value) in fields.durationFieldsForValidation(schema: schema) {
                guard DurationText.looksLikeGoDuration(value) else {
                    ConfigApply.invalidDurationAlert(label, value)
                    return
                }
            }
        }

        let pairs = fields.pairs()
        canApply = false
        status = "Applying…"
        // awaitPosture: true — this pane now carries guard-affecting keys (it used
        // to be false here, back when Settings held only switchWindow/endpointGrace
        // and VPNGuardView, which always awaited posture, held the rest). It only
        // comes into play if the user agrees to a restart for a key that needs one.
        ConfigApply.apply(pairs: pairs, awaitPosture: true,
                          title: "Settings") { outcome in
            canApply = true
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
            // Re-seed from disk so the fields show what actually landed, including
            // any value the daemon normalised on the way in.
            if outcome.ok { seed() }
        }
    }
}
