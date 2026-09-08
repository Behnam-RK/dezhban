package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/behnam-rk/dezhban/internal/applied"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/logread"
	"github.com/behnam-rk/dezhban/internal/redact"
)

// reportLogLimit caps how many log records the bundle carries. Enough to hold
// the run that went wrong plus what led to it; small enough that the bundle
// stays something someone will actually attach to an issue.
const reportLogLimit = 2000

// cmdReport writes a diagnostic bundle — everything someone would otherwise ask
// for one file at a time — as a zip, and reports where it went.
//
// **Nothing is sent anywhere.** The bundle is written to a local directory and
// that is the end of it. That is not a limitation to work around later: this is
// a tool whose entire job is that traffic does not leave the machine, and
// CLAUDE.md already refuses `dezhban upgrade` its own firewall pass on the same
// reasoning. Whether the file is shared, and with whom, is the operator's call.
//
// Redaction is ON by default. IP addresses and hostnames are replaced with
// stable placeholders, so the same server is the same token everywhere it
// appears and the bundle stays diagnosable. `--include-network` produces the
// full-fidelity version, through the same code path — there is no second, less
// tested route for the unredacted case to drift down.
//
// Read-only: it reads config and daemon state and writes one file. No root
// (every input is world-readable by design), no firewall effects.
func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config file (JSON)")
	outDir := fs.String("out", ".", "directory to write the bundle into")
	includeNetwork := fs.Bool("include-network", false,
		"keep real IP addresses and hostnames (default: replaced with stable placeholders)")
	_ = fs.Parse(args)

	r := redact.New(!*includeNetwork)
	stamp := time.Now()
	name := fmt.Sprintf("dezhban-report-%s.zip", stamp.Format("20060102-150405"))
	path := filepath.Join(*outDir, name)

	// 0600, not os.Create's 0666: with --include-network this file holds the real
	// exit IP and the VPN server addresses, and it lands in a directory the user
	// picked — often a shared or synced one. Every other local account being able
	// to read it would undo the choice the flag exists to make deliberate. The
	// redacted bundle gets the same mode; nothing here is improved by being
	// world-readable.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not create the bundle:", err)
		return 1
	}
	// Closed explicitly below, not only deferred: a zip whose final flush failed
	// (a full disk is the ordinary way) is truncated, and exiting 0 over it would
	// hand the operator a corrupt bundle that looks like a good one. The defer
	// stays for the error returns above it, where a second Close is harmless.
	defer f.Close()
	z := zip.NewWriter(f)

	var notes []string
	// add takes the (body, err) pair a reader returns, so each call site reads
	// as one line rather than four. asJSON picks how the body is redacted: a
	// document with structure is WALKED, so a key decides what a value means,
	// while a genuinely textual entry gets the shape passes.
	add := func(entry string, body string, err error, asJSON bool) {
		if err != nil {
			// A missing input is not a failure: a host with no daemon has no
			// state file, and a bundle that refused to exist because of that
			// would be useless exactly when it is needed. Record what was
			// missing INSIDE the bundle, so the reader is never left guessing
			// whether a file was absent or silently dropped.
			notes = append(notes, fmt.Sprintf("%s: not included — %v", entry, err))
			return
		}
		w, werr := z.Create(entry)
		if werr != nil {
			notes = append(notes, fmt.Sprintf("%s: not included — %v", entry, werr))
			return
		}
		if _, werr := w.Write([]byte(redactEntry(r, body, asJSON))); werr != nil {
			notes = append(notes, fmt.Sprintf("%s: truncated — %v", entry, werr))
		}
	}

	for _, item := range bundleEntries(cfgPath) {
		body, err := item.read()
		add(item.entry, body, err, item.asJSON)
	}

	// The README goes in LAST, so it can name what was missing. It is also the
	// entry that carries the redaction statement, so its absence is the one
	// omission the bundle cannot describe from the inside — say it on stderr
	// instead of dropping it silently.
	if w, err := z.Create("README.txt"); err != nil {
		fmt.Fprintln(os.Stderr, "note: README.txt: not included —", err)
	} else if _, err := io.WriteString(w, reportReadme(stamp, r, notes)); err != nil {
		fmt.Fprintln(os.Stderr, "note: README.txt: truncated —", err)
	}
	// A failed final flush leaves a TRUNCATED zip on disk, and it looks exactly
	// like a good one — same name, same place, plausible size. Removing it is
	// the whole point of checking Close at all: a bundle that cannot be trusted
	// must not be sitting there to be attached to an issue.
	if err := z.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "could not finish the bundle:", err)
		removeTruncated(path)
		return 1
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "could not finish the bundle:", err)
		removeTruncated(path)
		return 1
	}

	fmt.Println(path)
	if r.Enabled {
		fmt.Fprintln(os.Stderr, "IP addresses and hostnames were replaced with stable placeholders.")
		fmt.Fprintln(os.Stderr, "Use --include-network for the full-fidelity version (do not post that publicly).")
	} else {
		fmt.Fprintln(os.Stderr, "WARNING: this bundle contains your real VPN server addresses and exit IP.")
		fmt.Fprintln(os.Stderr, "Do not post it publicly. Re-run without --include-network for a shareable one.")
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	return 0
}

// reportConfig reads the config file for the bundle, given the path dezhban
// resolved for it.
//
// resolveConfigPath returns "" when there is no file anywhere and dezhban is
// running on its built-in defaults — the ordinary state on a host that has
// never run `setup`, and exactly the host someone collects a bundle from. That
// is an answer, so it says so: handing readFileString the empty path printed
// `config.json: not included — open : no such file or directory`, which reads
// like a fault in the collector rather than a fact about the host.
func reportConfig(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no config file; dezhban is running on its built-in defaults")
	}
	return readFileString(path)
}

// removeTruncated deletes a bundle whose final write failed, and says so if it
// cannot — a file left behind after "could not finish the bundle" is the one
// outcome worse than the failure itself.
func removeTruncated(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "note: the incomplete bundle is still at %s — delete it: %v\n", path, err)
	}
}

// ORDER IS LOAD-BEARING, and TestTheBundleCollectsNameSourcesFirst pins it.
// config.json and learned.json carry the profile names; doctor.json and
// state.json write those same names into PROSE — "every learned address for
// work-nord has aged out" — where no shape and no key can see them. The
// redactor reaches them there by remembering what it has already replaced,
// so the two files that TEACH it the names come first. learned.json is
// ahead of state.json for that reason: a name known only to learned.json
// still has to be known before state's display strings are walked.
// bundleEntry is one file in the bundle: what to call it, how to read it, and
// whether it is a document to walk or text to pattern-match.
type bundleEntry struct {
	entry  string
	read   func() (string, error)
	asJSON bool
}

// bundleEntries lists the bundle's contents IN COLLECTION ORDER.
func bundleEntries(cfgPath *string) []bundleEntry {
	return []bundleEntry{
		{"config.json", func() (string, error) { return reportConfig(resolveConfigPath(*cfgPath)) }, true},
		{"learned.json", func() (string, error) { return readFileString(defaultLearnedPath()) }, true},
		{"state.json", func() (string, error) { return readFileString(defaultStatePath()) }, true},
		{"armed.json", func() (string, error) { return readFileString(defaultArmedPath()) }, true},
		{"applied-rules.json", func() (string, error) { return readFileString(applied.Path(stateDir())) }, true},
		{"doctor.json", func() (string, error) { return reportDoctor(*cfgPath) }, true},
		{"rules-preview.txt", func() (string, error) { return reportRulePreviews(*cfgPath) }, false},
		{"log.txt", reportLog, false},
	}
}

// redactEntry redacts one entry body: a document is WALKED, a textual entry gets
// the shape passes.
//
// ONE pass, not two. This was an assignment followed by a conditional overwrite,
// and the overwritten pass still MINTED — Text reads `vpn.endpoints` in a doctor
// fix as a hostname, which the walk deliberately keeps, so the README's legend
// counted a token appearing nowhere in the bundle and pushed every real token's
// ordinal past it. A Redactor remembers, so a pass whose output you throw away
// is not free.
func redactEntry(r *redact.Redactor, body string, asJSON bool) string {
	if asJSON {
		return r.JSON(body)
	}
	return r.Text(body)
}

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// quietLogger discards everything the bundle's collectors log.
//
// newLogger writes to stderr, and runDoctor and policyForMode log autodetect
// and resolution warnings carrying REAL hostnames and endpoints. Printing those
// during a redacted `report` puts the identifiers the bundle just replaced onto
// the operator's terminal, interleaved with the `note:` lines — and a terminal
// session pasted beside a "safe to share" bundle undoes the redaction as
// completely as a missed field would. Their findings reach the bundle through
// doctor.json and rules-preview.txt, which do go through the redactor.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func reportDoctor(cfgPath string) (string, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return "", err
	}
	rep := runDoctor(cfg, quietLogger(), false)
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// reportRulePreviews renders what each posture would apply. Purely: this is the
// same rendering `print-rules` does, and it installs nothing.
func reportRulePreviews(cfgPath string) (string, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return "", err
	}
	log := quietLogger()
	var b strings.Builder
	for _, mode := range []string{"guard", "fullblock", "switch"} {
		fmt.Fprintf(&b, "===== %s =====\n", mode)
		pol, err := policyForMode(cfg, log, mode)
		if err != nil {
			fmt.Fprintf(&b, "(could not build this policy: %v)\n\n", err)
			continue
		}
		rules, err := firewall.RenderRules(pol)
		if err != nil {
			fmt.Fprintf(&b, "(could not render: %v)\n\n", err)
			continue
		}
		b.WriteString(rules)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func reportLog() (string, error) {
	// An error here can arrive WITH records, when one file in the rotation chain
	// was unreadable. Losing the readable half over that would drop the log from
	// a bundle collected precisely because something went wrong, so the problem
	// is written into the entry and the records are kept.
	recs, err := logread.Read(defaultLogPath(), logread.Options{Limit: reportLogLimit})
	if err != nil && len(recs) == 0 {
		return "", err
	}
	if len(recs) == 0 {
		return "", fmt.Errorf("no log records at %s", defaultLogPath())
	}
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "# part of the log could not be read: %v\n", err)
	}
	for _, rec := range recs {
		b.WriteString(rec.Raw)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func reportReadme(at time.Time, r *redact.Redactor, notes []string) string {
	// Notes carry error text from the readers above, and an error can quote the
	// value it choked on — a malformed endpoint in the config is exactly that
	// shape. They ship inside the bundle, so they go through the same redactor as
	// every other body: this package must never claim to have redacted something
	// it did not. Done FIRST, so any placeholder a note mints is counted by the
	// legend rendered below rather than missing from it.
	redacted := make([]string, len(notes))
	for i, n := range notes {
		redacted[i] = r.Text(n)
	}
	notes = redacted

	var b strings.Builder
	fmt.Fprintf(&b, "dezhban diagnostic bundle\n")
	fmt.Fprintf(&b, "collected %s\n", at.Format(time.RFC3339))
	fmt.Fprintf(&b, "dezhban %s (%s)\n\n", buildStamp.Version, buildStamp.short())

	b.WriteString("Contents\n")
	b.WriteString("  config.json         your configuration, as dezhban resolved it\n")
	b.WriteString("  state.json          dezhban's last published posture\n")
	b.WriteString("  learned.json        VPN endpoints dezhban learned by observation\n")
	b.WriteString("  armed.json          whether a tunnel has ever been observed up on this host\n")
	b.WriteString("  applied-rules.json  the ruleset dezhban last installed, and when\n")
	b.WriteString("  doctor.json         the same checks `dezhban doctor` reports\n")
	b.WriteString("  rules-preview.txt   what each posture WOULD apply, rendered without applying\n")
	b.WriteString("  log.txt             recent records from dezhban's own log\n\n")

	if r.Enabled {
		b.WriteString("Redaction\n")
		b.WriteString("  IP addresses and hostnames have been replaced with stable placeholders:\n")
		b.WriteString("  the same address is the same token everywhere it appears, so this bundle\n")
		b.WriteString("  is still diagnosable. Loopback, private, link-local and multicast addresses\n")
		b.WriteString("  are kept as-is — they identify nobody, and hiding them would make the\n")
		b.WriteString("  rulesets unreadable. The geo-provider hostnames dezhban ships are kept for\n")
		b.WriteString("  the same reason.\n\n")
		if legend := r.Legend(); len(legend) > 0 {
			b.WriteString("  What was replaced (the originals are deliberately not listed here):\n")
			for _, line := range legend {
				fmt.Fprintf(&b, "    %s\n", line)
			}
			b.WriteString("\n")
		}
		b.WriteString("  The JSON entries are re-serialised as they are redacted, so their\n")
		b.WriteString("  whitespace may differ from the file on disk, and a byte that was not\n")
		b.WriteString("  valid UTF-8 comes back as U+FFFD. Key order does not change.\n\n")
		b.WriteString("  Re-run with --include-network for the full-fidelity version. Do not post\n")
		b.WriteString("  that one publicly.\n\n")
	} else {
		b.WriteString("Redaction\n")
		b.WriteString("  NONE — this bundle was collected with --include-network and contains your\n")
		b.WriteString("  real VPN server addresses and public exit IP. Do not post it publicly.\n\n")
	}

	if len(notes) > 0 {
		b.WriteString("Not included\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
		b.WriteString("\n  A missing file is usually ordinary: a host where dezhban has never run\n")
		b.WriteString("  has no state, and one in standby has applied no rules.\n\n")
	}

	b.WriteString("Nothing in this bundle was sent anywhere. It was written to a local file and\n")
	b.WriteString("that is all — sharing it is your decision.\n")
	return b.String()
}
