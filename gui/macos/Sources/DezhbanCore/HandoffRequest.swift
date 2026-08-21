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
/// A file closes it because it waits: the incumbent looks for one when it installs
/// the observer, and again for a few seconds after, so a request written during
/// its own startup is still found. The window being covered is a launch-time one,
/// which is why the looking is bounded rather than permanent — once the observer
/// exists the notification carries every later hand-off.
///
/// Both signals describe the *same* request, so acting on it is a claim rather
/// than a read: whoever removes the file acts, and everyone else stands down. A
/// plain check-then-remove let the notification and the file-watcher both open the
/// window — a second `NSApp.activate` half a second after the first, or a window
/// reopening right after the user closed it.
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

    /// The result of trying to take a request.
    public enum Claim: Equatable {
        /// Taken, and recent enough to act on.
        case fresh
        /// Taken, but too old to act on — see `freshness`.
        case stale
        /// There was nothing to take.
        case absent
        /// There was a request, and somebody else took it first. Whoever did is
        /// acting on it, so this caller must not.
        case lost
    }

    /// Removes any request without acting on it.
    ///
    /// Called by a process that has just *taken* the lock: anything on disk at
    /// that moment predates its ownership and was meant for a predecessor.
    public func discard() {
        try? FileManager.default.removeItem(at: url)
    }

    /// Tries to take the request, and reports whether this caller owns it.
    ///
    /// The removal is what makes it a claim: exactly one `removeItem` can succeed
    /// for a given file, so exactly one caller is told `.fresh` and the other is
    /// told `.lost`. Reading the timestamp first and removing afterwards without
    /// checking — the first shape of this — let a background check and the
    /// notification handler both conclude they had it.
    ///
    /// A stale request is still taken, so a file that will never be acted on stops
    /// being re-examined.
    /// `interleaved` exists so the `.lost` branch can be tested at all. It is the
    /// one outcome that only occurs when two claimers overlap, and it is also the
    /// one that matters — it is what stops both of them acting — so asserting it
    /// in a comment rather than a test would be asserting the whole point.
    public func claim(now: Date = Date(), interleaved: () -> Void = {}) -> Claim {
        let attributes = try? FileManager.default.attributesOfItem(atPath: url.path)
        guard let attributes else { return .absent }
        interleaved()
        do {
            try FileManager.default.removeItem(at: url)
        } catch {
            // Gone between the stat and the remove: somebody else claimed it. (A
            // genuine permissions failure lands here too, and standing down is the
            // safe reading of it — a window that does not open, never two.)
            return .lost
        }
        guard let written = attributes[.modificationDate] as? Date else { return .stale }
        let age = now.timeIntervalSince(written)
        // A negative age means a clock change put the file in the future rather
        // than that it is impossibly fresh; treat it the same as too old.
        return age >= 0 && age <= Self.freshness ? .fresh : .stale
    }
}
