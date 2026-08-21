import Foundation

/// Which of several live copies of the app owns the session.
///
/// This exists because the login item is a launchd agent (see `LaunchVisibility`
/// and docs/adr/0014-login-item-launch-marker.md). `SMAppService.mainApp` was a
/// LaunchServices entry, and LaunchServices refuses to start a second copy of a
/// bundle that is already running. An agent with `RunAtLoad` is not that: launchd
/// `exec`s `Contents/MacOS/DezhbanMenu` directly, which bypasses that dedupe
/// entirely. So `SMAppService.agent(...).register()` — called from the Settings
/// toggle and from the one-shot migration, both while the app is running — starts
/// a *second* app right then: two menubar items, two Dock tiles, two 1-second
/// state-file timers, two update checkers. `RunAtLoad` has to stay true or the
/// login launch never happens, so the duplicate is caught at startup instead.
///
/// The rule is split out here, away from AppKit, because "both copies exit" is a
/// real way to get this wrong: two instances that each see the other and each
/// defer would leave the user with no app at all. So the comparison is a total
/// order over the candidates rather than a "does anyone else exist" test —
/// exactly one instance can be the smallest, so exactly one survives.
public struct InstanceIdentity: Sendable, Equatable {
    /// The process id. Unique among live processes, which is what makes the
    /// ordering below total even when two copies report the same launch date.
    public let pid: Int32
    /// When the process started, as AppKit reports it. Optional because
    /// `NSRunningApplication.launchDate` is documented to be nil when it cannot
    /// be determined; an instance whose age is unknown sorts last and therefore
    /// yields, which is the safe direction — the app that has been serving the
    /// menubar keeps serving it.
    public let launchedAt: Date?

    public init(pid: Int32, launchedAt: Date?) {
        self.pid = pid
        self.launchedAt = launchedAt
    }

    /// Older wins; unknown age loses; pid breaks the tie.
    fileprivate var rank: (Date, Int32) { (launchedAt ?? .distantFuture, pid) }
}

public enum SingleInstance {
    /// Whether this process should quit immediately because an equivalent copy
    /// already owns the session.
    ///
    /// `others` must exclude this process. Returns true only when some other
    /// candidate outranks us, so across any set of simultaneously-launched
    /// copies exactly one gets false — pids are distinct, so the ordering admits
    /// no ties and no cycles.
    public static func shouldYield(own: InstanceIdentity, others: [InstanceIdentity]) -> Bool {
        others.contains { other in
            other.pid != own.pid && isBefore(other.rank, own.rank)
        }
    }

    private static func isBefore(_ lhs: (Date, Int32), _ rhs: (Date, Int32)) -> Bool {
        if lhs.0 != rhs.0 { return lhs.0 < rhs.0 }
        return lhs.1 < rhs.1
    }
}
