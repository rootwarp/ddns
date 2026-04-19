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

// Issue 4.4: SIGHUP-triggered reload tests (subprocess-based).
//
// We spawn the ddns binary, send SIGHUP, and assert config_reload_ok or
// config_reload_failed appears in the output. The in-process hook is
// covered by internal/daemon/reload_test.go; these end-to-end tests cover
// the cmd/ddns wiring (signal.Notify goroutine → config.Load →
// Daemon.UpdateConfig).
//
// The binary is the one cached by sync_test.go's ddnsBinary helper — no
// need for a parallel build invocation.

// writeConfig writes a minimal valid config pointing at the fake
// provider and at HTTP echo-source URLs that will fail cleanly when
// hit (so ticks log errors but do not block indefinitely).
func writeConfig(t *testing.T, path, stateDir string, pollInterval string, record string) {
	t.Helper()
	body := []byte(`poll_interval: ` + pollInterval + `
state_path: ` + stateDir + `
log_format: json
resolver:
  sources:
    - http://127.0.0.1:1/a
    - http://127.0.0.1:1/b
  quorum: 1
  timeout: 500ms
records:
  - project: example-proj
    managed_zone: example-zone
    name: ` + record + `
    type: A
    ttl: 300
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// writeInvalidConfig writes a config that will fail validation (quorum
// exceeds source count) so we can confirm the reload path logs
// config_reload_failed and keeps running.
func writeInvalidConfig(t *testing.T, path, stateDir string) {
	t.Helper()
	body := []byte(`poll_interval: 1s
state_path: ` + stateDir + `
log_format: json
resolver:
  sources:
    - http://127.0.0.1:1/a
  quorum: 5
  timeout: 500ms
records:
  - project: example-proj
    managed_zone: example-zone
    name: broken.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
}

// TestSIGHUP_ValidConfigReloadsOK — start the daemon, send SIGHUP, verify
// config_reload_ok appears.
func TestSIGHUP_ValidConfigReloadsOK(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP not supported on windows")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	writeConfig(t, cfgPath, stateDir, "10s", "hup-ok.example.com.")

	bin := ddnsBinary(t)
	cmd := exec.Command(bin, "run", "--config", cfgPath)
	cmd.Env = append(os.Environ(), "DDNS_FAKE_PROVIDER=fake")
	out, done, cancel := startCapturing(t, cmd)
	defer cancel()

	// Wait for startup.
	if !waitFor(out, "startup", 5*time.Second) {
		t.Fatalf("startup never logged:\n%s", out.snapshot())
	}

	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	if !waitFor(out, "config_reload_ok", 3*time.Second) {
		t.Fatalf("config_reload_ok never logged:\n%s", out.snapshot())
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon did not exit within 3s of SIGTERM:\n%s", out.snapshot())
	}
}

// TestSIGHUP_InvalidConfigKeepsOld — overwrite the config with a bad
// document, SIGHUP, assert config_reload_failed and that the process is
// still alive.
func TestSIGHUP_InvalidConfigKeepsOld(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP not supported on windows")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	writeConfig(t, cfgPath, stateDir, "10s", "hup-bad.example.com.")

	bin := ddnsBinary(t)
	cmd := exec.Command(bin, "run", "--config", cfgPath)
	cmd.Env = append(os.Environ(), "DDNS_FAKE_PROVIDER=fake")
	out, done, cancel := startCapturing(t, cmd)
	defer cancel()

	if !waitFor(out, "startup", 5*time.Second) {
		t.Fatalf("startup never logged:\n%s", out.snapshot())
	}

	// Replace the config with an invalid one.
	writeInvalidConfig(t, cfgPath, stateDir)

	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	if !waitFor(out, "config_reload_failed", 3*time.Second) {
		t.Fatalf("config_reload_failed never logged:\n%s", out.snapshot())
	}

	// Process is still alive — SIGTERM should exit 0.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon exited non-zero after SIGTERM: %v\n%s", err, out.snapshot())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon did not exit within 3s of SIGTERM:\n%s", out.snapshot())
	}
}

// --- capture helpers -------------------------------------------------

type captureBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captureBuf) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captureBuf) snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *captureBuf) contains(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Contains(c.buf.Bytes(), []byte(s))
}

// startCapturing launches cmd with stdout+stderr drained into a synchronized
// buffer. Returns the buffer, a done-channel signaled when the process exits,
// and a cancel function that best-effort kills the process.
func startCapturing(t *testing.T, cmd *exec.Cmd) (*captureBuf, <-chan error, func()) {
	t.Helper()
	out := &captureBuf{}
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
	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(r io.Reader) {
		defer wg.Done()
		_, _ = io.Copy(out, r)
	}
	go drain(stdout)
	go drain(stderr)

	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		wg.Wait()
		done <- err
	}()

	cancel := func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	}
	return out, done, cancel
}

func waitFor(buf *captureBuf, needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if buf.contains(needle) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
