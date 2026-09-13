package logread

import (
	"bufio"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// scannerRef is the bufio.Scanner loop this package used before, kept verbatim as
// a reference oracle. Duplicated on purpose: the claim it supports is "for a file
// with no over-cap line, NOTHING changed", and the only way to assert that is to
// keep the thing being compared against. It is short, frozen, and has one caller.
//
// IF THIS TEST FAILS, THE READER CHANGED — do not edit the oracle to match. An
// oracle edited to agree with the thing it checks is not an oracle, and the next
// reader of this file will believe it is. Either the change to readFile was
// unintended, or it was intended and this test should be deleted along with the
// claim it makes. Both are decisions; silently re-aligning the copy is not.
//
// Note the oracle calls the REAL ParseLine and the real filters are not exercised
// here (Options{} is empty), so a change to either does not rot this quietly: it
// either fails loudly or is out of scope.
func scannerRef(t *testing.T, path string) []Record {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, ParseLine(line))
	}
	return out
}

// The reader agrees with the scanner it replaced, record for record.
//
// Swapping bufio.Scanner for a hand-rolled bufio.Reader loop puts every ordinary
// line at risk to fix a pathological one, and the per-case tests around this one
// each check a single behaviour. This checks the whole of it at once, over random
// files built from the shapes that have caused trouble before: blank and
// whitespace-only lines, a line with no level, an unparseable line, a quoted value
// with an escaped quote, an unterminated quote, a trailing CR, a line long enough
// to span several reads, and — half the time — a file with no closing newline.
//
// Deterministic seed, so a failure is reproducible rather than a story about CI.
func TestTheReaderAgreesWithTheScannerItReplaced(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{
		"time=2026-09-08T10:00:00Z level=ERROR msg=hello k=v",
		"level=WARN msg=\"quoted value with spaces\" n=2",
		"", "   ", "\t",
		"no level at all, just prose",
		"time=bad level=INFO msg=x",
		"msg=\"escaped \\\" quote\" tail",
		strings.Repeat("z", 70000), // multi-fragment, under the cap
		"trailing-cr\r",
		"key=\"unterminated",
	}
	for i := 0; i < 300; i++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "dezhban.log")
		var b strings.Builder
		n := rng.Intn(12)
		for j := 0; j < n; j++ {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
			b.WriteString("\n")
		}
		if rng.Intn(2) == 0 && n > 0 { // sometimes no trailing newline
			s := b.String()
			b.Reset()
			b.WriteString(strings.TrimSuffix(s, "\n"))
		}
		writeRaw(t, path, b.String())

		want := scannerRef(t, path)
		got, err := readFile(path, Options{})
		if err != nil {
			t.Fatalf("iteration %d: err = %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d disagrees with the scanner\nbody %q\n got %+v\nwant %+v",
				i, b.String(), got, want)
		}
	}
}
