import Foundation
import Testing
@testable import DezhbanCore

struct HandoffRequestTests {
    private func tempDir() throws -> URL {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("dezhban-handoff-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    /// The whole point: a request written while nobody was observing is still
    /// there to be found — and found once.
    @Test func aPostedRequestIsClaimedOnce() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("a.handoff"))

        request.post(token: "t1")
        #expect(request.claim() == .fresh(token: "t1"))
        #expect(request.claim() == .absent)
    }

    /// Nothing waiting means nothing to do, which is the ordinary case every time
    /// the backstop looks. Distinct from `.lost`, which means somebody else is
    /// already acting on one.
    @Test func noRequestIsNotARequest() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(HandoffRequest(url: dir.appendingPathComponent("b.handoff")).claim() == .absent)
    }

    /// `discard()` is what a process that has just taken the lock calls: whatever
    /// is on disk was meant for its predecessor.
    @Test func discardRemovesWithoutReporting() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("e.handoff"))

        request.post(token: "t1")
        request.discard()
        #expect(request.claim() == .absent)
    }

    /// The token is what lets the two signals for one request be told apart from two
    /// requests. Timing could not, which is why three attempts at a debounce each
    /// both duplicated a window and swallowed a real launch.
    @Test func theClaimCarriesThePostedToken() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("i.handoff"))

        request.post(token: "abc-123")
        #expect(request.claim() == .fresh(token: "abc-123"))

        // A request from an older build carries nothing; `nil` means "cannot dedupe",
        // which the caller treats as its own identity rather than as a match.
        try Data().write(to: request.url)
        #expect(request.claim() == .fresh(token: nil))
    }

    /// Two claimers overlapping on one request — the notification handler and the
    /// launch-time backstop, which is the pair this type exists to arbitrate. The
    /// loser must be told it lost, not that there was nothing there: only that
    /// distinction stops it opening a second window half a second after the first,
    /// or reopening one the user has just closed.
    @Test func anOverlappingClaimerIsToldItLost() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("f.handoff"))

        request.post(token: "t1")
        // Stands in for the other claimer winning between this one's stat and its
        // remove — the only way `.lost` can arise, and the reason `claim` takes
        // the hook.
        let claim = request.claim(interleaved: { request.discard() })
        #expect(claim == .lost)
    }

    /// A request that cannot be removed must not look like one somebody else took.
    /// Folded together, a permanent failure made every claimer stand down forever
    /// and killed the mechanism silently.
    @Test func anUnremovableRequestIsBlockedNotLost() throws {
        // Root ignores directory permissions, so the mechanism this test uses to
        // make a file unremovable does not work for it — the unlink succeeds,
        // `claim()` returns `.fresh`, and the test would fail for a reason that has
        // nothing to do with the code. testing.md routinely asks contributors to run
        // privileged checks on this machine, so skip rather than lie.
        try #require(getuid() != 0, "cannot make a file unremovable for root")
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("h.handoff"))
        request.post(token: "t1")

        // Read-only parent: the file is visible but cannot be unlinked.
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: dir.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path) }

        guard case .blocked = request.claim() else {
            Issue.record("expected .blocked for an unremovable request")
            return
        }
    }

    /// One install's sweep must not touch another's in-flight claim. The support
    /// directory is shared by every copy of the app on purpose, so a directory-wide
    /// sweep silently broke the other copy's dedupe.
    @Test func sweepingLeavesAnotherRequestsClaimsAlone() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let mine = HandoffRequest(url: dir.appendingPathComponent("instance-aaa.handoff"))
        let theirs = HandoffRequest(url: dir.appendingPathComponent("instance-bbb.handoff"))

        // Stand in for a claim each install has renamed aside but not yet read.
        let myClaim = dir.appendingPathComponent(
            HandoffRequest.claimPrefix(for: mine.url) + "1")
        let theirClaim = dir.appendingPathComponent(
            HandoffRequest.claimPrefix(for: theirs.url) + "1")
        try Data().write(to: myClaim)
        try Data().write(to: theirClaim)

        mine.sweepAbandonedClaims()
        #expect(!FileManager.default.fileExists(atPath: myClaim.path))
        #expect(FileManager.default.fileExists(atPath: theirClaim.path))
    }

    /// Scoped per install, like the lock it sits beside — two installs may
    /// legitimately run side by side.
    @Test func theRequestSitsBesideItsOwnLock() {
        let one = HandoffRequest.beside(lock: URL(fileURLWithPath: "/x/instance-aaa.lock"))
        let two = HandoffRequest.beside(lock: URL(fileURLWithPath: "/x/instance-bbb.lock"))
        #expect(one.url != two.url)
        #expect(one.url.path == "/x/instance-aaa.handoff")
    }
}
