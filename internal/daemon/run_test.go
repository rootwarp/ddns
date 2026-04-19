package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
)

// countingResolver lets Run tests observe tick cadence by counting Resolve
// calls; each tick invokes Resolve once.
type countingResolver struct {
	ip     netip.Addr
	report resolver.ResolveReport
	err    error
	calls  atomic.Int32
}

func (c *countingResolver) Resolve(_ context.Context) (netip.Addr, resolver.ResolveReport, error) {
	c.calls.Add(1)
	return c.ip, c.report, c.err
}

func TestRun_FirstTickFiresImmediately(t *testing.T) {
	cfg := baseConfig()
	// Long poll interval: if the first tick did NOT fire immediately, the
	// test would time out before the ticker ever ticked.
	cfg.PollInterval = time.Hour

	prov := fake.New()
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	// Wait for the first Resolve call.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := res.calls.Load(); got < 1 {
		cancel()
		<-done
		t.Fatalf("first tick never fired: resolver calls = %d", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after cancel")
	}
}

func TestRun_TickerDrivesSubsequentReconciles(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 50 * time.Millisecond

	prov := fake.New()
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	// Expect >=3 ticks within ~300ms (immediate + two ticker fires).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if res.calls.Load() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := res.calls.Load(); got < 3 {
		cancel()
		<-done
		t.Fatalf("expected >=3 ticks, got %d", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after cancel")
	}
}

func TestRun_FirstTickNoQuorumIsSwallowed(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = time.Hour

	prov := fake.New()
	res := &countingResolver{err: fmt.Errorf("resolver: %w", ddnserr.ErrNoQuorum)}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	// Wait for the first Resolve call (proof the loop ran its first tick).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := res.calls.Load(); got < 1 {
		cancel()
		<-done
		t.Fatalf("first tick never fired")
	}
	// Loop should still be alive — cancel ends it cleanly.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err = %v, want nil (ErrNoQuorum swallowed)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after cancel")
	}
}

func TestRun_FirstTickAuthErrorPropagates(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = time.Hour

	prov := fake.New()
	// Force a Get that returns an ErrAuth — not Transient or NoQuorum.
	prov.GetErr = fmt.Errorf("permission: %w", ddnserr.ErrAuth)
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Run err = nil, want ErrAuth propagated")
		}
		if !errors.Is(err, ddnserr.ErrAuth) {
			t.Fatalf("Run err = %v, want wrapping ErrAuth", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatalf("Run did not return within 2s — auth error was swallowed")
	}
}

func TestRun_ContextCancelReturnsNil(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 50 * time.Millisecond

	prov := fake.New()
	// Seed a record matching quorum so ticks are pure noops — no error noise.
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	res := &countingResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, false) }()

	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after cancel")
	}
}
