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

    /// The rendered Display is what a real daemon publishes for a guard whose
    /// tunnel is down — internal/render emits key "blocked" and this headline
    /// for exactly that state — so these use it rather than a hand-built
    /// snapshot claiming "on" while its tunnels say otherwise.
    private static let downedGuardDisplay = (
        key: "blocked", headline: "VPN down — traffic cut",
        detail: "Guard active, but no tunnel is up — all traffic is cut until your VPN redials."
    )

    @Test func guardHoldsDownedTunnelFalseWhenTunnelUp() {
        #expect(!PostureUI.guardHoldsDownedTunnel(snapshot(posture: "guard", tunnels: [(name: "utun4", up: true)])))
    }

    @Test func guardHoldsDownedTunnelTrueWhenTunnelDown() {
        #expect(PostureUI.guardHoldsDownedTunnel(
            snapshot(posture: "guard", display: Self.downedGuardDisplay, tunnels: [(name: "utun4", up: false)])))
    }

    @Test func guardHoldsDownedTunnelTrueWhenTunnelListEmpty() {
        // The zero-tunnel standing posture is a total egress cut — this is the
        // case the addendum's testing notes call out as having no coverage.
        #expect(PostureUI.guardHoldsDownedTunnel(
            snapshot(posture: "guard", display: Self.downedGuardDisplay, tunnels: [])))
    }

    @Test func guardHoldsDownedTunnelFalseForNilSnapshot() {
        #expect(!PostureUI.guardHoldsDownedTunnel(nil))
    }

    /// The daemon's Display is authoritative: this must NOT be re-derived from
    /// `tunnels`, or the GUI can call a guard healthy that `status` and
    /// `update.CanActivate` both call blocked. A snapshot whose tunnel list
    /// disagrees with its rendered key resolves to the key.
    @Test func guardHoldsDownedTunnelPrefersDisplayOverTunnelScan() {
        #expect(PostureUI.guardHoldsDownedTunnel(
            snapshot(posture: "guard", display: Self.downedGuardDisplay, tunnels: [(name: "utun4", up: true)])))
    }

    /// …and falls back to the tunnel scan only for a daemon predating Display.
    @Test func guardHoldsDownedTunnelFallsBackToTunnelsWithoutDisplay() {
        #expect(PostureUI.guardHoldsDownedTunnel(
            snapshot(posture: "guard", display: nil, tunnels: [(name: "utun4", up: false)])))
        #expect(!PostureUI.guardHoldsDownedTunnel(
            snapshot(posture: "guard", display: nil, tunnels: [(name: "utun4", up: true)])))
    }

    // MARK: - dockState / mmss / agoString

    @Test func dockStateCoarsensToBlockedOrOn() {
        #expect(PostureUI.dockState(for: "blocked") == "blocked")
        for state in ["on", "off", "warning", "paused"] {
            #expect(PostureUI.dockState(for: state) == "on")
        }
    }

    /// Under `swift test` there is no assembled .app, so every bundle lookup
    /// must MISS — and that nil is precisely what keeps the SF Symbol fallback
    /// in the Overview hero reachable for a bare `swift run`. A future change
    /// that made these resolve some other way (SPM resources, an asset catalog)
    /// would silently strand that branch.
    ///
    /// It cannot pin the opposite — that the tiles DO resolve in a real bundle —
    /// because that is a build-script property, not a Swift one. build-app.sh
    /// carries its own note for a missing state-tile-<key>.png.
    @Test(arguments: ["on", "off", "blocked", "warning", "paused"])
    func brandImagesMissOutsideTheAssembledBundle(_ state: String) {
        #expect(PostureUI.stateTile(state) == nil)
        #expect(PostureUI.dockIcon(state) == nil)
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
