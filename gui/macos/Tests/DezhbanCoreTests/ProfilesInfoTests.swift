import Foundation
import Testing
@testable import DezhbanCore

struct ProfilesInfoTests {
    @Test func decodesConfigShowVPNBlock() throws {
        // A trimmed real `config show` shape: only the fields ProfilesInfo cares
        // about, plus unrelated top-level keys it must ignore rather than fail on.
        let json = """
        {
            "pollInterval": "15s",
            "vpn": {
                "endpoints": ["203.0.113.9"],
                "profiles": [
                    {"name": "home", "endpoints": ["198.51.100.1"], "tunnelHint": "wg"},
                    {"name": "work", "endpoints": ["198.51.100.2"]}
                ]
            }
        }
        """.data(using: .utf8)!

        let info = try #require(ProfilesInfo.decode(json))
        #expect(info.defaultEndpoints == ["203.0.113.9"])
        #expect(info.profiles.count == 2)
        #expect(info.profiles[0].name == "home")
        #expect(info.profiles[0].tunnelHint == "wg")
        #expect(info.profiles[1].tunnelHint == nil)
    }

    @Test func decodesAbsentProfilesAndEndpointsAsEmpty() throws {
        let json = "{ \"vpn\": {} }".data(using: .utf8)!
        let info = try #require(ProfilesInfo.decode(json))
        #expect(info.defaultEndpoints.isEmpty)
        #expect(info.profiles.isEmpty)
    }

    @Test func corruptDataFailsToDecode() {
        #expect(ProfilesInfo.decode("not json".data(using: .utf8)!) == nil)
    }
}
