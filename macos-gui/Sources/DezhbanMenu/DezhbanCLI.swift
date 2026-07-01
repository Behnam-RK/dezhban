import Foundation

/// Locates and invokes the `dezhban` CLI. Read-only inspect commands run
/// unprivileged; privileged commands (start/stop/block/unblock) are elevated
/// through the native admin prompt via osascript — no bundled helper tool, no
/// code-signing infra for the MVP. macOS caches the authorization for ~5 min, so
/// a burst of actions prompts once.
enum DezhbanCLI {
    /// The daemon's root-owned config path (see cmd/dezhban defaultConfigPath).
    static let configPath = "/etc/dezhban/dezhban.json"

    /// Resolves the CLI binary: common install dirs first, then $PATH.
    static func binaryPath() -> String? {
        let candidates = ["/usr/local/bin/dezhban", "/opt/homebrew/bin/dezhban"]
        let fm = FileManager.default
        for c in candidates where fm.isExecutableFile(atPath: c) { return c }
        return which("dezhban")
    }

    /// Runs a privileged command via the native admin prompt. Returns true on
    /// success (exit 0 and no AppleScript error), false otherwise.
    @discardableResult
    static func runPrivileged(_ args: [String]) -> Bool {
        guard let bin = binaryPath() else { return false }
        // Single-quote each token so paths with spaces survive the shell; our
        // tokens never contain single quotes.
        let shellCmd = ([bin] + args).map { "'\($0)'" }.joined(separator: " ")
        // Embed shellCmd as an AppleScript string literal: escape backslashes then
        // double-quotes.
        let escaped = shellCmd
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
        let source = "do shell script \"\(escaped)\" with administrator privileges"
        guard let script = NSAppleScript(source: source) else { return false }
        var errInfo: NSDictionary?
        script.executeAndReturnError(&errInfo)
        return errInfo == nil
    }

    // MARK: - helpers

    private static func which(_ name: String) -> String? {
        let r = exec("/usr/bin/which", [name])
        let path = r.out.trimmingCharacters(in: .whitespacesAndNewlines)
        return (r.status == 0 && !path.isEmpty) ? path : nil
    }

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
