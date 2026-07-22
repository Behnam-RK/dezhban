import AppKit

/// Shared machinery for the Settings pane: raw-file seeding via `config get`,
/// batched writes via one `config set`, reset-to-defaults via `config reset
/// --all`, the explicit restart decision, and the post-restart posture check.
/// `config.Validate` (Go) stays the single validation authority, and the shipped
/// defaults live only in `config.Default()` — never mirrored in Swift.
enum ConfigApply {
    /// One `config get <key>` per field against the resolved config path — the raw
    /// on-disk file is the source of truth, never a cached second-schema mirror.
    /// Short-circuits on the first failure so an error string can never be seeded
    /// into a field (and later written back as a value by Apply). Calls back on
    /// the main queue with the resolved path, the values (in key order), or nil
    /// plus the failure text — the one resolution done here is also the one the
    /// caller needs for display, so it never has to resolve a second time itself.
    static func seed(keys: [String],
                     completion: @escaping (_ path: String, _ values: [String]?, _ error: String?) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            // Resolved HERE, not on the caller's main thread: resolving shells out,
            // and a shell-out on the main thread spins the run loop (DezhbanCLI.exec).
            let cfgPath = DezhbanCLI.resolvedConfigPath()
            let results = keys.map { (key: $0, result: DezhbanCLI.run(["config", "get", $0, "--config", cfgPath])) }
            DispatchQueue.main.async {
                if let failed = results.first(where: { !$0.result.ok }) {
                    let detail = failed.result.output.trimmingCharacters(in: .whitespacesAndNewlines)
                    completion(cfgPath, nil, "Failed to read \(failed.key): \(detail)")
                    return
                }
                completion(cfgPath, results.map { $0.result.output.trimmingCharacters(in: .whitespacesAndNewlines) }, nil)
            }
        }
    }

    /// The outcome of an apply run, delivered on the main queue.
    struct Outcome {
        let ok: Bool
        /// One-line status for the pane's footer.
        let status: String
        /// Full transcript for the Logs pane (nil: nothing worth showing).
        let transcriptTitle: String?
        let transcript: String?
    }

    /// Writes `pairs` through ONE batched `config set` (single validation, single
    /// write, single admin prompt), optionally followed by `restart` in the same
    /// batch — `set -e` semantics mean a rejected config never restarts anything.
    /// With `restart` and `awaitPosture`, waits (bounded) for the RESTARTED daemon
    /// to publish a posture rather than assuming the restart worked.
    static func apply(pairs: [String], restart: Bool, awaitPosture: Bool, title: String,
                      completion: @escaping (Outcome) -> Void) {
        runBatch(["config", "set"] + pairs,
                 restart: restart, awaitPosture: awaitPosture, title: title, completion: completion)
    }

    /// Restores every tunable to its shipped default through `config reset --all`,
    /// which deliberately PRESERVES identity — blockedCountries, tunnel
    /// interfaces, endpoints, and profiles — so a reset never silently unblocks a
    /// country or forgets the user's VPN. Same restart/posture plumbing as `apply`;
    /// the defaults themselves come from `config.Default()` in Go, so this pane
    /// never carries a second copy of the schema.
    static func resetAll(restart: Bool, awaitPosture: Bool, title: String,
                         completion: @escaping (Outcome) -> Void) {
        runBatch(["config", "reset", "--all"],
                 restart: restart, awaitPosture: awaitPosture, title: title, completion: completion)
    }

    /// `write` arrives WITHOUT `--config`; this appends it from the resolved path.
    /// The resolution happens on the background queue below rather than in `apply` /
    /// `resetAll`, which are called from button actions on the main thread — see
    /// DezhbanCLI.exec on why a main-thread shell-out is unsafe, not just slow.
    private static func runBatch(_ write: [String], restart: Bool, awaitPosture: Bool, title: String,
                                 completion: @escaping (Outcome) -> Void) {
        // Marked BEFORE the restart: only a snapshot published after this instant can
        // have come from the new daemon.
        let mark = Date()
        DispatchQueue.global(qos: .userInitiated).async {
            // Resolve the target path once and pass --config explicitly, so the write
            // and the daemon provably act on the same file rather than each
            // re-resolving it.
            var commands: [[String]] = [write + ["--config", DezhbanCLI.resolvedConfigPath()]]
            if restart {
                // `restart`, not stop-then-start: the CLI owns the in-between state. No
                // --config — it acts on the already-installed service unit, whose config
                // path was baked in at install time.
                commands.append(["restart"])
            }
            let result = DezhbanCLI.runPrivileged(batch: commands)
            guard result.ok else {
                DispatchQueue.main.async {
                    completion(Outcome(ok: false, status: "Rejected — see output.",
                                       transcriptTitle: "\(title) — not applied", transcript: result.output))
                }
                return
            }
            guard restart else {
                DispatchQueue.main.async {
                    completion(Outcome(ok: true, status: "Config saved; restart later to apply.",
                                       transcriptTitle: nil, transcript: nil))
                }
                return
            }
            guard awaitPosture else {
                DispatchQueue.main.async {
                    completion(Outcome(ok: true, status: "Applied and restarted.",
                                       transcriptTitle: nil, transcript: nil))
                }
                return
            }
            self.awaitPostureAfterRestart(since: mark, log: result.output, title: title, completion: completion)
        }
    }

    /// Waits (bounded, off the main thread) for the restarted daemon to publish a
    /// posture: launchd can accept the load and the daemon still fail to come up.
    ///
    /// `since` is what makes this a real check. The daemon writes a final
    /// posture="stopped" snapshot on clean shutdown (runner.publishStopped), so the
    /// state file is NOT empty between the stop and the start — reading whatever is
    /// there would hand back the dead daemon's goodbye note and call the restart a
    /// success. Only a snapshot stamped after the restart began counts.
    private static func awaitPostureAfterRestart(since mark: Date, log: String, title: String,
                                                 completion: @escaping (Outcome) -> Void) {
        // The daemon applies its startup ruleset and may take a geo reading before it
        // first publishes, so give it real time (a 5s poll used to report a scary
        // "restart incomplete" on a daemon that was merely still starting).
        let deadline = Date().addingTimeInterval(20)
        var posture: String?
        while Date() < deadline {
            Thread.sleep(forTimeInterval: 0.5)
            if let s = StateReader.read(), s.time > mark, s.posture != "stopped" {
                posture = s.posture
                break
            }
        }
        DispatchQueue.main.async {
            if let posture = posture {
                completion(Outcome(ok: true, status: "Restarted — posture: \(posture).",
                                   transcriptTitle: "\(title) — restarted",
                                   transcript: log + "resolved posture: \(posture)\n"))
            } else {
                completion(Outcome(ok: true, status: "Restarted, but no posture reported — run diagnostics.",
                                   transcriptTitle: "\(title) — no posture reported",
                                   transcript: log + """
                                       The service restarted but published no posture within 20s.

                                       The daemon writes its posture to \(StateReader.defaultPath). If that file
                                       is missing or unreadable, check that the service is actually running:

                                           dezhban status
                                           log show --last 5m --predicate 'process == "dezhban"'
                                       """))
            }
        }
    }

    /// The restart-window decision made explicit: no atomic reload exists
    /// (kardianos/service has no SIGHUP-style reconfigure, and Cleanup/panic
    /// deliberately shares the same rules-come-down path as `stop`), so this
    /// is disclosed plainly rather than papered over as seamless. Asked BEFORE
    /// elevating so the write and the restart go up as one batch under one prompt.
    static func confirmRestart() -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Restart dezhban to apply this change?"
        alert.informativeText = "A config change only takes effect when dezhban restarts. Network filtering is briefly disabled while it does (usually under a few seconds). Choosing “Save only” writes the config now and leaves the running daemon on its old settings."
        alert.addButton(withTitle: "Save and Restart")
        alert.addButton(withTitle: "Save Only")
        return alert.runModal() == .alertFirstButtonReturn
    }

    /// Blocking "that's not a duration" alert shared by both panes' pre-checks.
    static func invalidDurationAlert(_ label: String, _ value: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Invalid duration"
        alert.informativeText = "\(label) doesn't look like a Go duration string (e.g. \"30s\", \"5m\", \"1h30m\"): \"\(value)\""
        alert.runModal()
    }

    /// Superficial "looks like a Go duration" check: an optional sign, then
    /// either a bare "0" or one or more number(.number)?unit chunks, e.g. "30s",
    /// "5m", "1h30m", "500ms", "-1.5h", "0". Not a full parser — time.ParseDuration
    /// (via `config set`) remains the authority, so this errs permissive (it
    /// accepts everything ParseDuration does) and only exists to catch obviously
    /// wrong input before spending a privileged round trip.
    static func looksLikeGoDuration(_ s: String) -> Bool {
        guard !s.isEmpty else { return false }
        // Mirror ParseDuration: optional [-+], the special bare "0", or repeated
        // chunks of (number + unit). Each number needs at least one digit (before
        // or after the dot) so a bare unit like "s"/"ms" is rejected. Units: ns,
        // us/µs/μs, ms, s, m, h.
        let pattern = #"^[-+]?(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$"#
        return s.range(of: pattern, options: .regularExpression) != nil
    }
}
