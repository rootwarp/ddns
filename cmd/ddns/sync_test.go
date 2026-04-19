package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildDdnsBinary compiles the ddns binary into t.TempDir() and returns its
// absolute path. Compilation is cached within the test binary so Fix-J's
// six exit-code scenarios share one `go build` invocation.
var (
	syncBinOnce sync.Once
	syncBinPath string
	syncBinErr  error
)

func ddnsBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ddns sync subprocess tests are Unix-only")
	}
	syncBinOnce.Do(func() {
		tmp, err := os.MkdirTemp("", "ddns-sync-bin-*")
		if err != nil {
			syncBinErr = err
			return
		}
		// Intentionally not cleaning up: the binary is cheap disk and
		// leaving it lets developers re-inspect the build output on
		// failure. The OS's temp-dir GC handles the rest.
		p := filepath.Join(tmp, "ddns")
		cmd := exec.Command("go", "build", "-o", p, ".")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			syncBinErr = err
			return
		}
		syncBinPath = p
	})
	if syncBinErr != nil {
		t.Fatalf("build ddns binary: %v", syncBinErr)
	}
	return syncBinPath
}

// runDdns runs the ddns binary with the given args and environment. It
// returns the exit code, combined stdout+stderr as a string, and the raw
// *exec.Cmd error (if any).
func runDdns(t *testing.T, args []string, env map[string]string) (exitCode int, combined string) {
	t.Helper()
	bin := ddnsBinary(t)
	cmd := exec.Command(bin, args...)
	base := os.Environ()
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	cmd.Env = base

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	combined = out.String()
	if err == nil {
		return 0, combined
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), combined
	}
	t.Fatalf("exec.Run unexpected error: %v\n%s", err, combined)
	return -1, combined
}

// Phase-3 sync covers four exit paths. The tests build the real binary and
// verify exit codes against bad inputs — this is the cheapest way to
// confirm the sync wiring without mocking urfave/cli internals.

// TestSyncAction_MissingConfigExitsDefault — a missing config file path
// produces an os.ReadFile error that config.Load wraps but does NOT wrap
// with ddnserr.ErrConfig (only validation failures do). Per Fix J that
// means exit code = 2 (default error).
func TestSyncAction_MissingConfigExitsDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	code, out := runDdns(t, []string{"sync", "--config", "/does/not/exist.yaml"}, nil)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (missing file → default error)\nout:\n%s", code, out)
	}
	if !strings.Contains(out, "ddns:") {
		t.Fatalf("expected error banner, got:\n%s", out)
	}
}

// TestSyncAction_InvalidConfigExits3 uses a YAML file with validation
// errors (wraps ErrConfig) — this is what the PRD calls "config error →
// exit 3".
func TestSyncAction_InvalidConfigExits3(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "bad.yaml")
	// Quorum=5 exceeds len(sources)=2 — a validation error that wraps
	// ddnserr.ErrConfig.
	body := []byte(`poll_interval: 1s
state_path: /tmp/ddns-test
log_format: json
resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
  quorum: 5
  timeout: 10s
records:
  - project: example-proj
    managed_zone: example-zone
    name: a.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, out := runDdns(t, []string{"sync", "--config", cfgPath}, nil)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (ErrConfig)\nout:\n%s", code, out)
	}
}

// TestSyncAction_FakeProvider_Succeeds runs sync against the in-memory
// fake provider and a config pointed at an unreachable resolver — but we
// arrange for it to succeed by using the `example.yaml` that points at
// real IP-echo services. This is the happy-path smoke.
func TestSyncAction_FakeProvider_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	stateDir := t.TempDir()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	// Point at services that definitely reachable in most CI — but to
	// make the test deterministic in sandboxed environments, we use a
	// local httptest-like setup is overkill; instead we rely on the
	// resolver's quorum=1 over two sources and skip on network failure.
	body := []byte(`poll_interval: 1s
state_path: ` + stateDir + `
log_format: json
resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
  quorum: 1
  timeout: 10s
records:
  - project: example-proj
    managed_zone: example-zone
    name: sync-smoke.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	code, out := runDdns(t, []string{"sync", "--config", cfgPath}, map[string]string{
		"DDNS_FAKE_PROVIDER": "fake",
	})
	// Possible outcomes:
	//   - 0: resolver reached quorum and fake provider created the record.
	//   - 1: no network; resolver returned ErrNoQuorum (sandbox case).
	// Either is acceptable for the "build + wire" smoke we're asserting
	// here. What we MUST NOT see is 2 (generic), 3 (auth/config), or any
	// other code — those would indicate a wiring bug.
	if code != 0 && code != 1 {
		t.Fatalf("exit code = %d, want 0 (success) or 1 (no quorum in sandbox)\nout:\n%s", code, out)
	}
}

// TestSyncAction_ResolverUnreachableExits1 — all resolver sources point
// at closed ports, so the resolver returns ErrNoQuorum and ddns sync
// exits 1. This is the "no quorum" branch of Fix J.
func TestSyncAction_ResolverUnreachableExits1(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "unreachable.yaml")
	// Port 1 is reserved for tcpmux and is almost never bound on
	// end-user machines, so the resolver's GETs fail fast.
	body := []byte(`poll_interval: 1s
state_path: ` + t.TempDir() + `
log_format: json
resolver:
  sources:
    - http://127.0.0.1:1/a
    - http://127.0.0.1:1/b
  quorum: 1
  timeout: 1s
records:
  - project: example-proj
    managed_zone: example-zone
    name: a.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, out := runDdns(t, []string{"sync", "--config", cfgPath}, map[string]string{
		"DDNS_FAKE_PROVIDER": "fake",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (ErrNoQuorum)\nout:\n%s", code, out)
	}
}

// TestSyncAction_HelpIncludesDryRun verifies the --dry-run flag is
// declared on the sync subcommand, even though Phase 3 does not yet honor
// it (Phase 4 issue 4.1 wires it through).
func TestSyncAction_HelpIncludesDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	code, out := runDdns(t, []string{"sync", "--help"}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for --help\nout:\n%s", code, out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Fatalf("help output missing dry-run flag:\n%s", out)
	}
}

// TestSyncAction_Timeout is a safety check — the subprocess must exit
// within a few seconds regardless. A hung sync would indicate a daemon
// context-cancel bug.
func TestSyncAction_TimeoutBound(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	// Resolver timeout 1s × 2 sources + fake provider → sync completes
	// within well under 30s.
	body := []byte(`poll_interval: 1s
state_path: ` + t.TempDir() + `
log_format: json
resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
  quorum: 1
  timeout: 2s
records:
  - project: example-proj
    managed_zone: example-zone
    name: a.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bin := ddnsBinary(t)

	cmd := exec.Command(bin, "sync", "--config", cfgPath)
	cmd.Env = append(os.Environ(), "DDNS_FAKE_PROVIDER=fake")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Any exit within the bound is fine.
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("ddns sync did not exit within 30s\nout:\n%s", out.String())
	}
}
