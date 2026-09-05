package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"

	"github.com/behnam-rk/dezhban/internal/config"
	"github.com/behnam-rk/dezhban/internal/firewall"
	"github.com/behnam-rk/dezhban/internal/netdetect"
	"github.com/behnam-rk/dezhban/internal/setup"
	"github.com/behnam-rk/dezhban/internal/vpnimport"
)

// cmdSetup runs an interactive wizard that builds a config and writes it, so the
// user never hand-edits JSON. It reuses the same detection/validation/preview
// helpers as detect-vpn, validate and print-rules. Requires a TTY.
//
// WHAT it asks — the questions, their order, and which answers unlock which
// follow-ups — lives in internal/setup, not here. This file is presentation:
// huh fields, the ruleset preview, and the save/install prompts. The macOS
// app's own first-run wizard reads the same question set over
// `setup --questions --json`, so the two cannot drift apart.
func cmdSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to write the config (default: canonical system path)")
	questions := fs.Bool("questions", false, "print the wizard's questions and exit (read-only, no TTY needed)")
	asJSON := fs.Bool("json", false, "with --questions, print them as JSON")
	_ = fs.Parse(args)

	// Seed from the current config so setup edits rather than clobbers; fall back
	// to defaults if none exists or the current file is unreadable/invalid.
	cfg, err := loadConfig(*cfgPath)
	configExisted := err == nil
	if err != nil {
		d := config.Default()
		cfg = &d
	}

	detected, _ := netdetect.TunnelInterfaces()
	qs := setup.Questions(setup.Options{
		Config: cfg, GOOS: runtime.GOOS, DetectedTunnels: detected,
	})

	// --questions is read-only and needs no terminal: it is how another surface
	// (the macOS first-run wizard) asks what this wizard would ask.
	if *questions {
		return printQuestions(qs, *asJSON)
	}

	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "dezhban setup needs an interactive terminal.")
		fmt.Fprintln(os.Stderr, "Non-interactive? Use 'dezhban config set <key> <value>' or edit the file directly.")
		return 1
	}

	answers := setup.NewAnswers(qs)
	// Asked a screenful at a time, in Group order, so a gate can be evaluated
	// against answers already given — which is exactly what makes the VPN
	// branch a branch.
	//
	// Within a group, in waves. A huh form binds every field before any of them
	// is answered, so a question gated on another question in the SAME group
	// would be decided by that question's seeded default rather than by what
	// the user just typed. The macOS app has no such problem — it re-evaluates
	// gates as answers change and shows the whole step at once, which is what
	// makes step 2 a single screen there — so rather than splitting the shared
	// question set to suit one renderer, this one asks the ungated questions,
	// re-evaluates, and asks whatever that opened up.
	for _, group := range groupsOf(qs) {
		asked := map[string]bool{}
		for {
			wave := nextWave(qs, group, asked, answers)
			if len(wave) == 0 {
				break
			}
			var fields []huh.Field
			for _, q := range wave {
				asked[q.ID] = true
				fields = append(fields, field(q, answers))
			}
			if err := runForm(huh.NewForm(huh.NewGroup(fields...))); err != nil {
				return formExit(err)
			}
		}
	}

	// Import any named config files into profiles (best-effort; a bad file is
	// reported but doesn't abort the wizard). Reading files is the caller's job,
	// not internal/setup's.
	// Only what the user was actually shown: the question is gated behind manual
	// mode, and reading it unconditionally would import files from a field this
	// run never rendered — the same "unasked means untouched" rule Apply follows
	// for keys, and what keeps this in step with the macOS app, whose
	// profileFiles is empty for exactly that reason.
	var profiles []config.Profile
	if q, ok := questionByID(qs, "profileFiles"); ok && answers.ShouldAsk(q) {
		for _, f := range setup.SplitList(answers.Text("profileFiles")) {
			eps, format, ierr := vpnimport.Extract(f)
			if ierr != nil {
				fmt.Fprintf(os.Stderr, "  skipping %s: %v\n", f, ierr)
				continue
			}
			name := baseName(f)
			fmt.Fprintf(os.Stderr, "  imported %s (%s): %s\n", name, format, strings.Join(eps, ", "))
			profiles = append(profiles, config.Profile{Name: name, Endpoints: eps})
		}
	}

	in := answers.Input(strconv.Itoa(cfg.Hysteresis), profiles)
	in.MacOS = runtime.GOOS == "darwin"
	in.ConfigExisted = configExisted
	setup.Apply(cfg, in)

	config.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "\nthat config isn't valid yet:", err)
		fmt.Fprintln(os.Stderr, "re-run 'dezhban setup' and adjust the flagged field.")
		return 1
	}

	// --- lockout guard: warn if an endpoint sits inside a tunnel subnet ---
	if warn := setup.EndpointLockoutWarning(cfg); warn != "" {
		var proceed bool
		fmt.Fprintln(os.Stderr, warn)
		if err := runForm(huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Save anyway?").
				Description("The flagged endpoint would very likely lock you out.").Value(&proceed),
		))); err != nil {
			return formExit(err)
		}
		if !proceed {
			fmt.Fprintln(os.Stderr, "setup cancelled — fix the endpoint (see 'dezhban doctor').")
			return 1
		}
	}

	// --- preview the exact ruleset, then confirm ---
	// There is one mode now (docs/adr/0001): guard, which degrades to the
	// FullBlock shape when there is no tunnel to pass yet. Note this preview is
	// static config, not the daemon's runtime STANDBY check (docs/adr/0002) — a
	// fresh config with no tunnel configured previews as a full block here, but
	// the running daemon idles rule-free until a tunnel is actually observed up.
	if pol, err := policyForMode(cfg, newLogger(cfg), "guard"); err == nil {
		if rules, err := firewall.RenderRules(pol); err == nil {
			fmt.Fprintf(os.Stderr, "\nRuleset this config would apply once armed:\n\n%s\n", rules)
		}
	}

	path := *cfgPath
	if path == "" {
		path = writeTargetPath(*cfgPath)
	}
	var save bool
	if err := runForm(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(fmt.Sprintf("Write config to %s?", path)).Value(&save),
	))); err != nil {
		return formExit(err)
	}
	if !save {
		fmt.Fprintln(os.Stderr, "not saved.")
		return 0
	}
	if err := writeConfig(path, cfg); err != nil {
		return saveError(path, err)
	}
	fmt.Printf("saved %s\n", path)

	// --- close the one-time-setup loop: offer to install + start the service ---
	installNow := true
	if err := runForm(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Install and start dezhban as a system service now?").
			Description("Runs it at boot and re-enforces on crash (admin password required).").
			Value(&installNow),
	))); err != nil {
		return formExit(err)
	}
	if installNow {
		if code := cmdService("install", []string{"--config", path}); code == 0 {
			_ = cmdService("start", []string{"--config", path})
		}
	} else {
		fmt.Println("later, enable it with: sudo dezhban install && sudo dezhban start")
	}
	fmt.Println("to connect a brand-new VPN whose server isn't known yet: dezhban switch, then connect it.")
	return 0
}

// field renders one question as a huh field bound to its answer.
//
// The mapping is deliberately total over setup's kinds: a kind this does not
// know falls through to a text input, which is wrong-looking but still
// answerable — better than a question silently disappearing from the wizard.
func field(q setup.Question, a *setup.Answers) huh.Field {
	switch q.Kind {
	case setup.KindBool:
		return huh.NewConfirm().Title(q.Title).Description(q.Description).Value(a.BoolPtr(q.ID))
	case setup.KindSelect:
		opts := make([]huh.Option[string], 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, huh.NewOption(o.Label, o.Value))
		}
		return huh.NewSelect[string]().Title(q.Title).Description(q.Description).
			Options(opts...).Value(a.TextPtr(q.ID))
	case setup.KindMultiSelect:
		selected := map[string]bool{}
		for _, v := range q.Selected {
			selected[v] = true
		}
		opts := make([]huh.Option[string], 0, len(q.Options))
		for _, o := range q.Options {
			opt := huh.NewOption(o.Label, o.Value)
			if selected[o.Value] {
				opt = opt.Selected(true)
			}
			opts = append(opts, opt)
		}
		return huh.NewMultiSelect[string]().Title(q.Title).Description(q.Description).
			Options(opts...).Value(a.ListPtr(q.ID))
	case setup.KindDuration:
		return huh.NewInput().Title(q.Title).Description(q.Description).
			Value(a.TextPtr(q.ID)).Validate(setup.ValidDuration)
	default:
		return huh.NewInput().Title(q.Title).Description(q.Description).Value(a.TextPtr(q.ID))
	}
}

// groupsOf lists the question groups in ascending order.
func groupsOf(qs []setup.Question) []int {
	var out []int
	seen := map[int]bool{}
	for _, q := range qs {
		if !seen[q.Group] {
			seen[q.Group] = true
			out = append(out, q.Group)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// printQuestions answers "what would setup ask me?" without asking anything —
// read-only, no root, no terminal needed. `--json` is what another wizard reads.
func printQuestions(qs []setup.Question, asJSON bool) int {
	if asJSON {
		out, err := json.MarshalIndent(qs, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "encode questions:", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	for _, q := range qs {
		fmt.Printf("%s (%s)\n", q.Title, q.Kind)
		if q.Description != "" {
			fmt.Printf("    %s\n", q.Description)
		}
		if q.Key != "" {
			fmt.Printf("    writes: %s\n", q.Key)
		}
		if q.Default != "" {
			fmt.Printf("    default: %s\n", q.Default)
		}
		if len(q.Selected) > 0 {
			fmt.Printf("    selected: %s\n", strings.Join(q.Selected, ", "))
		}
		if len(q.Options) > 0 {
			// Value is what gets written, but an option whose label says more
			// than its value — a tunnel offered because it is configured rather
			// than because it was detected — has to show that here too. This is
			// the form a human reads to answer "why is that one on the list?";
			// --json already carries both.
			vals := make([]string, 0, len(q.Options))
			for _, o := range q.Options {
				switch {
				case o.Label == "" || o.Label == o.Value:
					vals = append(vals, o.Value)
				case strings.Contains(o.Label, o.Value):
					// The label already carries the value, as the tunnel list's
					// "utun9 (configured, not up right now)" does.
					vals = append(vals, o.Label)
				default:
					vals = append(vals, fmt.Sprintf("%s (%s)", o.Value, o.Label))
				}
			}
			fmt.Printf("    options: %s\n", strings.Join(vals, ", "))
		}
		if q.Gated() {
			fmt.Printf("    asked only when %s is %s\n", q.RequiresID, q.RequiresValue)
		}
	}
	return 0
}

// runForm runs a huh form with a consistent theme.
func runForm(f *huh.Form) error {
	return f.WithTheme(huh.ThemeBase16()).Run()
}

// formExit maps a form error to an exit code, treating user-abort as a clean cancel.
func formExit(err error) int {
	if errors.Is(err, huh.ErrUserAborted) {
		fmt.Fprintln(os.Stderr, "setup cancelled.")
		return 130
	}
	fmt.Fprintln(os.Stderr, "setup error:", err)
	return 1
}

// isInteractive reports whether both stdin and stdout are terminals.
func isInteractive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// isTerminal reports whether f is a real TTY (not just a character device like
// /dev/null — that distinction matters for deciding whether sudo can prompt).
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
}

// questionByID finds a question in the set the wizard is running, so a caller
// can ask whether the user was actually shown it.
func questionByID(qs []setup.Question, id string) (setup.Question, bool) {
	for _, q := range qs {
		if q.ID == id {
			return q, true
		}
	}
	return setup.Question{}, false
}

// nextWave picks the questions of this group to put on the next form: those
// whose gate is already satisfied, minus any whose gate is about to be answered
// on that very form.
//
// It takes `asked` as read-only and leaves the marking to the caller, which is
// what makes the deferral work at all. Marking inside the selection pass — as
// this did — defeats it silently whenever a gating question appears BEFORE its
// dependents in the question set, which is the normal way to write one: by the
// time the dependent is examined, the gate it is waiting on has been marked
// asked earlier in the same pass, so it is never held back. The visible symptom
// was a re-run on a pinned config, where autoMode seeds to false and so all of
// step 2 satisfied its gate up front and arrived as one form instead of two —
// and ticking automatic detection on that form then retracted the endpoint
// answer the same form had just collected.
//
// Being a plain function over (questions, answers) rather than a loop body is
// also the only reason this is testable without driving a terminal;
// TestStepTwoArrivesInWaves covers it.
func nextWave(qs []setup.Question, group int, asked map[string]bool, a *setup.Answers) []setup.Question {
	var wave []setup.Question
	for _, q := range qs {
		if q.Group != group || asked[q.ID] || !a.ShouldAsk(q) {
			continue
		}
		if q.Gated() && stillToAsk(qs, q.RequiresID, group, asked, a) {
			continue
		}
		wave = append(wave, q)
	}
	return wave
}

// stillToAsk reports whether the question a gate points at is in this same group
// and is genuinely still coming — the only case where deferring is right.
//
// A gate pointing at an EARLIER group is already decided by the time this group
// runs. A gate pointing at a question this run will never show — because that
// question's own gate is unmet — is fixed at its seeded default, so deferring
// for it would strand the dependent question forever: the wave it waits for
// never arrives, the loop runs out of fields and breaks, and the question is
// silently never asked.
//
// That second case is only sound while gates are ONE deep. A gate question that
// is itself gated could become askable later, and releasing its dependent now
// would evaluate it against a seed — the very bug this loop was fixed for, one
// level down. Depth 1 is a property of the question set, not of this function,
// so it is pinned there by TestGatesAreShallowAndPointBackwards; make this
// predicate transitive before adding a gated gate.
func stillToAsk(qs []setup.Question, id string, group int, asked map[string]bool, a *setup.Answers) bool {
	for _, q := range qs {
		if q.ID != id {
			continue
		}
		return q.Group == group && !asked[q.ID] && a.ShouldAsk(q)
	}
	return false
}
