import Foundation
import Testing
@testable import DezhbanCore

struct StatusInfoTests {
    @Test func decodesWithPresetFields() throws {
        let json = """
        {"service": "installed, running", "controlReachable": true, "pauseEnabled": true,
         "preset": "balanced", "presetExact": true}
        """.data(using: .utf8)!
        let info = try #require(StatusInfo.decode(json))
        #expect(info.serviceInstalled)
        #expect(info.preset == "balanced")
        #expect(info.presetLabel == "Balanced")
    }

    @Test func decodesWithoutPresetFields() throws {
        // A CLI older than the preset fields: nothing shown, nothing guessed.
        let json = """
        {"service": "not installed", "controlReachable": false, "pauseEnabled": false}
        """.data(using: .utf8)!
        let info = try #require(StatusInfo.decode(json))
        #expect(!info.serviceInstalled)
        #expect(info.preset == nil)
        #expect(info.presetLabel == nil)
    }

    @Test func inexactPresetShowsCustomWithClosest() {
        let info = StatusInfo(service: "installed", controlReachable: true, pauseEnabled: true,
                              preset: "focused", presetExact: false)
        #expect(info.presetLabel == "Custom (closest: Focused)")
    }

    @Test func emptyPresetHidesTheRow() {
        let info = StatusInfo(service: "installed", controlReachable: true, pauseEnabled: true,
                              preset: "", presetExact: false)
        #expect(info.presetLabel == nil)
    }

    // The prefix rule was moved here from an untested nested struct; pin it.
    @Test func serviceInstalledIsThePrefixRule() {
        #expect(StatusInfo(service: "installed, stopped", controlReachable: false, pauseEnabled: false).serviceInstalled)
        #expect(!StatusInfo(service: "not installed", controlReachable: false, pauseEnabled: false).serviceInstalled)
    }
}
