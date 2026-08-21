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
    /// there to be found.
    @Test func aPostedRequestIsConsumedOnce() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("a.handoff"))

        request.post()
        #expect(request.consume())
        #expect(!request.consume())
    }

    /// Nothing waiting means nothing to do — this is asked once a second, so it
    /// must be quiet.
    @Test func noRequestIsNotARequest() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(!HandoffRequest(url: dir.appendingPathComponent("b.handoff")).consume())
    }

    /// A request outlives its click only briefly. If the incumbent died before
    /// consuming one, the next app to start must not inherit it and open a window
    /// nobody asked for.
    @Test func aStaleRequestIsDiscardedNotObeyed() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("c.handoff"))

        request.post()
        let later = Date().addingTimeInterval(HandoffRequest.freshness + 5)
        #expect(!request.consume(now: later))
        // Consumed anyway, so it is not re-examined on every tick forever.
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
        #expect(!request.consume(now: earlier))
    }

    /// `discard()` is what a process that has just taken the lock calls: whatever
    /// is on disk was meant for its predecessor.
    @Test func discardRemovesWithoutReporting() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let request = HandoffRequest(url: dir.appendingPathComponent("e.handoff"))

        request.post()
        request.discard()
        #expect(!request.consume())
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
