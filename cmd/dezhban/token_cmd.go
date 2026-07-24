package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/behnam-rk/dezhban/internal/token"
)

const tokenUsage = `usage: dezhban token <subcommand>

Subcommands:
  status    Report whether a control token is enrolled on this host (no root)
  enroll    Generate a token, record its hash, and print the token (root)
  forget    Remove the enrollment, disabling token-gated ops (root)

The control token authorises the one control-socket op the socket's own group
check is not a strong enough gate for: config-write, which changes settings that
outlive the daemon. Everything else on the socket only moves between the
daemon's fail-closed postures and needs no token.

Only the token's HASH is stored, root-owned. 'enroll' prints the token itself
exactly once, on stdout — it is never recoverable afterwards. The macOS app
enrolls on your behalf and keeps its copy in the login keychain behind Touch ID;
enroll by hand only when scripting a client of your own.

Enrolling again replaces the previous token, which is how you revoke one that
has leaked. See docs/adr/0003-biometric-auth.md.`

func cmdToken(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, tokenUsage)
		return 2
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("token "+sub, flag.ContinueOnError)
	cfgFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	switch sub {
	case "status":
		return tokenStatus(*cfgFlag)
	case "enroll":
		return tokenEnroll(*cfgFlag)
	case "forget":
		return tokenForget()
	case "help", "-h", "--help":
		fmt.Println(tokenUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown token subcommand %q\n\n%s\n", sub, tokenUsage)
		return 2
	}
}

// tokenStatus reports the host's enrollment state without reading the hash. It
// needs no root: whether the feature is available is not itself a secret, and a
// user who cannot answer "is this set up?" cannot troubleshoot the app that
// depends on it.
func tokenStatus(cfgFlag string) int {
	path := defaultTokenPath()
	if !token.Enrolled(path) {
		fmt.Printf("control token: not enrolled  (%s)\n", path)
		fmt.Println("  config changes over the control socket are unavailable; they fall back to sudo")
		return 0
	}
	fmt.Printf("control token: enrolled  (%s)\n", path)

	// An enrolled token that the policy switch disables is a confusing state to
	// debug from the outside — the client holds a valid token and is still
	// refused. Say so here rather than leaving it to the refusal message.
	cfg, err := loadConfig(cfgFlag)
	if err == nil && !cfg.Control.AllowConfigOps {
		fmt.Println("  but control.allowConfigOps is false — config-write is refused even with a valid token")
	}
	return 0
}

// tokenEnroll mints a token, records its hash, and prints the token once.
//
// Root is needed exactly here, and exactly once: the hash lives in the daemon's
// root-owned state directory, because anything that could rewrite it could
// nominate its own token and authorise itself.
func tokenEnroll(cfgFlag string) int {
	if !requireRoot("token enroll") {
		return 1
	}
	path := defaultTokenPath()
	if err := ensureStateDir(); err != nil {
		fmt.Fprintln(os.Stderr, "token enroll:", err)
		return 1
	}
	replacing := token.Enrolled(path)

	tok, err := token.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "token enroll:", err)
		return 1
	}
	if err := token.Save(path, tok); err != nil {
		fmt.Fprintln(os.Stderr, "token enroll:", err)
		return 1
	}

	// The token goes to stdout ALONE, so `dezhban token enroll | pbcopy` and
	// friends capture the secret and nothing else. Everything explanatory is
	// stderr.
	if replacing {
		fmt.Fprintln(os.Stderr, "replaced the previous control token — any client holding it is now refused")
	}
	fmt.Println(tok)
	fmt.Fprintf(os.Stderr, "enrolled (%s). This token is not stored and cannot be shown again.\n", path)

	cfg, err := loadConfig(cfgFlag)
	if err == nil && !cfg.Control.AllowConfigOps {
		fmt.Fprintln(os.Stderr, "note: control.allowConfigOps is false, so config-write stays refused until you enable it")
	}
	return 0
}

// tokenForget un-enrolls. It is the recovery path for a token nobody holds any
// more — a wiped keychain, a lost script — which would otherwise leave a hash
// that no client can ever satisfy.
func tokenForget() int {
	if !requireRoot("token forget") {
		return 1
	}
	path := defaultTokenPath()
	if !token.Enrolled(path) {
		fmt.Println("control token: not enrolled — nothing to remove")
		return 0
	}
	if err := token.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "token forget:", err)
		return 1
	}
	fmt.Println("control token removed — config changes now fall back to sudo")
	return 0
}

// ensureStateDir creates the daemon's state directory if the daemon has not run
// yet, so enrolling before first start still works.
func ensureStateDir() error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create state dir %q: %w", stateDir(), err)
	}
	return nil
}
