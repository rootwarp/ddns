package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestRunSmoke builds the ddns binary, launches `ddns run`, SIGTERMs it after
// ~1s, and asserts it exits 0 with "startup" in the combined output.
//
// This test is skipped in short mode because it spawns a subprocess and waits
// a full second. `go test -short ./...` still runs the fast path; `go test
// ./...` (which `make test` invokes) runs this smoke test.
func TestRunSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not supported on windows")
	}

	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "ddns-smoke")

	// Build the binary into a temp dir so we don't collide with make build's
	// bin/ddns that a developer might have open.
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("go build failed: %v", err)
	}

	var combined bytes.Buffer
	cmd := exec.Command(binPath, "run")
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Give the daemon a moment to start and emit its "startup" record.
	time.Sleep(1 * time.Second)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}

	// Enforce a hard ceiling so a broken signal handler fails the test
	// instead of hanging CI.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ddns run exited with error: %v\noutput:\n%s", err, combined.String())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("ddns run did not exit within 3s after SIGTERM\noutput:\n%s", combined.String())
	}

	if !bytes.Contains(combined.Bytes(), []byte("startup")) {
		t.Fatalf("expected \"startup\" log in output, got:\n%s", combined.String())
	}
}
