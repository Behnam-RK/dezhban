#!/usr/bin/env bash
# Release logic — the single source of truth for version resolution, CHANGELOG
# rolling and release-notes extraction.
#
# Both the Release workflow and the local `task release:*` family call THIS file,
# so a local preview cannot drift from what CI will actually do. Keep the core
# verbs (resolve / notes / roll) free of `gh` and of anything but git+awk+sed:
# the workflow runs them on a bare runner, and scripts/*.sh must stay runnable
# without the dev tooling. Only `preflight` and `dispatch` may use `gh`.
#
# Version vocabulary, normalised in exactly one place (resolve):
#   TAG          v0.2.0        what git is tagged with; always carries the "v"
#   VERSION      0.2.0         filenames, release titles; never carries the "v"
#   VERSION_NUM  0.2.0         VERSION minus any -rc.N, for the dotted-numeric-only
#                              fields (pkg receipt, CFBundleShortVersionString)
#   KIND         final | rc
#
# RC semantics: an rc is a pure snapshot — tag only. It does NOT roll the
# CHANGELOG, does NOT commit to main, and is published --prerelease so it never
# becomes "latest". `bump` counts from the last FINAL tag and ignores rc tags,
# except `--bump rc`, which advances an existing rc line.

# Re-exec under bash when invoked as `sh scripts/release.sh`. /bin/sh is dash on
# the Ubuntu runners — it has no `set -o pipefail` and dies on the next line — but
# it is bash on macOS, so the mistake runs fine locally and only fails in CI.
# Must come before `set -o pipefail`.
if [ -z "${BASH_VERSION:-}" ]; then
	exec bash "$0" "$@"
fi

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

CHANGELOG="CHANGELOG.md"
FINAL_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
RC_RE='^[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'

die() { echo "error: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# resolve
# ---------------------------------------------------------------------------

# The `|| true` on each of these is load-bearing: with no matching tag, grep exits
# 1, and under `set -e` + `pipefail` that would kill the script from inside the
# command substitution — silently, before anything is printed. "No tags yet" is a
# normal state (it is exactly the state of the first release), not an error.
#
# Any tag that is neither X.Y.Z nor X.Y.Z-rc.N is simply invisible here, which is
# what retires the legacy non-semver `v0.1` without any special-casing.

# latest_final prints the highest strict-semver X.Y.Z tag, or nothing.
latest_final() {
	git tag -l 'v*' | sed 's/^v//' | grep -E "$FINAL_RE" | sort -V | tail -n1 || true
}

# latest_rc prints the highest rc tag, or nothing.
latest_rc() {
	git tag -l 'v*' | sed 's/^v//' | grep -E "$RC_RE" | sort -V | tail -n1 || true
}

# semver_gt A B — true when A has strictly higher SEMVER precedence than B.
#
# `sort -V` cannot do this: it ranks 0.2.0-rc.1 ABOVE 0.2.0, the exact inverse of
# the semver rule that a prerelease precedes its own final. Using it here would
# both reject the rc -> final promotion (0.2.0 "not greater than" 0.2.0-rc.1) and
# happily accept an rc for a version that already shipped. It is still fine for
# ordering plain X.Y.Z cores, which is all it is used for below.
semver_gt() {
	local a="$1" b="$2"
	local a_core="${a%%-*}" b_core="${b%%-*}"
	local a_rc="" b_rc=""
	case "$a" in *-rc.*) a_rc="${a##*-rc.}" ;; esac
	case "$b" in *-rc.*) b_rc="${b##*-rc.}" ;; esac

	if [ "$a_core" != "$b_core" ]; then
		[ "$(printf '%s\n%s\n' "$a_core" "$b_core" | sort -V | tail -n1)" = "$a_core" ]
		return
	fi
	# Same core: a final outranks any of its prereleases; two rc's compare numerically.
	if [ -z "$a_rc" ] && [ -z "$b_rc" ]; then return 1; fi  # identical
	if [ -z "$a_rc" ]; then return 0; fi                    # 0.2.0     > 0.2.0-rc.N
	if [ -z "$b_rc" ]; then return 1; fi                    # 0.2.0-rc.N < 0.2.0
	[ "$a_rc" -gt "$b_rc" ]
}

# latest_any prints the highest-precedence tag of either kind, or nothing.
latest_any() {
	local best="" v
	for v in $(git tag -l 'v*' | sed 's/^v//' | grep -E "$FINAL_RE|$RC_RE" || true); do
		if [ -z "$best" ] || semver_gt "$v" "$best"; then best="$v"; fi
	done
	printf '%s' "$best"
}

# resolve <--version X | --bump patch|minor|major|rc>
# Emits shell-eval'able KEY=VALUE lines.
cmd_resolve() {
	local in_version="" in_bump="" new=""

	while [ $# -gt 0 ]; do
		case "$1" in
			--version) in_version="${2:-}"; shift 2 ;;
			--bump)    in_bump="${2:-}";    shift 2 ;;
			*) die "resolve: unknown argument '$1'" ;;
		esac
	done

	in_version="${in_version#v}"

	if [ -n "$in_version" ]; then
		new="$in_version"
	else
		[ -n "$in_bump" ] || die "resolve: pass --version or --bump"

		if [ "$in_bump" = "rc" ]; then
			# Advance an existing rc line: 0.2.0-rc.1 -> 0.2.0-rc.2. There is no
			# sensible "invent the next rc from a final" — which minor would it
			# even be? — so require an explicit --version to OPEN an rc line.
			local rc; rc="$(latest_rc)"
			[ -n "$rc" ] || die "resolve: --bump rc needs an existing rc tag to advance; pass --version X.Y.Z-rc.1 to open a new rc line"
			local core="${rc%-rc.*}" n="${rc##*-rc.}"
			new="$core-rc.$((n + 1))"
		else
			# Finals always count from the last FINAL tag; rc tags are invisible
			# here, so an abandoned rc line can never drag the next release with it.
			local base; base="$(latest_final)"
			[ -n "$base" ] || die "resolve: no existing X.Y.Z tag to bump from; pass an explicit --version for the first release"
			local major minor patch
			major="${base%%.*}"; minor="$(echo "$base" | cut -d. -f2)"; patch="$(echo "$base" | cut -d. -f3)"
			case "$in_bump" in
				major) major=$((major + 1)); minor=0; patch=0 ;;
				minor) minor=$((minor + 1)); patch=0 ;;
				patch) patch=$((patch + 1)) ;;
				*) die "resolve: unknown bump '$in_bump' (want patch|minor|major|rc)" ;;
			esac
			new="$major.$minor.$patch"
		fi
	fi

	local kind
	if echo "$new" | grep -Eq "$FINAL_RE"; then
		kind=final
	elif echo "$new" | grep -Eq "$RC_RE"; then
		kind=rc
	else
		die "'$new' is neither X.Y.Z nor X.Y.Z-rc.N"
	fi

	local tag="v$new"
	! git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1 || die "tag $tag already exists"

	# Monotonicity against the highest tag of EITHER kind, so you cannot ship
	# 0.2.0-rc.1 after 0.2.0 has already gone out. semver_gt (not `sort -V`) is
	# what makes the rc -> final promotion legal while the reverse is not.
	local prev; prev="$(latest_any)"
	if [ -n "$prev" ] && ! semver_gt "$new" "$prev"; then
		die "$tag does not outrank the latest tag v$prev"
	fi

	echo "TAG=$tag"
	echo "VERSION=$new"
	echo "VERSION_NUM=${new%-rc.*}"
	echo "KIND=$kind"
	echo "PREV_TAG=${prev:+v$prev}"
}

# ---------------------------------------------------------------------------
# notes
# ---------------------------------------------------------------------------

# raw_unreleased_body prints the [Unreleased] section's body verbatim, untrimmed
# and possibly blank. It only fails if the heading itself is missing. Internal
# helper shared by unreleased_body (which additionally validates non-emptiness)
# and cmd_check_rolled (which needs to tell "rolled" from "not rolled" without
# dying on the empty case that PROVES a final was rolled).
#
# Stop at the next version heading OR at the reference-link block at the foot of
# the file. The link stop is load-bearing: until the first release there IS no
# next heading, so without it the extraction runs to EOF and swallows
# "[Unreleased]: https://..." into the release notes.
raw_unreleased_body() {
	grep -q '^## \[Unreleased\]$' "$CHANGELOG" || die "$CHANGELOG has no '## [Unreleased]' heading"
	awk '
		/^## \[Unreleased\]$/ { flag = 1; next }
		/^## \[/              { flag = 0 }
		/^\[[^]]+\]:[[:space:]]/ { flag = 0 }
		flag
	' "$CHANGELOG"
}

# unreleased_body prints the [Unreleased] section's body, with the blank lines the
# heading split leaves behind trimmed off. Fails if the section is empty — a
# release with no notes is a bug, not a valid release.
unreleased_body() {
	local body
	body="$(raw_unreleased_body)"
	body="$(printf '%s\n' "$body" | sed -e '/./,$!d' -e :a -e '/^\n*$/{$d;N;ba' -e '}')"
	[ -n "$(echo "$body" | tr -d '[:space:]')" ] || die "[Unreleased] is empty; nothing to release"
	printf '%s\n' "$body"
}

# section_body <version> prints the body of an already-rolled "## [<version>] -
# <date>" heading, trimmed the same way unreleased_body is. This is what a FINAL
# release's notes are read from: rolling happens locally now (see cmd_roll /
# `task release`), so by the time this runs [Unreleased] is expected to be empty
# and the real content has already moved under the dated heading.
section_body() {
	local version="$1"
	grep -q "^## \[$version\][[:space:]]*-" "$CHANGELOG" \
		|| die "$CHANGELOG has no '## [$version] - ...' heading — roll it locally first (see \`task release\`)"
	local body
	body="$(awk -v ver="$version" '
		$0 ~ ("^## \\[" ver "\\][[:space:]]*-") { flag = 1; next }
		/^## \[/                                { flag = 0 }
		/^\[[^]]+\]:[[:space:]]/                { flag = 0 }
		flag
	' "$CHANGELOG")"
	body="$(printf '%s\n' "$body" | sed -e '/./,$!d' -e :a -e '/^\n*$/{$d;N;ba' -e '}')"
	[ -n "$(echo "$body" | tr -d '[:space:]')" ] || die "[$version] section is empty"
	printf '%s\n' "$body"
}

# check-rolled <version> <kind> — the gate that replaces CI rolling the
# CHANGELOG itself. A final release is rolled LOCALLY now (`task release`, or
# `scripts/release.sh roll` by hand) and pushed before dispatch, so by the time
# this runs on the pinned commit, [Unreleased] must already be empty and the
# dated heading must already exist. An rc never rolls, so its check is the
# inverse: [Unreleased] must still hold the pending notes.
cmd_check_rolled() {
	local version="${1:?check-rolled: need a version}" kind="${2:?check-rolled: need a kind}"

	if [ "$kind" = rc ]; then
		unreleased_body >/dev/null # dies with a clear message if empty/missing
		return 0
	fi

	local raw; raw="$(raw_unreleased_body)"
	[ -z "$(echo "$raw" | tr -d '[:space:]')" ] \
		|| die "[Unreleased] still has content — CHANGELOG.md has not been rolled for $version. Run \`task release\` locally (it rolls, commits, and pushes before dispatching), or \`bash scripts/release.sh roll $version\` by hand, then push to main."
	grep -q "^## \[$version\][[:space:]]*-" "$CHANGELOG" \
		|| die "$CHANGELOG has no '## [$version] - ...' heading — roll it locally before dispatching (see \`task release\`)."
}

# notes <version> <kind> — the full release body.
#
# The macOS artifacts are unsigned, so a plain double-click is blocked by
# Gatekeeper. Put the working install line IN the release the user just
# downloaded rather than only in docs/contribute/releasing.md.
cmd_notes() {
	local version="${1:?notes: need a version}" kind="${2:-final}"

	if [ "$kind" = rc ]; then
		cat <<-EOF
			> **Pre-release.** This is a release candidate, not a final release.
			> The CHANGELOG is not rolled for an rc; the notes below are the
			> current \`[Unreleased]\` section as it stands.

		EOF
		unreleased_body
	else
		# Rolled locally before dispatch — read the dated section, not [Unreleased].
		section_body "$version"
	fi
	cat <<-EOF

		## Install (macOS)

		These artifacts are **unsigned** — a double-click is blocked by Gatekeeper.
		Install from the terminal, which skips the GUI assessment entirely:

		\`\`\`sh
		shasum -a 256 -c SHA256SUMS --ignore-missing
		sudo installer -pkg dezhban-$version.pkg -target /
		\`\`\`

		Then configure it: \`sudo dezhban setup\`, and start it: \`sudo dezhban start\`.
		The installer registers the service but deliberately does **not** arm the
		kill switch. See [docs/contribute/releasing.md](docs/contribute/releasing.md).
	EOF
}

# ---------------------------------------------------------------------------
# roll — final releases only
# ---------------------------------------------------------------------------

# roll <version> — rewrite CHANGELOG.md in place: split [Unreleased] into a fresh
# empty [Unreleased] plus a dated version heading, and refresh the ref links.
cmd_roll() {
	local version="${1:?roll: need a version}"
	local tag="v$version"
	local date; date="$(date -u +%Y-%m-%d)"
	local url="https://github.com/${GITHUB_REPOSITORY:-Behnam-RK/dezhban}"

	unreleased_body >/dev/null # validates; fails loudly on an empty section

	# Trim trailing blank lines so the END-block append below gets exactly one
	# blank separator.
	sed -e :a -e '/^[[:space:]]*$/{$d;N;ba' -e '}' "$CHANGELOG" > "$CHANGELOG.tmp"
	mv "$CHANGELOG.tmp" "$CHANGELOG"

	# One pass: split the heading, and replace the [Unreleased] compare link with a
	# refreshed one plus the new version's release link. Older version refs below it
	# are preserved so past headings keep working. The END fallback covers a
	# changelog with no [Unreleased] ref line at all.
	awk -v date="$date" -v ver="$version" -v url="$url" -v tag="$tag" '
		/^## \[Unreleased\]$/ {
			print "## [Unreleased]"
			print ""
			print "## [" ver "] - " date
			next
		}
		/^\[Unreleased\]:/ {
			print "[Unreleased]: " url "/compare/" tag "...HEAD"
			print "[" ver "]: " url "/releases/tag/" tag
			refdone = 1
			next
		}
		{ print }
		END {
			if (!refdone) {
				print ""
				print "[Unreleased]: " url "/compare/" tag "...HEAD"
				print "[" ver "]: " url "/releases/tag/" tag
			}
		}
	' "$CHANGELOG" > "$CHANGELOG.tmp"
	mv "$CHANGELOG.tmp" "$CHANGELOG"
}

# ---------------------------------------------------------------------------
# preflight — local go/no-go before dispatching (needs gh)
# ---------------------------------------------------------------------------

ok=0
fail=0
check_ok()   { printf '  \033[32m[ok]\033[0m   %s\n' "$1"; ok=$((ok + 1)); }
check_bad()  { printf '  \033[31m[FAIL]\033[0m %s\n' "$1"; fail=$((fail + 1)); }
check_warn() { printf '  \033[33m[warn]\033[0m %s\n' "$1"; }

# ci_conclusion <sha> — the CI workflow's conclusion for a commit, or a state word.
# Prints one of: success | failure | cancelled | in_progress | none
ci_conclusion() {
	local sha="$1" json
	json="$(gh run list --workflow=ci.yml --commit "$sha" --limit 1 \
		--json status,conclusion 2>/dev/null || echo '[]')"
	[ "$json" != "[]" ] || { echo none; return; }
	local status conclusion
	status="$(echo "$json" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
	conclusion="$(echo "$json" | sed -n 's/.*"conclusion":"\([^"]*\)".*/\1/p')"
	if [ "$status" != "completed" ]; then echo in_progress; else echo "${conclusion:-none}"; fi
}

cmd_preflight() {
	local sha; sha="$(git rev-parse HEAD)"
	echo "preflight"

	local branch; branch="$(git rev-parse --abbrev-ref HEAD)"
	[ "$branch" = main ] && check_ok "on main" || check_bad "on '$branch', not main"

	if [ -z "$(git status --porcelain)" ]; then
		check_ok "working tree clean"
	else
		check_bad "working tree dirty (the release builds from origin/main, not your tree)"
	fi

	git fetch -q origin main 2>/dev/null || true
	local remote; remote="$(git rev-parse origin/main 2>/dev/null || echo none)"
	if [ "$remote" = "$sha" ]; then
		check_ok "synced with origin/main ($(git rev-parse --short HEAD))"
	else
		check_bad "HEAD differs from origin/main — push or pull first"
	fi

	if body="$(unreleased_body 2>/dev/null)"; then
		check_ok "[Unreleased] has $(echo "$body" | grep -c '^- ' || true) entries"
	else
		check_bad "[Unreleased] is empty — nothing to release"
	fi

	if command -v gh >/dev/null 2>&1; then
		case "$(ci_conclusion "$sha")" in
			success)     check_ok   "CI green on $(git rev-parse --short HEAD)" ;;
			in_progress) check_warn "CI still running on $(git rev-parse --short HEAD) — the release will wait for it" ;;
			failure|cancelled) check_bad "CI is RED on $(git rev-parse --short HEAD)" ;;
			none)        check_bad "no CI run found for $(git rev-parse --short HEAD)" ;;
		esac
	else
		check_warn "gh not installed — cannot check CI"
	fi

	echo
	[ "$fail" -eq 0 ] || { echo "preflight failed ($fail blocking)"; return 1; }
	echo "preflight passed ($ok checks)"
}

# ---------------------------------------------------------------------------

usage() {
	cat >&2 <<-EOF
		usage: scripts/release.sh <command>

		  resolve --version X.Y.Z[-rc.N] | --bump patch|minor|major|rc
		                          resolve + validate the next version; prints
		                          TAG/VERSION/VERSION_NUM/KIND/PREV_TAG
		  notes <version> [kind]  render the release body (rolled section for
		                          final, [Unreleased] for rc)
		  roll <version>          rewrite CHANGELOG.md for a final release
		  check-rolled <version> <kind>
		                          gate: final must be rolled, rc must not be
		  preflight               local go/no-go checks (needs gh)
	EOF
	exit 2
}

case "${1:-}" in
	resolve)      shift; cmd_resolve "$@" ;;
	notes)        shift; cmd_notes "$@" ;;
	roll)         shift; cmd_roll "$@" ;;
	check-rolled) shift; cmd_check_rolled "$@" ;;
	preflight)    shift; cmd_preflight "$@" ;;
	*) usage ;;
esac
