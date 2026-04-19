package fs_atomic

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// fakeTmp drives every error branch in WriteFile that requires a failing
// syscall on the tempfile. Each branch is selected by an injected error
// field; callers write a short test per branch rather than relying on
// platform-specific filesystem states.
type fakeTmp struct {
	realFile *os.File
	writeErr error
	chmodErr error
	syncErr  error
	closeErr error
	// countCloses records how many times Close was called — the doc comment
	// on WriteFile promises each error branch closes the tempfile before
	// returning, and a test enforces that.
	countCloses int
}

func (f *fakeTmp) Name() string { return f.realFile.Name() }

func (f *fakeTmp) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.realFile.Write(p)
}

func (f *fakeTmp) Chmod(mode fs.FileMode) error {
	if f.chmodErr != nil {
		return f.chmodErr
	}
	return f.realFile.Chmod(mode)
}

func (f *fakeTmp) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.realFile.Sync()
}

func (f *fakeTmp) Close() error {
	f.countCloses++
	// Always close the underlying fd so the test doesn't leak it; the
	// injected error (if any) overrides the return value.
	realErr := f.realFile.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return realErr
}

// TestWriteFile_CreateTempHookFails drives the CreateTemp error branch via
// the injected hook. Previously this branch was only reachable on a
// read-only-directory test that skipped as root.
func TestWriteFile_CreateTempHookFails(t *testing.T) {
	orig := createTempFile
	t.Cleanup(func() { createTempFile = orig })
	boom := errors.New("boom")
	createTempFile = func(dir, pattern string) (tmpFile, error) {
		return nil, boom
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "file.json")
	err := WriteFile(target, []byte("x"), 0o600)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("WriteFile err = %v, want wrapping boom", err)
	}
}

// runWithFakeTmp invokes WriteFile with a fakeTmp that carries the given
// injected errors, returning (the fakeTmp captured during the call, the
// error from WriteFile). The test can then assert on both.
func runWithFakeTmp(t *testing.T, inject func(*fakeTmp)) (*fakeTmp, error) {
	t.Helper()
	orig := createTempFile
	t.Cleanup(func() { createTempFile = orig })
	var capt *fakeTmp
	createTempFile = func(dir, pattern string) (tmpFile, error) {
		real, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		f := &fakeTmp{realFile: real}
		if inject != nil {
			inject(f)
		}
		capt = f
		return f, nil
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "file.json")
	err := WriteFile(target, []byte("data"), 0o600)
	return capt, err
}

func TestWriteFile_WriteErrorClosesAndWraps(t *testing.T) {
	boom := errors.New("write-boom")
	f, err := runWithFakeTmp(t, func(f *fakeTmp) { f.writeErr = boom })
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrap of %v", err, boom)
	}
	if f.countCloses != 1 {
		t.Fatalf("Close calls = %d, want 1 (write branch must close tmp)", f.countCloses)
	}
}

func TestWriteFile_ChmodErrorClosesAndWraps(t *testing.T) {
	boom := errors.New("chmod-boom")
	f, err := runWithFakeTmp(t, func(f *fakeTmp) { f.chmodErr = boom })
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrap of %v", err, boom)
	}
	if f.countCloses != 1 {
		t.Fatalf("Close calls = %d, want 1", f.countCloses)
	}
}

func TestWriteFile_SyncErrorClosesAndWraps(t *testing.T) {
	boom := errors.New("sync-boom")
	f, err := runWithFakeTmp(t, func(f *fakeTmp) { f.syncErr = boom })
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrap of %v", err, boom)
	}
	if f.countCloses != 1 {
		t.Fatalf("Close calls = %d, want 1", f.countCloses)
	}
}

func TestWriteFile_CloseErrorWraps(t *testing.T) {
	boom := errors.New("close-boom")
	f, err := runWithFakeTmp(t, func(f *fakeTmp) { f.closeErr = boom })
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrap of %v", err, boom)
	}
	// Close was called exactly once — the close branch does not double-close.
	if f.countCloses != 1 {
		t.Fatalf("Close calls = %d, want 1", f.countCloses)
	}
}
