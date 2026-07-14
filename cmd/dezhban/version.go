package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Build stamps, injected by the Taskfile via
// -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
//
// They are empty in a plain `go build ./cmd/dezhban` or a `go install`, which is
// exactly the case buildStamp recovers: the Go toolchain embeds VCS information
// in every binary built inside a git work tree, so an un-stamped build can still
// report the commit it came from instead of an anonymous "dev".
var (
	version = ""
	commit  = ""
	date    = ""
)

// buildInfo is the resolved build identity: the ldflags stamps where present,
// otherwise whatever the toolchain recorded.
type buildInfo struct {
	Version  string // "v0.1.0", "v0.1.0-3-gabc123-dirty", or "(devel)"
	Commit   string // full SHA, may be empty outside a git tree
	Dirty    bool   // the tree had uncommitted changes at build time
	Date     string // RFC3339; the commit time for an un-stamped build
	Go       string
	Platform string
}

// buildStamp is resolved once at startup. Package-level initialisation is safe
// here: -X sets the string vars at link time, so they are already populated.
var buildStamp = resolveBuild()

func resolveBuild() buildInfo {
	b := buildInfo{
		Version:  version,
		Commit:   commit,
		Date:     date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		// Main.Version is a real module version for `go install module@vX.Y.Z`,
		// but a synthesised pseudo-version ("v0.0.0-<date>-<sha>") for a build
		// from a source tree. The pseudo-version only restates the commit and
		// date printed below it, so drop it in favour of a plain "(devel)".
		if b.Version == "" && info.Main.Version != "" &&
			!strings.HasPrefix(info.Main.Version, "v0.0.0-") {
			b.Version = info.Main.Version
		}
		if b.Version == "" && info.Main.Version != "" {
			b.Version = "(devel)"
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = s.Value
				}
			case "vcs.modified":
				b.Dirty = b.Dirty || s.Value == "true"
			case "vcs.time":
				if b.Date == "" {
					b.Date = s.Value
				}
			}
		}
	}

	if b.Version == "" {
		b.Version = "dev"
	}
	// `git describe --dirty` already carries the suffix; don't say it twice.
	if strings.HasSuffix(b.Version, "-dirty") {
		b.Dirty = true
	}
	return b
}

// short is the abbreviated commit, for the one-line forms.
func (b buildInfo) short() string {
	if len(b.Commit) > 12 {
		return b.Commit[:12]
	}
	return b.Commit
}

// line is the single-line identity used by `version` and the `status` header.
func (b buildInfo) line() string {
	return "dezhban " + b.Version
}

// cmdVersion prints the build identity: one line by default, the full stamp
// under the global -v/--verbose.
func cmdVersion() int {
	fmt.Println(buildStamp.line())
	if !verbose {
		return 0
	}
	if c := buildStamp.short(); c != "" {
		state := "clean"
		if buildStamp.Dirty {
			state = "dirty"
		}
		fmt.Printf("  commit: %s (%s)\n", c, state)
	}
	if buildStamp.Date != "" {
		fmt.Println("  built: ", buildStamp.Date)
	}
	fmt.Println("  go:    ", buildStamp.Go, buildStamp.Platform)
	return 0
}
