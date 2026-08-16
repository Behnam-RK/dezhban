import Foundation
import Testing
@testable import DezhbanCore

struct VPNInventoryTests {
    @Test func decodesFullInventory() throws {
        let json = """
        {
          "tunnels": ["utun5"],
          "connectedVPN": "RocketTunnel",
          "discoverySupported": true,
          "candidates": [
            {"vpn": "RocketTunnel", "server": "104.16.2.34", "port": 443,
             "process": "/Applications/RocketTunnel.app/PlugIns/PacketTunnel.appex/PacketTunnel"}
          ],
          "supportedVPNs": ["networkextension", ".appex", "vpn", "wireguard"],
          "tunnelPatterns": {"prefixes": ["utun", "wg"], "keywords": ["wireguard", "vpn"]}
        }
        """.data(using: .utf8)!
        let inv = try #require(VPNInventory.decode(json))
        #expect(inv.tunnels == ["utun5"])
        #expect(inv.connectedName == "RocketTunnel")
        #expect(inv.hasAnything)
        #expect(inv.candidates?.first?.displayName == "RocketTunnel")
        #expect(inv.tunnelPatterns?.prefixes == ["utun", "wg"])
    }

    @Test func decodesEmptyObject() throws {
        // Every field optional: a minimal or partial answer still decodes.
        let inv = try #require(VPNInventory.decode("{}".data(using: .utf8)!))
        #expect(!inv.hasAnything)
        #expect(inv.connectedName == nil)
    }

    @Test func garbageYieldsNil() {
        #expect(VPNInventory.decode("not json".data(using: .utf8)!) == nil)
    }

    @Test func candidateFallsBackToProcessName() throws {
        let json = """
        {"candidates": [{"server": "203.0.113.9", "port": 51820,
                         "process": "/usr/local/bin/wireguard-go"}]}
        """.data(using: .utf8)!
        let inv = try #require(VPNInventory.decode(json))
        #expect(inv.candidates?.first?.displayName == "wireguard-go")
        // A single attributed candidate names the Overview row even with no
        // connected NetworkExtension service.
        #expect(inv.connectedName == "wireguard-go")
    }

    @Test func multipleCandidatesDoNotGuessAConnectedName() throws {
        let json = """
        {"candidates": [{"process": "/a/one"}, {"process": "/b/two"}]}
        """.data(using: .utf8)!
        let inv = try #require(VPNInventory.decode(json))
        #expect(inv.connectedName == nil)
    }
}
