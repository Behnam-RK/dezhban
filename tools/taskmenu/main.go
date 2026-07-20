// taskmenu is the interactive picker behind a bare `task` on a TTY: pick a
// flow, answer the prompts for the vars it takes, and it execs `task <name>
// KEY=VAL…`. It is dev tooling only — never part of the daemon or enforcement
// path — and uses the same huh dependency the setup wizard already vendors.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
)

// runField runs a single huh field with Esc added to the quit binding —
// the default keymap only aborts on Ctrl-C, and a picker you can't Esc
// out of feels stuck.
func runField(f huh.Field) error {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "quit"))
	return huh.NewForm(huh.NewGroup(f)).WithKeyMap(km).Run()
}

type flow struct {
	name string
	desc string
	sudo bool
	// gated flows carry their own confirmation (task prompt:, /dev/tty read,
	// or the sudo password itself), so the picker must not stack another one.
	gated bool
	vars  func() ([]string, error)
}

type group struct {
	name  string
	flows []flow
}

var releaseVersionRE = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

func groups() []group {
	return []group{
		{"everyday", []flow{
			{name: "build", desc: "compile for this host into ./dezhban"},
			{name: "check", desc: "vet + test + lint"},
			{name: "dev", desc: "fast roll: rebuild CLI+GUI, swap, restart", sudo: true},
			{name: "clean", desc: "remove build artifacts"},
		}},
		{"safe loop (no root, no firewall effects)", []flow{
			{name: "monitor", desc: "build + run the monitor in dry-run, Ctrl-C to stop", vars: askConfig},
			{name: "validate", desc: "parse + validate a config", vars: askConfig},
			{name: "rules", desc: "print the ruleset for a mode, don't apply", vars: askRules},
			{name: "doctor", desc: "diagnose VPN-guard config", vars: askConfig},
			{name: "status", desc: "current posture"},
		}},
		{"real install (macOS installer)", []flow{
			{name: "pkg", desc: "build dist/dezhban-<version>.pkg"},
			{name: "install", desc: "pkg + macOS Installer + open app", sudo: true, vars: askFresh},
			{name: "uninstall", desc: "run the uninstaller (config kept)", sudo: true, gated: true},
			{name: "panic", desc: "force-remove firewall rules — lockout escape hatch", sudo: true, gated: true},
		}},
		{"release", []flow{
			{name: "release:check", desc: "preflight: safe to release right now?"},
			{name: "release:preview", desc: "dry run: version, notes, CHANGELOG diff", vars: askVersionSpec},
			{name: "release", desc: "cut a release: confirm, dispatch, watch", gated: true, vars: askVersionSpec},
		}},
	}
}

func askConfig() ([]string, error) {
	config := "configs/dezhban.local.json"
	err := runField(huh.NewInput().
		Title("Config file").
		Value(&config).
		Validate(func(s string) error {
			if _, statErr := os.Stat(s); statErr != nil {
				return fmt.Errorf("no such file: %s", s)
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return []string{"CONFIG=" + config}, nil
}

func askRules() ([]string, error) {
	args, err := askConfig()
	if err != nil {
		return nil, err
	}
	mode := "guard"
	err = runField(huh.NewSelect[string]().
		Title("Mode").
		Options(
			huh.NewOption("guard — always-on VPN interface guard", "guard"),
			huh.NewOption("fullblock — exit country blocked; everything cut but the handshake", "fullblock"),
			huh.NewOption("switch — the bounded relaxation for connecting a new VPN", "switch"),
		).
		Value(&mode))
	if err != nil {
		return nil, err
	}
	return append(args, "MODE="+mode), nil
}

func askFresh() ([]string, error) {
	fresh := false
	err := runField(huh.NewConfirm().
		Title("Wipe first? (runs the uninstaller before installing; config kept)").
		Value(&fresh))
	if err != nil {
		return nil, err
	}
	if fresh {
		return []string{"FRESH=1"}, nil
	}
	// Explicit FRESH=0: the answer was already given here, so the task's own
	// on-demand question must not re-ask.
	return []string{"FRESH=0"}, nil
}

func askVersionSpec() ([]string, error) {
	choice := "patch"
	err := runField(huh.NewSelect[string]().
		Title("Version").
		Options(
			huh.NewOption("bump patch", "patch"),
			huh.NewOption("bump minor", "minor"),
			huh.NewOption("bump major", "major"),
			huh.NewOption("bump rc", "rc"),
			huh.NewOption("specific version…", "version"),
		).
		Value(&choice))
	if err != nil {
		return nil, err
	}
	if choice != "version" {
		return []string{"BUMP=" + choice}, nil
	}
	version := ""
	err = runField(huh.NewInput().
		Title("Version (X.Y.Z or X.Y.Z-rc.N)").
		Value(&version).
		Validate(func(s string) error {
			if !releaseVersionRE.MatchString(s) {
				return errors.New("expected X.Y.Z or X.Y.Z-rc.N")
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return []string{"VERSION=" + version}, nil
}

func pick() (flow, error) {
	all := groups()
	groupOpts := make([]huh.Option[int], len(all))
	for i, g := range all {
		groupOpts[i] = huh.NewOption(g.name, i)
	}
	gi := 0
	if err := runField(huh.NewSelect[int]().Title("dezhban — dev tasks").Options(groupOpts...).Value(&gi)); err != nil {
		return flow{}, err
	}

	g := all[gi]
	flowOpts := make([]huh.Option[int], len(g.flows))
	for i, f := range g.flows {
		label := fmt.Sprintf("%-10s %s", f.name, f.desc)
		if f.sudo {
			label += "  (sudo)"
		}
		flowOpts[i] = huh.NewOption(label, i)
	}
	fi := 0
	if err := runField(huh.NewSelect[int]().Title(g.name).Options(flowOpts...).Value(&fi)); err != nil {
		return flow{}, err
	}
	return g.flows[fi], nil
}

func run() error {
	f, err := pick()
	if err != nil {
		return err
	}

	var vars []string
	if f.vars != nil {
		if vars, err = f.vars(); err != nil {
			return err
		}
	}

	argv := append([]string{"task", f.name}, vars...)
	cmdline := strings.Join(argv, " ")

	if f.sudo && !f.gated {
		confirmed := false
		if err := runField(huh.NewConfirm().Title("Run `" + cmdline + "`? (needs sudo)").Value(&confirmed)); err != nil {
			return err
		}
		if !confirmed {
			return huh.ErrUserAborted
		}
	}

	taskBin, err := exec.LookPath("task")
	if err != nil {
		return errors.New("go-task not found on PATH — brew install go-task")
	}
	fmt.Println("→ " + cmdline)
	return syscall.Exec(taskBin, argv, os.Environ())
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "taskmenu:", err)
		os.Exit(1)
	}
}
