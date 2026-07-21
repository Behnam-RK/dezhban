import Foundation

/// Mirrors internal/update.CheckResult's wire format (check.go) — the JSON
/// `dezhban upgrade check --json` prints.
struct UpgradeCheckResult: Codable, Equatable {
    let available: Bool
    let current: String
    let latest: String
    let url: String
}

/// Polls GitHub for a newer dezhban release. Runs ONLY here — in the GUI, in
/// user context, on launch and every ~24h — never in the root daemon: the
/// daemon's egress stays geo-providers-only, and an update check must never
/// become a second firewall pass alongside it (see CLAUDE.md's invariants and
/// internal/update.Check's doc comment). A human-triggered, user-context
/// check also matters for dezhban's own audience specifically — a root daemon
/// polling GitHub on a fixed cadence would be a stable fingerprint for a VPN
/// kill switch with a forbidden-country feature; this only ever happens when
/// someone is actually running the app.
enum UpdateChecker {
    private static let enabledKey = "checkForUpdates"

    /// Default on (mirrors NotificationManager.isEnabled's default) — but
    /// available to turn off entirely for anyone who doesn't want this host
    /// contacting GitHub at all, on any schedule.
    static var isEnabled: Bool {
        get { UserDefaults.standard.object(forKey: enabledKey) as? Bool ?? true }
        set { UserDefaults.standard.set(newValue, forKey: enabledKey) }
    }

    /// Runs `dezhban upgrade check --json` unprivileged and decodes the
    /// result. nil on ANY failure (CLI missing, tunnel down, bad JSON) —
    /// callers treat that as "nothing to report", not an error worth
    /// surfacing: a failed background check isn't itself informative to a
    /// user who didn't ask for one.
    static func check() -> UpgradeCheckResult? {
        guard isEnabled else { return nil }
        let result = DezhbanCLI.run(["upgrade", "check", "--json"])
        guard result.ok, let data = result.output.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(UpgradeCheckResult.self, from: data)
    }
}
