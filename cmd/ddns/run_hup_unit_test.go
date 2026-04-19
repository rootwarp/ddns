package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
	"github.com/rootwarp/ddns/internal/state"
)

// In-process unit tests for installSIGHUP and buildProvider. They
// complement the subprocess tests in sighup_test.go with finer-grained
// coverage (and faster runtime).

func TestBuildProvider_FakeEnvVar(t *testing.T) {
	t.Setenv(providerEnvVar, "fake")
	p, err := buildProvider(context.Background())
	if err != nil {
		t.Fatalf("buildProvider(fake): %v", err)
	}
	if p == nil {
		t.Fatalf("buildProvider(fake) = nil provider")
	}
}

func TestBuildProvider_InvalidEnvVar(t *testing.T) {
	t.Setenv(providerEnvVar, "not-a-real-value")
	if _, err := buildProvider(context.Background()); err == nil {
		t.Fatalf("buildProvider(invalid): expected error")
	}
}

// TestInstallSIGHUP_ReloadsConfig runs the installSIGHUP goroutine against
// a live *daemon.Daemon, sends ourselves a SIGHUP, and verifies the
// daemon's config has been swapped.
//
// Must run on Unix: windows does not have SIGHUP. Also single-process
// (sends signal to os.Getpid()).
func TestInstallSIGHUP_ReloadsConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP not supported on windows")
	}

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	stateDir := filepath.Join(tmp, "state")
	writeUnitCfg(t, cfgPath, stateDir, "5m")

	initial, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("initial config.Load: %v", err)
	}

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(lockedWriter{buf: &logBuf, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	d := daemon.New(initial, resolver.New(initial.Resolver), fake.New(), state.NewStore(stateDir), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := installSIGHUP(ctx, d, cfgPath, logger)
	defer stop()

	// Rewrite the config with a different PollInterval.
	writeUnitCfg(t, cfgPath, stateDir, "15s")

	// Send ourselves SIGHUP.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("kill SIGHUP: %v", err)
	}

	// Poll until the config has been swapped.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logMu.Lock()
		hit := strings.Contains(logBuf.String(), "config_reload_ok")
		logMu.Unlock()
		if hit {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logBuf.String(), "config_reload_ok") {
		t.Fatalf("config_reload_ok never logged:\n%s", logBuf.String())
	}
}

// TestInstallSIGHUP_InvalidConfigLogsFailed: point the watcher at a path
// whose config is rewritten to be invalid, send SIGHUP, assert
// config_reload_failed appears.
func TestInstallSIGHUP_InvalidConfigLogsFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP not supported on windows")
	}

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	stateDir := filepath.Join(tmp, "state")
	writeUnitCfg(t, cfgPath, stateDir, "5m")

	initial, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("initial config.Load: %v", err)
	}

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(lockedWriter{buf: &logBuf, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	d := daemon.New(initial, resolver.New(initial.Resolver), fake.New(), state.NewStore(stateDir), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := installSIGHUP(ctx, d, cfgPath, logger)
	defer stop()

	// Write an invalid config (quorum > sources).
	badBody := []byte(`poll_interval: 5m
state_path: ` + stateDir + `
log_format: json
resolver:
  sources:
    - http://127.0.0.1:1/a
  quorum: 5
  timeout: 500ms
records:
  - project: p
    managed_zone: z
    name: bad.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(cfgPath, badBody, 0o600); err != nil {
		t.Fatalf("write bad cfg: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("kill SIGHUP: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logMu.Lock()
		hit := strings.Contains(logBuf.String(), "config_reload_failed")
		logMu.Unlock()
		if hit {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logBuf.String(), "config_reload_failed") {
		t.Fatalf("config_reload_failed never logged:\n%s", logBuf.String())
	}
}

// TestInstallSIGHUP_ContextCancelStopsGoroutine — cancelling the parent
// context must cause the goroutine to exit cleanly without requiring the
// stop function.
func TestInstallSIGHUP_ContextCancelStopsGoroutine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGHUP not supported on windows")
	}
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	stateDir := filepath.Join(tmp, "state")
	writeUnitCfg(t, cfgPath, stateDir, "5m")

	initial, _ := config.Load(cfgPath)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	d := daemon.New(initial, resolver.New(initial.Resolver), fake.New(), state.NewStore(stateDir), logger)

	ctx, cancel := context.WithCancel(context.Background())
	stop := installSIGHUP(ctx, d, cfgPath, logger)
	cancel()
	// stop() blocks on the goroutine's exit; a hang here would fail the
	// test via the default go test timeout.
	stop()
}

// writeUnitCfg writes a minimal valid config for in-process unit tests.
func writeUnitCfg(t *testing.T, path, stateDir, pollInterval string) {
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
  - project: p
    managed_zone: z
    name: unit.example.com.
    type: A
    ttl: 300
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
}

type lockedWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
