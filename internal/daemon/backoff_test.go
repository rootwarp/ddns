package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
)

// Issue 4.2: exponential backoff.
//
// Strategy. Backoff state is internal to Daemon; we exercise it through
// the public surface (ReconcileOnce + the updateBackoff path that Run
// invokes). To observe the scheduled delay we inject an injectable clock
// via daemon.NewWithClock (test-only constructor added for this purpose)
// and parse the backoff_scheduled log line to extract the delay. Driving
// real time is brittle — the log event is the contract.

// fixedClock is a deterministic time source that returns whatever now is
// currently set via set(). It lets backoff tests observe nextTickNotBefore
// computation without flakiness.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// newDaemonWithClock wires a daemon with an injectable clock. It uses the
// exported daemon.SetClock helper (declared in export_test.go) so tests
// don't need an exported field.
func newDaemonWithClock(t *testing.T, cfg *config.Config, res resolver.IPResolver, prov dnsprovider.DNSProvider, clk *fixedClock) (*daemon.Daemon, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d := daemon.New(cfg, res, prov, newStore(t), logger)
	daemon.SetClock(d, clk.read)
	return d, &buf
}

// transientProvider injects wrap-of-ErrTransient into Get so every tick
// returns a provider-layer transient error; combined with a resolver that
// reaches quorum, this drives the transient-backoff branch exclusively.
type transientProvider struct {
	err error
}

func (p *transientProvider) Get(_ context.Context, _ dnsprovider.RecordRef) (dnsprovider.Record, error) {
	return dnsprovider.Record{}, p.err
}
func (p *transientProvider) Upsert(_ context.Context, _ dnsprovider.RecordRef, _ dnsprovider.Record) (dnsprovider.UpsertResult, error) {
	return dnsprovider.UpsertResult{}, p.err
}

func transientErr() error {
	return fmt.Errorf("fake: %w", ddnserr.ErrTransient)
}

// TestBackoff_FirstTransientFailure_SchedulesNextTickAtInterval — one
// failure; nextTickNotBefore = now + PollInterval; log event records
// delay=5m.
func TestBackoff_FirstTransientFailure_SchedulesNextTickAtInterval(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, logBuf := newDaemonWithClock(t, cfg, res, prov, clk)

	// First tick fails transiently.
	if err := d.ReconcileOnce(context.Background(), false); err == nil {
		t.Fatalf("ReconcileOnce: want transient err, got nil")
	}
	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("updateBackoff returned fatal: %v", err)
	}

	if !strings.Contains(logBuf.String(), "backoff_scheduled") {
		t.Fatalf("log missing backoff_scheduled:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "delay=5m0s") {
		t.Fatalf("expected delay=5m0s, got:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "consecutive_failures=1") {
		t.Fatalf("expected consecutive_failures=1, got:\n%s", logBuf.String())
	}
}

// TestBackoff_ConsecutiveFailures_ExponentialDoubling — three failures;
// delays are PollInterval * {1, 2, 4}.
func TestBackoff_ConsecutiveFailures_ExponentialDoubling(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, logBuf := newDaemonWithClock(t, cfg, res, prov, clk)

	for i := 0; i < 3; i++ {
		if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
			t.Fatalf("updateBackoff[%d]: %v", i, err)
		}
	}
	out := logBuf.String()
	for _, want := range []string{"delay=5m0s", "delay=10m0s", "delay=20m0s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in log output:\n%s", want, out)
		}
	}
}

// TestBackoff_CapAt30Min — six failures; delay caps at 30m.
func TestBackoff_CapAt30Min(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, logBuf := newDaemonWithClock(t, cfg, res, prov, clk)

	for i := 0; i < 6; i++ {
		if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
			t.Fatalf("updateBackoff[%d]: %v", i, err)
		}
	}
	out := logBuf.String()
	// Iterations 5, 6 produce delays 80m, 160m naively, both clamped to 30m.
	if !strings.Contains(out, "consecutive_failures=5") {
		t.Fatalf("expected consecutive_failures=5:\n%s", out)
	}
	if !strings.Contains(out, "delay=30m0s") {
		t.Fatalf("expected delay=30m0s (ceiling):\n%s", out)
	}
	// There must be no delay larger than 30m in the output.
	for _, bad := range []string{"delay=40m0s", "delay=80m0s", "delay=160m0s"} {
		if strings.Contains(out, bad) {
			t.Fatalf("saw %q in log, ceiling broken:\n%s", bad, out)
		}
	}
}

// TestBackoff_SuccessResetsCounter — failure, failure, success, failure →
// last delay is back to PollInterval * 1.
func TestBackoff_SuccessResetsCounter(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, logBuf := newDaemonWithClock(t, cfg, res, prov, clk)

	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("fail1: %v", err)
	}
	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("fail2: %v", err)
	}
	// Success.
	if err := daemon.UpdateBackoff(d, nil); err != nil {
		t.Fatalf("success: %v", err)
	}
	// Clear the log so we only assert on the next failure line.
	logBuf.Reset()
	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("fail3: %v", err)
	}
	out := logBuf.String()
	if !strings.Contains(out, "consecutive_failures=1") {
		t.Fatalf("expected counter reset to 1:\n%s", out)
	}
	if !strings.Contains(out, "delay=5m0s") {
		t.Fatalf("expected delay=5m0s after reset:\n%s", out)
	}
}

// TestBackoff_NoQuorum_DoesNotIncrement — ErrNoQuorum neither increments
// the transient counter nor resets it. After a NoQuorum between two
// transient failures, the second transient still reports counter=2.
func TestBackoff_NoQuorum_DoesNotIncrement(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, logBuf := newDaemonWithClock(t, cfg, res, prov, clk)

	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("fail1: %v", err)
	}
	noQuorum := fmt.Errorf("resolver: %w", ddnserr.ErrNoQuorum)
	if err := daemon.UpdateBackoff(d, noQuorum); err != nil {
		t.Fatalf("noquorum: %v", err)
	}
	logBuf.Reset()
	if err := daemon.UpdateBackoff(d, transientErr()); err != nil {
		t.Fatalf("fail2: %v", err)
	}
	out := logBuf.String()
	if !strings.Contains(out, "consecutive_failures=2") {
		t.Fatalf("expected counter=2 (ErrNoQuorum did not increment or reset):\n%s", out)
	}
	if !strings.Contains(out, "delay=10m0s") {
		t.Fatalf("expected delay=10m0s:\n%s", out)
	}
}

// TestDaemonRun_PersistentAuthErrorTerminates — Fix M: inject 5 consecutive
// ErrAuth returns from the provider; Run returns the wrapped ErrAuth.
// We drive this with a real (short) PollInterval so the loop actually
// exercises updateBackoff through the Run path.
func TestDaemonRun_PersistentAuthErrorTerminates(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 10 * time.Millisecond

	prov := fake.New()
	prov.GetErr = fmt.Errorf("auth: %w", ddnserr.ErrAuth)
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	select {
	case err := <-done:
		// First tick's error is propagated directly by Run's pre-loop
		// check (non-transient, non-quorum), so the loop may never
		// enter. Either way, Run must return an ErrAuth-wrapped error.
		if err == nil {
			t.Fatalf("Run returned nil; want ErrAuth")
		}
		if !errors.Is(err, ddnserr.ErrAuth) {
			t.Fatalf("Run err = %v, want wrap of ErrAuth", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatalf("Run did not return within 3s despite persistent ErrAuth")
	}
	// The resolver was called at least once (first tick).
	if res.calls.Load() < 1 {
		t.Fatalf("resolver calls = %d, want >= 1", res.calls.Load())
	}
}

// TestDaemonRun_BackoffAppliedEmitted — after driving a transient error
// Run schedules backoff; with a small PollInterval and a long backoff
// delay, the next iteration's wait is extended and backoff_applied is
// logged. We verify the event appears at least once within 1s.
func TestDaemonRun_BackoffAppliedEmitted(t *testing.T) {
	cfg := baseConfig()
	// Keep PollInterval tiny so the loop cycles fast, but backoff
	// computes delay = PollInterval * 2 (>> 200ms) after 2 failures, so
	// the next iteration waits longer and logs backoff_applied.
	cfg.PollInterval = 50 * time.Millisecond

	var buf bytes.Buffer
	var bufMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(testWriter{buf: &buf, mu: &bufMu}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Provider that fails transiently forever.
	prov := &transientProvider{err: transientErr()}
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bufMu.Lock()
		hit := strings.Contains(buf.String(), "backoff_applied")
		bufMu.Unlock()
		if hit {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()
	if !strings.Contains(out, "backoff_scheduled") {
		t.Fatalf("missing backoff_scheduled:\n%s", out)
	}
	if !strings.Contains(out, "backoff_applied") {
		t.Fatalf("missing backoff_applied:\n%s", out)
	}
}

// TestBackoff_FatalCeilingReachedReturnsErr — updateBackoff returns the
// wrapped error only on the N-th consecutive fatal (N = fatal ceiling).
// Earlier calls return nil to signal "keep running; not yet terminal".
func TestBackoff_FatalCeilingReachedReturnsErr(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, _ := newDaemonWithClock(t, cfg, res, prov, clk)

	authErr := fmt.Errorf("auth: %w", ddnserr.ErrAuth)
	// First 4 calls: returns nil (under ceiling).
	for i := 0; i < 4; i++ {
		if err := daemon.UpdateBackoff(d, authErr); err != nil {
			t.Fatalf("updateBackoff[%d] returned %v; want nil (under ceiling)", i, err)
		}
	}
	// 5th call: returns the wrapped auth error — this is what Run
	// propagates to the supervisor.
	err := daemon.UpdateBackoff(d, authErr)
	if err == nil || !errors.Is(err, ddnserr.ErrAuth) {
		t.Fatalf("updateBackoff[5] = %v, want wrap of ErrAuth", err)
	}
	if got := daemon.ConsecutiveFatalFailures(d); got != 5 {
		t.Fatalf("ConsecutiveFatalFailures = %d, want 5", got)
	}
}

// TestBackoff_FatalResetOnSuccess — a successful tick resets the fatal
// counter too. Otherwise a flappy auth token could trigger exit after
// intermittent failures.
func TestBackoff_FatalResetOnSuccess(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = time.Minute
	clk := &fixedClock{now: time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)}
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	prov := &transientProvider{err: transientErr()}
	d, _ := newDaemonWithClock(t, cfg, res, prov, clk)

	authErr := fmt.Errorf("auth: %w", ddnserr.ErrAuth)
	for i := 0; i < 3; i++ {
		_ = daemon.UpdateBackoff(d, authErr)
	}
	if got := daemon.ConsecutiveFatalFailures(d); got != 3 {
		t.Fatalf("pre-reset ConsecutiveFatalFailures = %d, want 3", got)
	}
	if err := daemon.UpdateBackoff(d, nil); err != nil {
		t.Fatalf("nil-err updateBackoff: %v", err)
	}
	if got := daemon.ConsecutiveFatalFailures(d); got != 0 {
		t.Fatalf("post-reset ConsecutiveFatalFailures = %d, want 0", got)
	}
}

// testWriter serializes writes to an underlying buffer; slog handlers
// call Write concurrently from multiple goroutines in our driven-by-
// ticker tests.
type testWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
