package daemon_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
)

// Issue 4.1: --dry-run honored.
//
// TestReconcileOnce_DryRun_NoProviderUpsert — dry-run with resolver IP != live
// IP; assert that the provider's Upsert was NOT called even though drift was
// detected. This is the load-bearing dry-run invariant.
func TestReconcileOnce_DryRun_NoProviderUpsert(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	// Seed the provider with an OLD IP — the dry-run resolver will see a
	// NEW IP, so drift is detected; we must verify we still do not write.
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"203.0.113.1"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background(), true); err != nil {
		t.Fatalf("ReconcileOnce(dryRun): %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 0 {
		t.Fatalf("Upsert calls after dry-run reconcile = %d, want 0", got)
	}
}

// TestReconcileOnce_DryRun_StillWritesStateAsNoop — dry-run detects drift
// but writes state with last_result=noop (NOT "updated"), because the
// record did not in fact change on the provider side. A subsequent non-
// dry-run tick will then correctly decide an Upsert is needed.
func TestReconcileOnce_DryRun_StillWritesStateAsNoop(t *testing.T) {
	cfg := baseConfig()
	store := newStore(t)
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"203.0.113.1"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, store, discardLogger())

	if err := d.ReconcileOnce(context.Background(), true); err != nil {
		t.Fatalf("ReconcileOnce(dryRun): %v", err)
	}

	st, err := store.Load(cfg.Records[0].Name)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if st.LastResult != "noop" {
		t.Fatalf("LastResult = %q, want noop (dry-run did not actually update the record)", st.LastResult)
	}
	// LastObservedIP tracks what the resolver saw; must be set even though
	// the provider was not written.
	if st.LastObservedIP != "192.0.2.42" {
		t.Fatalf("LastObservedIP = %q, want 192.0.2.42", st.LastObservedIP)
	}
}

// TestReconcileOnce_DryRun_LogsWouldSendPayload — the reconcile_dry_run
// event must appear with would_send_* attributes so operators can verify
// the payload before enabling writes.
func TestReconcileOnce_DryRun_LogsWouldSendPayload(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"203.0.113.1"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), logger)

	if err := d.ReconcileOnce(context.Background(), true); err != nil {
		t.Fatalf("ReconcileOnce(dryRun): %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "reconcile_dry_run") {
		t.Fatalf("log output missing reconcile_dry_run:\n%s", out)
	}
	for _, want := range []string{
		"would_send_name",
		"would_send_type",
		"would_send_ttl",
		"would_send_new=[192.0.2.42]",
		"would_send_old=[203.0.113.1]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q:\n%s", want, out)
		}
	}
}

// TestReconcileOnce_DryRun_CreatePath — first run, record does not exist;
// dry-run must still skip Upsert and log with would_send_old=[] (or nil).
func TestReconcileOnce_DryRun_CreatePath(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	res := &fakeResolver{ip: mustAddr(t, "198.51.100.7"), report: resolver.ResolveReport{Quorum: 2}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background(), true); err != nil {
		t.Fatalf("ReconcileOnce(dryRun, createPath): %v", err)
	}
	if prov.UpsertCallCount != 0 {
		t.Fatalf("UpsertCallCount = %d, want 0 (dry-run on create path)", prov.UpsertCallCount)
	}
}
