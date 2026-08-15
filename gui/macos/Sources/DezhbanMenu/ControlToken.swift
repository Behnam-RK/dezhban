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
/// originally specified and ADR-0011 replaced. The difference is real and worth
/// stating plainly: a patched copy of this app could skip the check, whereas
/// nothing could fake its way past the keychain-enforced version. What ADR-0011
/// weighs is that the strong version needs an entitlement an ad-hoc signature
/// cannot carry (ADR-0010), so on every build this project ships it did not
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
/// docs/adr/0011-app-checked-biometrics-on-unsigned-builds.md.
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
    /// `static let` gives lazy, once-only, thread-safe evaluation, and — unlike
    /// `biometryAvailable` — this genuinely cannot change while the app runs: the
    /// signature of the running binary is fixed for its lifetime.
    private static let probeStatus: OSStatus = {
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
        add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecSuccess {
            _ = remove(account: probeAccount)
        }
        return status
    }()

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
    static var capability: TokenCapability {
        guard biometryAvailable else {
            // Status is irrelevant on a Mac with no sensor; classify() ignores it.
            return TokenCapability.classify(addStatus: errSecSuccess, biometryAvailable: false)
        }
        return TokenCapability.classify(addStatus: probeStatus, biometryAvailable: true)
    }

    /// Runs the keychain probe off the main thread, so the first pane to ask for
    /// `capability` finds it already resolved. Worth doing at launch: the probe is
    /// a keychain write, and a locked login keychain answers one with a system
    /// dialog — on the main thread that is a frozen UI, not merely a slow one.
    /// Same reasoning as `DezhbanCLI.warmConfigPath`.
    static func warmCapability() {
        DispatchQueue.global(qos: .utility).async { _ = probeStatus }
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
        done.wait()
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
    /// `...ThisDeviceOnly` keeps it off iCloud Keychain. The daemon that checks
    /// this token runs on THIS host; syncing it would hand other Macs a
    /// credential they have no matching enrollment for.
    @discardableResult
    static func store(_ token: String) -> String? {
        guard let data = token.data(using: .utf8) else { return "token is not valid UTF-8" }

        // Replace, never accumulate a second item under the same account. The
        // result matters: a failed delete means the add below collides, and
        // reporting "duplicate item" would tell the user nothing they can act on.
        guard remove() else {
            return "the keychain already holds a dezhban secret that this copy of the app "
                + "cannot replace. Remove it once with: \(manualRemovalCommand)"
        }

        var add = query()
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            // `toggleExplanation`, NOT `enrollRefusal`: the latter promises
            // "Nothing was changed", and by the time `store` runs the caller has
            // already had the daemon mint a token and write its hash. Printing
            // that promise here put a flat contradiction in the transcript,
            // directly above "Rolling the enrollment back also failed".
            let verdict = TokenCapability.classify(addStatus: status, biometryAvailable: true)
            return "keychain refused to store the token (OSStatus \(status)) — \(verdict.toggleExplanation)"
        }
        return nil
    }

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
    /// the new build can never read back, which is worse than failing.
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
        return false
    }

    /// What to tell the user to run when this app cannot clear the item itself.
    /// Built from the same constants the queries use, so the instruction cannot
    /// drift away from the item it is meant to remove.
    static var manualRemovalCommand: String {
        "security delete-generic-password -s \(service) -a \(account)"
    }
}
