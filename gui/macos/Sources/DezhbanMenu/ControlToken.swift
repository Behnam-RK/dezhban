import DezhbanCore
import Foundation
import LocalAuthentication
import Security

/// The app's copy of the daemon's control token, held in the login keychain
/// behind biometry.
///
/// This is what makes changing a setting cost a Touch ID tap instead of a
/// password. The daemon stores only the token's HASH, root-owned; the app holds
/// the token itself, and the keychain will not hand it over without a successful
/// biometric check — so *reading* the token IS the authentication. There is no
/// separate "are you allowed?" question for the app to answer, and therefore no
/// answer for a tampered app to fake.
///
/// It raises the bar rather than lowering it. Without a token, a config change
/// over the control socket would be gated only by the socket's file permissions
/// (admin group), which is a fine bar for ops that merely move between
/// fail-closed postures and too weak for one that writes settings outliving the
/// daemon. See docs/adr/0003-biometric-token-over-existing-daemon.md.
/// Every query below passes `kSecUseDataProtectionKeychain: true`, and they must
/// keep agreeing. `store` uses `kSecAttrAccessControl`, which puts the item in
/// the data protection keychain whether or not the flag is passed; the other
/// three, without it, would address the *legacy* file keychain instead. That
/// split is currently invisible only because `store` never succeeds on an ad-hoc
/// build — the moment it does, `isStored` would answer for the wrong keychain and
/// report `false` right after a successful save, and `store`'s own `remove()`
/// would fail to clear the previous item, so re-enrolling would collide.
enum ControlToken {
    private static let service = "sh.dezhban.menu"
    private static let account = "control-token"

    /// Why the biometric prompt is appearing. Shown by the system HUD, so it has
    /// to name the actual consequence rather than the mechanism.
    private static let reason = "change dezhban settings"

    /// Whether this Mac has usable biometry. No biometry means the item could
    /// only be guarded by a password prompt, which is exactly what the token
    /// exists to avoid — such machines keep using the sudo path, which is no
    /// worse than what they had.
    ///
    /// **Private on purpose.** This used to be the gate the UI and the enrollment
    /// flow asked, and it is the wrong question: it says the *Mac* is capable
    /// while saying nothing about whether this *build* can create the item. Asking
    /// it is what let enrollment spend a root password before failing. Callers
    /// want `capability`, which folds this in.
    private static var biometryAvailable: Bool {
        var err: NSError?
        return LAContext().canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err)
    }

    /// The biometric policy every stored token carries. Shared with the
    /// capability probe so the probe tests the item we would actually create —
    /// a probe with weaker flags could pass while the real store still failed.
    ///
    /// `.biometryCurrentSet` binds the item to the fingerprints enrolled RIGHT
    /// NOW: adding or removing one invalidates it, so someone who enrols their own
    /// finger cannot thereby unlock a token the owner stored. The cost is that the
    /// user must enrol again after changing their fingerprints, which is why
    /// `dezhban token enroll` is repeatable and replaces rather than refuses.
    ///
    /// `...ThisDeviceOnly` keeps it off iCloud Keychain. The daemon that checks
    /// this token runs on THIS host; syncing it would hand other Macs a
    /// credential they have no matching enrollment for.
    private static func accessControl() -> (SecAccessControl?, String?) {
        var acError: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(
            nil,
            kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
            .biometryCurrentSet,
            &acError
        ) else {
            let err = acError?.takeRetainedValue()
            return (nil, "could not create a biometric policy: \(err.map { String(describing: $0) } ?? "unknown")")
        }
        return (access, nil)
    }

    /// Whether this build can hold a token at all, established by trying — once,
    /// at first use — rather than assumed.
    ///
    /// A biometry-gated item lives only in the data protection keychain, and an
    /// ad-hoc signature cannot carry the entitlement that keychain requires, so
    /// on every build this project currently ships `SecItemAdd` returns -34018.
    /// The app must know that BEFORE it spends a root password minting a token
    /// the keychain will then refuse; see `ConfigApply.enrollToken`.
    ///
    /// Adding a biometry-gated item does not prompt (only reading one does), so
    /// the probe is silent. `static let` gives us lazy, once-only, thread-safe
    /// evaluation — and the answer cannot change while the app runs, since it is
    /// a property of the signature the process was launched with.
    static let capability: TokenCapability = {
        guard biometryAvailable else {
            // Status is irrelevant on a Mac with no sensor; classify() ignores it.
            return TokenCapability.classify(addStatus: errSecSuccess, biometryAvailable: false)
        }
        let (access, _) = accessControl()
        guard let access else {
            return .failed(errSecParam)
        }
        // A distinct account, so a probe can never collide with — or worse,
        // delete — a real token.
        let probeAccount = account + ".capability-probe"
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: probeAccount,
            kSecUseDataProtectionKeychain as String: true,
        ]
        SecItemDelete(query as CFDictionary) // clear any leftover from a crash mid-probe
        var add = query
        add[kSecValueData as String] = Data("probe".utf8)
        add[kSecAttrAccessControl as String] = access
        let status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecSuccess {
            SecItemDelete(query as CFDictionary)
        }
        return TokenCapability.classify(addStatus: status, biometryAvailable: true)
    }()

    /// Whether a token is stored, WITHOUT reading it — deliberately, so the UI can
    /// show enrollment state without triggering a biometric prompt every time a
    /// pane opens. `kSecReturnData: false` plus `kSecUseAuthenticationUI: skip`
    /// asks only whether the item exists.
    static var isStored: Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: false,
            kSecUseAuthenticationUI as String: kSecUseAuthenticationUISkip,
            kSecUseDataProtectionKeychain as String: true,
        ]
        let status = SecItemCopyMatching(query as CFDictionary, nil)
        // interactionNotAllowed means "it's there, but you'd have to authenticate"
        // — which is a yes for this question.
        return status == errSecSuccess || status == errSecInteractionNotAllowed
    }

    /// Reads the token, prompting for Touch ID. Returns nil when the user
    /// cancels, when biometry fails, or when nothing is enrolled — all of which
    /// the caller treats the same way: fall back to the password path.
    ///
    /// MUST NOT run on the main thread: it blocks until the user answers the
    /// biometric prompt.
    static func load() -> String? {
        assert(!Thread.isMainThread, "ControlToken.load blocks on a biometric prompt — dispatch to a background queue")

        let ctx = LAContext()
        ctx.localizedReason = reason
        // The token is presented to the daemon once per write. Reusing an
        // authentication for a few seconds keeps a multi-field save from
        // prompting twice, without leaving a long-lived grant behind.
        ctx.touchIDAuthenticationAllowableReuseDuration = 10

        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecUseAuthenticationContext as String: ctx,
            kSecUseDataProtectionKeychain as String: true,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
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
    /// The biometric policy comes from `accessControl()`, which is also what
    /// `capability` probes — see there for what the flags buy.
    @discardableResult
    static func store(_ token: String) -> String? {
        guard let data = token.data(using: .utf8) else { return "token is not valid UTF-8" }

        let (access, policyError) = accessControl()
        guard let access else { return policyError }

        remove() // replace, never accumulate a second item under the same account

        let add: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: data,
            kSecAttrAccessControl as String: access,
            kSecUseDataProtectionKeychain as String: true,
        ]
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            // Name the cause, not just the number. -34018 in particular is not a
            // transient keychain hiccup the user could retry past — it is this
            // build being unable to reach the data protection keychain at all.
            let verdict = TokenCapability.classify(addStatus: status, biometryAvailable: true)
            return "keychain refused to store the token (OSStatus \(status)) — \(verdict.enrollRefusal)"
        }
        return nil
    }

    /// Forgets the app's copy. The daemon's hash is separate — removing only this
    /// leaves an enrollment no client can satisfy, so the UI pairs it with
    /// `dezhban token forget`.
    static func remove() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecUseDataProtectionKeychain as String: true,
        ]
        SecItemDelete(query as CFDictionary)
    }
}
