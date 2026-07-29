// Package help turns the repo's documentation into what the macOS app bundles
// and displays.
//
// It exists so the app can answer a question while the kill switch has cut
// traffic — which is exactly when a user most needs to know what is happening
// and cannot reach the web. Pages are rendered at build time and shipped inside
// the app, so the documentation always matches the version that documents it.
//
// Documentation stays single-source: this package RENDERS the repo's markdown,
// it never restates it. A page listed here is load-bearing, the same rule
// CLAUDE.md already applies to doc paths cited from source — a test fails the
// build when one goes missing or is renamed.
//
// Stdlib only, and dev tooling only (invoked by gui/macos/build-app.sh through
// tools/helpgen). Nothing here runs in the daemon.
package help

// A Page is one bundled document.
type Page struct {
	// Source is the path under docs/, which is also its identity: a DocAnchor
	// of "usage/config.md#fields" names this page and a heading within it.
	Source string
	// Title is how the page reads in the browser's sidebar.
	Title string
	// Summary is one line saying what the page answers, for search results and
	// the sidebar.
	Summary string
	// Tutorial is the position in the first-time reading order, 1-based. Zero
	// means the page is reference material — reachable and searchable, but not
	// part of the guided track.
	Tutorial int
}

// Pages is what the app ships, in sidebar order.
//
// It is deliberately not "everything under docs/". The ADRs and the contributor
// docs are written for people working ON dezhban and would drown a user looking
// for how to unblock themselves; they stay in the repo. What ships is what
// somebody running dezhban needs, offline, possibly mid-lockout.
var Pages = []Page{
	{
		Source: "usage/getting-started.md", Title: "Quick start", Tutorial: 1,
		Summary: "Install it, set it up, check it won't lock you out, and arm it.",
	},
	{
		Source: "concepts/how-it-works.md", Title: "How dezhban works", Tutorial: 2,
		Summary: "What the guard does, from launch to teardown.",
	},
	{
		Source: "concepts/modes.md", Title: "Postures", Tutorial: 3,
		Summary: "Every posture the guard can be in, and the exact rules each one installs.",
	},
	{
		Source: "usage/troubleshooting.md", Title: "Troubleshooting", Tutorial: 4,
		Summary: "Locked out, or the guard is not behaving — start here.",
	},
	{
		Source: "usage/config.md", Title: "Configuration reference",
		Summary: "Every setting: what it does, its default, and what it costs.",
	},
	{
		Source: "usage/cli.md", Title: "Command reference",
		Summary: "Every command and flag, and which of them need root.",
	},
	{
		Source: "usage/passwordless.md", Title: "Using the CLI without sudo",
		Summary: "Turn on control.group so routine ops need no password.",
	},
	{
		Source: "concepts/glossary.md", Title: "Glossary",
		Summary: "One word per concept — the words this app, the CLI, and the docs all use.",
	},
	{
		Source: "usage/install.md", Title: "Installing",
		Summary: "Every install path, and how to verify a download.",
	},
	{
		Source: "usage/upgrade.md", Title: "Upgrading",
		Summary: "How an upgrade is applied and activated, and how to roll one back.",
	},
}

// PageBySource finds a page by its docs-relative path.
func PageBySource(source string) (Page, bool) {
	for _, p := range Pages {
		if p.Source == source {
			return p, true
		}
	}
	return Page{}, false
}

// OutputName is the file a page is rendered to, flattened so the bundle is one
// directory: "usage/config.md" becomes "usage-config.html".
func OutputName(source string) string {
	out := make([]rune, 0, len(source))
	for _, r := range source {
		if r == '/' {
			r = '-'
		}
		out = append(out, r)
	}
	name := string(out)
	if len(name) > 3 && name[len(name)-3:] == ".md" {
		name = name[:len(name)-3]
	}
	return name + ".html"
}
