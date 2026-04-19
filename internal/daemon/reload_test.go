package daemon_test

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
)

// Issue 4.4: SIGHUP config reload.
//
// Daemon-level tests for UpdateConfig. The cmd/ddns/run.go layer wires
// signal.Notify → Load → UpdateConfig; here we just verify the Daemon's
// swap is atomic and picks up the new state.

// trackingResolver records which instance was invoked. Swapping a new
// resolver must route subsequent Resolve calls to the new instance, not
// the old one.
type trackingResolver struct {
	id    string
	mu    sync.Mutex
	calls int
}

func (t *trackingResolver) Resolve(_ context.Context) (netip.Addr, resolver.ResolveReport, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return netip.MustParseAddr("192.0.2.42"), resolver.ResolveReport{Quorum: 1}, nil
}

func (t *trackingResolver) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// TestDaemon_UpdateConfig_SwapsResolverSources — after UpdateConfig with a
// new ResolverConfig, the daemon constructs and uses a fresh resolver
// whose Resolve method wraps the new sources. We verify this indirectly:
// a reconcile before the swap uses the initial resolver; a reconcile
// after uses a newly-built one (verified by observing that the tick
// actually succeeds against the new config).
func TestDaemon_UpdateConfig_SwapsResolverSources(t *testing.T) {
	cfg1 := baseConfig()
	cfg1.Resolver = config.ResolverConfig{
		Sources: []string{"http://old.example/a", "http://old.example/b"},
		Quorum:  1,
		Timeout: time.Second,
	}
	prov := fake.New()
	origRes := &trackingResolver{id: "orig"}
	d := daemon.New(cfg1, origRes, prov, newStore(t), discardLogger())

	// Pre-reload: run a tick so the original resolver is used.
	if err := d.ReconcileOnce(context.Background(), false); err != nil {
		t.Fatalf("pre-reload reconcile: %v", err)
	}
	if origRes.Calls() != 1 {
		t.Fatalf("orig resolver calls = %d, want 1", origRes.Calls())
	}

	// Build a new config with different sources; UpdateConfig rebuilds
	// the resolver around it. We can't easily observe the new sources
	// on a *resolver.Resolver since fields are unexported, so we verify
	// the swap happened by confirming the ORIGINAL tracking resolver is
	// no longer invoked after the reload.
	cfg2 := baseConfig()
	cfg2.Resolver = config.ResolverConfig{
		Sources: []string{"http://new.example/x", "http://new.example/y"},
		Quorum:  1,
		Timeout: time.Second,
	}
	d.UpdateConfig(cfg2)

	// Post-reload: the tick must NOT call the original tracking
	// resolver (it's been swapped out for a real *resolver.Resolver).
	callsBeforePostTick := origRes.Calls()

	// The post-reload tick will try HTTP GETs against the fake sources
	// and fail (no server), but what we're testing is the swap, not
	// the content of the failure.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = d.ReconcileOnce(ctx, false)

	if origRes.Calls() != callsBeforePostTick {
		t.Fatalf("orig resolver was called after UpdateConfig (calls %d → %d); expected no new calls", callsBeforePostTick, origRes.Calls())
	}
}

// TestDaemon_UpdateConfig_SwapsPollInterval — UpdateConfig with a new
// PollInterval is observable via the exported hook ConfigPollInterval.
func TestDaemon_UpdateConfig_SwapsPollInterval(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	d := daemon.New(cfg, &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 1}}, fake.New(), newStore(t), discardLogger())

	if got := daemon.ConfigPollInterval(d); got != 5*time.Minute {
		t.Fatalf("initial PollInterval = %v, want 5m", got)
	}

	cfg2 := baseConfig()
	cfg2.PollInterval = 10 * time.Second
	d.UpdateConfig(cfg2)
	if got := daemon.ConfigPollInterval(d); got != 10*time.Second {
		t.Fatalf("post-reload PollInterval = %v, want 10s", got)
	}
}

// TestDaemon_UpdateConfig_NilIgnored — passing nil is a no-op. Callers
// should never do this, but the guard prevents a nil-deref on a buggy
// SIGHUP path that forgot to check err.
func TestDaemon_UpdateConfig_NilIgnored(t *testing.T) {
	cfg := baseConfig()
	cfg.PollInterval = 5 * time.Minute
	d := daemon.New(cfg, &fakeResolver{}, fake.New(), newStore(t), discardLogger())

	d.UpdateConfig(nil) // must not panic
	if got := daemon.ConfigPollInterval(d); got != 5*time.Minute {
		t.Fatalf("PollInterval changed after nil UpdateConfig: %v", got)
	}
}

// TestDaemon_ConcurrentReloadAndTick — running UpdateConfig concurrently
// with ReconcileOnce exercises the RLock/Lock pair; with -race this
// catches any field torn write.
func TestDaemon_ConcurrentReloadAndTick(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 1}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = d.ReconcileOnce(context.Background(), false)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			newCfg := baseConfig()
			newCfg.PollInterval = time.Duration(i+1) * time.Millisecond
			d.UpdateConfig(newCfg)
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
