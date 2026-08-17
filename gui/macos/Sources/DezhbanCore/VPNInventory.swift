import Foundation

/// Mirrors `dezhban detect-vpn --json` — the VPN inventory the Diagnostics
/// pane renders and the Overview's "VPN app" row reads. Every field is
/// optional and every value a raw string (the DoctorCheck convention): a CLI
/// older than the subcommand yields nil from `decode`, and callers hide the
/// UI rather than claiming anything.
public struct VPNInventory: Decodable {
    /// One discovered VPN transport socket: the far side of the connection a
    /// VPN client opened on the physical link, attributed by owning process.
    public struct Candidate: Decodable, Identifiable {
        public var id: String { "\(process ?? vpn ?? "?")@\(server ?? "?"):\(port ?? 0)" }
        public let vpn: String?
        public let server: String?
        public let port: Int?
        public let process: String?

        public init(vpn: String? = nil, server: String? = nil, port: Int? = nil, process: String? = nil) {
            self.vpn = vpn
            self.server = server
            self.port = port
            self.process = process
        }

        /// The candidate's display name: the attributed VPN service when known,
        /// else the last path component of the owning process.
        public var displayName: String {
            if let vpn, !vpn.isEmpty { return vpn }
            if let process, !process.isEmpty {
                return (process as NSString).lastPathComponent
            }
            return "Unknown VPN"
        }
    }

    /// Detected tunnel interfaces (the same scan the daemon's guard uses).
    public let tunnels: [String]?
    /// Friendly name of the connected NetworkExtension VPN (macOS scutil);
    /// nil/empty when none is connected or the platform can't say.
    public let connectedVPN: String?
    /// Whether endpoint discovery exists on this platform at all — distinct
    /// from discovery finding nothing.
    public let discoverySupported: Bool?
    public let candidates: [Candidate]?
    public let discoveryErr: String?
    /// Client-process patterns discovery can attribute a socket to. What lets
    /// the pane say "unrecognized" instead of just "not found".
    public let supportedVPNs: [String]?

    public struct Patterns: Decodable {
        public let prefixes: [String]?
        public let keywords: [String]?
    }

    public let tunnelPatterns: Patterns?

    public static func decode(_ data: Data) -> VPNInventory? {
        try? JSONDecoder().decode(VPNInventory.self, from: data)
    }

    /// The name to show on the Overview's "VPN app" row, nil when nothing is
    /// known: the connected service first, else a lone attributed candidate.
    public var connectedName: String? {
        if let connectedVPN, !connectedVPN.isEmpty { return connectedVPN }
        let cands = candidates ?? []
        if cands.count == 1 { return cands[0].displayName }
        return nil
    }

    /// Whether the inventory has anything worth a section at all.
    public var hasAnything: Bool {
        !(tunnels ?? []).isEmpty || !(candidates ?? []).isEmpty || !(connectedVPN ?? "").isEmpty
    }

    /// Whether an empty result can be quoted as an answer. A scan that errored
    /// — or that this platform has no discovery for — found nothing because it
    /// could not look, and "couldn't scan" is a different claim from "scanned,
    /// found none". Callers show the error instead of the empty state.
    public var scanConclusive: Bool {
        (discoveryErr ?? "").isEmpty && discoverySupported != false
    }
}
