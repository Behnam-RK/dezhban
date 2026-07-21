import AppKit
import SwiftUI

/// The Settings pane — SwiftUI port of the retired SettingsPanel. Two kinds of
/// controls, two behaviors, stated in the UI:
///   - Startup toggles act IMMEDIATELY (they are service/login-item actions, not
///     config values — there is nothing to batch or restart).
///   - Protection fields are staged and written by Apply through one batched
///     `config set` (single validation, write, and admin prompt).
struct SettingsView: View {
    @EnvironmentObject var state: AppState

    // Dotted keys from configFields (cmd/dezhban/config_cmd.go).
    private static let keys = ["blockedCountries", "vpn.switchWindow", "vpn.endpointGrace"]

    @State private var loginEnabled = false
    @State private var notifyEnabled = true
    @State private var checkUpdatesEnabled = true
    @State private var blockedCountries = ""
    @State private var switchWindow = ""
    @State private var endpointGrace = ""
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
                Section("Protection") {
                    TextField("Blocked countries (comma-sep, e.g. IR,CN)", text: $blockedCountries)
                        .disabled(!canApply)
                    TextField("Switch window (e.g. 2m)", text: $switchWindow)
                        .disabled(!canApply)
                        .help("Default duration of a VPN switch window. Clamped to the configured maximum.")
                    TextField("Endpoint grace (e.g. 15m)", text: $endpointGrace)
                        .disabled(!canApply)
                        .help("How long a discovered VPN server stays reachable after its connection "
                            + "disappears, so a dropped VPN can redial the same server.")
                }
                Section {
                    LabeledContent("Config file") {
                        Text(DezhbanCLI.resolvedConfigPath())
                            .textSelection(.enabled)
                            .foregroundStyle(.secondary)
                            .truncationMode(.middle)
                            .lineLimit(1)
                    }
                    Button("Open Config File…") {
                        NSWorkspace.shared.open(URL(fileURLWithPath: DezhbanCLI.resolvedConfigPath()))
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
    }

    private var footer: some View {
        HStack {
            Text(status)
                .font(.callout)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer()
            Button("Reload", action: seed)
            Button("Apply…", action: apply)
                .keyboardShortcut(.defaultAction)
                .disabled(!canApply)
        }
        .padding(12)
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
        let commands = wantInstalled ? AppActions.installCommands : AppActions.uninstallCommands
        let title = wantInstalled ? "dezhban — install service" : "dezhban — uninstall service"
        AppActions.capturedSequence(commands) { result in
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
        blockedCountries = ""; switchWindow = ""; endpointGrace = ""
        state.refreshServiceState()
        ConfigApply.seed(keys: Self.keys) { values, error in
            if let error = error {
                status = error
                return
            }
            guard let v = values else { return }
            blockedCountries = v[0]
            switchWindow = v[1]
            endpointGrace = v[2]
            status = "Seeded from \(DezhbanCLI.resolvedConfigPath())"
            canApply = true
        }
    }

    // MARK: - apply (staged protection fields)

    private func apply() {
        let blocked = blockedCountries.trimmingCharacters(in: .whitespaces)
        let window = switchWindow.trimmingCharacters(in: .whitespaces)
        let grace = endpointGrace.trimmingCharacters(in: .whitespaces)
        for (label, value) in [("Switch window", window), ("Endpoint grace", grace)] {
            guard ConfigApply.looksLikeGoDuration(value) else {
                ConfigApply.invalidDurationAlert(label, value)
                return
            }
        }

        let pairs = [
            "blockedCountries=\(blocked)",
            "vpn.switchWindow=\(window)",
            "vpn.endpointGrace=\(grace)",
        ]
        let restart = ConfigApply.confirmRestart()
        canApply = false
        status = restart ? "Applying and restarting…" : "Applying…"
        ConfigApply.apply(pairs: pairs, restart: restart, awaitPosture: false,
                          title: "Settings") { outcome in
            canApply = true
            status = outcome.status
            if let title = outcome.transcriptTitle, let text = outcome.transcript {
                state.showInLogs(title: title, text: text)
            }
        }
    }
}
