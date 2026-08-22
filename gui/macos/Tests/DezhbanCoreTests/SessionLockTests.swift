import Foundation
import Testing
@testable import DezhbanCore

struct SessionLockTests {
    private func tempDir() throws -> URL {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("dezhban-instancelock-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    /// The case that put this code here: registering the login agent execs a
    /// second copy while the first is serving the menubar. `flock` is per open
    /// file description, so a second `acquire()` on the same path is refused even
    /// from within one process — which is exactly what makes this testable.
    @Test func aSecondHolderIsRefused() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let path = dir.appendingPathComponent("a.lock")

        let first = SessionLock(url: path)
        let second = SessionLock(url: path)
        defer { first.release(); second.release() }

        #expect(first.acquire() == .acquired)
        #expect(second.acquire() == .heldByAnother)
    }

    /// The failure the previous design could not rule out: two copies each
    /// deciding the other one wins, leaving no app running at all. A lock cannot
    /// express that — someone holds it.
    @Test func exactlyOneOfManyContendersWins() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let path = dir.appendingPathComponent("b.lock")

        let contenders = (0 ..< 5).map { _ in SessionLock(url: path) }
        defer { contenders.forEach { $0.release() } }

        let winners = contenders.filter { $0.acquire() == .acquired }
        #expect(winners.count == 1)
    }

    /// Releasing hands the session to the next starter. This is what makes a
    /// crashed or force-quit predecessor harmless: the kernel does this for us
    /// when the process dies, which a pid file would not.
    @Test func releasingLetsTheNextInstanceIn() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let path = dir.appendingPathComponent("c.lock")

        let first = SessionLock(url: path)
        let second = SessionLock(url: path)
        defer { second.release() }

        #expect(first.acquire() == .acquired)
        #expect(second.acquire() == .heldByAnother)
        first.release()
        #expect(second.acquire() == .acquired)
    }

    /// Re-acquiring is not a second holder — a caller that asks twice must not be
    /// told it lost to itself.
    @Test func reacquiringTheSameLockIsIdempotent() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let lock = SessionLock(url: dir.appendingPathComponent("d.lock"))
        defer { lock.release() }

        #expect(lock.acquire() == .acquired)
        #expect(lock.acquire() == .acquired)
    }

    /// An unwritable location must not stop the app from starting. A broken
    /// support directory is a worse thing to fail a launch on than a duplicate
    /// icon.
    @Test func anUnopenableLockPathIsReportedRatherThanBlocking() {
        let lock = SessionLock(url: URL(fileURLWithPath: "/dev/null/nope/e.lock"))
        defer { lock.release() }
        guard case .unavailable = lock.acquire() else {
            Issue.record("expected .unavailable for an unopenable path")
            return
        }
    }

    /// Two installs of the same app are not duplicates of each other: the
    /// documented GUI dev loop runs dist/Dezhban.app while /Applications holds a
    /// released copy, and an identifier-scoped lock made the dev build exit on
    /// launch and silently test the installed one.
    @Test func differentInstallPathsGetDifferentLocks() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let installed = SessionLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        let built = SessionLock.forBundle(
            path: "/Users/x/dev/dezhban/dist/Dezhban.app", identifier: "com.example.app",
            supportDirectory: dir)
        defer { installed.release(); built.release() }

        #expect(installed.url != built.url)
        #expect(installed.acquire() == .acquired)
        #expect(built.acquire() == .acquired)
    }

    /// Two spellings of one install are one install. A symlinked install
    /// directory is the realistic case (`/tmp` is itself a symlink to
    /// `/private/tmp` on macOS), and deriving two locks from it would let both
    /// copies run — silently, which is the whole failure this class prevents.
    ///
    /// Built on a real symlink to a real bundle directory on purpose:
    /// `resolvingSymlinksInPath()` returns the path unchanged when the leaf does
    /// not exist, so a test written against two invented paths would pass or fail
    /// for reasons that have nothing to do with the code.
    @Test func equivalentPathsGetTheSameLock() throws {
        let root = try tempDir()
        defer { try? FileManager.default.removeItem(at: root) }
        let locks = root.appendingPathComponent("locks", isDirectory: true)
        try FileManager.default.createDirectory(at: locks, withIntermediateDirectories: true)

        let real = root.appendingPathComponent("real", isDirectory: true)
        let bundle = real.appendingPathComponent("Dezhban.app", isDirectory: true)
        try FileManager.default.createDirectory(at: bundle, withIntermediateDirectories: true)
        let link = root.appendingPathComponent("link", isDirectory: true)
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: real)

        let viaReal = SessionLock.forBundle(
            path: bundle.path, identifier: "com.example.app", supportDirectory: locks)
        let viaLink = SessionLock.forBundle(
            path: link.appendingPathComponent("Dezhban.app").path,
            identifier: "com.example.app", supportDirectory: locks)
        defer { viaReal.release(); viaLink.release() }

        #expect(viaReal.url == viaLink.url)
        #expect(viaReal.acquire() == .acquired)
        #expect(viaLink.acquire() == .heldByAnother)
    }

    /// The same install must derive the same name in every process, so the hash
    /// cannot be Swift's per-process-seeded one. Pinning a literal is the only
    /// way this test can fail if someone swaps it for `hashValue`.
    @Test func theLockNameIsStableAcrossProcesses() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let a = SessionLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        let b = SessionLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        #expect(a.url == b.url)
        // FNV-1a of the empty string is its offset basis; a seeded hash would not
        // reproduce it.
        #expect(SessionLock.fnv1a("") == 0xcbf2_9ce4_8422_2325)
        #expect(SessionLock.fnv1a("a") == SessionLock.fnv1a("a"))
        #expect(SessionLock.fnv1a("a") != SessionLock.fnv1a("b"))
    }
}
