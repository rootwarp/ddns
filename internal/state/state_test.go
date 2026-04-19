package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/state"
)

// TestStore_RoundTrip — Save a State, Load it by record name, asserts the
// value round-trips exactly.
func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	now := time.Date(2026, 4, 19, 16, 30, 2, 0, time.UTC)
	want := state.State{
		LastCheckedAt:  now,
		LastObservedIP: "192.0.2.42",
		LastUpdatedAt:  now.Add(-2 * time.Hour),
		LastResult:     "updated",
		RecordName:     "home.example.com.",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(want.RecordName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.LastCheckedAt.Equal(want.LastCheckedAt) {
		t.Fatalf("LastCheckedAt = %v, want %v", got.LastCheckedAt, want.LastCheckedAt)
	}
	if !got.LastUpdatedAt.Equal(want.LastUpdatedAt) {
		t.Fatalf("LastUpdatedAt = %v, want %v", got.LastUpdatedAt, want.LastUpdatedAt)
	}
	if got.LastObservedIP != want.LastObservedIP {
		t.Fatalf("LastObservedIP = %q, want %q", got.LastObservedIP, want.LastObservedIP)
	}
	if got.LastResult != want.LastResult {
		t.Fatalf("LastResult = %q, want %q", got.LastResult, want.LastResult)
	}
	if got.RecordName != want.RecordName {
		t.Fatalf("RecordName = %q, want %q", got.RecordName, want.RecordName)
	}
}

// TestStore_LoadMissing — Load on a record with no prior Save returns
// ddnserr.ErrNotFound so callers can treat it as "first run".
func TestStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	_, err := store.Load("does-not-exist.example.com.")
	if err == nil {
		t.Fatalf("Load: expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrNotFound) {
		t.Fatalf("Load err = %v, want ErrNotFound", err)
	}
}

// TestStore_SavePermissions verifies the state file on disk is mode 0600 —
// the state may carry the last error from the provider, which can leak the
// operator's project id; 0600 is a minimum sane default.
func TestStore_SavePermissions(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	if err := store.Save(state.State{RecordName: "home.example.com.", LastResult: "noop"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Walk dir to find the state file (path is hash-derived; we don't
	// recompute it here — we just look for the one JSON file under
	// dir/state/).
	matches, err := filepath.Glob(filepath.Join(dir, "state", "*.json"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("state files = %d, want 1", len(matches))
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}

	// Sanity: file contents are valid JSON and carry the record name.
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded state.State
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal on-disk: %v\n%s", err, raw)
	}
	if decoded.RecordName != "home.example.com." {
		t.Fatalf("on-disk RecordName = %q, want home.example.com.", decoded.RecordName)
	}
}

// TestStore_NameHashingCollision — two records with different names must
// map to different state files. Regression for a naive pathFor that uses
// the raw name or a too-short hash prefix.
func TestStore_NameHashingCollision(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	if err := store.Save(state.State{RecordName: "alpha.example.com.", LastResult: "noop"}); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}
	if err := store.Save(state.State{RecordName: "bravo.example.com.", LastResult: "noop"}); err != nil {
		t.Fatalf("Save bravo: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "state", "*.json"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("state files = %d, want 2 (one per record)", len(matches))
	}
}

// TestStore_TrailingDotNormalization — `home.example.com` and
// `home.example.com.` must resolve to the same state file, so operators
// whose config happens to be missing the trailing dot don't end up with two
// parallel state histories.
func TestStore_TrailingDotNormalization(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	if err := store.Save(state.State{RecordName: "home.example.com.", LastObservedIP: "192.0.2.42", LastResult: "updated"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load("home.example.com") // no trailing dot
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastObservedIP != "192.0.2.42" {
		t.Fatalf("Load(no trailing dot) LastObservedIP = %q, want %q", got.LastObservedIP, "192.0.2.42")
	}

	// Verify only ONE state file was produced.
	matches, err := filepath.Glob(filepath.Join(dir, "state", "*.json"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("state files = %d, want 1 (both names collapse)", len(matches))
	}
}

// TestStore_LoadCorruptFile — a file whose contents are not valid JSON
// returns a wrapped error that is NOT ErrNotFound. This guards operators
// against a silent "first-run fallback" after their state got corrupted.
func TestStore_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)

	// Write a valid state file, then corrupt it.
	if err := store.Save(state.State{RecordName: "home.example.com.", LastResult: "noop"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "state", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("setup: matches=%v err=%v", matches, err)
	}
	if err := os.WriteFile(matches[0], []byte("not-json"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	_, err = store.Load("home.example.com.")
	if err == nil {
		t.Fatalf("Load: expected error, got nil")
	}
	if errors.Is(err, ddnserr.ErrNotFound) {
		t.Fatalf("Load err = %v, must NOT be ErrNotFound for a corrupt file", err)
	}
}

// TestStore_LoadPermissionDenied exercises the Load path where ReadFile
// returns an error that is not os.ErrNotExist — the error must be wrapped
// and returned, NOT mistaken for ErrNotFound. Skipped as root, which
// bypasses mode bits.
func TestStore_LoadPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits; skipping")
	}
	dir := t.TempDir()
	store := state.NewStore(dir)
	if err := store.Save(state.State{RecordName: "home.example.com.", LastResult: "noop"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "state", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("setup: matches=%v err=%v", matches, err)
	}
	// Strip read permission so os.ReadFile fails with EACCES.
	if err := os.Chmod(matches[0], 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(matches[0], 0o600) })

	_, err = store.Load("home.example.com.")
	if err == nil {
		t.Fatalf("Load: expected error, got nil")
	}
	if errors.Is(err, ddnserr.ErrNotFound) {
		t.Fatalf("Load err = %v, must NOT be ErrNotFound for a permission error", err)
	}
}

// TestStore_SaveIntoBlockedDir exercises the Save → fs_atomic.WriteFile
// error branch by pointing Store at a directory whose path is blocked by
// an existing regular file.
func TestStore_SaveIntoBlockedDir(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Store's base dir includes a segment that's actually a file, so
	// MkdirAll inside fs_atomic.WriteFile fails with ENOTDIR.
	store := state.NewStore(filepath.Join(blocker, "inner"))

	err := store.Save(state.State{RecordName: "home.example.com.", LastResult: "noop"})
	if err == nil {
		t.Fatalf("Save: expected error, got nil")
	}
}

// TestStore_NormalizeEmptyName covers the empty-name branch of the
// normalization helper. An empty record name is nonsensical, but the
// helper's zero-value behavior is exercised so the coverage tool sees all
// branches.
func TestStore_NormalizeEmptyName(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore(dir)
	// An empty record name still saves — the path hash handles it — so the
	// call should succeed. We don't assert anything beyond non-error; the
	// goal is coverage of the normalizeName "empty" branch.
	if err := store.Save(state.State{RecordName: "", LastResult: "noop"}); err != nil {
		t.Fatalf("Save empty name: %v", err)
	}
}
