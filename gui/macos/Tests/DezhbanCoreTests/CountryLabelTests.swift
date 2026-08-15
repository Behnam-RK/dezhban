import Foundation
import Testing
@testable import DezhbanCore

/// The country-name fields are additive, so every one of these cases is really
/// the same question: what does the app show when talking to a daemon that
/// predates them? Never nothing — always at least the code it already had.
struct CountryLabelTests {
    private func snapshot(_ extra: String) -> Snapshot {
        let json = """
        {
            "time": "2026-07-25T10:00:00Z",
            "posture": "guard",
            "blocked": false,
            \(extra)
        }
        """.data(using: .utf8)!
        return StateReader.decode(json)!
    }

    @Test func namedCountryRendersNameAndCode() {
        let s = snapshot("""
        "countryCode": "KZ", "countryName": "Kazakhstan"
        """)
        #expect(s.countryLabel == "Kazakhstan (KZ)")
    }

    /// An older daemon sends countryCode with no countryName. Showing the bare
    /// code is right; showing nothing would report "no exit country" for a
    /// guard that knows exactly where it is exiting.
    @Test func missingNameFallsBackToTheBareCode() {
        #expect(snapshot("\"countryCode\": \"KZ\"").countryLabel == "KZ")
        #expect(snapshot("\"countryCode\": \"KZ\", \"countryName\": \"\"").countryLabel == "KZ")
    }

    @Test func noReadingHasNoLabel() {
        #expect(snapshot("\"ip\": \"203.0.113.9\"").countryLabel == nil)
        #expect(snapshot("\"countryCode\": \"\"").countryLabel == nil)
    }

    @Test func blockedListPrefersLabels() {
        let s = snapshot("""
        "blockedCountries": ["IR", "RU"],
        "blockedCountryNames": ["Iran (IR)", "Russia (RU)"]
        """)
        #expect(s.blockedCountryLabels == ["Iran (IR)", "Russia (RU)"])
    }

    @Test func blockedListFallsBackToCodesFromAnOlderDaemon() {
        let s = snapshot("\"blockedCountries\": [\"IR\", \"RU\"]")
        #expect(s.blockedCountryLabels == ["IR", "RU"])
        #expect(snapshot("\"ip\": \"1.1.1.1\"").blockedCountryLabels.isEmpty)
    }

    /// Wholesale fallback, never a per-index zip: the two arrays are written
    /// together, so a length mismatch means the pairing itself is untrustworthy
    /// and zipping them would put the wrong name on a country — the one mistake
    /// this feature must not make.
    @Test func aLengthMismatchFallsBackRatherThanMislabelling() {
        let s = snapshot("""
        "blockedCountries": ["IR", "RU", "KP"],
        "blockedCountryNames": ["Iran (IR)", "Russia (RU)"]
        """)
        #expect(s.blockedCountryLabels == ["IR", "RU", "KP"])
    }
}
