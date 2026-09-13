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

@Suite("LogRecords zero timestamp")
struct LogRecordsZeroTimeTests {
    /// Go leaves `logread.Record.Time` zero for a line whose timestamp it could
    /// not read, and `encoding/json` writes that as "0001-01-01T00:00:00Z" — a
    /// perfectly valid RFC 3339 string. Decoding it as a real date meant the
    /// "record whose timestamp did not parse keeps a nil date" contract never
    /// fired, and the row showed a year-0001 clock time as though it were a real
    /// one.
    @Test func goesZeroTimeDecodesAsNoDate() throws {
        let json = Data("""
        [{"time":"0001-01-01T00:00:00Z","level":"WARN","msg":"unparseable line","raw":"garbage"}]
        """.utf8)
        let recs = try #require(LogRecord.decodeList(json))
        try #require(recs.count == 1)
        #expect(recs[0].time == nil)
        #expect(recs[0].msg == "unparseable line")
    }

    /// A real timestamp still decodes, so the guard above cannot have been
    /// implemented by dropping every date.
    @Test func aRealTimestampStillDecodes() throws {
        let json = Data("""
        [{"time":"2026-09-08T10:00:00.123456Z","level":"ERROR","msg":"real","raw":"real"}]
        """.utf8)
        let recs = try #require(LogRecord.decodeList(json))
        #expect(recs[0].time != nil)
    }
}

extension LogRecordsTests {
    /// The record logread puts in place of a line it could not read.
    ///
    /// It is the one record shape with no timestamp that a person actually sees —
    /// Recent problems fetches `--level warn`, so a gap lands in that list. The Go
    /// side pins what it emits; this pins that the pane can render it: a warning
    /// rather than an error, a date the row knows to omit (problemRow's
    /// `if let t = r.time` drops the column), and a detail line carrying how much
    /// was lost.
    @Test func theOversizedLineMarkerDecodesAsAWarningWithNoDate() throws {
        let json = """
        [{"time":"0001-01-01T00:00:00Z","level":"WARN",
          "msg":"log line too long to read; skipped",
          "attrs":[{"key":"logread.oversized","value":"5242880"},
                   {"key":"limit","value":"4194304"}],
          "raw":"level=WARN msg=\\"log line too long to read; skipped\\" logread.oversized=5242880 limit=4194304"}]
        """.data(using: .utf8)!

        let recs = try #require(LogRecord.decodeList(json))
        let r = try #require(recs.first)
        #expect(r.time == nil)
        #expect(r.isWarning)
        #expect(!r.isError)
        #expect(!r.msg.isEmpty)
        #expect(r.detail.contains("logread.oversized=5242880"))
    }
}
