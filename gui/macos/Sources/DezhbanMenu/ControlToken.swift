import DezhbanCore
import Foundation
import LocalAuthentication
import Security

/// The app's copy of the daemon's control token, held in the login keychain and
/// released only after a Touch ID check this app performs.
///
/// This is what makes changing a setting cost a Touch ID tap instead of a
/// password. The daemon stores only the token's HASH, root-owned; the app holds
/// the token itself.
///
/// **Be precise about where the gate lives.** The check is
/// `LAContext.evaluatePolicy` in `load()` — the app asks macOS for a fingerprint,
/// and reads a plain keychain item once macOS says yes. It is NOT the keychain
/// refusing to release the item, which is the stronger arrangement ADR-0003
/// originally specified and ADR-0012 replaced. The difference is real and worth
/// stating plainly: a patched copy of this app could skip the check, whereas
/// nothing could fake its way past the keychain-enforced version. What ADR-0012
/// weighs is that the strong version needs an entitlement an ad-hoc signature
/// cannot carry (ADR-0011), so on every build this project ships it did not
/// merely weaken — it failed outright, leaving users on the password path.
///
/// Two things still bound the loss, and they are why this is defensible rather
/// than merely convenient:
/// - The item keeps its ordinary keychain ACL, bound to this app's code
///   identity. Another binary reading it gets a keychain password prompt, not
///   silent access.
/// - Patching the app requires writing to /Applications, which requires admin —
///   and an admin can already run `sudo dezhban config set` and bypass the token
///   entirely. The token was never the barrier against that attacker.
///
/// It still raises the bar rather than lowering it. Without a token, a config
/// change over the control socket would be gated only by the socket's file
/// permissions (admin group), which is a fine bar for ops that merely move
/// between fail-closed postures and too weak for one that writes settings
/// outliving the daemon. See
/// docs/adr/0003-biometric-token-over-existing-daemon.md and
/// docs/adr/0012-app-checked-biometrics-on-unsigned-builds.md.
///
/// Every query below addresses the LEGACY (file) keychain, and they must keep
/// agreeing. Passing `kSecUseDataProtectionKeychain: true` on any one of them
/// would point it at a different store than the others, so `isStored` would
/// answer for the wrong keychain and `store`'s own `remove()` would fail to clear
/// the previous item.
enum ControlToken {
    private static let service = "sh.dezhban.menu"
    private static let account = "control-token"

    /// Why the biometric prompt is appearing. Shown by the system HUD, so it has
    /// to name the actual consequence rather than the mechanism.
    private static let reason = "change dezhban settings"

    /// Whether this Mac has usable biometry.
    ///
    /// **Private on purpose.** This used to be the gate the UI and the enrollment
    /// flow asked, and it is the wrong question on its own: it says the *Mac* is
    /// capable while saying nothing about whether the keychain will take the item.
    /// Asking it is what let enrollment spend a root password before failing.
    /// Callers want `capability`, which folds this in.
    private static var biometryAvailable: Bool {
        var err: NSError?
        return LAContext().canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err)
    }

    /// The attributes identifying our one item. Shared by every call so they
    /// cannot drift apart onto different keychains or different accounts.
    private static func query(account: String = ControlToken.account) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    /// The account the capability probe writes under. Distinct from `account`, so
    /// a probe can never collide with — or worse, delete — a real token.
    private static let probeAccount = ControlToken.account + ".capability-probe"

    /// Whether the keychain will ACCEPT an item from this build, established by
    /// trying — once, at first use — rather than assumed.
    ///
    /// Kept even though a plain keychain item is expected to succeed everywhere:
    /// the probe is what stops enrollment spending a root password on a store
    /// that will fail, and that ordering guarantee should not depend on the
    /// current storage scheme continuing to be the forgiving one. It costs one
    /// silent add and delete.
    ///
    /// **Only a PERMANENT verdict is remembered.** A `static let` would have been
    /// simpler, but it memoizes whatever the first attempt returned — and the add
    /// status is not purely a function of the signature. A login-item launch races
    /// the login keychain's unlock, and a user who dismisses the unlock dialog
    /// that the probe raises gets `errSecAuthFailed` back; frozen for the life of
    /// a menubar app that runs for weeks, that one cancelled dialog would disable
    /// the feature until the next reboot, with the toggle greyed out and blaming
    /// the keychain. So success and `errSecMissingEntitlement` — the two answers
    /// that really are fixed for this binary — are cached, and anything else is
    /// left uncached, which makes `capabilityIfKnown` nil again and lets the next
    /// pane re-probe in the background.
    private static var probeStatus: OSStatus {
        if let cached = probeFlag.sync(execute: { cachedProbe }) { return cached }
        // Serialised so two callers cannot run overlapping adds under the same
        // probe account and hand each other a spurious -25299. The probe itself
        // must NOT run inside `probeFlag`, which `probeHasRun` reads from the main
        // thread — it can block on a keychain-unlock dialog.
        return probeQueue.sync {
            if let cached = probeFlag.sync(execute: { cachedProbe }) { return cached }
            let status = runProbe()
            if status == errSecSuccess || status == TokenCapability.missingEntitlement {
                probeFlag.sync { cachedProbe = status }
            }
            return status
        }
    }

    private static func runProbe() -> OSStatus {
        // Clear any leftover from a crash between the add and the delete below.
        // MUST go through `remove(account:)`, not `SecItemDelete`: a leftover from
        // a PREVIOUS build belongs to a different code identity, and that is
        // exactly the case `SecItemDelete` refuses with -25244 (see `remove`). A
        // stranded probe item would then make the `SecItemAdd` below collide with
        // -25299 forever, permanently disabling the feature on that host with no
        // in-app way back.
        _ = remove(account: probeAccount)
        var add = query(account: probeAccount)
        add[kSecValueData as String] = Data("probe".utf8)
        var status = SecItemAdd(add as CFDictionary, nil)
        // A DUPLICATE IS NOT A REFUSAL. The question this probe asks is "will the
        // keychain take an item from this build", and -25299 answers it with "there
        // is already one there" — which is a yes, arrived at the long way round.
        // The pre-clean above should have removed it, but it returns false whenever
        // the lookup itself failed (a locked keychain, a cancelled unlock dialog),
        // and reporting that as `.failed(-25299)` would grey the toggle out and
        // blame the keychain for holding what this build put there. Worse, -25299
        // is not a cacheable verdict, so the misreading would repeat on every probe.
        if status == errSecDuplicateItem { status = errSecSuccess }
        if status == errSecSuccess {
            _ = remove(account: probeAccount)
        }
        return status
    }

    /// Guards `cachedProbe`. A serial queue rather than a lock keeps this to the
    /// concurrency vocabulary the rest of the app already uses. Held only across
    /// the read/write of one optional — never across the probe itself.
    private static let probeFlag = DispatchQueue(label: "sh.dezhban.menu.probe-flag")
    private static var cachedProbe: OSStatus?

    /// Serialises the probe so overlapping callers cannot collide on the probe
    /// account. Separate from `probeFlag` precisely because this one can block.
    private static let probeQueue = DispatchQueue(label: "sh.dezhban.menu.capability-probe")

    /// Whether `probeStatus` has already settled on a permanent answer — asked
    /// WITHOUT establishing it. Reading `probeStatus` itself would trigger the
    /// very keychain write this exists to let a caller avoid.
    private static var probeHasRun: Bool { probeFlag.sync { cachedProbe != nil } }

    /// Whether this build can hold a token at all, and why not when it cannot.
    ///
    /// Deliberately a computed property over a cached probe rather than a cached
    /// verdict: only the *keychain* half is fixed for the process's lifetime.
    /// Biometry availability is not — a MacBook in clamshell mode reports no
    /// usable sensor, and so does a Touch ID lockout after repeated failures. A
    /// menubar app runs for weeks, so freezing the whole verdict would leave the
    /// toggle greyed out with "this Mac has no Touch ID" until the user quit and
    /// relaunched.
    ///
    /// Ordering matters: the `guard` short-circuits before `probeStatus`, so a Mac
    /// with no sensor never writes to the keychain at all.
    /// MAY BLOCK on its first evaluation, so never call it on the main thread —
    /// use `capabilityIfKnown` there and fall back to a background resolve.
    static var capability: TokenCapability {
        guard biometryAvailable else {
            // Status is irrelevant on a Mac with no sensor; classify() ignores it.
            return TokenCapability.classify(addStatus: errSecSuccess, biometryAvailable: false)
        }
        return TokenCapability.classify(addStatus: probeStatus, biometryAvailable: true)
    }

    /// The verdict when it can be given without touching the keychain, and nil
    /// when answering would mean running the probe.
    ///
    /// Exactly one of the two halves can block, and only once: `biometryAvailable`
    /// is a cheap local call, so a Mac with no usable sensor is answered
    /// immediately — the probe is not consulted at all, and must not be, since
    /// that is the promise `capability`'s guard makes. What is left is the single
    /// case where a caller would have to wait: biometry present, probe not yet
    /// run.
    ///
    /// This exists because `warmCapability()` cannot cover that case. The warm
    /// runs when the main window first appears and is gated on biometry too, so a
    /// Mac whose lid was shut then — or one in a Touch ID lockout — warms nothing.
    /// Open the lid and the sensor comes back, and the *next* reader is the one
    /// that pays for the probe. If that reader is a SwiftUI view initialiser, it
    /// pays on the main thread, behind a keychain-unlock dialog if the login
    /// keychain is locked. A pane asks this first and only resolves in the
    /// background when it is nil.
    static var capabilityIfKnown: TokenCapability? {
        guard biometryAvailable else {
            return TokenCapability.classify(addStatus: errSecSuccess, biometryAvailable: false)
        }
        guard probeHasRun else { return nil }
        return TokenCapability.classify(addStatus: probeStatus, biometryAvailable: true)
    }

    /// Resolves `capability` off the main thread and hands it back on the main
    /// queue. For the one case `capabilityIfKnown` returns nil for.
    static func resolveCapability(_ completion: @escaping (TokenCapability) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            let verdict = capability
            DispatchQueue.main.async { completion(verdict) }
        }
    }

    /// Runs the keychain probe off the main thread, so the first pane to ask for
    /// `capability` finds it already resolved.
    ///
    /// **Called when the main window first appears, deliberately not at launch.**
    /// The probe is a keychain WRITE, so warming it in
    /// `applicationDidFinishLaunching` made every session pay for a feature the
    /// user may never open — and on a Mac whose login keychain password has
    /// diverged from the account password, an unexplained unlock dialog at every
    /// login, from a menubar app. Warming it with the window means a
    /// menubar-only session never touches the keychain, while anyone who opens
    /// the window has the answer long before they can reach Settings or About.
    ///
    /// It is an optimisation and nothing more: both panes ask `capabilityIfKnown`
    /// and resolve through `resolveCapability` when it comes back nil, so they
    /// stay off the main thread whether or not this ever runs. Do not let a
    /// caller start depending on it having run.
    ///
    /// Goes through `capability`, NOT `probeStatus` directly: the biometry guard
    /// there is what keeps a Mac with no sensor from writing to the keychain at
    /// all, and warming the probe behind its back would break that promise on
    /// exactly the desktops (mini, Studio, iMac) that can never use the feature.
    static func warmCapability() {
        DispatchQueue.global(qos: .utility).async {
            // An enrolled host needs no verdict: `capability` answers whether
            // ENROLLING could succeed, and both panes skip it once a token is
            // stored (AboutView.describeSettingsAuthIfKnown, SettingsView's
            // refreshTokenCapability). Warming it anyway would spend a keychain
            // write — and, on a locked login keychain, a modal unlock dialog at
            // every login — for an answer nothing goes on to read.
            guard !isStored else { return }
            _ = capability
        }
    }

    /// Whether a token is stored, WITHOUT reading it — deliberately, so the UI can
    /// show enrollment state without triggering a biometric prompt every time a
    /// pane opens.
    static var isStored: Bool {
        var q = query()
        q[kSecReturnData as String] = false
        q[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUISkip
        let status = SecItemCopyMatching(q as CFDictionary, nil)
        // interactionNotAllowed means "it's there, but you'd have to authenticate"
        // — which is a yes for this question.
        return status == errSecSuccess || status == errSecInteractionNotAllowed
    }

    /// Reads the token, prompting for Touch ID first. Returns nil when the user
    /// cancels, when biometry fails, or when nothing is enrolled — all of which
    /// the caller treats the same way: fall back to the password path.
    ///
    /// The biometric check gates the read rather than being enforced by it; see
    /// the type comment. Failing closed here is therefore load-bearing: **every**
    /// path that does not reach a confirmed success must return nil, so an error
    /// can never be mistaken for permission.
    ///
    /// MUST NOT run on the main thread: it blocks until the user answers the
    /// biometric prompt.
    static func load() -> String? {
        assert(!Thread.isMainThread, "ControlToken.load blocks on a biometric prompt — dispatch to a background queue")

        // Nothing to authenticate for if there is nothing to read. Checking first
        // means a host with no enrollment never shows a prompt that could only
        // end in "…and now nothing happens".
        guard isStored else { return nil }

        let ctx = LAContext()
        // Deliberately `.deviceOwnerAuthenticationWithBiometrics`, not
        // `.deviceOwnerAuthentication`: the latter falls back to the login
        // password, and a password that unlocks a settings change is exactly what
        // the token exists to avoid. The caller's fallback is the sudo path,
        // which is at least honest about being a password.
        var policyError: NSError?
        guard ctx.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &policyError) else {
            return nil
        }

        // evaluatePolicy is callback-based; this method's contract is that it
        // blocks off the main thread, so wait on it rather than restructuring
        // every caller around an async read.
        let done = DispatchSemaphore(value: 0)
        var authorised = false
        ctx.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, localizedReason: reason) { ok, _ in
            authorised = ok
            done.signal()
        }
        // `withExtendedLifetime` is load-bearing, not decoration: `ctx` is not
        // touched again after the call above, so an optimised build is free to
        // release it immediately — and deallocating an LAContext CANCELS the
        // evaluation in flight. That would dismiss the prompt the user is looking
        // at and, at worst, leave this thread parked on `done.wait()` forever with
        // the Settings toggle stuck busy.
        withExtendedLifetime(ctx) { done.wait() }
        guard authorised else { return nil }

        var q = query()
        q[kSecReturnData as String] = true
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data,
              let token = String(data: data, encoding: .utf8)?
                  .trimmingCharacters(in: .whitespacesAndNewlines),
              !token.isEmpty
        else { return nil }
        return token
    }

    /// Stores `token`, replacing any previous one. Returns nil on success, or a
    /// sentence naming what went wrong.
    ///
    /// The item lands in the legacy (file) login keychain, which does not sync to
    /// iCloud Keychain at all — which is what this design needs, since the daemon
    /// that checks the token runs on THIS host and syncing it would hand other
    /// Macs a credential they have no matching enrollment for.
    ///
    /// **No `kSecAttrAccessible` here, deliberately.** It applies only to the data
    /// protection keychain, so on this store it does nothing: measured, an add
    /// with it and an add without it both produce an item that vends a
    /// `SecKeychainItem` ref and is invisible to a `kSecUseDataProtectionKeychain`
    /// query — identical placement. Carrying it anyway would name a protection
    /// that is not in force and, worse, invite someone to reconcile the mismatch
    /// by pinning `kSecUseDataProtectionKeychain` — which would send this add to a
    /// store `remove()`'s `SecKeychainItemDelete` cannot reach, breaking
    /// re-enrollment after an app upgrade. The type comment's rule is that every
    /// query addresses one keychain and carries nothing that could point elsewhere.
    @discardableResult
    static func store(_ token: String) -> String? {
        guard let data = token.data(using: .utf8) else { return "token is not valid UTF-8" }

        // Replace, never accumulate a second item under the same account. The
        // result matters: a failed delete means the add below collides, and
        // reporting "duplicate item" would tell the user nothing they can act on.
        guard remove() else {
            // `remove()` reports one bit, and it covers two different failures:
            // an item this build cannot delete, and a LOOKUP that never got far
            // enough to see one (a locked login keychain, or an unlock dialog the
            // user dismissed — that comes back as -128, not "not found"). Only the
            // first is fixed by `security delete-generic-password`; sending the
            // second there hands the user a command that answers "The specified
            // item could not be found in the keychain."
            //
            // `isStored` tells them apart without a second failure mode of its own:
            // it asks with `kSecUseAuthenticationUISkip`, so a present-but-locked
            // item answers `errSecInteractionNotAllowed` (a yes) while a keychain
            // holding nothing answers `errSecItemNotFound` (a no).
            guard isStored else {
                return "the keychain could not be read, so nothing was stored — it may be "
                    + "locked, or the unlock prompt was dismissed. Unlock your login keychain "
                    + "and try again."
            }
            return "the keychain already holds a dezhban secret that this copy of the app "
                + "cannot replace. Remove it once with: \(manualRemovalCommand)"
        }

        var add = query()
        add[kSecValueData as String] = data
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            // `toggleExplanation`, NOT `enrollRefusal`: the latter promises
            // "Nothing was changed", and by the time `store` runs the caller has
            // already had the daemon mint a token and write its hash. Printing
            // that promise here put a flat contradiction in the transcript,
            // directly above "Rolling the enrollment back also failed".
            // The status is spelled once, not twice: `.failed`'s explanation
            // already carries the number, and printing it on both halves read as
            // two separate failures.
            let verdict = TokenCapability.classify(addStatus: status, biometryAvailable: true)
            if case .failed = verdict {
                return "keychain refused to store the token — \(verdict.toggleExplanation)"
            }
            return "keychain refused to store the token (OSStatus \(status)) — \(verdict.toggleExplanation)"
        }
        // The secret under this account is now one the daemon has just hashed, so
        // whatever went before is no longer orphaned.
        clearOrphaned()
        return nil
    }

    /// Set when the stored secret is known not to match the daemon's hash —
    /// because the hash was removed and the app's copy could not be, or because
    /// enrollment replaced the hash and then failed to store the token that goes
    /// with it. Either way the item authorises nothing.
    ///
    /// Without this, that state is a dead end rather than a degradation.
    /// `writeConfig` offers the token whenever `isStored` is true, the daemon
    /// answers an orphaned token with a refusal, and a refusal is deliberately
    /// never retried through the privileged path — so EVERY settings save fails
    /// until the user finds and runs `manualRemovalCommand`. Skipping the token
    /// path restores the password fallback, which is where turning the toggle off
    /// was heading anyway.
    ///
    /// It records something the app observed, not a policy: nothing here decides
    /// whether a write is allowed, so the rule that a daemon refusal is never
    /// routed around is untouched. Session-only and never persisted — a restart
    /// re-reads the keychain and asks the daemon afresh, which is the right
    /// answer if the user cleared the item in between.
    private static let orphanFlag = DispatchQueue(label: "sh.dezhban.menu.orphan-flag")
    private static var orphaned = false

    /// Whether the stored secret is known to be orphaned. Checked before the
    /// token path is offered.
    static var isKnownOrphaned: Bool { orphanFlag.sync { orphaned } }

    /// Called from both paths that can leave a keychain item the daemon will not
    /// accept: a `forgetToken` whose keychain removal failed, and an `enrollToken`
    /// whose `store` failed with an earlier build's item still in place —
    /// whatever its rollback did, since `token enroll` has by then replaced the
    /// daemon's hash with one for a token that was never stored.
    static func markOrphaned() { orphanFlag.sync { orphaned = true } }

    /// Cleared by a successful `store`, and only there. Everywhere else the flag
    /// is checked alongside `isStored`, so once the item is gone a stale `true`
    /// changes nothing — but a re-enrollment puts a NEW secret under the same
    /// account, and that one the daemon does know. Leaving the flag set would
    /// suppress the token path for a token that works.
    private static func clearOrphaned() { orphanFlag.sync { orphaned = false } }

    /// Forgets the app's copy. The daemon's hash is separate — removing only this
    /// leaves an enrollment no client can satisfy, so the UI pairs it with
    /// `dezhban token forget`.
    ///
    /// **Uses the deprecated SecKeychain API deliberately, and must keep doing
    /// so.** A login-keychain item's ACL is bound to the creating binary's code
    /// identity, and for an ad-hoc signature that identity is the cdhash — which
    /// changes on every single build. So after any app upgrade, the new build
    /// asking `SecItemDelete` to remove the token it "owns" is refused with
    /// `-25244` (`errSecInvalidOwnerEdit`), and the `SecItemAdd` that follows
    /// then collides with `-25299` (`errSecDuplicateItem`). That made
    /// re-enrolling — the documented recovery, and the revocation path for a
    /// leaked token — impossible after an upgrade.
    ///
    /// `SecKeychainItemDelete` is not subject to that check: it is the call
    /// behind `security delete-generic-password`, and it succeeds across code
    /// identities without a prompt. Measured, not assumed — `SecItemUpdate` also
    /// succeeds cross-identity but does NOT re-own the ACL, so it writes a token
    /// the new build can never read back, which is worse than failing. See
    /// docs/adr/0012-app-checked-biometrics-on-unsigned-builds.md, which records
    /// the measurements and names "modernising this back to `SecItemDelete`" as
    /// the regression to watch for.
    /// Removes every keychain item this app owns: the token and the capability
    /// probe. Reports whether anything was actually there.
    ///
    /// Lives here rather than in `Purge` because the account names are private
    /// to this type, and deliberately so — the probe account exists precisely so
    /// a probe can never collide with a real token, and a second place naming it
    /// would be a second place to get that wrong. Uses `remove`, never
    /// `SecItemDelete`, for the ACL reason documented on it.
    @discardableResult
    static func purge() -> Bool {
        let hadToken = remove(account: account)
        let hadProbe = remove(account: probeAccount)
        clearOrphaned()
        return hadToken || hadProbe
    }

    @discardableResult
    static func remove(account: String = ControlToken.account) -> Bool {
        // Look the item up with the modern API — `kSecReturnRef` hands back a
        // reference without decrypting the secret, so this needs no ACL access —
        // and delete it with the old one, which is the only call that is not
        // subject to the owner check. One deprecated call, not two.
        //
        // Loop rather than delete once: `SecItemDelete` (which this replaces)
        // removed EVERY match, and more than one keychain in the search list can
        // hold a (service, account) pair. Deleting only the first would leave a
        // second copy that `isStored` still finds and `store`'s `SecItemAdd`
        // still collides with.
        var q = query(account: account)
        q[kSecReturnRef as String] = true
        // A dezhban host should never accumulate copies; the bound just keeps a
        // pathological keychain from spinning this forever.
        for _ in 0..<16 {
            var ref: CFTypeRef?
            let found = SecItemCopyMatching(q as CFDictionary, &ref)
            if found == errSecItemNotFound { return true } // nothing left is success
            guard found == errSecSuccess, let ref else { return false }
            // Check the CFTypeID before casting. A cast to a CoreFoundation type
            // is UNCHECKED in Swift — `as!` here does not trap on a wrong type,
            // it hands whatever came back straight to `SecKeychainItemDelete` as
            // if it were a keychain item. Only the legacy (file) keychain returns
            // a SecKeychainItem, and every query here addresses it, but that
            // assumption should fail as a `false` the callers already have words
            // for, not as undefined behaviour inside a C API.
            guard CFGetTypeID(ref) == SecKeychainItemGetTypeID() else { return false }
            // swiftlint:disable:next force_cast
            guard SecKeychainItemDelete(ref as! SecKeychainItem) == errSecSuccess else { return false }
        }
        // Falling out of the loop is not itself a failure — sixteen successful
        // deletions could have cleared everything. Ask, rather than assume:
        // returning false here with nothing left would tell `store` the item is
        // unreplaceable and send the user to `security delete-generic-password`
        // for an item that is already gone.
        var last: CFTypeRef?
        return SecItemCopyMatching(q as CFDictionary, &last) == errSecItemNotFound
    }

    /// What to tell the user to run when this app cannot clear the item itself.
    /// Built from the same constants the queries use, so the instruction cannot
    /// drift away from the item it is meant to remove.
    static var manualRemovalCommand: String {
        "security delete-generic-password -s \(service) -a \(account)"
    }
}
