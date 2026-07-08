// Package learned persists VPN server endpoints that the daemon discovered at
// runtime (during a switch window, or via live socket discovery under normal
// guard), so a VPN that has been connected once stays reachable across restarts
// without the user hand-typing its server address.
//
// The store lives beside the state file (see cmd/dezhban.defaultStatePath) and is
// written ONLY by the daemon. It is machine-observed fact, deliberately separate
// from the user's config (which is user intent): a corrupt learned.json is safe
// to discard, whereas a corrupt config could brick the kill switch. Callers that
// only read it (the `vpn list` CLI) need no privilege; edits go through the
// daemon.
package learned

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// version is the on-disk schema version. Bump on an incompatible change.
const version = 1

// Endpoint is a single learned server address.
type Endpoint struct {
	Addr      string    `json:"addr"`
	Port      int       `json:"port,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// Entry groups the endpoints learned for one attribution key (a profile name, or
// "_unattributed" when the switch window carried no profile).
type Entry struct {
	Name      string     `json:"name"`
	Endpoints []Endpoint `json:"endpoints"`
	Iface     string     `json:"iface,omitempty"`
	Source    string     `json:"source,omitempty"`
	LearnedAt time.Time  `json:"learnedAt"`
}

// Store is the whole learned.json document.
type Store struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Unattributed is the entry name used when a learned endpoint has no profile.
const Unattributed = "_unattributed"

// Load reads the store at path. A missing file yields an empty store and a nil
// error (first run). A corrupt file yields an empty store and a non-nil error:
// callers should log it and carry on — learned data is convenience, never
// load-bearing, so a parse failure must not be fatal.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{Version: version}, nil
		}
		return &Store{Version: version}, fmt.Errorf("learned: read %q: %w", path, err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return &Store{Version: version}, fmt.Errorf("learned: parse %q (discarding): %w", path, err)
	}
	if s.Version == 0 {
		s.Version = version
	}
	return &s, nil
}

// Save atomically writes the store to path (temp + fsync + rename), 0644 so the
// unprivileged CLI can read it. The parent directory is created if needed.
func (s *Store) Save(path string) error {
	s.Version = version
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("learned: create dir %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("learned: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".learned-*.json.tmp")
	if err != nil {
		return fmt.Errorf("learned: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("learned: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("learned: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("learned: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("learned: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("learned: rename into place: %w", err)
	}
	return nil
}

// Record merges the given addresses into the entry named `name` (a profile name,
// or Unattributed), stamping FirstSeen/LastSeen and recording the iface/source.
// New addresses are appended; already-known ones have LastSeen refreshed. The
// per-name cap is enforced (oldest by LastSeen evicted) so a rotating-pool VPN
// cannot grow the store without bound.
func (s *Store) Record(name, iface, source string, addrs []netip.Addr, maxPerEntry int, now time.Time) {
	if name == "" {
		name = Unattributed
	}
	e := s.entry(name)
	if e == nil {
		s.Entries = append(s.Entries, Entry{Name: name, LearnedAt: now})
		e = &s.Entries[len(s.Entries)-1]
	}
	e.Iface = iface
	e.Source = source
	for _, a := range addrs {
		if !a.IsValid() {
			continue
		}
		as := a.String()
		if ep := findEndpoint(e.Endpoints, as); ep != nil {
			ep.LastSeen = now
			continue
		}
		e.Endpoints = append(e.Endpoints, Endpoint{Addr: as, FirstSeen: now, LastSeen: now})
	}
	capEndpoints(e, maxPerEntry)
}

// Prune drops endpoints unused for longer than ttl and re-applies the per-entry
// cap; entries left with no endpoints are removed. Returns the number of
// endpoints dropped.
func (s *Store) Prune(ttl time.Duration, maxPerEntry int, now time.Time) int {
	dropped := 0
	kept := s.Entries[:0]
	for i := range s.Entries {
		e := &s.Entries[i]
		fresh := e.Endpoints[:0]
		for _, ep := range e.Endpoints {
			if ttl > 0 && now.Sub(ep.LastSeen) > ttl {
				dropped++
				continue
			}
			fresh = append(fresh, ep)
		}
		e.Endpoints = fresh
		dropped += capEndpoints(e, maxPerEntry)
		if len(e.Endpoints) > 0 {
			kept = append(kept, *e)
		}
	}
	s.Entries = kept
	return dropped
}

// Forget removes the entry named `name` (case-insensitive, matching how
// profiles are compared elsewhere). Returns true if it existed.
func (s *Store) Forget(name string) bool {
	for i := range s.Entries {
		if strings.EqualFold(s.Entries[i].Name, name) {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// Addrs returns every learned address across all entries, deduplicated, sorted
// for a stable ruleset. This is what feeds the endpoint-resolution union.
func (s *Store) Addrs() []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range s.Entries {
		for _, ep := range e.Endpoints {
			if !seen[ep.Addr] {
				seen[ep.Addr] = true
				out = append(out, ep.Addr)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) entry(name string) *Entry {
	for i := range s.Entries {
		if s.Entries[i].Name == name {
			return &s.Entries[i]
		}
	}
	return nil
}

func findEndpoint(eps []Endpoint, addr string) *Endpoint {
	for i := range eps {
		if eps[i].Addr == addr {
			return &eps[i]
		}
	}
	return nil
}

// capEndpoints evicts the oldest endpoints (by LastSeen) until at most max
// remain. Returns the number evicted.
func capEndpoints(e *Entry, max int) int {
	if max <= 0 || len(e.Endpoints) <= max {
		return 0
	}
	sort.SliceStable(e.Endpoints, func(i, j int) bool {
		return e.Endpoints[i].LastSeen.After(e.Endpoints[j].LastSeen)
	})
	evicted := len(e.Endpoints) - max
	e.Endpoints = e.Endpoints[:max]
	return evicted
}
