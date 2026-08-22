import Foundation

/// An advisory `flock(2)` on a per-install lock file: whoever holds it owns this
/// login session, and a second copy of the same app exits.
///
/// This exists because the login item is a launchd agent (see `LaunchVisibility`
/// and docs/adr/0014-login-item-launch-marker.md). `SMAppService.mainApp` was a
/// LaunchServices entry, and LaunchServices refuses to start a second copy of a
/// bundle that is already running. An agent with `RunAtLoad` is not that: launchd
/// `exec`s `Contents/MacOS/DezhbanMenu` directly, bypassing that dedupe. So
/// `SMAppService.agent(...).register()` — called from the Settings toggle and from
/// the one-shot migration, both while the app is running — starts a *second* app
/// right then: two menubar items, two Dock tiles, two 1-second state-file timers,
/// two update checkers. `RunAtLoad` has to stay true or the login launch never
/// happens, so the duplicate is caught at startup instead.
///
/// A lock rather than a comparison of the running copies, which is what this was
/// first written as. Comparing "who launched earlier" cannot work here: only a
/// *newly started* process ever evaluates the question, and the copy already
/// serving the menubar never re-evaluates anything. Any rule under which the
/// newcomer might decide it wins leaves both running, and any rule under which an
/// undatable process yields can — with two copies racing — retire both and leave
/// the Mac with no app at all. `NSRunningApplication.launchDate` is documented as
/// optional, so both failures were reachable. The kernel has none of these
/// problems: exactly one open file description holds an exclusive `flock`, and it
/// is released when that process dies, however it dies, so a crashed or
/// force-quit predecessor cannot lock its successor out.
///
/// Keyed on the bundle's **path**, not its identifier. Two builds of the same app
/// in different places are not duplicates of each other — `dist/Dezhban.app` run
/// against an installed `/Applications/Dezhban.app` is the documented GUI dev loop
/// (docs/contribute/testing.md), and an identifier-scoped lock would have made the
/// freshly built copy exit on launch and silently test the installed one instead.
public final class SessionLock {
    public enum Acquisition: Equatable {
        /// This process now owns the session and holds the lock until it exits.
        case acquired
        /// Another live process of the same install holds it.
        case heldByAnother
        /// The lock file could not be opened at all (unwritable directory, and so
        /// on). Treated as `acquired` by callers: refusing to start because a
        /// support directory is broken would be a worse bug than a duplicate icon.
        case unavailable(String)
    }

    /// Where the lock file lives.
    public let url: URL

    /// Held for the process's lifetime once acquired. Never closed on purpose —
    /// see `release()`.
    private var fd: Int32 = -1

    public init(url: URL) {
        self.url = url
    }

    /// The conventional location: one file per install, under Application
    /// Support.
    ///
    /// **Not** under `~/Library/Caches`, which is where this first lived. `flock`
    /// is per-inode, and macOS is licensed to purge a caches directory under disk
    /// pressure — if it removed the file while the incumbent held its descriptor,
    /// the next launch's `open(O_CREAT)` would make a *new* inode, take the lock
    /// on that, and run a second copy of the app with nothing able to detect it.
    ///
    /// `bundlePath` is hashed rather than embedded so the name cannot exceed a
    /// filename limit, and hashed with FNV-1a rather than `hashValue` because
    /// Swift's is seeded per process — two copies of the app must derive the
    /// *same* name, which a randomly seeded hash would not give them.
    /// Symlinks are resolved before hashing, and must be: two launches of the same
    /// install whose `bundleURL` spells differently (`/tmp` against
    /// `/private/tmp`, a symlinked install directory) would otherwise derive
    /// *different* lock files, both acquire, and both run — the failure this class
    /// exists to prevent, arrived at silently. `standardizedFileURL` alone is not
    /// enough: it collapses `.` and `..` and expands `~`, and does not touch
    /// symlinks. The incumbent match in `acquireSessionOwnership` resolves the
    /// same way, for the same reason.
    public static func forBundle(path bundlePath: String,
                                 identifier: String,
                                 supportDirectory: URL) -> SessionLock {
        let dir = supportDirectory.appendingPathComponent(identifier, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let key = URL(fileURLWithPath: bundlePath).resolvingSymlinksInPath().standardizedFileURL.path
        let name = "instance-" + String(fnv1a(key), radix: 16) + ".lock"
        return SessionLock(url: dir.appendingPathComponent(name))
    }

    /// FNV-1a, 64-bit. Deterministic across processes and OS versions, which is
    /// the only property required of it here — this is a name, not a digest.
    static func fnv1a(_ s: String) -> UInt64 {
        var hash: UInt64 = 0xcbf2_9ce4_8422_2325
        for byte in s.utf8 {
            hash ^= UInt64(byte)
            hash = hash &* 0x0000_0100_0000_01b3
        }
        return hash
    }

    public func acquire() -> Acquisition {
        guard fd < 0 else { return .acquired }
        // O_CLOEXEC because the lock IS this descriptor: a child that inherited it
        // would hold the lock past this app's death and lock its own successor out
        // — permanently, since nothing would ever release it. Every subprocess the
        // app starts goes through Foundation's `Process`, which spawns with
        // POSIX_SPAWN_CLOEXEC_DEFAULT on macOS and so does not leak it today. That
        // makes this insurance rather than a fix, which is exactly why it belongs
        // here: the safety currently rests on an implementation detail of a
        // framework, stated nowhere, and the failure it would cause is an app that
        // never starts again.
        let opened = open(url.path, O_CREAT | O_RDWR | O_CLOEXEC, 0o644)
        if opened < 0 {
            return .unavailable("open(\(url.path)): \(String(cString: strerror(errno)))")
        }
        // LOCK_NB: never block. A blocking wait would hang the launch behind a
        // process that is not going to exit.
        if flock(opened, LOCK_EX | LOCK_NB) != 0 {
            let err = errno
            close(opened)
            if err == EWOULDBLOCK { return .heldByAnother }
            return .unavailable("flock(\(url.path)): \(String(cString: strerror(err)))")
        }
        fd = opened
        return .acquired
    }

    /// Whether the lock file sits on a local volume, where `flock` is the kernel's
    /// and is therefore released when its holder dies.
    ///
    /// The distinction decides what "held by another" is allowed to mean. Locally it
    /// means a live holder, full stop — so a launch that finds the lock taken must
    /// yield even when LaunchServices has not yet registered the incumbent, which at
    /// login is the ordinary case. On a network home the server emulates the lock and
    /// it can outlive the process that took it, and there yielding forever would make
    /// the app permanently unstartable.
    ///
    /// Unknown counts as local: trusting the lock risks a launch that hands off
    /// instead of opening, while distrusting it risks two copies of a kill switch's
    /// UI, and the first is the smaller failure.
    public var isOnLocalVolume: Bool {
        let values = try? url.deletingLastPathComponent()
            .resourceValues(forKeys: [.volumeIsLocalKey])
        return values?.volumeIsLocal ?? true
    }

    /// Drops the lock. Only tests need this: a real process holds the lock until
    /// it exits, and the kernel releases it then — including on a crash, which is
    /// the whole reason for using a lock rather than a pid file.
    public func release() {
        guard fd >= 0 else { return }
        flock(fd, LOCK_UN)
        close(fd)
        fd = -1
    }
}
