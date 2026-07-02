import Foundation

/// Locates and invokes the `dezhban` CLI. Read-only inspect commands run
/// unprivileged; privileged commands (start/stop/block/unblock) are elevated
/// through the native admin prompt via osascript — no bundled helper tool, no
/// code-signing infra for the MVP. macOS caches the authorization for ~5 min, so
/// a burst of actions prompts once.
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

    /// Runs a privileged command via the native admin prompt. Returns true on
    /// success (exit 0 and no AppleScript error), false otherwise.
    @discardableResult
    static func runPrivileged(_ args: [String]) -> Bool {
        guard let bin = binaryPath() else { return false }
        let tokens = [bin] + args
        // Defense in depth: bin is a trusted absolute path and args are hardcoded
        // literals, but since these run through `do shell script … with
        // administrator privileges` as root, refuse any token carrying a single
        // quote or backslash rather than risk breaking the quoting into an
        // injection. (The alternative — argv without a shell — isn't available
        // through NSAppleScript's `do shell script`.)
        guard tokens.allSatisfy({ !$0.contains("'") && !$0.contains("\\") }) else { return false }
        let shellCmd = tokens.map { "'\($0)'" }.joined(separator: " ")
        // Embed shellCmd as an AppleScript string literal: escape double-quotes.
        let escaped = shellCmd.replacingOccurrences(of: "\"", with: "\\\"")
        let source = "do shell script \"\(escaped)\" with administrator privileges"
        guard let script = NSAppleScript(source: source) else { return false }
        var errInfo: NSDictionary?
        script.executeAndReturnError(&errInfo)
        return errInfo == nil
    }

    /// The config path the daemon actually uses, asked from the CLI so the GUI
    /// agrees with the `--config` → $DEZHBAN_CONFIG → system-path resolution order
    /// instead of hardcoding one location. Falls back to `configPath`.
    static func resolvedConfigPath() -> String {
        guard let bin = binaryPath() else { return configPath }
        let r = exec(bin, ["config", "path"])
        // `config path` prints the winning path (possibly followed by a note like
        // "(not present — using built-in defaults)"); take the first field.
        let first = r.out.split(whereSeparator: { $0 == " " || $0 == "\n" }).first.map(String.init) ?? ""
        return (r.status == 0 && !first.isEmpty) ? first : configPath
    }

    // MARK: - helpers

    private static func exec(_ launchPath: String, _ args: [String]) -> (status: Int32, out: String, err: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: launchPath)
        p.arguments = args
        let outPipe = Pipe(), errPipe = Pipe()
        p.standardOutput = outPipe
        p.standardError = errPipe
        do {
            try p.run()
        } catch {
            return (127, "", "\(error)")
        }
        let outData = outPipe.fileHandleForReading.readDataToEndOfFile()
        let errData = errPipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus,
                String(data: outData, encoding: .utf8) ?? "",
                String(data: errData, encoding: .utf8) ?? "")
    }
}
