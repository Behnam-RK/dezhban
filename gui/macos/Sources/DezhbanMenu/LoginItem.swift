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
enum LoginItem {
    /// Must match `LoginAgent.plist`'s `Label` and the filename build-app.sh
    /// installs it under; launchd rejects a mismatch, and SMAppService reports
    /// it only as a `.notFound` status.
    private static let plistName = "com.behnam-rk.dezhban.app.login.plist"

    private static var service: SMAppService { .agent(plistName: plistName) }

    static var isEnabled: Bool {
        service.status == .enabled
    }

    /// Toggles login-at-launch. Returns the resulting enabled state; on error it
    /// logs and returns the unchanged prior state.
    @discardableResult
    static func toggle() -> Bool {
        do {
            if isEnabled {
                try service.unregister()
            } else {
                try service.register()
            }
        } catch {
            NSLog("DezhbanMenu: login item toggle failed: \(error)")
        }
        return isEnabled
    }

    /// Moves an install that registered `SMAppService.mainApp` (every build
    /// before the agent existed) onto the agent, once.
    ///
    /// Deliberately gated on the OLD registration being enabled: a user who had
    /// login-at-launch switched off must not have it switched on by an upgrade.
    /// Idempotent — after the first run `mainApp.status` is no longer `.enabled`,
    /// so this does nothing on every launch thereafter, and it never touches an
    /// agent registration that already exists.
    static func migrateFromMainAppRegistration() {
        guard SMAppService.mainApp.status == .enabled else { return }
        do {
            try SMAppService.mainApp.unregister()
        } catch {
            NSLog("DezhbanMenu: could not unregister the legacy login item: \(error)")
            // Fall through and register the agent anyway: two registrations is a
            // cosmetic duplicate in System Settings, but skipping the register
            // would leave the user with a login launch that never sets
            // --background — the exact bug this migration exists to fix.
        }
        if !isEnabled {
            do {
                try service.register()
            } catch {
                NSLog("DezhbanMenu: could not register the login agent: \(error)")
            }
        }
    }
}
