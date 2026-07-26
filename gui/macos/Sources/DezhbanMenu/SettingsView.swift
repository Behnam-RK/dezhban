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
    @State private var notifyEnabled = true
    @State private var checkUpdatesEnabled = true

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
    /// Evaluated once rather than in `body`: whether this Mac has biometry cannot
    /// change while the pane is open, and a body getter should stay cheap.
    private let biometryAvailable = ControlToken.biometryAvailable

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("Strictness preset") {
                    presetPicker
                }
                Section("Startup") {
                    Toggle("Start the guard at boot (install the system service)", isOn: bootBinding)
                        .disabled(bootBusy || !state.cliFound)
                        .help("Installs dezhban as a background system service: the guard starts at boot — "
                            + "before any login — and survives restarts and crashes. Unchecking uninstalls the "
                            + "service (rules are torn down first so nothing is left blocking).")
                    Toggle("Open this app at login", isOn: loginBinding)
                        .help("Registers the app as a login item (System Settings → General → Login Items). "
                            + "This is only the status display — the guard itself is the system service above.")
                    Toggle("Notify on essential events", isOn: notifyBinding)
                        .help("macOS notifications for the transitions that matter: guard armed, egress "
                            + "blocked, warnings (enforcement error / switch window open), standby, stopped. "
                            + "Nothing else.")
                    Toggle("Check for updates automatically", isOn: checkUpdatesBinding)
                        .help("Checks GitHub for a newer release at launch and every ~24h — never from the "
                            + "background service, only here, in this app, on this schedule. Turn off to stop this "
                            + "host contacting GitHub about updates entirely; \"Check Now\" in About still "
                            + "works either way.")
                }
                Section("VPN guard") {
                    schemaField("vpn.tunnelInterfaces", "Your VPN tunnel (comma-sep)",
                                text: $fields.tunnelInterfaces)
                    schemaField("vpn.endpoints", "VPN server addresses (comma-sep)", text: $fields.endpoints)
                }
                Section("Autodetection") {
                    schemaToggle("vpn.autoDetect", "Find my VPN tunnel automatically", isOn: $fields.autoDetect)
                    schemaToggle("vpn.autoDiscoverEndpoints", "Find the VPN server address automatically",
                                 isOn: $fields.autoDiscover)
                    schemaToggle("vpn.autoArm", "Arm the guard when a VPN connects", isOn: $fields.autoArm)
                }
                Section("Local network") {
                    schemaToggle("vpn.allowLocalNetwork", "Keep local devices reachable",
                                 isOn: $fields.allowLocalNetwork)
                }
                Section("Blocking") {
                    schemaField("blockedCountries", "Blocked countries (comma-sep)",
                                text: $fields.blockedCountries)
                    schemaField("pollInterval", "Exit country check interval", text: $fields.pollInterval)
                }
                Section("Windows") {
                    schemaField("vpn.switchWindow", "Switch window", text: $fields.switchWindow)
                    schemaField("vpn.redialWindow", "Redial window", text: $fields.redialWindow)
                    schemaField("vpn.pauseMax", "Longest pause", text: $fields.pauseMax)
                    schemaField("vpn.endpointGrace", "VPN server address grace", text: $fields.endpointGrace)
                }
                Section("Timing") {
                    schemaField("vpn.endpointRefresh", "VPN server address refresh",
                                text: $fields.endpointRefresh)
                    schemaField("vpn.tunnelWatch", "Tunnel check interval", text: $fields.tunnelWatch)
                }
                Section("Authorization") {
                    Toggle("Use Touch ID for settings changes", isOn: tokenBinding)
                        .disabled(tokenBusy || !biometryAvailable)
                        .help("Applying a change asks dezhban to make it, authorised by a "
                            + "secret kept in your login keychain behind Touch ID — so saving costs a "
                            + "fingerprint instead of your password. Turning this on stores that secret "
                            + "(one password prompt, now); turning it off removes it from both the "
                            + "keychain and dezhban. Nothing else about what dezhban enforces changes.")
                    if !biometryAvailable {
                        Text("This Mac has no Touch ID, so settings changes ask for your password.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                }
                Section {
                    DisclosureGroup("Advanced") {
                        Text("Touch only if you know why — these override recommended defaults.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                        schemaField("vpn.advanced.switchWindowMax", "Switch window cap",
                                    text: $fields.advSwitchWindowMax)
                        schemaField("vpn.advanced.redialWindowMax", "Redial window cap",
                                    text: $fields.advRedialWindowMax)
                        schemaField("vpn.advanced.redialMinUptime", "Redial anti-flap uptime",
                                    text: $fields.advRedialMinUptime)
                        schemaField("vpn.advanced.commandFreshness", "Command freshness",
                                    text: $fields.advCommandFreshness)
                        schemaField("vpn.advanced.windowDiscoveryInterval", "Window discovery interval",
                                    text: $fields.advWindowDiscoveryInterval)
                        schemaField("vpn.advanced.tunnelPruneAfter", "Tunnel prune delay",
                                    text: $fields.advTunnelPruneAfter)
                        schemaField("vpn.advanced.learnedEndpointTTL", "Learned address lifetime",
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
                    }
                }
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
                } footer: {
                    Text("Some advanced options (control socket, geo providers, allowlist) live only in the config file.")
                        .foregroundStyle(.secondary)
                }
            }
            .formStyle(.grouped)

            Divider()
            footer
        }
        .navigationTitle("Settings")
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
        HStack {
            Text(status)
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer()
            Button("Restart dezhban…", action: restartNow)
                .disabled(restartBusy || !state.cliFound)
            Button("Reset to Defaults…", action: resetToDefaults)
                .disabled(!canApply)
            Button("Reload", action: seed)
            Button("Apply…", action: apply)
                .keyboardShortcut(.defaultAction)
                .disabled(!canApply)
        }
        .padding(12)
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
            HStack(spacing: 8) {
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
    private func refreshSchema() {
        DispatchQueue.global(qos: .userInitiated).async {
            let loaded = DezhbanCLI.readSchema()
            DispatchQueue.main.async { schema = loaded }
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

    /// A text field for one config key, labelled and explained from the schema.
    ///
    /// `fallback` is the label to use when the schema is unavailable. It names
    /// the concept but states no default, because the whole point of this change
    /// is that the app no longer holds an opinion about what a default is.
    @ViewBuilder
    private func schemaField(_ key: String, _ fallback: String, text: Binding<String>) -> some View {
        let field = TextField(hint(key, fallback), text: text).disabled(!canApply)
        if let help = helpText(key) {
            field.help(help)
        } else {
            field
        }
    }

    /// A toggle for one boolean config key, labelled and explained from the schema.
    @ViewBuilder
    private func schemaToggle(_ key: String, _ fallback: String, isOn: Binding<Bool>) -> some View {
        let toggle = Toggle(schema?[key]?.label ?? fallback, isOn: isOn).disabled(!canApply)
        if let help = helpText(key) {
            toggle.help(help)
        } else {
            toggle
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
            set: { _ in
                loginEnabled = LoginItem.toggle()
                status = loginEnabled
                    ? "App will open at login."
                    : "App will not open at login."
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

    private var notifyBinding: Binding<Bool> {
        Binding(
            get: { notifyEnabled },
            set: { on in
                NotificationManager.isEnabled = on
                notifyEnabled = NotificationManager.isEnabled
                status = on ? "Notifications on for essential events." : "Notifications off."
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
        status = "Loading…"
        canApply = false
        loginEnabled = LoginItem.isEnabled
        notifyEnabled = NotificationManager.isEnabled
        checkUpdatesEnabled = UpdateChecker.isEnabled
        fields = SettingsFields()
        state.refreshServiceState()
        refreshPresets()
        refreshSchema()
        // `path` is the same resolution ConfigApply.seed already did for the
        // `config get` calls — reusing it here means configPath never needs its
        // own second background resolve, so there's nothing to race.
        ConfigApply.seed(keys: SettingsFields.keys) { path, values, error in
            configPath = path
            if let error = error {
                status = error
                return
            }
            guard let v = values else { return }
            fields = SettingsFields(seeded: v)
            // Recorded AFTER the fields are populated, so `currentValues` and the
            // seeded snapshot are the same thing at this instant and the pane
            // starts out clean.
            seededValues = fields.currentValues
            status = "Seeded from \(path)"
            canApply = true
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
