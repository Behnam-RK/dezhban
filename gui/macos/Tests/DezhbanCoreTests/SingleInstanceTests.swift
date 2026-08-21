import Foundation
import Testing
@testable import DezhbanCore

struct SingleInstanceTests {
    private func at(_ seconds: TimeInterval) -> Date {
        Date(timeIntervalSince1970: 1_700_000_000 + seconds)
    }

    /// The case that put this code here: registering the login agent execs a
    /// second copy while the first is serving the menubar. The newcomer yields.
    @Test func theNewcomerYieldsToTheAppAlreadyRunning() {
        let running = InstanceIdentity(pid: 100, launchedAt: at(0))
        let spawned = InstanceIdentity(pid: 900, launchedAt: at(60))
        #expect(SingleInstance.shouldYield(own: spawned, others: [running]))
        #expect(!SingleInstance.shouldYield(own: running, others: [spawned]))
    }

    /// The failure this ordering exists to prevent: if the rule were merely
    /// "does anyone else exist", two copies racing at login would both stand
    /// down and the Mac would end up with no app at all. Exactly one survives.
    @Test func exactlyOneSurvivesWhenEveryCopyAsksAtOnce() {
        let same = at(0)
        let copies = [
            InstanceIdentity(pid: 300, launchedAt: same),
            InstanceIdentity(pid: 100, launchedAt: same),
            InstanceIdentity(pid: 200, launchedAt: same),
        ]
        let survivors = copies.filter { own in
            !SingleInstance.shouldYield(own: own, others: copies.filter { $0.pid != own.pid })
        }
        #expect(survivors.count == 1)
        #expect(survivors.first?.pid == 100)
    }

    /// `NSRunningApplication.launchDate` is documented as optional. An instance
    /// whose age is unknown must lose, so the copy already serving the menubar
    /// keeps serving it rather than being displaced by a newcomer AppKit could
    /// not date.
    @Test func unknownAgeYields() {
        let dated = InstanceIdentity(pid: 500, launchedAt: at(10))
        let undated = InstanceIdentity(pid: 100, launchedAt: nil)
        #expect(SingleInstance.shouldYield(own: undated, others: [dated]))
        #expect(!SingleInstance.shouldYield(own: dated, others: [undated]))
    }

    /// Two undated copies still resolve — pid breaks the tie, so this cannot
    /// degrade into both-exit either.
    @Test func twoUndatedCopiesStillResolve() {
        let a = InstanceIdentity(pid: 100, launchedAt: nil)
        let b = InstanceIdentity(pid: 200, launchedAt: nil)
        #expect(!SingleInstance.shouldYield(own: a, others: [b]))
        #expect(SingleInstance.shouldYield(own: b, others: [a]))
    }

    /// The ordinary case — one app, nothing to yield to.
    @Test func aLoneInstanceNeverYields() {
        let only = InstanceIdentity(pid: 100, launchedAt: at(0))
        #expect(!SingleInstance.shouldYield(own: only, others: []))
    }

    /// A caller that fails to exclude this process from `others` must not make
    /// the app quit on every launch.
    @Test func seeingItselfInTheListIsNotADuplicate() {
        let me = InstanceIdentity(pid: 100, launchedAt: at(0))
        #expect(!SingleInstance.shouldYield(own: me, others: [me]))
    }
}
