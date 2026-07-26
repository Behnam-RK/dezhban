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

    /// The values this pane was last seeded with, in `SettingsFields.keys` order.
    /// Comparing the live fields against these is how an unsaved edit is told
    /// from a pane that is merely displaying what is on disk — which decides
    /// whether it is safe to re-read the file underneath the user.
    @State private var seededValues: [String] = []

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
                    TextField("Your VPN tunnel (comma-sep)", text: $fields.tunnelInterfaces)
                        .disabled(!canApply)
                    TextField("Endpoints (comma-sep)", text: $fields.endpoints)
                        .disabled(!canApply)
                }
                Section("Autodetection") {
                    Toggle("Find my VPN tunnel automatically", isOn: $fields.autoDetect)
                        .disabled(!canApply)
                    Toggle("Auto-discover endpoints (vpn.autoDiscoverEndpoints)", isOn: $fields.autoDiscover)
                        .disabled(!canApply)
                    Toggle("Auto-arm when a VPN connects (vpn.autoArm)", isOn: $fields.autoArm)
                        .disabled(!canApply)
                        .help("With no VPN connected dezhban idles in standby (nothing blocked) and arms "
                            + "the guard the moment a tunnel appears. It never disarms on a drop — that's the "
                            + "kill switch — only an explicit Unblock with the VPN off returns to standby.")
                }
                Section("Local network") {
                    Toggle("Keep local devices reachable", isOn: $fields.allowLocalNetwork)
                        .disabled(!canApply)
                        .help("Printers, NAS, your router's admin page, AirPlay and Chromecast, and local "
                            + "dev servers keep working while the guard is armed. This is not a hole in the "
                            + "kill switch: it allows local destinations only, so anything on the internet "
                            + "stays blocked. The one cost is on untrusted Wi-Fi (a café, a hotel), where it "
                            + "also lets other devices on that network reach you.")
                }
                Section("Blocking") {
                    TextField("Blocked countries (comma-sep, e.g. IR,RU,KP)", text: $fields.blockedCountries)
                        .disabled(!canApply)
                    TextField("Geo IP lookup interval (e.g. 15s)", text: $fields.pollInterval)
                        .disabled(!canApply)
                        .help("How often the current VPN exit's country is checked.")
                }
                Section("Windows") {
                    TextField("Switch window (e.g. 5s)", text: $fields.switchWindow)
                        .disabled(!canApply)
                        .help("Manual switch window (`dezhban switch`): 0 disables it, otherwise up to 3m.")
                    TextField("Redial window (e.g. 30s)", text: $fields.redialWindow)
                        .disabled(!canApply)
                        .help("Automatic window opened when a healthy tunnel drops, so the VPN client can "
                            + "redial: 0 disables it, otherwise up to 10m.")
                    TextField("Endpoint grace (e.g. 15m)", text: $fields.endpointGrace)
                        .disabled(!canApply)
                        .help("How long a discovered VPN server stays reachable after its connection "
                            + "disappears, so a dropped VPN can redial the same server.")
                }
                Section("Timing") {
                    TextField("Endpoint refresh (e.g. 30s)", text: $fields.endpointRefresh)
                        .disabled(!canApply)
                    TextField("Tunnel watch (e.g. 5s)", text: $fields.tunnelWatch)
                        .disabled(!canApply)
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
                        TextField("Manual switch window cap (e.g. 3m)", text: $fields.advSwitchWindowMax)
                            .disabled(!canApply)
                        TextField("Redial window cap (e.g. 10m)", text: $fields.advRedialWindowMax)
                            .disabled(!canApply)
                        TextField("Redial anti-flap uptime (e.g. 15s; 0 disables the gate)", text: $fields.advRedialMinUptime)
                            .disabled(!canApply)
                        TextField("Command freshness (e.g. 30s)", text: $fields.advCommandFreshness)
                            .disabled(!canApply)
                            .help("How recent a control command must be to be acted on (replay guard).")
                        TextField("Window discovery interval (e.g. 1s)", text: $fields.advWindowDiscoveryInterval)
                            .disabled(!canApply)
                            .help("How often a new VPN server is looked for while a switch window is open.")
                        TextField("Tunnel prune delay (e.g. 60s)", text: $fields.advTunnelPruneAfter)
                            .disabled(!canApply)
                            .help("How long a dynamically-detected tunnel must be gone before it's dropped.")
                        TextField("Learned endpoint lifetime (e.g. 720h)", text: $fields.advLearnedEndpointTTL)
                            .disabled(!canApply)
                            .help("How long an unused learned endpoint is kept.")
                        TextField("Learned endpoints per profile (e.g. 16)", text: $fields.advLearnedMaxPerProfile)
                            .disabled(!canApply)
                        TextField("Promote after N sightings (e.g. 3)", text: $fields.advPromoteAfterRefreshes)
                            .disabled(!canApply)
                            .help("Consecutive sightings before a discovered endpoint is learned under normal guard.")
                        TextField("Endpoint-bloat warning threshold (e.g. 256)", text: $fields.advEndpointWarnThreshold)
                            .disabled(!canApply)
                        TextField("Switch-window protocols (comma-sep, e.g. udp,tcp)", text: $fields.advWindowProtocols)
                            .disabled(!canApply)
                            .help("Restricts a switch window to these protocols instead of allowing all outbound. Needs a restart to take effect.")
                        TextField("Switch-window ports (comma-sep, e.g. 51820,443)", text: $fields.advWindowPorts)
                            .disabled(!canApply)
                            .help("Restricts a switch window to these ports instead of allowing all outbound. Needs a restart to take effect.")
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
                    .disabled(presetBusy || !canApply)
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
        for (label, value) in fields.durationFieldsForValidation {
            guard DurationText.looksLikeGoDuration(value) else {
                ConfigApply.invalidDurationAlert(label, value)
                return
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
