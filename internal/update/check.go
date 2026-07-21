package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Repo is the GitHub repository releases are published under.
const Repo = "Behnam-RK/dezhban"

// apiLatestURL is a var, not a const, so tests can point it at an
// httptest server instead of the real GitHub API.
var apiLatestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// CheckResult is what Check reports.
type CheckResult struct {
	Available bool   // a newer final release exists
	Current   string // currentVersion normalised to X.Y.Z, or "" for a dev build
	Latest    string // the latest release's version, X.Y.Z (no leading v)
	URL       string // the release page, for a human to read notes
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// Check asks GitHub for the latest final release and compares it against
// currentVersion (buildStamp.Version — e.g. "v0.3.0", the git-describe form
// "v0.3.0-5-gabc123-dirty", or the toolchain's "(devel)"). GitHub's "latest"
// already excludes --prerelease builds — the release workflow always tags an
// rc that way — so this never offers an rc.
//
// This is the ONLY network call anywhere in the upgrade path that the daemon
// itself must never make (see docs/upgrade.md and CLAUDE.md): Check is called
// from the GUI, in user context, or from the CLI on demand — never from the
// always-on root daemon, whose egress stays geo-providers-only. If the tunnel
// is down when this runs, it fails; it does not get its own firewall pass.
//
// httpClient lets callers set a short timeout; nil uses http.DefaultClient.
func Check(currentVersion string, httpClient *http.Client) (CheckResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequest(http.MethodGet, apiLatestURL, nil)
	if err != nil {
		return CheckResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return CheckResult{}, fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CheckResult{}, fmt.Errorf("checking for updates: GitHub API returned %s", resp.Status)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return CheckResult{}, fmt.Errorf("checking for updates: %w", err)
	}

	current := normalizeVersion(currentVersion)
	latest := strings.TrimPrefix(rel.TagName, "v")

	return CheckResult{
		Current:   current,
		Latest:    latest,
		URL:       rel.HTMLURL,
		Available: current != "" && semverLess(current, latest),
	}, nil
}

// normalizeVersion strips a leading "v", leaving a bare X.Y.Z core to compare
// — or "" when currentVersion isn't a real release build.
//
// Two shapes are real release builds (task build:all VERSION=<tag> stamps the
// exact tag, never a git-describe form, for both a final and an rc): a clean
// "vX.Y.Z", and "vX.Y.Z-rc.N", compared here by its base X.Y.Z core against
// latest-final (a deliberate simplification — an rc for 0.5.0 sitting next to
// a published final 0.5.0 reads as "no update", even though a final strictly
// outranks its own rc in release.sh's semver_gt; this package only ever needs
// "is a newer LINE out", not full prerelease precedence).
//
// Anything else — buildStamp.Version's git-describe tail on a local dev build
// ("v0.4.0-3-gabc123-dirty") or the toolchain's "(devel)" — normalises to "".
// Comparing a dev build against a tagged release is meaningless, and Check
// treats "" as never offering an update rather than guessing at one.
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if isSemverCore(v) {
		return v
	}
	if core, _, ok := strings.Cut(v, "-rc."); ok && isSemverCore(core) {
		return core
	}
	return ""
}

func isSemverCore(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// semverLess reports whether a < b for two X.Y.Z cores. Both must already
// satisfy isSemverCore; callers only ever pass values that do.
func semverLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		an, _ := strconv.Atoi(as[i])
		bn, _ := strconv.Atoi(bs[i])
		if an != bn {
			return an < bn
		}
	}
	return false
}
