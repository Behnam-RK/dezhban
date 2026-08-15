import AppKit
import SwiftUI

/// Pure snapshot → presentation derivations, shared by the menubar (AppDelegate)
/// and the main window so the two surfaces can never disagree about what a
/// posture looks like. Lives in DezhbanCore (not DezhbanMenu) specifically so it
/// can be unit-tested — these functions are the seam where the reported GUI bugs
/// actually lived (stale/mismatched posture reads, prose-coupled classification).
public enum PostureUI {
    /// Floor for the staleness threshold. The daemon's actual poll cadence — carried
    /// in the snapshot as `pollIntervalSeconds` — scales this up (see staleThreshold),
    /// so a deliberately long pollInterval doesn't make an enforcing daemon read as
    /// "stopped" between polls. 90s tolerates a couple of missed default-cadence cycles.
    public static let staleFloor: TimeInterval = 90

    /// The age past which a snapshot reads as stopped, derived from the daemon's own
    /// poll cadence (3× the interval) so it scales with a custom pollInterval, floored
    /// at staleFloor. Falls back to the floor when the field is absent (older daemon).
    public static func staleThreshold(_ s: Snapshot) -> TimeInterval {
        guard let p = s.pollIntervalSeconds, p > 0 else { return staleFloor }
        return max(staleFloor, TimeInterval(p) * 3)
    }

    /// Whether a snapshot represents a live, enforcing daemon (fresh and not
    /// stopped). Single source of truth for the icon, the menu's gating, and the
    /// window's Overview.
    public static func isLive(_ s: Snapshot?) -> Bool {
        guard let s = s else { return false }
        return s.age <= staleThreshold(s) && s.posture != "stopped"
    }

    /// Maps a snapshot (or its absence/staleness) to one of the five brand states
    /// (on / off / blocked / warning / paused — the full-color icons from
    /// gui/artifacts/), an SF Symbol fallback for running outside the assembled
    /// bundle, and the headline sentence. The key and headline both come from
    /// the daemon's own render.Display (internal/render on the Go side) — this
    /// function no longer composes any prose itself, only the SF Symbol choice
    /// and the liveness/staleness override, which are inherently client-side.
    public static func iconFor(_ s: Snapshot?) -> (state: String, symbol: String, help: String) {
        guard let s = s, isLive(s) else {
            return ("off", "shield", "stopped") // gray / hollow shield: not enforcing
        }
        guard let d = s.display else {
            // An older daemon predating Display, or a snapshot built by hand
            // (tests). Should be rare and transient in practice.
            return ("warning", "exclamationmark.triangle.fill", "unknown posture (\(s.posture))")
        }
        // A failed firewall action gets its own SF Symbol (distinct from the
        // shield used for a relaxed-but-intentional window), even though the key
        // and headline are already "warning"/"Enforcement failed" from Go.
        if let e = s.enforcementErr, !e.isEmpty {
            return (d.key, "exclamationmark.triangle.fill", d.headline)
        }
        return (d.key, sfSymbol(for: d.key), d.headline)
    }

    private static func sfSymbol(for key: String) -> String {
        switch key {
        case "on": return "checkmark.shield.fill"
        case "blocked": return "shield.slash.fill"
        case "warning": return "exclamationmark.shield.fill"
        case "paused": return "pause.circle.fill"
        default: return "shield" // "off"
        }
    }

    /// The headline sentence for a snapshot, or a plain fallback for one with no
    /// rendered Display. Thin wrapper so callers don't each need their own
    /// fallback.
    public static func humanPosture(_ s: Snapshot) -> String {
        s.display?.headline ?? "posture: \(s.posture)"
    }

    /// Guard mode holding a downed tunnel: Unblock doubles as the "my VPN is off
    /// on purpose — release the line" action (a vpn.autoArm daemon returns to
    /// standby; without autoArm it's a harmless no-op the daemon acknowledges).
    ///
    /// READ from the daemon's own rendering, not re-derived: under the "guard"
    /// posture, `render.Text` (Go) already emits key "blocked" for exactly this
    /// state and "on" for a guard with a tunnel up, and
    /// `render.GuardHoldsDownedTunnel`'s doc comment says in as many words that
    /// callers must not compute this from `tunnels` themselves — a second copy
    /// of the rule is how the CLI, the GUI, and `update.CanActivate` would come
    /// to disagree about what a healthy guard is. This used to be that second
    /// copy.
    ///
    /// The tunnel scan survives only as the fallback for a snapshot with no
    /// Display (a daemon older than that field). There, an EMPTY tunnel list
    /// counts as down: the zero-tunnel standing posture is a total egress cut
    /// (ModeFullBlock shape under the "guard" posture string), and the icon must
    /// never show a calm green shield while the network is cut.
    public static func guardHoldsDownedTunnel(_ s: Snapshot?) -> Bool {
        guard let s = s, s.posture == "guard" else { return false }
        if let d = s.display { return d.key == "blocked" }
        guard let tuns = s.tunnels, !tuns.isEmpty else { return true }
        return !tuns.contains(where: { $0.up })
    }

    /// SwiftUI accent for a brand state — used where the bundled bitmap isn't
    /// (SF Symbol fallback, text highlights).
    public static func color(for state: String) -> Color {
        switch state {
        case "on": return .green
        case "blocked": return .red
        case "warning": return .orange
        // Blue, not amber: a pause is deliberate and expected, so it must not
        // borrow the colour that means "something went wrong".
        case "paused": return .blue
        default: return .secondary
        }
    }

    /// Coarsens a menu-bar state down to what the Dock tile is allowed to show.
    /// The Dock icon answers one question — "is dezhban actively cutting my
    /// traffic right now?" — so only "blocked" may stand out; "off" (stopped /
    /// standby) and "warning" (switch-window / enforcement error) collapse to the
    /// calm default "on" (guard) look instead of their own Dock badge. This is a
    /// Dock-only narrowing: the menu bar icon keeps showing all four states via
    /// `iconFor` untouched, since that's where the real-time nuance belongs.
    public static func dockState(for state: String) -> String {
        state == "blocked" ? "blocked" : "on"
    }

    /// Lock-guarded image cache. This was a plain `private static var [String:
    /// NSImage]`, which is mutable global state with no isolation — legal under
    /// this package's swift-tools-version:5.9, a hard error the day it moves to
    /// Swift 6 strict concurrency, and already a data race in principle.
    ///
    /// A lock rather than `@MainActor`: `dockIcon` is a pure lookup with no UI
    /// side effects, and isolating it would force every caller onto the main
    /// actor (or into an `await`) just to read an image. The load runs inside
    /// the lock so two simultaneous first-lookups of the same state decode the
    /// PNG once instead of racing to store two copies.
    private final class ImageCache: @unchecked Sendable {
        private let lock = NSLock()
        private var images: [String: NSImage] = [:]

        func image(_ key: String, load: () -> NSImage?) -> NSImage? {
            lock.lock()
            defer { lock.unlock() }
            if let img = images[key] { return img }
            guard let img = load() else { return nil }
            images[key] = img
            return img
        }
    }

    /// Dock-size brand state images from the app bundle's Resources (put there by
    /// build-app.sh from gui/artifacts/png), cached per state. Empty outside the bundle,
    /// where callers fall back to SF Symbols.
    ///
    /// The Dock tile ONLY — pass it a `dockState`, never a raw posture key.
    /// build-app.sh ships exactly two of these files ("blocked" and "on"),
    /// because `dockState` coarsens every posture into one of those two. Any
    /// other key resolves to no file at all. Surfaces that show the full
    /// five-state nuance want `stateTile` instead.
    private static let dockIcons = ImageCache()

    public static func dockIcon(_ state: String) -> NSImage? {
        dockIcons.image(state) {
            Bundle.main.url(forResource: "dock-state-\(state)", withExtension: "png")
                .flatMap { NSImage(contentsOf: $0) }
        }
    }

    /// Full-color brand STATE tiles (gui/artifacts/png's icon-<state>-512.png),
    /// bundled by build-app.sh for all five states. Color IS the state, so
    /// these are what a status hero shows.
    ///
    /// Deliberately a different resource family from `dockIcon`. The Dock's two
    /// files are a coarsening (see `dockState`) and the window's hero is the
    /// opposite of coarse — it is the surface with room to say "warning" or
    /// "paused" rather than merely "not a cut". Sharing `dock-state-*` was the
    /// bug: three of the five keys resolved to no file, so the hero silently
    /// fell back to a generic SF Symbol shield, which is not a dezhban artifact
    /// at all, and the three degraded pages hit it every single time.
    ///
    /// nil outside the assembled bundle (a bare `swift run`), where callers keep
    /// their SF Symbol fallback. A miss is not cached, so a lookup that fails
    /// once outside the bundle does not poison a later in-bundle run.
    private static let stateTiles = ImageCache()

    public static func stateTile(_ state: String) -> NSImage? {
        stateTiles.image(state) {
            Bundle.main.url(forResource: "state-tile-\(state)", withExtension: "png")
                .flatMap { NSImage(contentsOf: $0) }
        }
    }

    public static func agoString(_ seconds: TimeInterval) -> String {
        let s = Int(seconds.rounded())
        if s < 60 { return "\(s)s ago" }
        return "\(s / 60)m ago"
    }

    /// Round DOWN so the countdown never shows more time than is actually left
    /// (e.g. 59.6s reads "0:59", not "1:00"). For the switch-window exposure
    /// timer, under-stating the remaining time is the safe direction.
    public static func mmss(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds.rounded(.down)))
        return String(format: "%d:%02d", s / 60, s % 60)
    }
}
