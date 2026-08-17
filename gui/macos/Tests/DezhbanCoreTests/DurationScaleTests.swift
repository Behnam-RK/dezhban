import Foundation
import Testing
@testable import DezhbanCore

struct DurationScaleTests {
    @Test func unusableDefaultFailsInit() {
        #expect(DurationScale(defaultValue: "", cap: nil, disablable: false) == nil)
        #expect(DurationScale(defaultValue: "0s", cap: nil, disablable: true) == nil)
        #expect(DurationScale(defaultValue: "soon", cap: "3m", disablable: false) == nil)
    }

    @Test func capBoundsTheTop() throws {
        let s = try #require(DurationScale(defaultValue: "5s", cap: "3m", disablable: true))
        #expect(s.maxSeconds == 180)
        // The cap itself is reachable: the far right snaps to exactly it.
        #expect(s.snapped(at: 1.0).value == "3m0s")
    }

    @Test func noCapSynthesizesABoundedTop() throws {
        let s = try #require(DurationScale(defaultValue: "1m", cap: nil, disablable: false))
        #expect(s.maxSeconds == 480)
    }

    @Test func aCapAtOrBelowTheDefaultIsIgnored() throws {
        // Staged values can be mid-edit nonsense; the scale must stay usable.
        let s = try #require(DurationScale(defaultValue: "1m", cap: "10s", disablable: false))
        #expect(s.maxSeconds == 480)
    }

    @Test func offDetentOnlyWhenDisablable() throws {
        let off = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: true))
        #expect(off.hasOff)
        #expect(off.snapped(at: 0).isOff)
        #expect(off.snapped(at: 0).value == "0")

        // Offering Off on a non-disablable key would present a choice the
        // daemon's Normalize silently discards — the one thing this control
        // must never do.
        let noOff = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: false))
        #expect(!noOff.hasOff)
        #expect(!noOff.snapped(at: 0).isOff)
    }

    @Test func roundTripIsStable() throws {
        let s = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: true))
        // Snapping what a position produced must land on the same stop —
        // otherwise the thumb crawls on its own as bindings re-fire.
        for p in stride(from: 0.0, through: 1.0, by: 0.05) {
            let snap = s.snapped(at: p)
            let back = s.snapped(at: s.position(for: snap.value))
            #expect(back == snap, "position \(p) → \(snap.value) → \(back.value)")
        }
    }

    @Test func defaultIsLandableAndMarked() throws {
        let s = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: true))
        let snap = s.snapped(at: s.position(for: "30s"))
        #expect(snap.value == "30s")
        #expect(snap.isDefault)
        #expect(!s.snapped(at: 1.0).isDefault)
    }

    @Test func snapsToTheHumanLadder() throws {
        let s = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: false))
        // Wherever the thumb lands, the value is a ladder step (or a bound).
        let allowed = Set(DurationScale.ladder(upTo: s.maxSeconds) + [Int(s.minSeconds), Int(s.maxSeconds), 30])
        for p in stride(from: 0.0, through: 1.0, by: 0.01) {
            let snap = s.snapped(at: p)
            let secs = Int(DurationChoices.seconds(snap.value) ?? -1)
            #expect(allowed.contains(secs), "\(snap.value) is off the ladder")
        }
    }

    @Test func offScaleCustomValueClampsThePosition() throws {
        let s = try #require(DurationScale(defaultValue: "30s", cap: "10m", disablable: false))
        #expect(s.position(for: "4h") == 1.0)
        #expect(s.position(for: "1s") == 0.0)
    }
}
