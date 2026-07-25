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
enum ControlToken {
    private static let service = "sh.dezhban.menu"
    private static let account = "control-token"

    /// Why the biometric prompt is appearing. Shown by the system HUD, so it has
    /// to name the actual consequence rather than the mechanism.
    private static let reason = "change dezhban settings"

    /// Whether this Mac can hold the token at all. No biometry means the item
    /// could only be protected by a password prompt, which is exactly what the
    /// token exists to avoid — such machines keep using the sudo path, which is
    /// no worse than what they had.
    static var biometryAvailable: Bool {
        var err: NSError?
        return LAContext().canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err)
    }

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

    /// Stores `token`, replacing any previous one.
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
    @discardableResult
    static func store(_ token: String) -> String? {
        guard let data = token.data(using: .utf8) else { return "token is not valid UTF-8" }

        var acError: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(
            nil,
            kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
            .biometryCurrentSet,
            &acError
        ) else {
            let err = acError?.takeRetainedValue()
            return "could not create a biometric protection policy: \(err.map { String(describing: $0) } ?? "unknown")"
        }

        remove() // replace, never accumulate a second item under the same account

        let add: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: data,
            kSecAttrAccessControl as String: access,
        ]
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            return "keychain refused to store the token (OSStatus \(status))"
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
        ]
        SecItemDelete(query as CFDictionary)
    }
}
