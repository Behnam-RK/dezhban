import AppKit
import Foundation

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

    /// One thing this purge was responsible for, and what is still there.
    ///
    /// `error == nil` means nothing was left behind — which covers both "removed
    /// it" and "there was nothing to remove", the two outcomes a user needs no
    /// words for. `error` is the third, and it carries the command that finishes
    /// the job. Deliberately no `removed` flag beside it: nobody read that field,
    /// and it could not answer the only question that matters without ambiguity,
    /// since `false` meant "was not there" in one step and "could not be deleted"
    /// in another. Reported rather than only logged, so the caller can tell the
    /// user what actually happened.
    struct Step {
        let what: String
        let error: String?
    }

    /// Removes the per-user state that CANNOT take this process down with it:
    /// the keychain items, the saved window state, the preference domains.
    /// Returns one Step per item, in the order performed.
    ///
    /// The login-item registration is deliberately not here — see
    /// `retractLoginItem()` for why unregistering is the one step that must
    /// never run before the root uninstaller has been handed off.
    ///
    /// Ordering matters in one place: the preference domains go LAST. AppKit
    /// writes window frames and other defaults as the app winds down, so a
    /// domain cleared early would simply be recreated before the process exits.
    /// The root uninstaller repeats the domain deletion for the console user
    /// for the same reason — belt and braces, because this is the step whose
    /// survival caused the original bug.
    static func perUserState() -> [Step] {
        var steps: [Step] = []
        steps.append(removeKeychainToken())
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
        let clean = ControlToken.purge()
        return Step(what: "control token (login keychain)",
                    error: clean ? nil
                        : "still in your login keychain — clear it with: "
                            + ControlToken.manualPurgeCommand)
    }

    // MARK: - login item

    /// Retracts everything that could start this app at login.
    ///
    /// Separated from `perUserState()` because this call can end the process:
    /// launchd terminates a loaded job's running process, and in a
    /// login-started session that process is this app — recorded as an open
    /// risk in docs/adr/0014-login-item-launch-marker.md and worked around
    /// throughout `LoginItem`. So it must be the LAST thing an uninstall does,
    /// and it must never run before the root uninstaller has been handed off:
    /// dying here with Terminal not yet open would leave the keychain and the
    /// preferences gone, the guard still enforcing, and nothing on screen.
    ///
    /// On the normal path nothing calls this at all. `uninstall.sh` runs the
    /// app's own `--unregister-login-item` errand (as the console user, in that
    /// user's launchd session) and already handles being killed doing it, so
    /// duplicating the call here would buy nothing and risk everything. It is
    /// for the one case the script cannot cover: the script is not installed
    /// AND nothing root-owned is left either. With the script missing on a
    /// machine that is still enforcing, this is deliberately not called — the
    /// app is then the only surface the user has left for a running guard, and
    /// retracting its login item would take that away too.
    ///
    /// `LoginItem.retractAll()` rather than a second hand-rolled loop — it
    /// takes `LoginItem.queue`, so it cannot race a concurrent `set(enabled:)`,
    /// and it treats `.requiresApproval` as the live registration it is. A
    /// registration the user has switched off in System Settings is still on
    /// file, and skipping it is exactly the orphan this errand exists to remove.
    static func retractLoginItem() -> Step {
        let clean = LoginItem.retractAll()
        return Step(what: "start at login",
                    error: clean ? nil
                        : "still registered — untick Dezhban in System Settings › "
                            + "General › Login Items")
    }

    // MARK: - on-disk per-user state

    /// Unlike the preference domains, this needs no second pass.
    ///
    /// The app's only window sets `isRestorable = false` (MainWindow), so AppKit
    /// does not write this directory back as the process winds down — what is
    /// here is residue from builds before that opt-out. The domains are the
    /// opposite case and that is why they are treated differently: the window
    /// frame and the split position persist through `setFrameAutosaveName` and
    /// the split view's autosave, which are `UserDefaults` writes that really do
    /// land while the app is closing down. `uninstall.sh` sweeps these paths for
    /// every account anyway, because it can reach accounts this app cannot.
    private static func removeSavedState() -> [Step] {
        bundleIDs.map { id in
            let url = FileManager.default.homeDirectoryForCurrentUser
                .appendingPathComponent("Library/Saved Application State/\(id).savedState")
            return removeItem(at: url, what: "saved window state (\(id))")
        }
    }

    /// Removes this account's Application Support directories — the session lock
    /// and any hand-off file beside it.
    ///
    /// NOT part of `perUserState()`, deliberately: that runs on every branch,
    /// including the two where this app keeps running, and deleting its own live
    /// session lock out from under itself is not a purge step but a bug. The
    /// root script sweeps these for every account, so the only branch that needs
    /// its own copy is the one where no script will ever run — and that branch
    /// quits immediately afterwards.
    @discardableResult
    static func removeSupportDirectories() -> [Step] {
        guard let support = FileManager.default
            .urls(for: .applicationSupportDirectory, in: .userDomainMask).first
        else { return [] }
        return bundleIDs.map { id in
            removeItem(at: support.appendingPathComponent(id),
                       what: "session lock and hand-off files (\(id))")
        }
    }

    /// Clears the domains again, discarding the report.
    ///
    /// For the one branch where no root script will ever repeat the deletion:
    /// `perUserState()` puts the domains last because AppKit writes defaults as
    /// the app winds down, and everywhere else `uninstall.sh` deletes them a
    /// second time for exactly that reason. Where nothing is going to run, this
    /// is that second time, and the caller quits immediately after.
    @discardableResult
    static func clearPreferenceDomains() -> [Step] {
        removePreferenceDomains()
    }

    private static func removePreferenceDomains() -> [Step] {
        bundleIDs.map { id in
            let defaults = UserDefaults.standard
            defaults.removePersistentDomain(forName: id)
            // Deprecated, and correct here: the process is about to exit, and
            // the point is that the deletion reaches disk before it does.
            defaults.synchronize()
            // Read back rather than trusting the call. `removePersistentDomain`
            // returns Void, so without this the Step could only ever say
            // "removed" — and this is the domain whose survival is the entire
            // bug, on the step Step's own contract says must never let a failure
            // look like an absence. Empty counts as gone: cfprefsd can leave the
            // domain present but bare, and the flag that matters is not in it.
            let left = defaults.persistentDomain(forName: id) ?? [:]
            return Step(what: "preferences (\(id))",
                        error: left.isEmpty ? nil
                            : "\(left.count) setting(s) still in that domain — "
                                + "remove them with: defaults delete \(id)")
        }
    }

    /// This app's identifier and the one it used to have. `Bundle.main` is nil
    /// only for a bare SwiftPM binary, where there is no domain to clear.
    private static var bundleIDs: [String] {
        [Bundle.main.bundleIdentifier, legacyBundleID].compactMap { $0 }
    }

    /// The existence check is load-bearing, not a micro-optimisation:
    /// `removeItem(at:)` throws for a path that is not there, and reporting
    /// "nothing to remove" as a failure would send the user off to clear
    /// something that was already clear.
    private static func removeItem(at url: URL, what: String) -> Step {
        guard FileManager.default.fileExists(atPath: url.path) else {
            return Step(what: what, error: nil)
        }
        do {
            try FileManager.default.removeItem(at: url)
            return Step(what: what, error: nil)
        } catch {
            return Step(what: what, error: error.localizedDescription)
        }
    }
}
