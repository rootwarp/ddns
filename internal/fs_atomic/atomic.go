package fs_atomic

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// tmpFile is the subset of *os.File that WriteFile uses. Expressed as an
// interface so tests can drive the chmod/sync/close error branches without
// platform-specific filesystem tricks. Production code always passes a real
// *os.File returned from os.CreateTemp.
type tmpFile interface {
	Name() string
	Write(p []byte) (int, error)
	Chmod(mode fs.FileMode) error
	Sync() error
	Close() error
}

// createTempFile is the syscall seam that returns a fresh tmpFile in dir.
// It is a package-level var so tests can substitute a fake implementation.
var createTempFile = func(dir, pattern string) (tmpFile, error) {
	return os.CreateTemp(dir, pattern)
}

// WriteFile writes data to path atomically. The target file is either the
// new bytes or the old bytes (or absent, if no prior file existed) — never
// a torn or zero-length intermediate.
//
// Implementation:
//
//  1. MkdirAll the parent directory with 0700 so the state tree is created
//     on first run with owner-only access.
//  2. Create a tempfile in the same directory as path (so the later rename
//     is a same-filesystem operation, not a cross-device copy).
//  3. Write the bytes, Chmod to mode, Sync, Close.
//  4. os.Rename(tmp, path) — POSIX guarantees this is atomic on the same FS.
//  5. Open the parent dir and fsync it so the rename is durable across a
//     power loss. fsync of the dir is intentionally non-fatal: the rename
//     has already succeeded from the process's point of view, and forcing
//     an error here would contradict that.
//
// The tmp file is removed via defer: on the success path the rename already
// moved the inode out of the dir so os.Remove is a harmless no-op; on any
// failure path we ensure the half-written tmp does not survive.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("fs_atomic: mkdir %q: %w", dir, err)
	}

	tmp, err := createTempFile(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fs_atomic: create temp: %w", err)
	}
	// Safe: on the happy path, Rename has already removed tmp.Name() from
	// the directory; os.Remove returns an ignored ENOENT. On any error
	// path this guarantees the half-written tmp does not survive.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fs_atomic: write: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fs_atomic: chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fs_atomic: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fs_atomic: close: %w", err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("fs_atomic: rename: %w", err)
	}

	// Directory fsync: non-fatal on error. The rename has already succeeded
	// at the VFS level; a dir-fsync failure only means the rename may not
	// be crash-durable, not that the caller's write failed.
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
	return nil
}
