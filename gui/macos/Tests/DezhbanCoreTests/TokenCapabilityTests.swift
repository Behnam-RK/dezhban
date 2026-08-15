import Foundation
import Testing
@testable import DezhbanCore

struct TokenCapabilityTests {
    /// No longer the expected outcome — the token is a plain keychain item since
    /// ADR-0012 — but -34018 must keep its own words if it ever returns, rather
    /// than degrading into a bare number the user might retry past.
    @Test func missingEntitlementIsItsOwnVerdict() {
        let v = TokenCapability.classify(addStatus: -34018, biometryAvailable: true)
        #expect(v == .notEntitled)
        #expect(!v.isAvailable)
    }

    @Test func successIsAvailable() {
        let v = TokenCapability.classify(addStatus: 0, biometryAvailable: true)
        #expect(v == .available)
        #expect(v.isAvailable)
    }

    /// No sensor wins over any status code — including a successful one. Telling
    /// someone with no Touch ID that their *build* is wrong sends them off to fix
    /// something that was never the problem.
    @Test func noBiometryWinsOverStatus() {
        #expect(TokenCapability.classify(addStatus: 0, biometryAvailable: false) == .noBiometry)
        #expect(TokenCapability.classify(addStatus: -34018, biometryAvailable: false) == .noBiometry)
    }

    /// An unrecognised refusal must still be reportable — carrying the number, so
    /// a bug report can name it instead of saying "it didn't work".
    @Test func unknownStatusKeepsItsNumber() {
        let v = TokenCapability.classify(addStatus: -25300, biometryAvailable: true)
        #expect(v == .failed(-25300))
        #expect(v.enrollRefusal.contains("-25300"))
        #expect(v.settingsAuthSummary.contains("-25300"))
    }

    /// Every unavailable verdict must say something, or the Settings pane shows a
    /// disabled toggle with no reason beside it — which reads as a bug.
    @Test(arguments: [TokenCapability.noBiometry, .notEntitled, .failed(-1)])
    func everyUnavailableVerdictExplainsItself(_ v: TokenCapability) {
        #expect(!v.toggleExplanation.isEmpty)
        #expect(!v.enrollRefusal.isEmpty)
        #expect(v.settingsAuthSummary.hasPrefix("Password"),
                "the About pane must name what the user will actually be asked for")
    }

    /// The available case has nothing to explain — the toggle's own help text
    /// covers it — and must not leave stray copy under an enabled control.
    @Test func availableVerdictHasNoExplanation() {
        #expect(TokenCapability.available.toggleExplanation.isEmpty)
        #expect(TokenCapability.available.enrollRefusal.isEmpty)
    }

    /// The refusal shown when enrollment is declined BEFORE anything is spent has
    /// to promise that nothing happened. That promise is the whole difference
    /// from the failure it replaced, which left a token enrolled on the daemon
    /// and a host that needed `sudo dezhban token forget` to recover.
    @Test(arguments: [TokenCapability.notEntitled, .failed(-1)])
    func preflightRefusalsPromiseNothingChanged(_ v: TokenCapability) {
        #expect(v.enrollRefusal.contains("Nothing was changed"))
    }

    /// The About pane never invites a retry that cannot work. "turn on Touch ID
    /// in Settings" is only true when the toggle would actually succeed.
    @Test func onlyTheAvailableVerdictPointsAtTheToggle() {
        #expect(TokenCapability.available.settingsAuthSummary.contains("turn on Touch ID"))
        for v in [TokenCapability.noBiometry, .notEntitled, .failed(-1)] {
            #expect(!v.settingsAuthSummary.contains("turn on Touch ID"),
                    "\(v) must not send the user to a toggle that will fail")
        }
    }
}
