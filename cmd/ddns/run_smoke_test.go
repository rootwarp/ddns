package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestRunSmoke builds the ddns binary, launches `ddns run`, waits for the
// daemon's "startup" log to appear, SIGTERMs it, and asserts it exits 0.
//
// It reads the subprocess's output incrementally and signals only after the
// startup record has been observed, so the test is deterministic under load
// rather than depending on a fixed sleep.
//
// Skipped in -short mode because it spawns a subprocess and cold-builds the
// binary. `go test -short ./...` skips it; plain `go test ./...` (which `make
// test` invokes) runs it.
func TestRunSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not supported on windows")
	}

	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "ddns-smoke")

	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("go build failed: %v", err)
	}

	cmd := exec.Command(binPath, "run", "--config", "./testdata/example.yaml")
	// DDNS_FAKE_PROVIDER=fake tells runAction to skip gcp.New (which would
	// fail with ErrAuth on a CI runner with no ADC) and use the in-memory
	// fake provider instead. See the providerEnvVar doc in cmd/ddns/run.go
	// for the escape-hatch rationale.
	cmd.Env = append(os.Environ(), "DDNS_FAKE_PROVIDER=fake")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Drain stdout+stderr concurrently into a shared buffer.
	var (
		outBuf bytes.Buffer
		outMu  sync.Mutex
		wg     sync.WaitGroup
	)
	drain := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				outMu.Lock()
				outBuf.Write(buf[:n])
				outMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go drain(stdout)
	go drain(stderr)

	// Wait up to 5s for the startup record to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		outMu.Lock()
		hasStartup := bytes.Contains(outBuf.Bytes(), []byte("startup"))
		outMu.Unlock()
		if hasStartup {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	outMu.Lock()
	gotStartup := bytes.Contains(outBuf.Bytes(), []byte("startup"))
	snapshot := outBuf.String()
	outMu.Unlock()
	if !gotStartup {
		_ = cmd.Process.Kill()
		wg.Wait()
		t.Fatalf("startup record never appeared within 5s\noutput:\n%s", snapshot)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		wg.Wait()
		if err != nil {
			t.Fatalf("ddns run exited with error: %v\noutput:\n%s", err, outBuf.String())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		wg.Wait()
		t.Fatalf("ddns run did not exit within 3s after SIGTERM\noutput:\n%s", outBuf.String())
	}
}
