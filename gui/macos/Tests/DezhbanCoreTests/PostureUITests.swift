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
        // nil OMITS the key, rather than writing an empty array. `tunnels` is
        // `omitempty` on the wire and `[Tunnel]?` here, so absent and `[]` decode to
        // two different values and are two different cases — and writing `[]` for
        // nil made the test for the absent one silently exercise the empty one,
        // which another test already covered. Pass `[]` for the empty case.
        if let tuns = tunnels {
            obj["tunnels"] = tuns.map { ["name": $0.name, "up": $0.up] }
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

    // MARK: - unblockConsequence

    /// Overview enables Unblock in two states and its caption is now the primary
    /// pre-click explanation, so the sentence has to name what is actually being
    /// released.
    ///
    /// `postureName` derives "full-block" from `blocked` alone, so an operator's
    /// own block and a blocked-country escalation are the same string on the wire.
    /// The caption may therefore claim neither — only what is true of both.
    @Test func unblockConsequenceNamesTheFullBlockItLifts() {
        let text = PostureUI.unblockConsequence(snapshot(posture: "full-block",
                                                         display: Self.downedGuardDisplay))
        #expect(text.contains("full block"))
        #expect(!text.contains("manual"))
    }

    /// The posture check may not come first. `runner`'s unblock handler branches on
    /// `AutoArm && !tunnelUp && !standby` without looking at *why* egress was cut,
    /// so a full block standing over a downed tunnel — `dezhban block` with the VPN
    /// off, or a tunnel that dropped under a geo block, which opens no redial window
    /// — drops to STANDBY exactly like the guard case does. Answering that with
    /// "resumes monitoring" is the same false promise this function removed from the
    /// guard branch, in the branch the first version did not cover.
    @Test func unblockConsequenceWarnsWhenAFullBlockSitsOverADownedTunnel() {
        let text = PostureUI.unblockConsequence(
            snapshot(posture: "full-block", display: Self.downedGuardDisplay,
                     tunnels: [(name: "utun4", up: false)]))
        #expect(text.contains("real IP"))
        #expect(!text.contains("resumes monitoring"))
    }

    /// …and an absent tunnel list reads the same way. It is what the daemon sends
    /// when it has none (`omitempty`), and guessing "up" there would put the false
    /// promise back for the one snapshot that says least.
    @Test func unblockConsequenceTreatsNoTunnelAsDown() {
        let text = PostureUI.unblockConsequence(
            snapshot(posture: "full-block", display: Self.downedGuardDisplay, tunnels: nil))
        #expect(text.contains("real IP"))
    }

    /// The caption reserves three lines sized for the longest hint the row already
    /// had, and `AppState.routineHint` appends 60 characters to whatever this
    /// returns. A longer sentence truncates, and the tail that disappears is the
    /// password clause — the exact failure the three-line reservation was
    /// introduced to prevent.
    @Test func everyUnblockSentenceFitsTheCaption() {
        let states = [
            snapshot(posture: "guard"),
            snapshot(posture: "guard", display: Self.downedGuardDisplay),
            snapshot(posture: "full-block", display: Self.downedGuardDisplay),
            snapshot(posture: "full-block", display: Self.downedGuardDisplay,
                     tunnels: [(name: "utun4", up: false)]),
        ]
        for s in states {
            let text = PostureUI.unblockConsequence(s)
            #expect(text.count <= 80, "too long for the caption (\(text.count)): \(text)")
        }
        #expect(PostureUI.unblockConsequence(nil).count <= 80)
    }

    /// With `vpn.autoArm` on — the default — an explicit unblock with the tunnel
    /// down drops the daemon to STANDBY, which installs nothing. Saying
    /// "resumes monitoring" there was the opposite of what happens.
    @Test func unblockConsequenceNamesTheRealIPExposure() {
        let text = PostureUI.unblockConsequence(
            snapshot(posture: "guard", display: Self.downedGuardDisplay))
        #expect(text.contains("real IP"))
        #expect(!text.contains("resumes monitoring"))
    }

    /// A healthy guard does not offer the button at all (`blocked` is false and the
    /// guard is not holding), so this is the default rather than a live state — but
    /// it must still be a sentence, and must not inherit either warning.
    @Test func unblockConsequenceFallsBackToThePlainSentence() {
        let text = PostureUI.unblockConsequence(snapshot(posture: "guard"))
        #expect(text.contains("resumes monitoring"))
        #expect(!text.contains("real IP"))
        #expect(!text.contains("full block"))
    }

    /// No snapshot is not a state the button is offered in, but the function must
    /// still answer with a sentence rather than an empty caption.
    @Test func unblockConsequenceHasATextForNoSnapshot() {
        #expect(!PostureUI.unblockConsequence(nil).isEmpty)
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
