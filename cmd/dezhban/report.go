package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
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

	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not create the bundle:", err)
		return 1
	}
	defer f.Close()
	z := zip.NewWriter(f)

	var notes []string
	// add takes the (body, err) pair a reader returns, so each call site reads
	// as one line rather than four.
	add := func(entry string, body string, err error) {
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
		if _, werr := w.Write([]byte(r.Text(body))); werr != nil {
			notes = append(notes, fmt.Sprintf("%s: truncated — %v", entry, werr))
		}
	}

	for _, item := range []struct {
		entry string
		read  func() (string, error)
	}{
		{"config.json", func() (string, error) { return readFileString(resolveConfigPath(*cfgPath)) }},
		{"state.json", func() (string, error) { return readFileString(defaultStatePath()) }},
		{"learned.json", func() (string, error) { return readFileString(defaultLearnedPath()) }},
		{"armed.json", func() (string, error) { return readFileString(defaultArmedPath()) }},
		{"applied-rules.json", func() (string, error) { return readFileString(applied.Path(stateDir())) }},
		{"doctor.json", func() (string, error) { return reportDoctor(*cfgPath) }},
		{"rules-preview.txt", func() (string, error) { return reportRulePreviews(*cfgPath) }},
		{"log.txt", reportLog},
	} {
		body, err := item.read()
		add(item.entry, body, err)
	}

	// The README goes in LAST, so it can name what was missing.
	if w, err := z.Create("README.txt"); err == nil {
		fmt.Fprint(w, reportReadme(stamp, r, notes))
	}
	if err := z.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "could not finish the bundle:", err)
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

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func reportDoctor(cfgPath string) (string, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return "", err
	}
	rep := runDoctor(cfg, newLogger(cfg), false)
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
	log := newLogger(cfg)
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
	recs, err := logread.Read(defaultLogPath(), logread.Options{Limit: reportLogLimit})
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "", fmt.Errorf("no log records at %s", defaultLogPath())
	}
	var b strings.Builder
	for _, rec := range recs {
		b.WriteString(rec.Raw)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func reportReadme(at time.Time, r *redact.Redactor, notes []string) string {
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
