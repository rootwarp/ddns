// Package fake is a test double for dnsprovider.DNSProvider. It stores
// records in an in-memory map keyed by "Name|Type" so callers (primarily
// the Phase 2.6 daemon tests) can arrange realistic scenarios without
// touching the real Google Cloud DNS API.
//
// This package is never imported by main — it lives alongside the real
// provider specifically so unit tests can pull it in without dragging the
// GCP SDK into the binary surface.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
)

// Provider is an in-memory DNSProvider suitable for unit tests.
//
// GetErr and UpsertErr let tests force an error path without reaching into
// private state. The CallCount fields are exposed so tests can assert "no
// Upsert was issued" or "exactly one Get happened" — Phase 2.6 relies on
// these to verify reconcile short-circuits.
type Provider struct {
	mu      sync.Mutex
	records map[string]dnsprovider.Record

	// GetErr, when non-nil, is returned by Get before the map lookup.
	GetErr error
	// UpsertErr, when non-nil, is returned by Upsert before any mutation.
	UpsertErr error

	// GetCallCount tracks every call to Get, including error-injected ones.
	GetCallCount int
	// UpsertCallCount tracks every call to Upsert, including error-injected ones.
	UpsertCallCount int
}

// New returns a fresh Provider with an initialized record store.
func New() *Provider {
	return &Provider{records: make(map[string]dnsprovider.Record)}
}

// Get returns the stored record or ddnserr.ErrNotFound when absent. If
// GetErr has been set, it wins over both paths.
func (p *Provider) Get(_ context.Context, r dnsprovider.RecordRef) (dnsprovider.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.GetCallCount++
	if p.GetErr != nil {
		return dnsprovider.Record{}, p.GetErr
	}

	rec, ok := p.records[key(r)]
	if !ok {
		return dnsprovider.Record{}, ddnserr.ErrNotFound
	}
	return copyRecord(rec), nil
}

// Upsert replaces the stored record with new. When UpsertErr is set, Upsert
// returns it without mutating state. When the stored record is already
// byte-for-byte equal to new, it reports Changed=false, matching the
// provider-layer no-op safeguard that the real Google Cloud DNS provider
// will implement in Phase 2.5.
func (p *Provider) Upsert(_ context.Context, r dnsprovider.RecordRef, new dnsprovider.Record) (dnsprovider.UpsertResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.UpsertCallCount++
	if p.UpsertErr != nil {
		return dnsprovider.UpsertResult{}, p.UpsertErr
	}

	k := key(r)
	old, hadOld := p.records[k]

	var oldRrdatas []string
	if hadOld {
		oldRrdatas = append([]string(nil), old.Rrdatas...)
	}

	if hadOld && recordEqual(old, new) {
		return dnsprovider.UpsertResult{
			Changed:    false,
			OldRrdatas: oldRrdatas,
			NewRrdatas: append([]string(nil), old.Rrdatas...),
		}, nil
	}

	p.records[k] = copyRecord(new)
	return dnsprovider.UpsertResult{
		Changed:    true,
		OldRrdatas: oldRrdatas,
		NewRrdatas: append([]string(nil), new.Rrdatas...),
	}, nil
}

// key builds the in-memory map key. Name is already trailing-dot normalized
// by the config layer, so two refs that differ only by dot collapse here.
func key(r dnsprovider.RecordRef) string {
	return fmt.Sprintf("%s|%s", r.Name, r.Type)
}

// copyRecord defensively copies so callers cannot mutate stored state.
func copyRecord(r dnsprovider.Record) dnsprovider.Record {
	return dnsprovider.Record{
		Rrdatas: append([]string(nil), r.Rrdatas...),
		TTL:     r.TTL,
	}
}

// recordEqual compares TTL and ordered Rrdatas. The fake does not sort
// slices — callers are expected to pass values in canonical order, which
// matches how the daemon builds its desired record.
func recordEqual(a, b dnsprovider.Record) bool {
	if a.TTL != b.TTL {
		return false
	}
	if len(a.Rrdatas) != len(b.Rrdatas) {
		return false
	}
	for i := range a.Rrdatas {
		if a.Rrdatas[i] != b.Rrdatas[i] {
			return false
		}
	}
	return true
}
