import Testing
@testable import DezhbanCore

struct LaunchVisibilityTests {
    /// The default must reproduce what the app did before the setting existed:
    /// open on a deliberate launch, stay hidden when macOS started us at login.
    /// An upgrade is a no-op for anyone who never touches it.
    @Test func bootOnlyIsTheOldUnconditionalBehaviour() {
        #expect(LaunchVisibility.bootOnly.opensWindow(deliberateLaunch: true))
        #expect(!LaunchVisibility.bootOnly.opensWindow(deliberateLaunch: false))
    }

    @Test func neverAndAlwaysIgnoreHowTheLaunchHappened() {
        for deliberate in [true, false] {
            #expect(LaunchVisibility.never.opensWindow(deliberateLaunch: deliberate))
            #expect(!LaunchVisibility.always.opensWindow(deliberateLaunch: deliberate))
        }
    }

    /// The raw values are persisted in UserDefaults, so renaming one silently
    /// resets every existing user's choice to the default.
    @Test func rawValuesArePersistedIdentifiers() {
        #expect(LaunchVisibility.never.rawValue == "never")
        #expect(LaunchVisibility.always.rawValue == "always")
        #expect(LaunchVisibility.bootOnly.rawValue == "bootOnly")
        #expect(LaunchVisibility.allCases.count == 3)
    }

    @Test func everyCaseIsLabelledAndExplained() {
        for choice in LaunchVisibility.allCases {
            #expect(!choice.label.isEmpty)
            #expect(!choice.detail.isEmpty)
        }
    }
}
