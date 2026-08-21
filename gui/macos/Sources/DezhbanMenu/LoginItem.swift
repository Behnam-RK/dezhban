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
/// That is caught at startup by `yieldToRunningInstance()` in main.swift, not
/// here — the duplicate is a *process* problem and this type has no way to see
/// it.
enum LoginItem {
    /// Must match `LoginAgent.plist`'s `Label` and the filename build-app.sh
    /// installs it under; launchd rejects a mismatch, and SMAppService reports
    /// it only as a `.notFound` status. build-app.sh asserts the equality at
    /// build time so this cannot drift silently.
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

    /// Toggles login-at-launch. Returns the resulting enabled state; on error it
    /// logs and returns whatever state the system is actually in.
    ///
    /// Turning it OFF retracts both registrations, for the reason `isEnabled`
    /// reports both. Turning it ON registers only the agent — the legacy one is
    /// never created again.
    @discardableResult
    static func toggle() -> Bool {
        if isEnabled {
            if agentEnabled { unregister(service, what: "login agent") }
            if legacyEnabled { unregister(.mainApp, what: "legacy login item") }
        } else {
            do {
                try service.register()
            } catch {
                NSLog("DezhbanMenu: could not register the login agent: \(error)")
            }
        }
        return isEnabled
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
            // Leave the agent unregistered. Registering it now would mean TWO
            // launches at login — the agent with `--background` and the legacy
            // item without — and whichever won the race would decide whether the
            // window appeared, which is worse than the old behaviour it would be
            // replacing. `isEnabled` reports the legacy item, so the Settings
            // toggle is honest, and switching it off and on again retracts the
            // legacy registration and lands a clean agent.
            NSLog("DezhbanMenu: the legacy login item could not be retracted; "
                + "leaving login-at-launch as it was. Toggle it off and on in "
                + "Settings to move onto the login agent.")
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
