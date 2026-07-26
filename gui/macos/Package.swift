// swift-tools-version:5.9
import PackageDescription

// DezhbanMenu is the macOS menubar client for the dezhban kill switch. It is a
// standalone Swift package (no third-party deps — AppKit + ServiceManagement
// only) that reads the daemon's state file and drives the `dezhban` CLI. It is
// intentionally separate from the Go module so the Go binary stays 100%
// dependency-free. `build-app.sh` wraps `swift build` and assembles Dezhban.app.
//
// DezhbanCore holds the app's testable logic layer (Snapshot decoding,
// posture→icon derivation, settings-field batching, duration text) — split out
// specifically so it can be unit-tested: an .executableTarget cannot be
// `@testable import`ed. It still imports AppKit/SwiftUI for the bits that are
// pure functions of their input despite that (NSImage/Color lookups, no
// subprocess or global state) — the split is about testability, not about
// which frameworks a file may import. DezhbanMenu is the executable (CLI
// shell-out, elevation, app lifecycle) and imports DezhbanCore.
let package = Package(
    name: "DezhbanMenu",
    platforms: [.macOS(.v13)], // SMAppService (login item) needs macOS 13+
    targets: [
        .target(
            name: "DezhbanCore",
            path: "Sources/DezhbanCore"
        ),
        .executableTarget(
            name: "DezhbanMenu",
            dependencies: ["DezhbanCore"],
            path: "Sources/DezhbanMenu"
        ),
        .testTarget(
            name: "DezhbanCoreTests",
            dependencies: ["DezhbanCore"],
            path: "Tests/DezhbanCoreTests"
        ),
    ]
)
