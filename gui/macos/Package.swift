// swift-tools-version:5.9
import PackageDescription

// DezhbanMenu is the macOS menubar client for the dezhban kill switch. It is a
// standalone Swift executable (no third-party deps — AppKit + ServiceManagement
// only) that reads the daemon's state file and drives the `dezhban` CLI. It is
// intentionally separate from the Go module so the Go binary stays 100%
// dependency-free. `build-app.sh` wraps `swift build` and assembles Dezhban.app.
let package = Package(
    name: "DezhbanMenu",
    platforms: [.macOS(.v13)], // SMAppService (login item) needs macOS 13+
    targets: [
        .executableTarget(
            name: "DezhbanMenu",
            path: "Sources/DezhbanMenu"
        )
    ]
)
