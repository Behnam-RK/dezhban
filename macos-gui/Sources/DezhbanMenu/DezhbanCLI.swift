import Foundation

/// The outcome of a CLI invocation: whether it succeeded and everything it
/// printed (stdout + stderr combined), so callers can show the user what
/// actually happened instead of a bare pass/fail.
struct CommandResult {
    let ok: Bool
    let output: String
    /// The CLI's exit code. `refused` reads it to tell "the daemon said no" apart
    /// from "no daemon was there", which decides whether escalating to an admin
    /// prompt is legitimate.
    var status: Int32 = 0

    /// The daemon was reached and deliberately refused (see cmd/dezhban's
    /// ExitDaemonRefused). Retrying this as root would bypass the daemon's own
    /// gating, so callers must surface it instead of escalating.
    var refused: Bool { status == 3 }
}

/// Locates and invokes the `dezhban` CLI. Three tiers, by what the command needs:
///
///   - `run` — read-only inspect (doctor, version, status). Unprivileged.
///   - `runRoutine` — routine posture ops (block, unblock, switch). Unprivileged:
///     they reach the running daemon over its admin-group control socket, so they
///     complete with NO password prompt. This is the common case, and the reason
///     the socket exists.
///   - `runPrivileged` — service lifecycle (install/uninstall/start/stop) and
///     `panic`. Elevated through the native admin prompt via osascript. These
///     genuinely cannot be daemon-mediated: a daemon can't install, start or stop
///     itself, and panic must work when no daemon is running at all. They are rare
///     (install-time or emergency), so a prompt is the right cost.
///
/// Routine ops fall back to `runPrivileged` only when no daemon is listening. A
/// daemon REFUSAL (exit 3) is never escalated — see `CommandResult.refused`.
enum DezhbanCLI {
    /// Fallback config path if the CLI can't be asked (see cmd/dezhban
    /// defaultConfigPath). Prefer `resolvedConfigPath()`, which honors
    /// $DEZHBAN_CONFIG / the system path via `dezhban config path`.
    static let configPath = "/etc/dezhban/dezhban.json"

    /// Resolves the CLI binary from trusted, root-controlled absolute locations
    /// ONLY — never from $PATH. `runPrivileged` executes this path as root, so a
    /// $PATH-resolved candidate would be a privilege-escalation vector (a binary
    /// planted earlier in $PATH would run with administrator privileges).
    static func binaryPath() -> String? {
        let candidates = ["/usr/local/bin/dezhban", "/opt/homebrew/bin/dezhban"]
        let fm = FileManager.default
        for c in candidates where fm.isExecutableFile(atPath: c) { return c }
        return nil
    }

    /// Runs a privileged command via the native admin prompt, capturing what it
    /// printed. The wrapper captures the command's combined stdout+stderr and
    /// routes it so both outcomes surface it: on success `executeAndReturnError`
    /// returns it as stdout; on failure `do shell script` raises an AppleScript
    /// error whose message is the command's stderr, so the wrapper re-emits the
    /// captured output to stderr before exiting non-zero. Every call site gets
    /// real output instead of a bare pass/fail, so a failure alert can show what
    /// actually went wrong.
    @discardableResult
    static func runPrivileged(_ args: [String]) -> CommandResult {
        guard let bin = binaryPath() else {
            return CommandResult(ok: false, output: "dezhban CLI not found in a trusted install location")
        }
        let tokens = [bin] + args
        // Defense in depth: bin is a trusted absolute path and args are hardcoded
        // literals, but since these run through `do shell script … with
        // administrator privileges` as root, refuse any token carrying a single
        // quote or backslash rather than risk breaking the quoting into an
        // injection. (The alternative — argv without a shell — isn't available
        // through NSAppleScript's `do shell script`.)
        guard tokens.allSatisfy({ !$0.contains("'") && !$0.contains("\\") }) else {
            return CommandResult(ok: false, output: "refused: an argument contained a quote or backslash")
        }
        // Route the command's combined output so BOTH outcomes surface it.
        // `do shell script` returns stdout on success but reports a non-zero exit
        // as an AppleScript *error* whose message is the command's stderr — so a
        // plain `2>&1` would send failure diagnostics to stdout and leave the
        // error as a bare "error code N" (notably for `config set` validation
        // failures). Instead capture stdout+stderr in $out, print it to stdout on
        // success, and to stderr (then re-exit non-zero) on failure.
        let quoted = tokens.map { "'\($0)'" }.joined(separator: " ")
        let shellCmd = "out=$(\(quoted) 2>&1); rc=$?; if [ \"$rc\" -eq 0 ]; then printf '%s' \"$out\"; else printf '%s' \"$out\" >&2; exit \"$rc\"; fi"
        // Embed shellCmd as an AppleScript string literal: escape double-quotes.
        let escaped = shellCmd.replacingOccurrences(of: "\"", with: "\\\"")
        let source = "do shell script \"\(escaped)\" with administrator privileges"
        guard let script = NSAppleScript(source: source) else {
            return CommandResult(ok: false, output: "failed to construct AppleScript")
        }
        var errInfo: NSDictionary?
        let result = script.executeAndReturnError(&errInfo)
        if let errInfo = errInfo {
            let message = (errInfo[NSAppleScript.errorMessage] as? String)
                ?? (errInfo[NSAppleScript.errorBriefMessage] as? String)
                ?? "\(errInfo)"
            return CommandResult(ok: false, output: message)
        }
        return CommandResult(ok: true, output: result.stringValue ?? "")
    }

    /// Runs an unprivileged, read-only command (e.g. `doctor`, `version`,
    /// `config get`) and returns its captured output. Thin wrapper over `exec`
    /// for call sites that don't need the raw (status, out, err) tuple.
    static func run(_ args: [String]) -> CommandResult {
        guard let bin = binaryPath() else {
            return CommandResult(ok: false, output: "dezhban CLI not found in a trusted install location")
        }
        let r = exec(bin, args)
        return CommandResult(ok: r.status == 0, output: combinedOutput(r), status: r.status)
    }

    /// Runs a ROUTINE posture command (block / unblock / switch) unprivileged.
    ///
    /// These reach the running daemon over its control socket, which is group-
    /// writable by admins — so no password is needed. This is the whole point of
    /// the control socket: the daily operations stop prompting.
    ///
    /// The Swift side never speaks the socket protocol itself; the Go CLI is the
    /// single client, so both `dezhban block` in a terminal and this menu item take
    /// exactly the same path. DEZHBAN_NO_SUDO=1 makes the failure mode explicit
    /// rather than latent: with no daemon listening, the CLI reports the root error
    /// instead of trying to re-exec under sudo from a GUI process with no TTY (which
    /// could not prompt anyway). The caller then decides whether to escalate through
    /// the native admin prompt.
    static func runRoutine(_ args: [String]) -> CommandResult {
        guard let bin = binaryPath() else {
            return CommandResult(ok: false, output: "dezhban CLI not found in a trusted install location")
        }
        let r = exec(bin, args, env: ["DEZHBAN_NO_SUDO": "1"])
        return CommandResult(ok: r.status == 0, output: combinedOutput(r), status: r.status)
    }

    /// Whether the daemon's control socket is answering — i.e. whether routine ops
    /// will go through without a password. Parsed from `status`'s "daemon control"
    /// line, so the GUI and the CLI agree on one source of truth.
    static func daemonControlReachable() -> Bool {
        guard let bin = binaryPath() else { return false }
        let r = exec(bin, ["status"])
        guard r.status == 0 else { return false }
        for line in r.out.split(separator: "\n") where line.hasPrefix("daemon control:") {
            return line.contains("reachable") && !line.contains("unreachable")
        }
        return false
    }

    /// Whether the OS service is currently registered, per `status --json`'s
    /// merged service field (itself `internal/svc.Status()`) — the single
    /// source of truth, so the GUI never invents its own notion of "installed"
    /// that could drift from the CLI's.
    static func serviceInstalled() -> Bool {
        guard let bin = binaryPath() else { return false }
        let r = exec(bin, ["status", "--json"])
        guard r.status == 0, let data = r.out.data(using: .utf8) else { return false }
        struct StatusJSON: Decodable { let service: String }
        guard let decoded = try? JSONDecoder().decode(StatusJSON.self, from: data) else { return false }
        return decoded.service.hasPrefix("installed")
    }

    /// The daemon's currently-published enforcement posture from `status --json`,
    /// or nil if none is reported yet / the read failed. Reads stdout only (via
    /// `exec`, like `serviceInstalled()`) rather than `run`'s combined output —
    /// a warning on stderr with a 0 exit would otherwise corrupt the JSON parse.
    static func reportedPosture() -> String? {
        guard let bin = binaryPath() else { return nil }
        let r = exec(bin, ["status", "--json"])
        guard r.status == 0, let data = r.out.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let stateObj = obj["state"] as? [String: Any],
              let posture = stateObj["posture"] as? String, !posture.isEmpty
        else { return nil }
        return posture
    }

    /// The config path the daemon actually uses, asked from the CLI so the GUI
    /// agrees with the `--config` → $DEZHBAN_CONFIG → system-path resolution order
    /// instead of hardcoding one location. Falls back to `configPath`.
    static func resolvedConfigPath() -> String {
        guard let bin = binaryPath() else { return configPath }
        let r = exec(bin, ["config", "path"])
        // `config path` prints a single line: the winning path, or (when no file
        // is present) the default path followed by this exact fixed note. Strip
        // only that exact suffix — not any trailing parenthetical — so a real
        // path that itself ends in ")" (a directory with parentheses) survives.
        // Never split on spaces either: that truncates a valid path containing
        // them (e.g. "~/Library/Application Support/…").
        let absentNote = " (not present — using built-in defaults)"
        var path = (r.out.split(separator: "\n", maxSplits: 1).first.map(String.init) ?? "")
            .trimmingCharacters(in: .whitespaces)
        if path.hasSuffix(absentNote) {
            path = String(path.dropLast(absentNote.count)).trimmingCharacters(in: .whitespaces)
        }
        return (r.status == 0 && !path.isEmpty) ? path : configPath
    }

    // MARK: - logs

    /// `log`'s absolute path — a system binary, so it's fine to shell out to
    /// directly (no privilege implications, unlike `binaryPath()`'s allowlist).
    static let logBinary = "/usr/bin/log"
    private static let logPredicate = "process == \"dezhban\""

    /// `log show --last 1h --predicate 'process == "dezhban"'`, captured like
    /// any other read-only command.
    static func showRecentLogs() -> CommandResult {
        let r = exec(logBinary, ["show", "--last", "1h", "--predicate", logPredicate])
        return CommandResult(ok: r.status == 0, output: combinedOutput(r))
    }

    /// Args for a live `log stream` — used with `StreamingProcess`, the one
    /// action needing a running (not run-to-completion) child process.
    static let streamLogsArgs = ["stream", "--predicate", logPredicate]

    // MARK: - helpers

    /// Promoted from `private` so read-only call sites elsewhere in the app
    /// (log show/stream, status JSON parsing) can reuse this one capture path
    /// instead of each writing a second `Process` wrapper.
    static func exec(_ launchPath: String, _ args: [String], env: [String: String] = [:]) -> (status: Int32, out: String, err: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: launchPath)
        p.arguments = args
        if !env.isEmpty {
            // Overlay onto the inherited environment rather than replacing it: the
            // CLI still needs PATH/HOME (and honors $DEZHBAN_CONFIG) to behave the
            // same way it does in a terminal.
            p.environment = ProcessInfo.processInfo.environment.merging(env) { _, new in new }
        }
        let outPipe = Pipe(), errPipe = Pipe()
        p.standardOutput = outPipe
        p.standardError = errPipe
        do {
            try p.run()
        } catch {
            return (127, "", "\(error)")
        }
        // Drain both pipes without letting either block the other. Reading
        // stdout to EOF and only then stderr can deadlock: if the child fills
        // its stderr pipe buffer it blocks before exiting, so its stdout never
        // closes and we wait forever. Read stderr on one background thread and
        // stdout on this one. `errData` has a single writer (the background
        // thread) and a single reader (this thread) with the semaphore
        // establishing the happens-before between them — no shared mutation.
        var errData = Data()
        let errReady = DispatchSemaphore(value: 0)
        DispatchQueue.global(qos: .userInitiated).async {
            errData = errPipe.fileHandleForReading.readDataToEndOfFile()
            errReady.signal()
        }
        let outData = outPipe.fileHandleForReading.readDataToEndOfFile()
        errReady.wait()
        p.waitUntilExit()
        return (p.terminationStatus,
                String(data: outData, encoding: .utf8) ?? "",
                String(data: errData, encoding: .utf8) ?? "")
    }

    /// Joins stdout/stderr the way the output panel wants to show them: stdout
    /// first, then stderr if present, separated so a caller can tell them apart
    /// visually without a second field to thread through every call site.
    private static func combinedOutput(_ r: (status: Int32, out: String, err: String)) -> String {
        var parts: [String] = []
        if !r.out.isEmpty { parts.append(r.out) }
        if !r.err.isEmpty { parts.append(r.err) }
        return parts.joined(separator: "\n")
    }
}

/// A cancellable, streaming child process — used only for `log stream`'s
/// unbounded live output ("Stream live…"), the one place this app needs a
/// running process rather than a run-to-completion capture.
final class StreamingProcess {
    private let process = Process()
    private let pipe = Pipe()

    init(_ launchPath: String, _ args: [String]) {
        process.executableURL = URL(fileURLWithPath: launchPath)
        process.arguments = args
        process.standardOutput = pipe
        process.standardError = pipe
    }

    /// Starts the process, delivering output chunks to `onOutput` on the main
    /// queue as they arrive. Returns false if the process couldn't be launched.
    @discardableResult
    func start(onOutput: @escaping (String) -> Void) -> Bool {
        pipe.fileHandleForReading.readabilityHandler = { handle in
            let data = handle.availableData
            if data.isEmpty {
                // EOF — the child closed its output. Drop the handler so it
                // doesn't dangle, firing repeatedly on a closed handle.
                handle.readabilityHandler = nil
                return
            }
            // String(decoding:as:) never returns nil. availableData can split a
            // multi-byte UTF-8 sequence across two reads, which would make
            // String(data:encoding:) nil out and silently drop the whole chunk;
            // here the worst case is a lone U+FFFD at the split boundary.
            let text = String(decoding: data, as: UTF8.self)
            DispatchQueue.main.async { onOutput(text) }
        }
        do {
            try process.run()
            return true
        } catch {
            pipe.fileHandleForReading.readabilityHandler = nil
            return false
        }
    }

    /// Stops output delivery and terminates the child process if still running.
    /// Safe to call more than once (e.g. both a Stop-button tap and the output
    /// window closing).
    func stop() {
        pipe.fileHandleForReading.readabilityHandler = nil
        if process.isRunning {
            process.terminate()
        }
    }
}
