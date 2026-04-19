package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
)

// Compile-time check that *fake.Provider satisfies dnsprovider.DNSProvider.
// If this breaks, the daemon can't use it as a stand-in, which defeats the
// whole point of the fake.
var _ dnsprovider.DNSProvider = (*fake.Provider)(nil)

// TestFakeProvider_GetUpsertGet is the contract test the issue doc calls for:
// start empty, Get returns ErrNotFound, Upsert populates, Get returns the
// stored value, and the call counters advance.
func TestFakeProvider_GetUpsertGet(t *testing.T) {
	p := fake.New()
	ctx := context.Background()
	ref := dnsprovider.RecordRef{
		Project:     "p",
		ManagedZone: "z",
		Name:        "home.example.com.",
		Type:        "A",
	}

	// Round 1: Get on empty fake returns ErrNotFound.
	_, err := p.Get(ctx, ref)
	if !errors.Is(err, ddnserr.ErrNotFound) {
		t.Fatalf("initial Get err = %v, want ErrNotFound", err)
	}
	if p.GetCallCount != 1 {
		t.Errorf("GetCallCount = %d, want 1", p.GetCallCount)
	}

	// Round 2: Upsert the record. Changed should be true, OldRrdatas empty.
	want := dnsprovider.Record{Rrdatas: []string{"192.0.2.42"}, TTL: 300}
	res, err := p.Upsert(ctx, ref, want)
	if err != nil {
		t.Fatalf("Upsert err = %v", err)
	}
	if !res.Changed {
		t.Errorf("Upsert result Changed=false, want true (create path)")
	}
	if len(res.OldRrdatas) != 0 {
		t.Errorf("Upsert OldRrdatas = %v, want empty", res.OldRrdatas)
	}
	if len(res.NewRrdatas) != 1 || res.NewRrdatas[0] != "192.0.2.42" {
		t.Errorf("Upsert NewRrdatas = %v", res.NewRrdatas)
	}
	if p.UpsertCallCount != 1 {
		t.Errorf("UpsertCallCount = %d, want 1", p.UpsertCallCount)
	}

	// Round 3: Get now returns the Upserted record, GetCallCount bumps to 2.
	got, err := p.Get(ctx, ref)
	if err != nil {
		t.Fatalf("second Get err = %v", err)
	}
	if len(got.Rrdatas) != 1 || got.Rrdatas[0] != "192.0.2.42" {
		t.Errorf("Get Rrdatas = %v, want [192.0.2.42]", got.Rrdatas)
	}
	if got.TTL != 300 {
		t.Errorf("Get TTL = %d, want 300", got.TTL)
	}
	if p.GetCallCount != 2 {
		t.Errorf("GetCallCount = %d, want 2", p.GetCallCount)
	}
}

// TestFakeProvider_GetErrInjection routes the configurable GetErr through.
func TestFakeProvider_GetErrInjection(t *testing.T) {
	sentinel := errors.New("boom")
	p := fake.New()
	p.GetErr = sentinel

	_, err := p.Get(context.Background(), dnsprovider.RecordRef{Name: "x."})
	if !errors.Is(err, sentinel) {
		t.Errorf("Get err = %v, want %v", err, sentinel)
	}
}

// TestFakeProvider_UpsertErrInjection routes the configurable UpsertErr through.
func TestFakeProvider_UpsertErrInjection(t *testing.T) {
	sentinel := errors.New("upsert-boom")
	p := fake.New()
	p.UpsertErr = sentinel

	_, err := p.Upsert(
		context.Background(),
		dnsprovider.RecordRef{Name: "x."},
		dnsprovider.Record{Rrdatas: []string{"192.0.2.1"}, TTL: 300},
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("Upsert err = %v, want %v", err, sentinel)
	}
	// The fake should NOT persist the record when the injected error fired.
	if p.UpsertCallCount != 1 {
		t.Errorf("UpsertCallCount = %d, want 1 (count tracks attempts, not successes)", p.UpsertCallCount)
	}
}

// TestFakeProvider_UpsertNoOp: upserting an identical record reports Changed=false
// and still counts the call — Phase 2.6 depends on this to assert "daemon
// saw state was equal and declined to write". Matches the provider-layer
// no-op safeguard behavior the real Google Cloud DNS provider will implement.
func TestFakeProvider_UpsertNoOp(t *testing.T) {
	p := fake.New()
	ctx := context.Background()
	ref := dnsprovider.RecordRef{Name: "x.", Type: "A"}
	first := dnsprovider.Record{Rrdatas: []string{"192.0.2.1"}, TTL: 300}

	if _, err := p.Upsert(ctx, ref, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	res, err := p.Upsert(ctx, ref, first)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if res.Changed {
		t.Errorf("second Upsert reported Changed=true; want false")
	}
}

// TestFakeProvider_KeyedByNameAndType confirms two records that differ only
// in Type are stored independently. Although v1 only uses A, the fake must
// not collapse records by Name alone.
func TestFakeProvider_KeyedByNameAndType(t *testing.T) {
	p := fake.New()
	ctx := context.Background()
	refA := dnsprovider.RecordRef{Name: "x.", Type: "A"}
	refAAAA := dnsprovider.RecordRef{Name: "x.", Type: "AAAA"}

	if _, err := p.Upsert(ctx, refA, dnsprovider.Record{Rrdatas: []string{"192.0.2.1"}, TTL: 300}); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}

	// AAAA should still come back as NotFound.
	_, err := p.Get(ctx, refAAAA)
	if !errors.Is(err, ddnserr.ErrNotFound) {
		t.Errorf("Get AAAA err = %v, want ErrNotFound", err)
	}
}
