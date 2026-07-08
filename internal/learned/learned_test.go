package learned

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(s.Entries) != 0 || s.Version != version {
		t.Errorf("missing file store = %+v, want empty v%d", s, version)
	}
}

func TestLoadCorruptIsEmptyNotFatal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "learned.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err == nil {
		t.Error("Load(corrupt) = nil error, want a (non-fatal) parse error")
	}
	if s == nil || len(s.Entries) != 0 {
		t.Errorf("corrupt load must yield an empty store, got %+v", s)
	}
}

func TestRecordAndSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "learned.json")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	s.Record("proton", "utun6", "switch-window", []netip.Addr{addr(t, "203.0.113.7")}, 16, now)
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 0644 so the unprivileged CLI can read it. Windows doesn't honor Unix perm
	// bits (Stat reports 0666), so the exact-mode check is POSIX-only.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o644 {
		t.Errorf("perm = %o, want 0644", perm)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "proton" ||
		len(got.Entries[0].Endpoints) != 1 || got.Entries[0].Endpoints[0].Addr != "203.0.113.7" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Entries[0].Iface != "utun6" || got.Entries[0].Source != "switch-window" {
		t.Errorf("iface/source not persisted: %+v", got.Entries[0])
	}
}

func TestRecordRefreshesLastSeenAndDedupes(t *testing.T) {
	t0 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	s := &Store{}
	s.Record("wg", "", "discovery", []netip.Addr{addr(t, "1.2.3.4")}, 16, t0)
	s.Record("wg", "", "discovery", []netip.Addr{addr(t, "1.2.3.4")}, 16, t1)
	if n := len(s.Entries[0].Endpoints); n != 1 {
		t.Fatalf("endpoints = %d, want 1 (deduped)", n)
	}
	ep := s.Entries[0].Endpoints[0]
	if !ep.FirstSeen.Equal(t0) || !ep.LastSeen.Equal(t1) {
		t.Errorf("firstSeen/lastSeen = %v/%v, want %v/%v", ep.FirstSeen, ep.LastSeen, t0, t1)
	}
}

// Record must match entry names case-insensitively (like Forget and profile
// handling), so "Proton" and "proton" merge into one entry rather than bloating
// the store with case-variant duplicates.
func TestRecordMergesCaseInsensitively(t *testing.T) {
	t0 := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	s.Record("Proton", "", "discovery", []netip.Addr{addr(t, "1.2.3.4")}, 16, t0)
	s.Record("proton", "", "discovery", []netip.Addr{addr(t, "5.6.7.8")}, 16, t0.Add(time.Minute))
	if n := len(s.Entries); n != 1 {
		t.Fatalf("entries = %d, want 1 (case-variant names merged)", n)
	}
	if n := len(s.Entries[0].Endpoints); n != 2 {
		t.Fatalf("endpoints = %d, want 2 merged into the one entry", n)
	}
	// A case-variant Forget must then drop it.
	if !s.Forget("PROTON") {
		t.Fatal("Forget(PROTON) = false, want true (case-insensitive)")
	}
	if len(s.Entries) != 0 {
		t.Fatalf("entries after forget = %d, want 0", len(s.Entries))
	}
}

func TestRecordEnforcesPerEntryCap(t *testing.T) {
	base := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	// 5 addresses, cap 3 → the 3 most-recently-seen survive.
	for i, a := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5"} {
		s.Record("p", "", "discovery", []netip.Addr{addr(t, a)}, 3, base.Add(time.Duration(i)*time.Minute))
	}
	if n := len(s.Entries[0].Endpoints); n != 3 {
		t.Fatalf("endpoints = %d, want cap 3", n)
	}
	got := map[string]bool{}
	for _, ep := range s.Entries[0].Endpoints {
		got[ep.Addr] = true
	}
	if got["1.1.1.1"] || got["2.2.2.2"] || !got["5.5.5.5"] {
		t.Errorf("cap kept the wrong (not most-recent) endpoints: %+v", s.Entries[0].Endpoints)
	}
}

func TestPruneDropsExpiredAndEmptyEntries(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	s.Record("old", "", "discovery", []netip.Addr{addr(t, "1.1.1.1")}, 16, now.Add(-48*time.Hour))
	s.Record("new", "", "discovery", []netip.Addr{addr(t, "2.2.2.2")}, 16, now)
	dropped := s.Prune(24*time.Hour, 16, now)
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	if len(s.Entries) != 1 || s.Entries[0].Name != "new" {
		t.Errorf("prune left %+v, want only the fresh entry", s.Entries)
	}
}

func TestForgetAndAddrs(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	s := &Store{}
	s.Record("a", "", "", []netip.Addr{addr(t, "3.3.3.3"), addr(t, "1.1.1.1")}, 16, now)
	s.Record("b", "", "", []netip.Addr{addr(t, "2.2.2.2"), addr(t, "1.1.1.1")}, 16, now)
	// Addrs are deduped and sorted.
	got := s.Addrs()
	want := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}
	if len(got) != len(want) {
		t.Fatalf("Addrs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Addrs = %v, want %v", got, want)
		}
	}
	if !s.Forget("a") || s.Forget("missing") {
		t.Error("Forget return values wrong")
	}
	if len(s.Entries) != 1 || s.Entries[0].Name != "b" {
		t.Errorf("after Forget: %+v", s.Entries)
	}
}

func TestSaveWritesSchemaVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "learned.json")
	if err := (&Store{}).Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"].(float64) != float64(version) {
		t.Errorf("version = %v, want %d", raw["version"], version)
	}
}
