import AppKit
import Foundation
import ServiceManagement

/// Removing the per-user half of a dezhban install — the half
/// `packaging/macos/uninstall.sh` cannot reach.
///
/// The uninstaller runs as root and removes root-owned things: the binary, the
/// app bundle, `/etc/dezhban`, `/var/db/dezhban`, the launchd plist, the pkg
/// receipts. Everything belonging to the logged-in user survived it: the
/// preference domain, the login-keychain token, the login-item registration.
/// A user's login keychain is not usefully reachable from a root script, and a
/// login item is registered per user, so this work has to happen in the user's
/// own session — which is exactly what this app is.
///
/// The consequence of it never having happened: `dezhban.firstRunCompleted`
/// outlived every reinstall, so `FirstRunDecision.offer` refused to show the
/// setup wizard on what was, from the daemon's side, a completely fresh
/// machine. Fixing the purge is what fixes that.
///
/// What this deliberately does NOT touch is recorded in
/// docs/adr/0015-complete-purge-semantics.md: other user accounts (this app
/// speaks only for the account running it) and notification authorization
/// (macOS owns it; there is no API to revoke it).
enum Purge {
    /// The app's preference domain before the bundle identifier was settled.
    /// Still on disk on any Mac that ran an early build — a purge that leaves
    /// it behind is not a purge.
    static let legacyBundleID = "com.dezhban.DezhbanMenu"

    /// One thing removed, and whether it was there to remove. Reported rather
    /// than logged so the caller can tell the user what actually happened —
    /// "nothing to remove" and "failed to remove" must not look alike.
    struct Step {
        let what: String
        let removed: Bool
        let error: String?
    }

    /// Removes everything this account holds. Returns one Step per item, in the
    /// order performed.
    ///
    /// Ordering matters in one place: the preference domains go LAST. AppKit
    /// writes window frames and other defaults as the app winds down, so a
    /// domain cleared early would simply be recreated before the process exits.
    /// The root uninstaller repeats the domain deletion for `$SUDO_USER` for
    /// the same reason — belt and braces, because this is the step whose
    /// survival caused the original bug.
    @discardableResult
    static func perUser() -> [Step] {
        var steps: [Step] = []
        steps.append(removeKeychainToken())
        steps.append(removeLoginItem())
        steps.append(contentsOf: removeSavedState())
        steps.append(contentsOf: removePreferenceDomains())
        return steps
    }

    // MARK: - keychain

    /// The control token, plus the capability probe item that enrollment writes.
    /// Routed through `ControlToken` rather than `SecItemDelete` here, because
    /// that type owns both the account names and the reason a plain
    /// `SecItemDelete` is refused with `-25244` across code identities.
    private static func removeKeychainToken() -> Step {
        Step(what: "control token (login keychain)",
             removed: ControlToken.purge(),
             error: nil)
    }

    // MARK: - login item

    /// Unregisters both the LaunchAgent and any surviving `mainApp`
    /// registration from before ADR-0014. Either may legitimately be absent, so
    /// "not registered" is a success, not an error.
    private static func removeLoginItem() -> Step {
        var failures: [String] = []
        var removed = false
        for (name, svc) in [("login agent", SMAppService.agent(plistName: LoginItem.plistName)),
                            ("legacy login item", SMAppService.mainApp)] {
            guard svc.status == .enabled else { continue }
            do {
                try svc.unregister()
                removed = true
            } catch {
                failures.append("\(name): \(error.localizedDescription)")
            }
        }
        return Step(what: "start at login",
                    removed: removed,
                    error: failures.isEmpty ? nil : failures.joined(separator: "; "))
    }

    // MARK: - on-disk per-user state

    private static func removeSavedState() -> [Step] {
        bundleIDs.map { id in
            let url = FileManager.default.homeDirectoryForCurrentUser
                .appendingPathComponent("Library/Saved Application State/\(id).savedState")
            return removeItem(at: url, what: "saved window state (\(id))")
        }
    }

    private static func removePreferenceDomains() -> [Step] {
        bundleIDs.map { id in
            let defaults = UserDefaults.standard
            let had = defaults.persistentDomain(forName: id) != nil
            defaults.removePersistentDomain(forName: id)
            // Deprecated, and correct here: the process is about to exit, and
            // the point is that the deletion reaches disk before it does.
            defaults.synchronize()
            return Step(what: "preferences (\(id))", removed: had, error: nil)
        }
    }

    /// This app's identifier and the one it used to have. `Bundle.main` is nil
    /// only for a bare SwiftPM binary, where there is no domain to clear.
    private static var bundleIDs: [String] {
        [Bundle.main.bundleIdentifier, legacyBundleID].compactMap { $0 }
    }

    private static func removeItem(at url: URL, what: String) -> Step {
        guard FileManager.default.fileExists(atPath: url.path) else {
            return Step(what: what, removed: false, error: nil)
        }
        do {
            try FileManager.default.removeItem(at: url)
            return Step(what: what, removed: true, error: nil)
        } catch {
            return Step(what: what, removed: false, error: error.localizedDescription)
        }
    }
}
