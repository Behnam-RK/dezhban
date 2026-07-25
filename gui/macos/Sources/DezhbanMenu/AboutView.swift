import SwiftUI

/// Real-data About pane: version, resolved config path, binary path, the
/// enforcement posture (from the shared snapshot), whether the OS service is
/// installed, and how each of the two authorisation paths will actually
/// authenticate — surfaced so "why did I get a password dialog?" is diagnosable
/// from the app itself.
///
/// The two paths are genuinely different and must not be collapsed into one row:
/// settings changes go through the daemon, authorised by the keychain token, and
/// cost a Touch ID tap; lifecycle actions cannot go through a daemon they are
/// installing or stopping, so they still elevate.
struct AboutView: View {
    @EnvironmentObject var state: AppState

    @State private var version = ""
    @State private var configPath = ""
    @State private var binaryPath = ""
    @State private var isCheckingUpdate = false
    @State private var isUpgrading = false

    /// Evaluated once per pane appearance rather than in `body`: neither answer can
    /// change while the pane is open except through the Settings toggle, which
    /// reopens this view. Both read state only — no biometric prompt is triggered
    /// by looking.
    @State private var settingsAuth = ""
    @State private var privilegedAuth = ""

    var body: some View {
        Form {
            Section {
                LabeledContent("Version", value: version.isEmpty ? "dezhban (version unknown)" : version)
                LabeledContent("Config path") { pathText(configPath) }
                LabeledContent("Binary path") { pathText(binaryPath) }
            }
            Section {
                LabeledContent("Posture",
                               value: state.isLive ? PostureUI.humanPosture(state.snapshot!) : "stopped")
                LabeledContent("Service",
                               value: state.serviceIsInstalled ? "installed" : "not installed")
                LabeledContent("Settings changes", value: settingsAuth)
                LabeledContent("Privileged actions", value: privilegedAuth)
            }
            updateSection
        }
        .formStyle(.grouped)
        .navigationTitle("About Dezhban")
        .onAppear(perform: load)
    }

    /// Self-apply is macOS-only and this view only exists on macOS, but the
    /// check itself (state.updateCheck) works everywhere `dezhban upgrade
    /// check` does — the button below is what's actually gated.
    @ViewBuilder
    private var updateSection: some View {
        Section {
            if isUpgrading {
                LabeledContent("Status") {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text("Downloading and installing…")
                    }
                }
            } else if let check = state.updateCheck, check.available {
                LabeledContent("Update available", value: "v\(check.latest)")
                Button("Download and Install v\(check.latest)…") { upgradeNow(check) }
                    .disabled(!state.cliFound)
                Link("Release notes", destination: URL(string: check.url) ?? URL(string: "https://github.com/Behnam-RK/dezhban/releases")!)
            } else if let check = state.updateCheck {
                LabeledContent("Status", value: "up to date (v\(check.current))")
            } else {
                LabeledContent("Status", value: "not checked yet")
            }
            Button(isCheckingUpdate ? "Checking…" : "Check Now") { checkNow() }
                .disabled(isCheckingUpdate || isUpgrading || !state.cliFound)
        } header: {
            Text("Updates")
        } footer: {
            Text("Checks GitHub for a newer release. Applying restarts the app and, only if the daemon is in a safe posture (guard or standby — never during FULL BLOCK or an open switch window), briefly restarts enforcement to activate it. See docs/usage/upgrade.md.")
                .foregroundStyle(.secondary)
        }
    }

    private func checkNow() {
        isCheckingUpdate = true
        DispatchQueue.global(qos: .userInitiated).async {
            let result = UpdateChecker.check()
            DispatchQueue.main.async {
                state.updateCheck = result
                isCheckingUpdate = false
            }
        }
    }

    private func upgradeNow(_ check: UpgradeCheckResult) {
        guard AppActions.confirmUpgrade(to: check.latest) else { return }
        isUpgrading = true
        AppActions.performUpgrade { result in
            isUpgrading = false
            if !result.ok {
                AppActions.outputAlert(title: "Upgrade failed", ok: false, output: result.output)
            }
            // On success the app relaunches itself (AppActions.relaunch) — no
            // success alert needed; the app reopening back up IS the
            // confirmation, and it happens within moments of this closure.
        }
    }

    private func pathText(_ s: String) -> some View {
        Text(s)
            .textSelection(.enabled)
            .foregroundStyle(.secondary)
            .truncationMode(.middle)
            .lineLimit(1)
    }

    /// Only `version` needs a fresh CLI call — posture and service state are
    /// already live in AppState for the rest of the window.
    /// What a settings change will actually cost the user right now. It used to
    /// read "Authorization Services (Touch ID capable)" unconditionally, which was
    /// false in every case that mattered: that dialog is password-only in
    /// practice, which is the finding that produced the control token.
    private static func describeSettingsAuth() -> String {
        if ControlToken.isStored {
            return "Touch ID (control token enrolled)"
        }
        if ControlToken.biometryAvailable {
            return "Password — turn on Touch ID in Settings"
        }
        return "Password — this Mac has no Touch ID"
    }

    /// Lifecycle actions (install/start/stop/panic) cannot go through the daemon,
    /// so they still elevate. Which prompt appears depends on `pam_tid`, and that
    /// is precisely the "why did I get a password dialog?" question this pane
    /// exists to answer.
    private static func describePrivilegedAuth() -> String {
        if Elevation.sudoTouchIDConfigured {
            return "Touch ID via sudo (pam_tid)"
        }
        if Elevation.isAvailable {
            return "Password — Authorization Services (no pam_tid)"
        }
        return "Password — AppleScript fallback"
    }

    private func load() {
        settingsAuth = Self.describeSettingsAuth()
        privilegedAuth = Self.describePrivilegedAuth()
        // Show the memoized path immediately; the authoritative resolution happens
        // below, off the main thread (DezhbanCLI.exec explains why that matters).
        configPath = DezhbanCLI.displayConfigPath
        binaryPath = DezhbanCLI.binaryPath() ?? "(not found — install it first)"
        DispatchQueue.global(qos: .userInitiated).async {
            let path = DezhbanCLI.resolvedConfigPath()
            let v = DezhbanCLI.run(["version"]).output.trimmingCharacters(in: .whitespacesAndNewlines)
            DispatchQueue.main.async {
                configPath = path
                version = v
            }
        }
    }
}
