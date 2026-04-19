package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/resolver"
	"github.com/rootwarp/ddns/internal/state"
)

// newStore returns a state.Store rooted at a temp directory whose cleanup
// is tied to t. Phase 3 tests use it everywhere a Daemon is constructed.
func newStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(t.TempDir())
}

// fakeResolver is an inline test double for resolver.IPResolver. We keep it
// here (not in the resolver package) because daemon tests are the only
// consumer; a shared test double would be premature.
type fakeResolver struct {
	ip     netip.Addr
	report resolver.ResolveReport
	err    error
	calls  int
}

func (f *fakeResolver) Resolve(_ context.Context) (netip.Addr, resolver.ResolveReport, error) {
	f.calls++
	return f.ip, f.report, f.err
}

// discardLogger is a logger that writes to io.Discard so test output stays
// clean while the daemon still exercises its logging code paths.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func baseConfig() *config.Config {
	return &config.Config{
		PollInterval: 5 * time.Minute,
		Records: []config.RecordConfig{{
			Project:     "test-proj",
			ManagedZone: "test-zone",
			Name:        "home.example.com.",
			TTL:         300,
			Type:        "A",
		}},
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse addr %q: %v", s, err)
	}
	return a
}

func TestReconcileOnce_NoOp(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	// Seed provider with the exact desired record.
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 0 {
		t.Fatalf("Upsert calls after reconcile = %d, want 0 (no-op)", got)
	}
	if res.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", res.calls)
	}
}

func TestReconcileOnce_CreatePath(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	res := &fakeResolver{ip: mustAddr(t, "198.51.100.7"), report: resolver.ResolveReport{Quorum: 2}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}

	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	got, err := prov.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get after reconcile: %v", err)
	}
	if len(got.Rrdatas) != 1 || got.Rrdatas[0] != "198.51.100.7" {
		t.Fatalf("Rrdatas = %v, want [198.51.100.7]", got.Rrdatas)
	}
	if got.TTL != 300 {
		t.Fatalf("TTL = %d, want 300", got.TTL)
	}
	if prov.UpsertCallCount != 1 {
		t.Fatalf("UpsertCallCount = %d, want 1", prov.UpsertCallCount)
	}
}

func TestReconcileOnce_UpdatePath(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	// Seed with the old IP.
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"203.0.113.1"},
		TTL:     300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if prov.UpsertCallCount-upsertCallsBefore != 1 {
		t.Fatalf("Upsert calls after reconcile = %d, want 1", prov.UpsertCallCount-upsertCallsBefore)
	}
	got, err := prov.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get after reconcile: %v", err)
	}
	if len(got.Rrdatas) != 1 || got.Rrdatas[0] != "192.0.2.42" {
		t.Fatalf("Rrdatas = %v, want [192.0.2.42]", got.Rrdatas)
	}
}

func TestReconcileOnce_ResolverNoQuorum(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	res := &fakeResolver{err: fmt.Errorf("resolver: %w", ddnserr.ErrNoQuorum)}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	err := d.ReconcileOnce(context.Background())
	if err == nil {
		t.Fatalf("ReconcileOnce: expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Fatalf("ReconcileOnce err = %v, want ErrNoQuorum", err)
	}
	if prov.GetCallCount != 0 {
		t.Fatalf("GetCallCount = %d, want 0 when resolver fails", prov.GetCallCount)
	}
	if prov.UpsertCallCount != 0 {
		t.Fatalf("UpsertCallCount = %d, want 0 when resolver fails", prov.UpsertCallCount)
	}
}

func TestReconcileOnce_ProviderTransient(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	prov.GetErr = fmt.Errorf("get flake: %w", ddnserr.ErrTransient)
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 2}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	err := d.ReconcileOnce(context.Background())
	if err == nil {
		t.Fatalf("ReconcileOnce: expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Fatalf("ReconcileOnce err = %v, want ErrTransient", err)
	}
	if prov.UpsertCallCount != 0 {
		t.Fatalf("UpsertCallCount = %d, want 0 when Get fails transiently", prov.UpsertCallCount)
	}
}

func TestReconcileOnce_ProviderUpsertError(t *testing.T) {
	cfg := baseConfig()
	prov := fake.New()
	prov.UpsertErr = fmt.Errorf("create flake: %w", ddnserr.ErrTransient)
	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	err := d.ReconcileOnce(context.Background())
	if err == nil {
		t.Fatalf("ReconcileOnce: expected error, got nil")
	}
	if !errors.Is(err, ddnserr.ErrTransient) {
		t.Fatalf("ReconcileOnce err = %v, want ErrTransient", err)
	}
}

// --- Phase 3 issue 3.3: state integration tests ------------------------

// seedState writes a State fixture to the store under the given record
// name. Tests use it to simulate "the daemon ran before and observed IP X".
func seedState(t *testing.T, store *state.Store, recordName, ip, result string) {
	t.Helper()
	if err := store.Save(state.State{
		RecordName:     recordName,
		LastObservedIP: ip,
		LastResult:     result,
		LastCheckedAt:  time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// TestReconcileOnce_StateMatchesLive_FastPathNoop — state says IP=X,
// provider says IP=X, resolver says IP=X → no Upsert, state refreshed.
func TestReconcileOnce_StateMatchesLive_FastPathNoop(t *testing.T) {
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
		Rrdatas: []string{"192.0.2.42"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	seedState(t, store, cfg.Records[0].Name, "192.0.2.42", "updated")
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, store, discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 0 {
		t.Fatalf("Upsert calls = %d, want 0 (fast-path noop)", got)
	}

	st, err := store.Load(cfg.Records[0].Name)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if st.LastResult != "noop" {
		t.Fatalf("LastResult = %q, want noop", st.LastResult)
	}
	if st.LastObservedIP != "192.0.2.42" {
		t.Fatalf("LastObservedIP = %q, want 192.0.2.42", st.LastObservedIP)
	}
}

// TestReconcileOnce_StateStale_ProviderSays_DifferentIP — Fix H regression:
// state=X, provider=Y, resolver=Y → no Upsert (live and desired already
// agree), but state is refreshed to Y. The buggy noop gate in the issue
// doc (which also required state.LastObservedIP == ip) would force an
// unnecessary Upsert on this input.
func TestReconcileOnce_StateStale_ProviderSays_DifferentIP(t *testing.T) {
	cfg := baseConfig()
	store := newStore(t)
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	// Provider already has Y (someone fixed it via gcloud).
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"198.51.100.7"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	// State still carries stale X.
	seedState(t, store, cfg.Records[0].Name, "192.0.2.42", "updated")
	upsertCallsBefore := prov.UpsertCallCount

	// Resolver also sees Y.
	res := &fakeResolver{ip: mustAddr(t, "198.51.100.7"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, store, discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 0 {
		t.Fatalf("Upsert calls = %d, want 0 (live already matches desired)", got)
	}

	// State must be refreshed to Y — the whole point of observability is
	// that status never lies about the last-observed IP.
	st, err := store.Load(cfg.Records[0].Name)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if st.LastObservedIP != "198.51.100.7" {
		t.Fatalf("LastObservedIP = %q, want 198.51.100.7 (state refreshed)", st.LastObservedIP)
	}
	if st.LastResult != "noop" {
		t.Fatalf("LastResult = %q, want noop", st.LastResult)
	}
}

// TestReconcileOnce_StateStale_ResolverSays_DifferentIP — state=X,
// provider=X, resolver=Y → Upsert fires with Y.
func TestReconcileOnce_StateStale_ResolverSays_DifferentIP(t *testing.T) {
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
		Rrdatas: []string{"192.0.2.42"}, TTL: 300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	seedState(t, store, cfg.Records[0].Name, "192.0.2.42", "updated")
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "198.51.100.7"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, store, discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 1 {
		t.Fatalf("Upsert calls = %d, want 1", got)
	}

	st, err := store.Load(cfg.Records[0].Name)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if st.LastObservedIP != "198.51.100.7" {
		t.Fatalf("LastObservedIP = %q, want 198.51.100.7", st.LastObservedIP)
	}
	if st.LastResult != "updated" {
		t.Fatalf("LastResult = %q, want updated", st.LastResult)
	}
}

// TestReconcileOnce_StateWritten_OnError — resolver ErrNoQuorum causes a
// state write with last_result=error (so ddns status shows the failure).
func TestReconcileOnce_StateWritten_OnError(t *testing.T) {
	cfg := baseConfig()
	store := newStore(t)
	prov := fake.New()
	res := &fakeResolver{err: fmt.Errorf("resolver: %w", ddnserr.ErrNoQuorum)}
	d := daemon.New(cfg, res, prov, store, discardLogger())

	err := d.ReconcileOnce(context.Background())
	if err == nil || !errors.Is(err, ddnserr.ErrNoQuorum) {
		t.Fatalf("ReconcileOnce err = %v, want ErrNoQuorum", err)
	}

	st, err := store.Load(cfg.Records[0].Name)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if st.LastResult != "error" {
		t.Fatalf("LastResult = %q, want error", st.LastResult)
	}
	if st.LastError == "" {
		t.Fatalf("LastError is empty; want the resolver error message")
	}
}

func TestReconcileOnce_TTLChangeTriggersUpdate(t *testing.T) {
	cfg := baseConfig()
	cfg.Records[0].TTL = 600
	prov := fake.New()
	ref := dnsprovider.RecordRef{
		Project:     cfg.Records[0].Project,
		ManagedZone: cfg.Records[0].ManagedZone,
		Name:        cfg.Records[0].Name,
		Type:        cfg.Records[0].Type,
	}
	// Seed the rrdatas equal to what's desired but with a different TTL.
	if _, err := prov.Upsert(context.Background(), ref, dnsprovider.Record{
		Rrdatas: []string{"192.0.2.42"},
		TTL:     300,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	upsertCallsBefore := prov.UpsertCallCount

	res := &fakeResolver{ip: mustAddr(t, "192.0.2.42"), report: resolver.ResolveReport{Quorum: 3}}
	d := daemon.New(cfg, res, prov, newStore(t), discardLogger())

	if err := d.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := prov.UpsertCallCount - upsertCallsBefore; got != 1 {
		t.Fatalf("Upsert calls after reconcile = %d, want 1 (TTL changed)", got)
	}
}
