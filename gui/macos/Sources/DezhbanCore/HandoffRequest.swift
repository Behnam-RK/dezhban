import Foundation

/// A file beside the session lock saying "a user tried to launch this app;
/// please show yourself".
///
/// The distributed notification that normally carries this is delivered
/// immediately and never queued, and the incumbent takes the session lock
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
    public let url: URL

    public init(url: URL) {
        self.url = url
    }

    /// Derived from the lock's own URL, so it is scoped per install for exactly
    /// the reasons the lock is (see `SessionLock.forBundle`).
    public static func beside(lock: URL) -> HandoffRequest {
        HandoffRequest(url: lock.deletingPathExtension().appendingPathExtension("handoff"))
    }

    /// Records a request, reporting whether it landed.
    ///
    /// Still best effort — a failure must never stop the losing process from
    /// exiting — but not silent. If this fails while the incumbent is between
    /// taking the lock and installing its observer, which is the exact gap the file
    /// exists to cover, then neither signal arrives and the user's launch is the
    /// silent no-op the mechanism was written to prevent. The caller cannot repair
    /// that, but it can say so.
    @discardableResult
    public func post() -> Result<Void, Error> {
        do {
            try Data().write(to: url, options: .atomic)
            return .success(())
        } catch {
            return .failure(error)
        }
    }

    /// The result of trying to take a request.
    ///
    /// There is deliberately no "too old to act on" case. An age cutoff was the
    /// first design, to keep a request its intended reader never got from being
    /// inherited by the *next* app to start and turned into a window nobody asked
    /// for — but `discard()` at lock acquisition already does that, exactly rather
    /// than by guessing, so the cutoff only added a way to be wrong. And it was: a
    /// cold login where the incumbent takes longer than the cutoff to finish
    /// starting is precisely the "user impatient with a slow start" case this whole
    /// mechanism is written around, and the cutoff threw that request away.
    public enum Claim: Equatable {
        /// Taken. Because the session owner discards whatever it finds when it takes
        /// the lock, a request seen after that was written by a live duplicate.
        case fresh
        /// There was nothing to take.
        case absent
        /// There was a request, and somebody else took it first. Whoever did is
        /// acting on it, so this caller must not.
        case lost
        /// There was a request and it could not be removed for a reason other than
        /// somebody else having taken it — an unwritable directory, most likely.
        ///
        /// Its own case because folding it into `.lost` made a *permanent* failure
        /// indistinguishable from a benign race: every claimer forever reported
        /// "somebody else has this", every one stood down, `discard()` could not
        /// clear it either, and the hand-off mechanism was dead for every future
        /// launch with nothing said. Callers still stand down — acting on a file
        /// that cannot be removed would repeat on every check — but they log it.
        case blocked(String)
    }

    /// Removes any request without acting on it.
    ///
    /// Called by a process that has just *taken* the lock: anything on disk at that
    /// moment predates its ownership and was meant for a predecessor. This is what
    /// makes an age cutoff unnecessary — and it is exact where a cutoff was a guess.
    /// The residual window is the microseconds between taking the lock and this
    /// call, in which a duplicate could post a request that then gets discarded;
    /// orders of magnitude narrower than the launches a 30-second cutoff threw away.
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
    /// `interleaved` exists so the `.lost` branch can be tested at all. It is the
    /// one outcome that only occurs when two claimers overlap, and it is also the
    /// one that matters — it is what stops both of them acting — so asserting it
    /// in a comment rather than a test would be asserting the whole point.
    public func claim(interleaved: () -> Void = {}) -> Claim {
        guard FileManager.default.fileExists(atPath: url.path) else { return .absent }
        interleaved()
        do {
            try FileManager.default.removeItem(at: url)
        } catch let error as NSError {
            // Gone between the check and the remove is the benign case: somebody
            // else claimed it and is acting on it. Anything else is a real failure
            // and must not wear the same face — see `Claim.blocked`.
            if error.domain == NSCocoaErrorDomain, error.code == NSFileNoSuchFileError {
                return .lost
            }
            if let posix = error.underlyingErrors.first as NSError?,
               posix.domain == NSPOSIXErrorDomain, posix.code == Int(ENOENT) {
                return .lost
            }
            return .blocked(error.localizedDescription)
        }
        return .fresh
    }
}
