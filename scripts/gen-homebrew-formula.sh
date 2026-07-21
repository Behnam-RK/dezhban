#!/usr/bin/env bash
# Fills packaging/homebrew/dezhban.rb.tmpl from a real release's SHA256SUMS and
# prints the finished formula on stdout. Not run by CI or any task — this repo
# does not own or push to the tap; a human copies the output into
# Formula/dezhban.rb in a SEPARATE behnam-rk/homebrew-tap repository (which
# does not exist yet — see docs/install.md).
#
#   bash scripts/gen-homebrew-formula.sh 0.5.0 > dezhban.rb
set -euo pipefail

version="${1:?usage: gen-homebrew-formula.sh <version, no v prefix>}"
version="${version#v}"
tag="v$version"
repo="Behnam-RK/dezhban"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/SHA256SUMS" "https://github.com/$repo/releases/download/$tag/SHA256SUMS"

sha_for() {
	# `|| true`: a no-match here must NOT trip set -e/pipefail and kill the
	# script — that would abort silently, before the explicit empty-check loop
	# below ever runs, replacing a clear "no checksum entry" error with nothing
	# printed at all (confirmed: that is exactly what happened without this).
	grep " $1\$" "$tmp/SHA256SUMS" | awk '{print $1}' || true
}

darwin_arm64="$(sha_for "dezhban-$version-darwin-arm64.tar.gz")"
darwin_amd64="$(sha_for "dezhban-$version-darwin-amd64.tar.gz")"
linux_arm64="$(sha_for "dezhban-$version-linux-arm64.tar.gz")"
linux_amd64="$(sha_for "dezhban-$version-linux-amd64.tar.gz")"

for name in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
	[ -n "${!name}" ] || {
		echo "error: no checksum entry for the $name tarball in $tag's SHA256SUMS" >&2
		echo "       (tarballs were added after v0.4.0 — pick a version that has them)" >&2
		exit 1
	}
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Drop the .tmpl's own header comment (it explains the templating mechanism —
# useful reading THIS repo, meaningless noise in the formula a tap maintainer
# actually ships) by starting output at `class Dezhban`, then substitute.
sed -n '/^class Dezhban/,$p' "$here/packaging/homebrew/dezhban.rb.tmpl" | sed \
	-e "s/@VERSION@/$version/" \
	-e "s/@SHA256_DARWIN_ARM64@/$darwin_arm64/" \
	-e "s/@SHA256_DARWIN_AMD64@/$darwin_amd64/" \
	-e "s/@SHA256_LINUX_ARM64@/$linux_arm64/" \
	-e "s/@SHA256_LINUX_AMD64@/$linux_amd64/"
