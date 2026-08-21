import Foundation
import Testing
@testable import DezhbanCore

/// Parsing lives in Go (internal/logread), where the format is written. This is
/// the consumer side: that the app decodes what `dezhban logs --json` emits.
struct LogRecordsTests {
    static let json = """
    [
      {"time":"2026-08-21T12:06:53.234567+03:30","level":"WARN",
       "msg":"cannot resolve provider",
       "attrs":[{"key":"host","value":"ipinfo.io"},{"key":"err","value":"no such host"}],
       "raw":"time=... level=WARN msg=\\"cannot resolve provider\\""},
      {"time":"2026-08-21T12:07:00Z","level":"ERROR","msg":"install startup guard","raw":"raw2"}
    ]
    """

    @Test func decodesWhatTheCLIEmits() throws {
        let recs = try #require(LogRecord.decodeList(Data(Self.json.utf8)))
        #expect(recs.count == 2)
        #expect(recs[0].isWarning)
        #expect(!recs[0].isError)
        #expect(recs[1].isError)
        #expect(recs[0].time != nil, "Go's fractional RFC 3339 must parse")
        #expect(recs[1].time != nil, "whole-second RFC 3339 must parse too")
    }

    /// The attr order is the daemon's; it reads as a sentence. A dictionary
    /// anywhere on this path would shuffle it, which is why Go carries ordered
    /// pairs all the way here.
    @Test func attrOrderIsPreserved() throws {
        let recs = try #require(LogRecord.decodeList(Data(Self.json.utf8)))
        #expect(recs[0].attrs.map(\.key) == ["host", "err"])
        #expect(recs[0].detail == "host=ipinfo.io  err=no such host")
    }

    /// A retry loop emits the same message, level and timestamp repeatedly. An
    /// id derived from the content would collapse them into one row and hide
    /// exactly the thing that says it is a loop.
    @Test func identicalRecordsRemainDistinct() throws {
        let json = """
        [{"level":"WARN","msg":"same","raw":"x"},{"level":"WARN","msg":"same","raw":"x"}]
        """
        let recs = try #require(LogRecord.decodeList(Data(json.utf8)))
        #expect(recs.count == 2)
        #expect(recs[0].id != recs[1].id)
        #expect(Set(recs).count == 2)
    }

    /// A record whose timestamp does not parse is still a record. Dropping it
    /// would break the one rule this whole path lives by.
    @Test func anUnparseableTimestampDoesNotDropTheRecord() throws {
        let json = """
        [{"time":"not a time","level":"ERROR","msg":"still here","raw":"r"}]
        """
        let recs = try #require(LogRecord.decodeList(Data(json.utf8)))
        #expect(recs.count == 1)
        #expect(recs[0].time == nil)
        #expect(recs[0].msg == "still here")
    }

    /// "There were none" is the GOOD answer and must be distinguishable from
    /// "could not ask", which is why the CLI emits [] rather than null.
    @Test func anEmptyListIsNotAFailureToDecode() throws {
        let recs = try #require(LogRecord.decodeList(Data("[]".utf8)))
        #expect(recs.isEmpty)
    }
}
