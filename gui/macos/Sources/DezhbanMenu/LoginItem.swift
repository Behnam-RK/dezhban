import Foundation
import ServiceManagement

/// Wraps the menubar app's own login-item registration via SMAppService
/// (macOS 13+). Requires a proper bundle with a bundle identifier (assembled by
/// build-app.sh), so it is a no-op / failure when run as a bare SwiftPM binary.
///
/// It registers an **agent** — `Contents/Library/LaunchAgents/…login.plist`,
/// installed by build-app.sh — rather than `SMAppService.mainApp`. The two are
/// equivalent as far as "start me at login" goes; the difference is that the
/// agent's `ProgramArguments` carry `--background`, which is the only reliable
/// way for the app to know that macOS started it rather than the user. See
/// `LaunchVisibility` and docs/adr/0014-login-item-launch-marker.md.
///
/// Registering an agent `exec`s the app immediately (`RunAtLoad`), so both
/// `toggle()` and the migration below can spawn a second copy of a running app.
/// That is caught at startup by the instance lock in main.swift, not here — the
/// duplicate is a *process* problem and this type has no way to see it.
enum LoginItem {
    /// Must match `LoginAgent.plist`'s `Label` and the filename build-app.sh
    /// installs it under; launchd rejects a mismatch, and SMAppService reports
    /// it only as a `.notFound` status. build-app.sh greps this file for the
    /// label it installs, so the three cannot drift apart silently.
    private static let plistName = "com.behnam-rk.dezhban.app.login.plist"

    /// Whether the one-shot move off `SMAppService.mainApp` has been attempted.
    ///
    /// Persisted, and deliberately not re-derived from `mainApp.status`: that
    /// read is only a truthful "already migrated" signal when the unregister
    /// succeeded, and it can fail for real (a login item added by hand in System
    /// Settings was never registered through SMAppService; unregister is also
    /// known to fail after the bundle moves). Inferring it meant the migration
    /// ran again on every launch, which re-registered the agent after the user
    /// had switched login-at-launch off in Settings — login-at-launch back on,
    /// with no way to turn it off from the UI. Same UserDefaults-flag call as
    /// `FirstRun`: a fact about this app on this account, not daemon config.
    private static let migratedKey = "dezhban.loginItemMigratedToAgent"

    /// What a `toggle()` actually achieved, so the UI can say something true.
    ///
    /// A plain `Bool` could not: `register()` reports "the user has to approve
    /// this in System Settings" as a *status* rather than an error, and the one
    /// path where the legacy registration cannot be retracted needs its own
    /// message because the app has no way out of it on its own.
    enum Outcome: Equatable {
        /// Login-at-launch is on, via the agent.
        case enabled
        /// Nothing will start the app at login.
        case disabled
        /// Registered, but macOS is holding it for the user's approval — most
        /// often because they switched this app off in System Settings before.
        case awaitingApproval
        /// The legacy LaunchServices login item is still live and macOS refuses
        /// to retract it, so the app still starts at login without the launch
        /// marker. Only the user can clear this, in System Settings.
        case legacyStuck
        /// Registration failed outright.
        case failed(String)

        /// Whether anything starts the app at login — what the Settings switch
        /// shows.
        var isOn: Bool {
            switch self {
            case .enabled, .awaitingApproval, .legacyStuck: return true
            case .disabled, .failed: return false
            }
        }

        /// One line for the Settings status area.
        var message: String {
            switch self {
            case .enabled: return "App will open at login."
            case .disabled: return "App will not open at login."
            case .awaitingApproval:
                return "macOS is holding this for your approval — enable Dezhban in "
                    + "System Settings → General → Login Items."
            case .legacyStuck:
                return "macOS would not remove the old login item. Remove \"Dezhban\" under "
                    + "System Settings → General → Login Items, then switch this on again."
            case .failed(let why):
                return "Could not change the login item: \(why)"
            }
        }
    }

    private static var service: SMAppService { .agent(plistName: plistName) }

    private static var agentEnabled: Bool { service.status == .enabled }

    /// The legacy LaunchServices registration every build before the agent used.
    private static var legacyEnabled: Bool { SMAppService.mainApp.status == .enabled }

    /// Whether anything at all will start this app at login — the agent or a
    /// legacy registration the migration could not retract.
    ///
    /// Both, not just the agent: on the failed-migration path the legacy item is
    /// still live, so the app still starts at login, and it starts *without*
    /// `--background`, which is the very bug this PR fixes. Reporting only the
    /// agent would show "off" while startup kept happening, and leave the user
    /// no control that reaches the thing launching them.
    static var isEnabled: Bool { agentEnabled || legacyEnabled }

    /// Toggles login-at-launch and reports what actually happened.
    ///
    /// Turning it OFF retracts both registrations, for the reason `isEnabled`
    /// reports both. Turning it ON registers only the agent — the legacy one is
    /// never created again.
    @discardableResult
    static func toggle() -> Outcome {
        if isEnabled { return disable() }
        return enable()
    }

    private static func enable() -> Outcome {
        do {
            try service.register()
        } catch {
            NSLog("DezhbanMenu: could not register the login agent: \(error)")
            return .failed(error.localizedDescription)
        }
        // Checked, not assumed: `register()` returns without throwing when macOS
        // is going to make the user approve it, and the switch snapping back with
        // no explanation is indistinguishable from a bug.
        if service.status == .requiresApproval { return .awaitingApproval }
        return agentEnabled ? .enabled : .failed("macOS reported the login item as \(service.status)")
    }

    private static func disable() -> Outcome {
        if agentEnabled { unregister(service, what: "login agent") }
        if legacyEnabled { unregister(.mainApp, what: "legacy login item") }
        if legacyEnabled {
            // The stuck path. Reported rather than worked around: registering the
            // agent alongside it would mean two launches at login, one with the
            // marker and one without, and whichever won the race would decide
            // whether the window appeared. `Outcome.legacyStuck` tells the user
            // the one thing that does clear it.
            NSLog("DezhbanMenu: the legacy login item could not be retracted")
            return .legacyStuck
        }
        return agentEnabled ? .failed("the login agent is still registered") : .disabled
    }

    /// Retracts everything that could start this app at login, best effort.
    ///
    /// Used by the `--unregister-login-item` errand the uninstaller runs (see
    /// main.swift). `SMAppService.unregister()` is the only thing that actually
    /// retracts an agent registration — `launchctl bootout` unloads the job for
    /// this boot and leaves the record that recreates it at the next login — and
    /// only the app can call it.
    static func retractAll() {
        if agentEnabled { unregister(service, what: "login agent") }
        if legacyEnabled { unregister(.mainApp, what: "legacy login item") }
    }

    /// Moves an install that registered `SMAppService.mainApp` (every build
    /// before the agent existed) onto the agent, exactly once per account.
    ///
    /// Gated on the OLD registration being enabled: a user who had
    /// login-at-launch switched off must not have it switched on by an upgrade.
    /// The attempt is recorded either way, so this runs at most once whether it
    /// succeeded or not — see `migratedKey`.
    static func migrateFromMainAppRegistration() {
        guard !UserDefaults.standard.bool(forKey: migratedKey) else { return }
        defer { UserDefaults.standard.set(true, forKey: migratedKey) }

        guard legacyEnabled else { return }
        unregister(.mainApp, what: "legacy login item")

        // Checked after the attempt rather than trusting it not to throw: what
        // matters is whether the old item is actually gone.
        if legacyEnabled {
            // Same reasoning as `disable()`'s stuck path — the agent is left
            // unregistered rather than stacked on top of a live legacy item. The
            // Settings toggle reports the legacy registration, so the user can
            // see login-at-launch is on; clearing it is a System Settings job,
            // which `Outcome.legacyStuck` spells out when they try.
            NSLog("DezhbanMenu: the legacy login item could not be retracted; "
                + "leaving login-at-launch as it was. Remove \"Dezhban\" under "
                + "System Settings → General → Login Items to move onto the login agent.")
            return
        }
        guard !agentEnabled else { return }
        do {
            try service.register()
        } catch {
            NSLog("DezhbanMenu: could not register the login agent: \(error)")
        }
    }

    private static func unregister(_ target: SMAppService, what: String) {
        do {
            try target.unregister()
        } catch {
            NSLog("DezhbanMenu: could not unregister the \(what): \(error)")
        }
    }
}
