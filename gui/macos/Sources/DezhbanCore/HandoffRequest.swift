import Foundation

/// A file beside the instance lock saying "a user tried to launch this app;
/// please show yourself".
///
/// The distributed notification that normally carries this is delivered
/// immediately and never queued, and the incumbent takes the instance lock
/// *before* `NSApplication` exists — so between acquiring the lock and installing
/// its observer there is a window in which a hand-off is posted to nobody. It is
/// short, but it lands exactly at login, when a user impatient with a slow start
/// double-clicks the app: the duplicate exits, the notification hits no observer,
/// and their launch does nothing visible at all, which is the one outcome the
/// hand-off exists to prevent.
///
/// A file closes it because it waits. The incumbent consumes it when it installs
/// the observer *and* on its ordinary once-a-second tick, so a request written at
/// any point is picked up. The notification is kept as the fast path — this is the
/// one that cannot be missed.
public struct HandoffRequest {
    /// How long a request stays meaningful.
    ///
    /// A request is a live "the user just did something", not a queued command. If
    /// the incumbent died before consuming one, the *next* app to start must not
    /// inherit it and pop a window nobody asked for, so an old file is discarded
    /// rather than obeyed. Generous enough to cover a slow launch, short enough
    /// that it cannot outlive the click that caused it.
    public static let freshness: TimeInterval = 30

    public let url: URL

    public init(url: URL) {
        self.url = url
    }

    /// Derived from the lock's own URL, so it is scoped per install for exactly
    /// the reasons the lock is (see `InstanceLock.forBundle`).
    public static func beside(lock: URL) -> HandoffRequest {
        HandoffRequest(url: lock.deletingPathExtension().appendingPathExtension("handoff"))
    }

    /// Records a request. Best effort: it is the notification's backstop, and a
    /// failure to write it must never stop the losing process from exiting.
    public func post() {
        try? Data().write(to: url, options: .atomic)
    }

    /// Removes any request without acting on it.
    ///
    /// Called by a process that has just *taken* the lock: anything on disk at
    /// that moment predates its ownership and was meant for a predecessor.
    public func discard() {
        try? FileManager.default.removeItem(at: url)
    }

    /// Whether a fresh request is waiting, removing it either way.
    ///
    /// Consuming even a stale one keeps a file that will never be obeyed from
    /// sitting there being re-examined on every tick.
    public func consume(now: Date = Date()) -> Bool {
        let attributes = try? FileManager.default.attributesOfItem(atPath: url.path)
        guard let attributes else { return false }
        try? FileManager.default.removeItem(at: url)
        guard let written = attributes[.modificationDate] as? Date else { return false }
        let age = now.timeIntervalSince(written)
        // A negative age means a clock change put the file in the future rather
        // than that it is impossibly fresh; treat it the same as too old.
        return age >= 0 && age <= Self.freshness
    }
}
