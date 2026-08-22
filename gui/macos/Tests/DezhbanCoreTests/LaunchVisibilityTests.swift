import Testing
@testable import DezhbanCore

struct LaunchVisibilityTests {
    /// The default must reproduce what the app did before the setting existed:
    /// open when the user starts it, stay hidden when macOS started us at login.
    /// An upgrade is a no-op for anyone who never touches it.
    @Test func bootOnlyIsTheOldUnconditionalBehaviour() {
        #expect(LaunchVisibility.bootOnly.opensWindow(backgroundLaunch: false))
        #expect(!LaunchVisibility.bootOnly.opensWindow(backgroundLaunch: true))
    }

    @Test func neverAndAlwaysIgnoreHowTheLaunchHappened() {
        for background in [true, false] {
            #expect(LaunchVisibility.never.opensWindow(backgroundLaunch: background))
            #expect(!LaunchVisibility.always.opensWindow(backgroundLaunch: background))
        }
    }

    /// The marker is the whole mechanism: the login LaunchAgent passes it and
    /// nothing else does. A rename here without a matching edit to
    /// LoginAgent.plist silently restores the bug this replaced.
    @Test func backgroundLaunchIsReadFromTheArgumentTheAgentPasses() {
        #expect(LaunchVisibility.backgroundArgument == "--background")
        #expect(LaunchVisibility.isBackgroundLaunch(
            arguments: ["/Applications/Dezhban.app/Contents/MacOS/DezhbanMenu", "--background"]))
        #expect(!LaunchVisibility.isBackgroundLaunch(
            arguments: ["/Applications/Dezhban.app/Contents/MacOS/DezhbanMenu"]))
    }

    /// An unmarked launch must read as a user launch. The failure mode of
    /// guessing wrong in this direction is a window nobody asked for; guessing
    /// wrong the other way hides the window from someone who did.
    @Test func anEmptyArgumentListIsAUserLaunch() {
        #expect(!LaunchVisibility.isBackgroundLaunch(arguments: []))
        #expect(LaunchVisibility.bootOnly.opensWindow(
            backgroundLaunch: LaunchVisibility.isBackgroundLaunch(arguments: [])))
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
