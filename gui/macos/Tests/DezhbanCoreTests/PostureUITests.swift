import Foundation
import Testing
@testable import DezhbanCore

struct PostureUITests {
    /// Builds a Snapshot via JSON (Snapshot has no public memberwise init by
    /// design — decoding is the one real construction path, so tests exercise
    /// the same path production code does).
    private func snapshot(
        posture: String = "guard",
        ageSeconds: TimeInterval = 0,
        display: (key: String, headline: String, detail: String)? = ("on", "Guarding", "Traffic leaves only through your VPN tunnel."),
        enforcementErr: String? = nil,
        tunnels: [(name: String, up: Bool)]? = [(name: "utun4", up: true)]
    ) -> Snapshot {
        let time = ISO8601DateFormatter().string(from: Date().addingTimeInterval(-ageSeconds))
        var obj: [String: Any] = ["time": time, "posture": posture, "blocked": posture == "full-block"]
        if let d = display {
            obj["display"] = ["key": d.key, "headline": d.headline, "detail": d.detail]
        }
        if let e = enforcementErr { obj["enforcementErr"] = e }
        if let tuns = tunnels {
            obj["tunnels"] = tuns.map { ["name": $0.name, "up": $0.up] }
        } else {
            obj["tunnels"] = []
        }
        let data = try! JSONSerialization.data(withJSONObject: obj)
        return StateReader.decode(data)!
    }

    @Test func isLiveFalseForNilSnapshot() {
        #expect(!PostureUI.isLive(nil))
    }

    @Test func isLiveFalseForStoppedPosture() {
        #expect(!PostureUI.isLive(snapshot(posture: "stopped")))
    }

    @Test func isLiveFalseForStaleSnapshot() {
        // Past the 90s floor with no pollIntervalSeconds to scale it.
        #expect(!PostureUI.isLive(snapshot(ageSeconds: 200)))
    }

    @Test func isLiveTrueForFreshGuard() {
        #expect(PostureUI.isLive(snapshot(ageSeconds: 1)))
    }

    @Test func iconForNilSnapshotReadsStopped() {
        let icon = PostureUI.iconFor(nil)
        #expect(icon.state == "off")
        #expect(icon.help == "stopped")
    }

    @Test func iconForStaleSnapshotReadsStopped() {
        let icon = PostureUI.iconFor(snapshot(ageSeconds: 500))
        #expect(icon.state == "off")
    }

    @Test(arguments: [
        (key: "on", symbol: "checkmark.shield.fill"),
        (key: "blocked", symbol: "shield.slash.fill"),
        (key: "warning", symbol: "exclamationmark.shield.fill"),
        (key: "paused", symbol: "pause.circle.fill"),
        (key: "off", symbol: "shield"),
    ])
    func iconForEveryDisplayKey(_ c: (key: String, symbol: String)) {
        let s = snapshot(display: (c.key, "Headline", "Detail"))
        let icon = PostureUI.iconFor(s)
        #expect(icon.state == c.key)
        #expect(icon.symbol == c.symbol)
        #expect(icon.help == "Headline")
    }

    @Test func iconForNoDisplayFallsBackToUnknownPosture() {
        // An older daemon predating render.Display.
        let icon = PostureUI.iconFor(snapshot(posture: "guard", display: nil))
        #expect(icon.state == "warning")
        #expect(icon.help.contains("guard"))
    }

    @Test func iconForEnforcementErrorOverridesSymbolNotKeyOrHeadline() {
        let s = snapshot(display: ("warning", "Enforcement failed", "some detail"), enforcementErr: "pfctl: permission denied")
        let icon = PostureUI.iconFor(s)
        #expect(icon.state == "warning")
        #expect(icon.symbol == "exclamationmark.triangle.fill")
        #expect(icon.help == "Enforcement failed")
    }

    @Test func humanPostureFallsBackWithoutDisplay() {
        let s = snapshot(posture: "full-block", display: nil)
        #expect(PostureUI.humanPosture(s) == "posture: full-block")
    }

    @Test func humanPostureUsesDisplayHeadline() {
        let s = snapshot(display: ("blocked", "Traffic cut", "detail"))
        #expect(PostureUI.humanPosture(s) == "Traffic cut")
    }

    // MARK: - guardHoldsDownedTunnel

    @Test func guardHoldsDownedTunnelFalseWhenNotGuardPosture() {
        #expect(!PostureUI.guardHoldsDownedTunnel(snapshot(posture: "standby")))
    }

    @Test func guardHoldsDownedTunnelFalseWhenTunnelUp() {
        #expect(!PostureUI.guardHoldsDownedTunnel(snapshot(posture: "guard", tunnels: [(name: "utun4", up: true)])))
    }

    @Test func guardHoldsDownedTunnelTrueWhenTunnelDown() {
        #expect(PostureUI.guardHoldsDownedTunnel(snapshot(posture: "guard", tunnels: [(name: "utun4", up: false)])))
    }

    @Test func guardHoldsDownedTunnelTrueWhenTunnelListEmpty() {
        // The zero-tunnel standing posture is a total egress cut — this is the
        // case the addendum's testing notes call out as having no coverage.
        #expect(PostureUI.guardHoldsDownedTunnel(snapshot(posture: "guard", tunnels: [])))
    }

    @Test func guardHoldsDownedTunnelFalseForNilSnapshot() {
        #expect(!PostureUI.guardHoldsDownedTunnel(nil))
    }

    // MARK: - dockState / mmss / agoString

    @Test func dockStateCoarsensToBlockedOrOn() {
        #expect(PostureUI.dockState(for: "blocked") == "blocked")
        for state in ["on", "off", "warning", "paused"] {
            #expect(PostureUI.dockState(for: state) == "on")
        }
    }

    @Test func mmssRoundsDown() {
        #expect(PostureUI.mmss(59.6) == "0:59")
        #expect(PostureUI.mmss(60) == "1:00")
        #expect(PostureUI.mmss(-5) == "0:00")
    }

    @Test func agoStringSecondsVsMinutes() {
        #expect(PostureUI.agoString(5) == "5s ago")
        #expect(PostureUI.agoString(125) == "2m ago")
    }
}
