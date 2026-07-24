import Foundation
import os

/// Unified-logging handles for the menubar app.
///
/// The daemon's own log directory is root-owned, so the GUI cannot write there.
/// Unified logging is the surface that survives the app being killed or hung and
/// can be retrieved after the fact:
///
///     log show --last 1h --predicate 'subsystem == "sh.dezhban.menu"'
enum Diag {
    static let subsystem = "sh.dezhban.menu"
    static let watchdog = Logger(subsystem: subsystem, category: "watchdog")
}

/// Detects main-thread stalls — the "the app stops responding to clicks" symptom.
///
/// A background timer enqueues a ping on the main queue; the main queue answers
/// it by clearing the pending mark. If a ping is still unanswered when the next
/// timer tick fires, the main thread has been unable to run a block for at least
/// that long, which is what a beachball is. Crossing `threshold` logs the stall,
/// and the main thread coming back logs how long it was gone.
///
/// This observes only. The ping is an async enqueue, so the watchdog never
/// blocks the thread it is measuring, and it touches neither the daemon nor the
/// firewall. It exists because the beachballs have no cause visible in the
/// source: every subprocess and elevation call is already dispatched off-main,
/// so the culprit has to be caught in the act rather than reasoned about.
final class MainThreadWatchdog {
    private let interval: TimeInterval
    private let threshold: TimeInterval
    private let queue = DispatchQueue(label: "sh.dezhban.menu.watchdog")
    private var timer: DispatchSourceTimer?

    /// Both are owned by `queue`. `pendingSince` is nil whenever the main thread
    /// has answered the outstanding ping; `reported` keeps one stall from
    /// logging on every tick for as long as it lasts.
    private var pendingSince: Date?
    private var reported = false

    init(interval: TimeInterval = 0.5, threshold: TimeInterval = 1.0) {
        self.interval = interval
        self.threshold = threshold
    }

    func start() {
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + interval, repeating: interval, leeway: .milliseconds(100))
        t.setEventHandler { [weak self] in self?.tick() }
        timer = t
        t.resume()
    }

    private func tick() {
        if let since = pendingSince {
            // A ping is still outstanding: main has not run a block since `since`.
            // Don't enqueue another — they would just pile up behind the stall.
            let stalled = Date().timeIntervalSince(since)
            if stalled >= threshold, !reported {
                reported = true
                Diag.watchdog.error(
                    "main thread unresponsive for \(Self.secs(stalled), privacy: .public)")
            }
            return
        }
        let sent = Date()
        pendingSince = sent
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            // Hop back to the watchdog queue to mutate its state; the elapsed
            // time is measured from `sent`, so the hop's own cost isn't counted
            // as part of the stall.
            self.queue.async {
                if self.reported {
                    let waited = Date().timeIntervalSince(sent)
                    Diag.watchdog.error(
                        "main thread recovered after \(Self.secs(waited), privacy: .public)")
                }
                self.reported = false
                self.pendingSince = nil
            }
        }
    }

    private static func secs(_ t: TimeInterval) -> String {
        String(format: "%.1fs", t)
    }
}
