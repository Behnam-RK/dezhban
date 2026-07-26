import Foundation

/// Pure text-parsing helpers shared by the Settings pane and ConfigApply — no
/// AppKit, no subprocess, no state. Split out so they can be unit-tested
/// directly rather than only indirectly through a CLI round trip.
public enum DurationText {
    /// Superficial "looks like a Go duration" check: an optional sign, then
    /// either a bare "0" or one or more number(.number)?unit chunks, e.g. "30s",
    /// "5m", "1h30m", "500ms", "-1.5h", "0". Not a full parser — time.ParseDuration
    /// (via `config set`) remains the authority, so this errs permissive (it
    /// accepts everything ParseDuration does) and only exists to catch obviously
    /// wrong input before spending a privileged round trip.
    public static func looksLikeGoDuration(_ s: String) -> Bool {
        guard !s.isEmpty else { return false }
        // Mirror ParseDuration: optional [-+], the special bare "0", or repeated
        // chunks of (number + unit). Each number needs at least one digit (before
        // or after the dot) so a bare unit like "s"/"ms" is rejected. Units: ns,
        // us/µs/μs, ms, s, m, h.
        let pattern = #"^[-+]?(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$"#
        return s.range(of: pattern, options: .regularExpression) != nil
    }

    /// The keys the daemon could not adopt live, read from `config set`'s own
    /// report. Reading the CLI's answer rather than re-deriving it keeps the
    /// live/restart classification in exactly one place — the daemon, which is
    /// the only thing that knows what it actually built at startup. A GUI-side
    /// copy would be a second source of truth, and the one guaranteed to drift.
    ///
    /// `marker` below is therefore a contract, not a display string: it must stay
    /// identical to `restartMarker` in cmd/dezhban/config_cmd.go, which is pinned
    /// there by TestRestartMarkerIsTheContractTheAppScrapes so a reword cannot
    /// silently stop this scan from matching — which would report a key that is
    /// still waiting on a restart as fully applied.
    public static func pendingRestartKeys(in output: String) -> [String] {
        let marker = "Restart dezhban to apply:"
        for line in output.split(separator: "\n") {
            guard let r = line.range(of: marker) else { continue }
            return line[r.upperBound...]
                .split(separator: ",")
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .filter { !$0.isEmpty }
        }
        return []
    }
}
