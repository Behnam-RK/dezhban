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

        request.post()
        #expect(request.claim() == .fresh)
        #expect(request.claim() == .absent)
    }

    /// Nothing waiting means nothing to do, which is the ordinary case every time
    /// the backstop looks.
    @Test func noRequestIsNotARequest() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(HandoffRequest(url: dir.appendingPathComponent("b.handoff")).claim() == .absent)
    }

    /// A request outlives its click only briefly. If the incumbent died before
    /// claiming one, the next app to start must not inherit it and open a window
    /// nobody asked for.
    @Test func aStaleRequestIsDiscardedNotObeyed() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("c.handoff"))

        request.post()
        let later = Date().addingTimeInterval(HandoffRequest.freshness + 5)
        #expect(request.claim(now: later) == .stale)
        // Taken anyway, so a file that will never be acted on stops being looked at.
        #expect(!FileManager.default.fileExists(atPath: request.url.path))
    }

    /// A clock that moved backwards must not turn an old request into an
    /// impossibly fresh one.
    @Test func aRequestFromTheFutureIsNotFresh() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("d.handoff"))

        request.post()
        let earlier = Date().addingTimeInterval(-3600)
        #expect(request.claim(now: earlier) == .stale)
    }

    /// `discard()` is what a process that has just taken the lock calls: whatever
    /// is on disk was meant for its predecessor.
    @Test func discardRemovesWithoutReporting() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("e.handoff"))

        request.post()
        request.discard()
        #expect(request.claim() == .absent)
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

        request.post()
        // Stands in for the other claimer winning between this one's stat and its
        // remove — the only way `.lost` can arise, and the reason `claim` takes
        // the hook.
        let claim = request.claim(interleaved: { request.discard() })
        #expect(claim == .lost)
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
