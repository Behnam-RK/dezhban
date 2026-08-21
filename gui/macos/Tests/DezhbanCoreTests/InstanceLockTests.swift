import Foundation
import Testing
@testable import DezhbanCore

struct InstanceLockTests {
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

        let first = InstanceLock(url: path)
        let second = InstanceLock(url: path)
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

        let contenders = (0 ..< 5).map { _ in InstanceLock(url: path) }
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

        let first = InstanceLock(url: path)
        let second = InstanceLock(url: path)
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
        let lock = InstanceLock(url: dir.appendingPathComponent("d.lock"))
        defer { lock.release() }

        #expect(lock.acquire() == .acquired)
        #expect(lock.acquire() == .acquired)
    }

    /// An unwritable location must not stop the app from starting. A broken
    /// support directory is a worse thing to fail a launch on than a duplicate
    /// icon.
    @Test func anUnopenableLockPathIsReportedRatherThanBlocking() {
        let lock = InstanceLock(url: URL(fileURLWithPath: "/dev/null/nope/e.lock"))
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
        let installed = InstanceLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        let built = InstanceLock.forBundle(
            path: "/Users/x/dev/dezhban/dist/Dezhban.app", identifier: "com.example.app",
            supportDirectory: dir)
        defer { installed.release(); built.release() }

        #expect(installed.url != built.url)
        #expect(installed.acquire() == .acquired)
        #expect(built.acquire() == .acquired)
    }

    /// The same install must derive the same name in every process, so the hash
    /// cannot be Swift's per-process-seeded one. Pinning a literal is the only
    /// way this test can fail if someone swaps it for `hashValue`.
    @Test func theLockNameIsStableAcrossProcesses() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let a = InstanceLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        let b = InstanceLock.forBundle(
            path: "/Applications/Dezhban.app", identifier: "com.example.app", supportDirectory: dir)
        #expect(a.url == b.url)
        // FNV-1a of the empty string is its offset basis; a seeded hash would not
        // reproduce it.
        #expect(InstanceLock.fnv1a("") == 0xcbf2_9ce4_8422_2325)
        #expect(InstanceLock.fnv1a("a") == InstanceLock.fnv1a("a"))
        #expect(InstanceLock.fnv1a("a") != InstanceLock.fnv1a("b"))
    }
}
