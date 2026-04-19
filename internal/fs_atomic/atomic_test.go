package fs_atomic_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rootwarp/ddns/internal/fs_atomic"
)

// TestWriteFile_HappyPath verifies WriteFile creates the target, writes the
// bytes exactly, and sets the requested mode.
func TestWriteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "subdir", "file.json")

	data := []byte(`{"k":"v"}`)
	if err := fs_atomic.WriteFile(target, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("content = %q, want %q", got, data)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}
}

// TestWriteFile_OverwriteExisting pre-populates the file with old content
// (and a different mode), then overwrites it. The final file must contain the
// new bytes and the new mode.
func TestWriteFile_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.json")

	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := fs_atomic.WriteFile(target, []byte("new-content"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new-content" {
		t.Fatalf("content = %q, want %q", got, "new-content")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}
}

// TestWriteFile_Permissions verifies 0600 survives the tempfile+rename path —
// os.CreateTemp defaults to 0600 on Unix, but the test asserts it rather than
// assuming it for future auditors.
func TestWriteFile_Permissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.json")

	if err := fs_atomic.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}
}

// TestWriteFile_NoTempLeak verifies that after a successful write the
// directory contains only the final file — no *.tmp-* artifacts are left
// behind by os.CreateTemp + os.Rename.
func TestWriteFile_NoTempLeak(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file.json")

	if err := fs_atomic.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leaked tempfile: %q", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1 (%v)", len(entries), entries)
	}
}

// TestWriteFile_MkdirFailureReturnsError covers the error path when the
// parent cannot be created — e.g., a file exists in the path where a dir is
// required.
func TestWriteFile_MkdirFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	// A plain file in the middle of the requested directory hierarchy forces
	// MkdirAll to fail with "not a directory".
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	target := filepath.Join(blocker, "child", "file.json")

	err := fs_atomic.WriteFile(target, []byte("x"), 0o600)
	if err == nil {
		t.Fatalf("WriteFile: expected error, got nil")
	}
}

// TestWriteFile_RenameFailureReturnsError exercises the rename error path by
// pointing the target at the path of an existing non-empty directory. POSIX
// rename(file, existing-non-empty-dir) fails with ENOTDIR/EISDIR depending on
// the OS; either way it surfaces as a wrapped error from WriteFile.
func TestWriteFile_RenameFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	// Make the final path point at a directory that already exists. The
	// parent dir is dir itself (MkdirAll no-ops), CreateTemp succeeds inside
	// dir, but rename(tmp, targetDir) where targetDir is non-empty fails.
	targetDir := filepath.Join(dir, "target-as-dir")
	if err := os.MkdirAll(filepath.Join(targetDir, "inner"), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := fs_atomic.WriteFile(targetDir, []byte("x"), 0o600)
	if err == nil {
		t.Fatalf("WriteFile: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("err = %v, want a rename error", err)
	}

	// Ensure we did not leak the tmp file on the error path either.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leaked tempfile on error path: %q", e.Name())
		}
	}
}

// TestWriteFile_EmptyData verifies a zero-byte payload is a legal input —
// WriteFile should still produce a file of size zero, not error out.
func TestWriteFile_EmptyData(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "empty.json")
	if err := fs_atomic.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("size = %d, want 0", info.Size())
	}
}

// TestWriteFile_CreateTempFailsOnReadOnlyDir exercises the CreateTemp error
// branch. Skipped if running as root, because root bypasses directory mode
// bits and CreateTemp would succeed.
func TestWriteFile_CreateTempFailsOnReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits; skipping")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "ro")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Strip write+execute permission so os.CreateTemp inside it fails.
	if err := os.Chmod(sub, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore write perm on cleanup so t.TempDir can clean up.
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	target := filepath.Join(sub, "file.json")
	err := fs_atomic.WriteFile(target, []byte("x"), 0o600)
	if err == nil {
		t.Fatalf("WriteFile: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create temp") {
		t.Fatalf("err = %v, want a 'create temp' error", err)
	}
}
