import AppKit
import SwiftUI

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

    // Dotted keys from configFields (cmd/dezhban/config_cmd.go). Order matches the
    // seed() destructuring below.
    private static let keys = [
        "vpn.tunnelInterfaces", "vpn.endpoints",
        "vpn.autodetect", "vpn.autoDiscoverEndpoints", "vpn.autoArm",
        "vpn.allowLocalNetwork",
        "blockedCountries", "pollInterval",
        "vpn.switchWindow", "vpn.reconnectWindow", "vpn.endpointGrace",
        "vpn.endpointRefresh", "vpn.tunnelWatch",
    ]

    @State private var loginEnabled = false
    @State private var notifyEnabled = true
    @State private var checkUpdatesEnabled = true

    /// Displayed in the Config file row and used by "Open Config File…". Seeded from
    /// the memoized value (warmed at launch) and refreshed off the main thread by
    /// `seed()`, so neither the body nor a button action ever shells out.
    @State private var configPath = DezhbanCLI.displayConfigPath

    @State private var tunnelInterfaces = ""
    @State private var endpoints = ""
    @State private var autodetect = false
    @State private var autoDiscover = false
    @State private var autoArm = false
    @State private var allowLocalNetwork = true
    @State private var blockedCountries = ""
    @State private var pollInterval = ""
    @State private var switchWindow = ""
    @State private var reconnectWindow = ""
    @State private var endpointGrace = ""
    @State private var endpointRefresh = ""
    @State private var tunnelWatch = ""

    /// The values this pane was last seeded with, in `keys` order. Comparing the
    /// live fields against these is how an unsaved edit is told from a pane that
    /// is merely displaying what is on disk — which decides whether it is safe to
    /// re-read the file underneath the user.
    @State private var seededValues: [String] = []

    /// Field values in `keys` order, for the dirtiness check above.
    private var currentValues: [String] {
        [tunnelInterfaces, endpoints,
         String(autodetect), String(autoDiscover), String(autoArm),
         String(allowLocalNetwork),
         blockedCountries, pollInterval,
         switchWindow, reconnectWindow, endpointGrace,
         endpointRefresh, tunnelWatch]
    }

    private var hasUnsavedEdits: Bool {
        !seededValues.isEmpty && currentValues != seededValues
    }

    @State private var status = ""
    @State private var canApply = false
    @State private var bootBusy = false

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("Startup") {
                    Toggle("Start protection at boot (install the system service)", isOn: bootBinding)
                        .disabled(bootBusy || !state.cliFound)
                        .help("Installs dezhban as a launchd system daemon: enforcement starts at boot — "
                            + "before any login — and survives restarts and crashes. Unchecking uninstalls the "
                            + "service (rules are torn down first so nothing is left blocking).")
                    Toggle("Open this app at login", isOn: loginBinding)
                        .help("Registers the app as a login item (System Settings → General → Login Items). "
                            + "This is only the status display — protection itself is the system service above.")
                    Toggle("Notify on essential events", isOn: notifyBinding)
                        .help("macOS notifications for the transitions that matter: guard armed, egress "
                            + "blocked, warnings (enforcement error / switch window open), standby, stopped. "
                            + "Nothing else.")
                    Toggle("Check for updates automatically", isOn: checkUpdatesBinding)
                        .help("Checks GitHub for a newer release at launch and every ~24h — never from the "
                            + "root daemon, only here, in this app, on this schedule. Turn off to stop this "
                            + "host contacting GitHub about updates entirely; \"Check Now\" in About still "
                            + "works either way.")
                }
                Section("VPN guard") {
                    TextField("Tunnel interfaces (comma-sep)", text: $tunnelInterfaces)
                        .disabled(!canApply)
                    TextField("Endpoints (comma-sep)", text: $endpoints)
                        .disabled(!canApply)
                }
                Section("Autodetection") {
                    Toggle("Autodetect tunnel interface (vpn.autodetect)", isOn: $autodetect)
                        .disabled(!canApply)
                    Toggle("Auto-discover endpoints (vpn.autoDiscoverEndpoints)", isOn: $autoDiscover)
                        .disabled(!canApply)
                    Toggle("Auto-arm when a VPN connects (vpn.autoArm)", isOn: $autoArm)
                        .disabled(!canApply)
                        .help("With no VPN connected the daemon idles in standby (nothing blocked) and arms "
                            + "the guard the moment a tunnel appears. It never disarms on a drop — that's the "
                            + "kill switch — only an explicit Unblock with the VPN off returns to standby.")
                }
                Section("Local network") {
                    Toggle("Keep local devices reachable", isOn: $allowLocalNetwork)
                        .disabled(!canApply)
                        .help("Printers, NAS, your router's admin page, AirPlay and Chromecast, and local "
                            + "dev servers keep working while the guard is armed. This is not a hole in the "
                            + "kill switch: it allows local destinations only, so anything on the internet "
                            + "stays blocked. The one cost is on untrusted Wi-Fi (a café, a hotel), where it "
                            + "also lets other devices on that network reach you.")
                }
                Section("Protection") {
                    TextField("Blocked countries (comma-sep, e.g. IR,RU,KP)", text: $blockedCountries)
                        .disabled(!canApply)
                    TextField("Geo IP lookup interval (e.g. 15s)", text: $pollInterval)
                        .disabled(!canApply)
                        .help("How often the current VPN exit's country is checked.")
                }
                Section("Windows") {
                    TextField("Switch window (e.g. 5s)", text: $switchWindow)
                        .disabled(!canApply)
                        .help("Manual switch window (`dezhban switch`): 0 disables it, otherwise up to 3m.")
                    TextField("Reconnect window (e.g. 30s)", text: $reconnectWindow)
                        .disabled(!canApply)
                        .help("Automatic window opened when a healthy tunnel drops, so the VPN client can "
                            + "redial: 0 disables it, otherwise up to 10m.")
                    TextField("Endpoint grace (e.g. 15m)", text: $endpointGrace)
                        .disabled(!canApply)
                        .help("How long a discovered VPN server stays reachable after its connection "
                            + "disappears, so a dropped VPN can redial the same server.")
                }
                Section("Timing") {
                    TextField("Endpoint refresh (e.g. 30s)", text: $endpointRefresh)
                        .disabled(!canApply)
                    TextField("Tunnel watch (e.g. 5s)", text: $tunnelWatch)
                        .disabled(!canApply)
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
            Button("Reset to Defaults…", action: resetToDefaults)
                .disabled(!canApply)
            Button("Reload", action: seed)
            Button("Apply…", action: apply)
                .keyboardShortcut(.defaultAction)
                .disabled(!canApply)
        }
        .padding(12)
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
            alert.informativeText = "Protection will stop and will no longer start at boot. "
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
        tunnelInterfaces = ""; endpoints = ""
        autodetect = false; autoDiscover = false; autoArm = false; allowLocalNetwork = true
        blockedCountries = ""; pollInterval = ""
        switchWindow = ""; reconnectWindow = ""; endpointGrace = ""
        endpointRefresh = ""; tunnelWatch = ""
        state.refreshServiceState()
        // `path` is the same resolution ConfigApply.seed already did for the
        // `config get` calls — reusing it here means configPath never needs its
        // own second background resolve, so there's nothing to race.
        ConfigApply.seed(keys: Self.keys) { path, values, error in
            configPath = path
            if let error = error {
                status = error
                return
            }
            guard let v = values else { return }
            tunnelInterfaces = v[0]
            endpoints = v[1]
            autodetect = (v[2] == "true")
            autoDiscover = (v[3] == "true")
            autoArm = (v[4] == "true")
            allowLocalNetwork = (v[5] == "true")
            blockedCountries = v[6]
            pollInterval = v[7]
            switchWindow = v[8]
            reconnectWindow = v[9]
            endpointGrace = v[10]
            endpointRefresh = v[11]
            tunnelWatch = v[12]
            // Recorded AFTER the fields are populated, so `currentValues` and the
            // seeded snapshot are the same thing at this instant and the pane
            // starts out clean.
            seededValues = currentValues
            status = "Seeded from \(path)"
            canApply = true
        }
    }

    // MARK: - apply (staged fields)

    private func apply() {
        let poll = pollInterval.trimmingCharacters(in: .whitespaces)
        let window = switchWindow.trimmingCharacters(in: .whitespaces)
        let reconnect = reconnectWindow.trimmingCharacters(in: .whitespaces)
        let grace = endpointGrace.trimmingCharacters(in: .whitespaces)
        let refresh = endpointRefresh.trimmingCharacters(in: .whitespaces)
        let watch = tunnelWatch.trimmingCharacters(in: .whitespaces)
        for (label, value) in [
            ("Geo IP lookup interval", poll),
            ("Switch window", window),
            ("Reconnect window", reconnect),
            ("Endpoint grace", grace),
            ("Endpoint refresh", refresh),
            ("Tunnel watch", watch),
        ] {
            guard ConfigApply.looksLikeGoDuration(value) else {
                ConfigApply.invalidDurationAlert(label, value)
                return
            }
        }

        let pairs = [
            "vpn.tunnelInterfaces=\(tunnelInterfaces)",
            "vpn.endpoints=\(endpoints)",
            "vpn.autodetect=\(autodetect)",
            "vpn.autoDiscoverEndpoints=\(autoDiscover)",
            "vpn.autoArm=\(autoArm)",
            "vpn.allowLocalNetwork=\(allowLocalNetwork)",
            "blockedCountries=\(blockedCountries.trimmingCharacters(in: .whitespaces))",
            "pollInterval=\(poll)",
            "vpn.switchWindow=\(window)",
            "vpn.reconnectWindow=\(reconnect)",
            "vpn.endpointGrace=\(grace)",
            "vpn.endpointRefresh=\(refresh)",
            "vpn.tunnelWatch=\(watch)",
        ]
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
