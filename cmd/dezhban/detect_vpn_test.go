package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestDetectVPNJSONIsValidAndStable pins `detect-vpn --json`'s top-level key
// set — the contract the app's Diagnostics pane decodes. Environment-dependent
// keys (connectedVPN, discoveryErr, supportedVPNs) are allowed rather than
// required; the always-emitted skeleton is required.
func TestDetectVPNJSONIsValidAndStable(t *testing.T) {
	out := captureStdout(t, func() {
		if code := cmdDetectVPN([]string{"--json"}); code != 0 {
			t.Fatalf("detect-vpn --json exited %d, want 0", code)
		}
	})

	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}

	required := []string{"tunnels", "discoverySupported", "candidates", "tunnelPatterns"}
	// scanPrivileged is optional because it is absent exactly when discovery did
	// not run — that absence is the "no scan" answer, distinct from the "partial
	// scan" false it carries otherwise. See TestDetectVPNJSONReportsScanPrivilege.
	optional := []string{"connectedVPN", "discoveryErr", "supportedVPNs", "scanPrivileged"}
	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Errorf("detect-vpn --json is missing required key %q", k)
		}
	}
	allowed := map[string]bool{}
	for _, k := range append(required, optional...) {
		allowed[k] = true
	}
	var extra []string
	for k := range got {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("detect-vpn --json grew unpinned key(s) %s — add them here deliberately", strings.Join(extra, ", "))
	}

	// tunnels and candidates must be arrays even when empty — "scanned, found
	// none" and "couldn't scan" are different answers, and null conflates them.
	for _, k := range []string{"tunnels", "candidates"} {
		if strings.TrimSpace(string(got[k])) == "null" {
			t.Errorf("%s is null, want an array (possibly empty)", k)
		}
	}

	var pats struct {
		Prefixes []string `json:"prefixes"`
		Keywords []string `json:"keywords"`
	}
	if err := json.Unmarshal(got["tunnelPatterns"], &pats); err != nil {
		t.Fatalf("tunnelPatterns: %v", err)
	}
	if len(pats.Prefixes) == 0 || len(pats.Keywords) == 0 {
		t.Error("tunnelPatterns must carry the classification lists (prefixes and keywords)")
	}
}

// TestDetectVPNJSONReportsScanPrivilege pins the three-way answer scanPrivileged
// exists to give. An unprivileged discovery scan shells out to `lsof`, which
// lists only the calling user's sockets, so `candidates: []` from it is not
// evidence of absence — and the app's Diagnostics pane, which always runs
// unprivileged, would otherwise render it as a confident "No VPN apps found".
// Absence of the key must keep meaning "discovery did not run at all".
func TestDetectVPNJSONReportsScanPrivilege(t *testing.T) {
	out := captureStdout(t, func() {
		if code := cmdDetectVPN([]string{"--json"}); code != 0 {
			t.Fatalf("detect-vpn --json exited %d, want 0", code)
		}
	})

	var got struct {
		DiscoverySupported bool  `json:"discoverySupported"`
		ScanPrivileged     *bool `json:"scanPrivileged"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}

	switch {
	case !got.DiscoverySupported:
		if got.ScanPrivileged != nil {
			t.Errorf("scanPrivileged = %v with discovery unsupported, want absent — no scan ran to be privileged or not", *got.ScanPrivileged)
		}
	case got.ScanPrivileged == nil:
		t.Error("scanPrivileged is absent although discovery ran — an empty candidate list would read as authoritative")
	case *got.ScanPrivileged != (os.Geteuid() == 0):
		t.Errorf("scanPrivileged = %v, want %v (euid %d)", *got.ScanPrivileged, os.Geteuid() == 0, os.Geteuid())
	}
}

// The human output's recommended-config sample must not name retired keys — a
// user pasting it verbatim would immediately see a retired-key warning from
// `dezhban validate`.
func TestDetectVPNHumanSampleUsesOnlyLiveKeys(t *testing.T) {
	out := captureStdout(t, func() {
		// Exit code depends on the machine (tunnels or not); both texts share
		// the property under test only when tunnels exist, so skip if none.
		_ = cmdDetectVPN(nil)
	})
	if strings.Contains(out, "no VPN tunnel interfaces detected") {
		t.Skip("no tunnel on this host — the config sample is not printed")
	}
	if strings.Contains(out, `"enabled"`) {
		t.Error(`the recommended config sample still names the retired vpn.enabled key`)
	}
}
