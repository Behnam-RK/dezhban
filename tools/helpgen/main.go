// Command helpgen renders the documentation the macOS app bundles.
//
// Dev tooling only — the same standing as tools/taskmenu. It is invoked by
// gui/macos/build-app.sh at package time and is never installed, never shipped,
// and never on the enforcement path.
//
//	go run ./tools/helpgen -docs docs -out dist/help
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/behnam-rk/dezhban/internal/help"
)

func main() {
	docs := flag.String("docs", "docs", "path to the docs/ directory")
	out := flag.String("out", "dist/help", "directory to write the rendered bundle into")
	flag.Parse()

	index, err := help.Build(*docs, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helpgen:", err)
		os.Exit(1)
	}
	fmt.Printf("helpgen: %d pages → %s\n", len(index), *out)
}
