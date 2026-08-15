import SwiftUI
import DezhbanCore

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

    /// Evaluated once per pane appearance rather than in `body`. Both read state
    /// only — no biometric prompt is triggered by looking.
    ///
    /// `privilegedAuth` cannot change while the pane is open. `settingsAuth` can:
    /// besides the Settings toggle (which reopens this view), it now folds in
    /// whether the sensor is usable RIGHT NOW, and a lid that opens or a Touch ID
    /// lockout that expires changes that with nobody navigating anywhere. The row
    /// is therefore accurate as of the moment you opened it and is re-asked on the
    /// next appearance; leaving the window parked on About across a dock/undock
    /// will show the previous answer until you navigate away and back. Deliberate:
    /// polling the sensor from a pane that is merely on screen would spend an
    /// LAContext evaluation on a timer for a diagnostic row nobody is reading.
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
            Text("Checks GitHub for a newer release. Applying restarts the app and, only if dezhban is in a safe posture (guard or standby — never during FULL BLOCK or an open switch window), briefly restarts enforcement to activate it. See docs/usage/upgrade.md.")
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
    private static func describeSettingsAuth(_ capability: TokenCapability) -> String {
        if ControlToken.isStored {
            return enrolledSummary
        }
        // Not "turn on Touch ID in Settings" unless that would actually work.
        // The old copy said it unconditionally, so a Mac whose keychain refuses
        // the item was sent to a toggle that could only spend a password and fail.
        return capability.settingsAuthSummary
    }

    /// The row's value when it can be given without touching the keychain — which
    /// `warmCapability()` makes the normal case — so the pane does not render a
    /// blank "Settings changes" for as long as two subprocesses take to answer.
    /// nil only when nothing is enrolled, the probe has not run, and biometry is
    /// available.
    ///
    /// An enrolled host is answered FIRST and without ever running the keychain
    /// probe: the probe's verdict is about whether *enrolling* could succeed, and a
    /// host that already has a token does not need it. Asking `capability` here
    /// would have shown "Checking…" — and, on a host whose probe status is not
    /// cacheable, run a keychain add/delete (a modal unlock dialog on a locked
    /// login keychain) — every time the pane opened, for a row whose value was
    /// already known. `enrolledSummary` does ask `capabilityIfKnown`, which is the
    /// half of the verdict that answers "is the sensor usable right now" from a
    /// local `LAContext` check and returns nil rather than probing.
    private static func describeSettingsAuthIfKnown() -> String? {
        if ControlToken.isStored {
            return enrolledSummary
        }
        return ControlToken.capabilityIfKnown.map(describeSettingsAuth)
    }

    /// What an enrolled host is told — which is NOT unconditionally "Touch ID".
    /// A secret the daemon will not accept (`isKnownOrphaned`, set when
    /// `forgetToken`'s keychain removal or a failed enrollment left one behind)
    /// authorises nothing: `ConfigApply.writeConfig` deliberately skips it and the
    /// save falls to the password path. Saying "Touch ID" here would name a cost
    /// the user will not actually pay, and this row exists precisely to answer
    /// "why did I get a password dialog?".
    private static var enrolledSummary: String {
        if ControlToken.isKnownOrphaned {
            return "Password — the stored secret is stale; turn Touch ID off and on in Settings"
        }
        // A sensor that is unusable RIGHT NOW — a shut lid, or a Touch ID lockout
        // after repeated failures — makes `ControlToken.load()` return nil on its
        // `canEvaluatePolicy` guard, so the save falls to the sudo password prompt
        // with the enrollment perfectly intact. "Touch ID (control token enrolled)"
        // would name a cost the user is not about to pay, in the one row that
        // exists to answer "why did I get a password dialog?".
        //
        // Asked through `capabilityIfKnown`, never `capability`: the no-sensor case
        // is answered from a local check and NEVER runs the keychain probe, which
        // is the whole reason an enrolled host is otherwise not asked for a verdict.
        if ControlToken.capabilityIfKnown == .noBiometry {
            return "Password — Touch ID is unavailable right now (a closed lid, or a lockout); "
                + "the enrollment is intact"
        }
        return "Touch ID (control token enrolled)"
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
        privilegedAuth = Self.describePrivilegedAuth()
        // Same "show it now, confirm it below" shape as the paths: an empty
        // LabeledContent value for the length of two subprocess calls reads as a
        // missing row, not a pending one.
        // Asked ONCE and reused by the background block below: this call reads the
        // keychain (`isStored`) and builds an `LAContext`, and re-asking it a
        // moment later on the background queue would repeat both for an answer
        // that cannot have changed in between.
        let known = Self.describeSettingsAuthIfKnown()
        settingsAuth = known ?? "Checking…"
        // Show the memoized path immediately; the authoritative resolution happens
        // below, off the main thread (DezhbanCLI.exec explains why that matters).
        configPath = DezhbanCLI.displayConfigPath
        binaryPath = DezhbanCLI.binaryPath() ?? "(not found — install it first)"
        DispatchQueue.global(qos: .userInitiated).async {
            // `ControlToken.capability` is a keychain probe — an ADD plus a DELETE
            // — the first time anything asks for it. `MainView` warms it when the
            // window first appears (NOT `AppDelegate`, deliberately — see
            // `ControlToken.warmCapability`), but only when biometry was usable at
            // that moment: a Mac woken from clamshell, or one in Touch ID lockout
            // then, reaches this pane with the probe still unresolved. Doing it
            // here rather than on
            // `.onAppear`'s main thread means the worst case is a row that fills in
            // late, not a frozen window behind a keychain-unlock dialog.
            //
            // Entered ONLY when `…IfKnown` came back nil above, so the probe runs
            // only when it is the thing standing between us and an answer — an
            // enrolled host, or one with no sensor, never writes to the keychain
            // here at all.
            //
            // Published on its OWN hop, before the two subprocess calls below.
            // Bundling it with them left "Checking…" on screen for as long as
            // `resolvedConfigPath()` and `dezhban version` took — two process
            // spawns — even once the verdict had been in hand for milliseconds.
            // These are independent values; only the row that is still unknown
            // should wait for the thing that is actually slow.
            if known == nil {
                let auth = Self.describeSettingsAuth(ControlToken.capability)
                DispatchQueue.main.async { settingsAuth = auth }
            }
            let path = DezhbanCLI.resolvedConfigPath()
            let v = DezhbanCLI.run(["version"]).output.trimmingCharacters(in: .whitespacesAndNewlines)
            DispatchQueue.main.async {
                configPath = path
                version = v
            }
        }
    }
}
