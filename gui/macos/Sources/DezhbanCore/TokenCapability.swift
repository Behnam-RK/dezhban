import Foundation

/// Whether this build can actually hold a control token, and what to tell the
/// user when it cannot.
///
/// This is deliberately NOT the same question as "does this Mac have Touch ID".
/// The app used to ask only that one, and it is the weaker of the two: a Mac can
/// have a perfectly good sensor while the keychain still refuses the item.
/// Asking the weak question let the app take a root password, mint a token, write
/// the daemon's hash, and only THEN discover the keychain would not have it —
/// leaving a host whose daemon holds a hash nobody can present. See
/// docs/adr/0010-biometric-enrollment-requires-a-signed-build.md.
///
/// `notEntitled` is no longer the expected outcome — the token is stored as an
/// ordinary keychain item since
/// docs/adr/0011-app-checked-biometrics-on-unsigned-builds.md — but it is kept,
/// with its own words, because that is the status a future OS change or a
/// different storage scheme would report, and "we stopped being able to store
/// anything" deserves better than a bare number.
///
/// It lives in DezhbanCore, away from the Security framework calls that produce
/// the status code, so the mapping from "what the keychain said" to "what the
/// user is told" can be unit-tested. `DezhbanMenu.ControlToken` performs the
/// probe and hands the raw status here.
public enum TokenCapability: Equatable {
    /// The keychain accepted a biometry-gated item. Enrollment can proceed.
    case available
    /// No usable biometric sensor. Nothing is wrong with the build.
    case noBiometry
    /// `errSecMissingEntitlement`. The Mac is fine; this *build* is not allowed
    /// to store the item. No longer expected — see the type comment.
    case notEntitled
    /// The keychain refused for some reason we do not have specific words for.
    /// Carries the status so a bug report can name it.
    case failed(OSStatus)

    /// `errSecMissingEntitlement`, spelled out rather than imported: keeping the
    /// Security framework out of this target is what makes it testable.
    public static let missingEntitlement: OSStatus = -34018

    /// Maps a `SecItemAdd` probe result to a verdict.
    ///
    /// `biometryAvailable` is checked FIRST and independently: on a Mac with no
    /// sensor the probe's status is beside the point, and reporting a signing
    /// problem to someone whose hardware simply lacks Touch ID would send them
    /// off to fix the wrong thing.
    public static func classify(addStatus: OSStatus, biometryAvailable: Bool) -> TokenCapability {
        guard biometryAvailable else { return .noBiometry }
        switch addStatus {
        case 0: return .available
        case missingEntitlement: return .notEntitled
        default: return .failed(addStatus)
        }
    }

    public var isAvailable: Bool { self == .available }

    /// What a settings change will cost, for the About pane's "Settings changes"
    /// row. Must never read as an invitation when enrollment cannot succeed —
    /// the old copy said "turn on Touch ID in Settings" in exactly the case
    /// where doing so burned a password prompt and stranded an enrollment.
    public var settingsAuthSummary: String {
        switch self {
        case .available:
            return "Password — turn on Touch ID in Settings"
        case .noBiometry:
            return "Password — this Mac has no Touch ID"
        case .notEntitled:
            return "Password — this build isn't allowed to use the keychain"
        case let .failed(status):
            return "Password — the keychain refused Touch ID (OSStatus \(status))"
        }
    }

    /// The line under the Settings toggle. Empty when enrollment is available,
    /// because the toggle's own description already covers that case.
    public var toggleExplanation: String {
        switch self {
        case .available:
            return ""
        case .noBiometry:
            return "This Mac has no Touch ID, so settings changes ask for your password."
        case .notEntitled:
            return "macOS won't let this copy of Dezhban store the secret in your "
                + "keychain, so settings changes ask for your password instead."
        case let .failed(status):
            return "The keychain refused to hold the secret (OSStatus \(status)), so "
                + "settings changes ask for your password."
        }
    }

    /// Shown when the user asks to enrol and we decline BEFORE spending
    /// anything. The "nothing was changed" half is the point: it distinguishes
    /// this from the old failure, which left a token enrolled on the daemon.
    public var enrollRefusal: String {
        switch self {
        case .available:
            return ""
        case .noBiometry:
            return "This Mac has no Touch ID, so settings changes keep using your password."
        case .notEntitled:
            return "macOS won't let this copy of Dezhban store the secret in your "
                + "keychain. Nothing was changed — settings changes keep using your password."
        case let .failed(status):
            return "The keychain won't hold the secret (OSStatus \(status)). Nothing was "
                + "changed — settings changes keep using your password."
        }
    }
}
