// Package state persists per-record observability state across process
// restarts. Each record gets its own file under <dir>/state/<hash>.json,
// written atomically via internal/fs_atomic so a power cut mid-write can
// never corrupt it.
//
// The state file serves two roles:
//
//  1. Observability: ddns status reads it to answer "what did the daemon
//     last do for this record?".
//  2. Fast-path cache: a future phase can short-circuit the provider Get
//     when state.LastObservedIP matches the resolver's quorum IP and the
//     state is recent. Phase 3 writes the state but does not yet skip the
//     provider call.
//
// Trailing-dot normalization: `home.example.com` and `home.example.com.`
// refer to the same record from the provider's perspective, so they share a
// single state file. The normalization happens inside pathFor, before
// hashing, so both spellings collapse to the same on-disk path.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/fs_atomic"
)

// State is the on-disk representation of one record's last tick outcome.
//
// Field tags are json (not yaml) because the state file is machine-only —
// operators read it via `ddns status`, not by eyeballing the JSON. Pretty-
// printing happens in Save so humans who do cat it still get sensible
// output.
type State struct {
	LastCheckedAt  time.Time `json:"last_checked_at"`
	LastObservedIP string    `json:"last_observed_ip"`
	LastUpdatedAt  time.Time `json:"last_updated_at,omitempty"`
	LastResult     string    `json:"last_result"` // "noop" | "updated" | "error"
	LastError      string    `json:"last_error,omitempty"`
	RecordName     string    `json:"record_name"`
}

// Store reads and writes per-record state files under a single base
// directory. One Store instance is shared across all records in a process.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. The directory is created lazily
// on the first Save via fs_atomic.WriteFile's MkdirAll, so NewStore itself
// cannot fail — which keeps wiring in cmd/ddns simple.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Load returns the persisted State for recordName. If no state file exists
// (first run), Load returns (State{}, ddnserr.ErrNotFound) so callers can
// distinguish "never saved" from "saved but unreadable". JSON-decode errors
// and other I/O failures are wrapped and returned as-is — they are NOT
// reported as ErrNotFound, because silently falling back to first-run
// behavior on a corrupt file would hide real problems.
func (s *Store) Load(recordName string) (State, error) {
	path := s.pathFor(recordName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ddnserr.ErrNotFound
		}
		return State{}, fmt.Errorf("state: read %q: %w", path, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("state: decode %q: %w", path, err)
	}
	return st, nil
}

// Save atomically writes the State for st.RecordName. The JSON payload is
// pretty-printed so a human tailing the state file with `cat` gets sensible
// output. Mode is 0600 because the state can carry project ids and last
// error messages that may include hostnames — worth protecting from other
// local users.
func (s *Store) Save(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	path := s.pathFor(st.RecordName)
	if err := fs_atomic.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("state: write %q: %w", path, err)
	}
	return nil
}

// pathFor computes the on-disk path for recordName. The path is
//
//	<dir>/state/<sha256hex(normalized-name)[:16]>.json
//
// The sha256 prefix keeps file names short (16 hex = 8 bytes of entropy =
// 2^64 → no collision risk for the record counts a single host will ever
// configure) and avoids any filesystem's filename-length limits for long
// FQDNs. It also normalizes trailing dots so `home.example.com` and
// `home.example.com.` hash to the same file.
func (s *Store) pathFor(recordName string) string {
	norm := normalizeName(recordName)
	sum := sha256.Sum256([]byte(norm))
	hex16 := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(s.dir, "state", hex16+".json")
}

// normalizeName forces trailing-dot form. The config layer already does
// this for RecordConfig.Name, but callers that Load by raw user-supplied
// strings (e.g., an ad-hoc test or a future `ddns status <name>` flag)
// benefit from the belt-and-braces normalization here too.
func normalizeName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return n
	}
	if !strings.HasSuffix(n, ".") {
		n += "."
	}
	return n
}
