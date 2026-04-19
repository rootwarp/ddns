// Package dnsprovider defines the provider-agnostic contract (Get, Upsert)
// that internal/daemon depends on. The real Google Cloud DNS implementation
// lives in the gcp subpackage (added in Phase 2 issues 2.4 and 2.5); this
// file is just interface and value types, so internal/daemon can take a
// DNSProvider parameter and tests can inject a fake from
// internal/dnsprovider/fake.
//
// In v1 only Type "A" (IPv4) is supported; any other value is treated as a
// programmer bug at the provider boundary.
package dnsprovider

import "context"

// DNSProvider is the interface every DNS backend implements.
//
// Get returns the current record; on 404-equivalent it must return
// ddnserr.ErrNotFound so the daemon can distinguish "missing, create it"
// from "we could not reach the API".
//
// Upsert is the atomic replace operation. Callers pass the desired new
// Record; the implementation is responsible for deleting any old RRset and
// adding the new one in one atomic change. The returned UpsertResult
// signals whether a write was actually issued (Changed=false when the
// provider noticed the state was already what was asked for).
type DNSProvider interface {
	Get(ctx context.Context, r RecordRef) (Record, error)
	Upsert(ctx context.Context, r RecordRef, new Record) (UpsertResult, error)
}

// RecordRef identifies a single DNS record for both Get and Upsert. Name is
// the trailing-dot FQDN the config layer has already normalized.
type RecordRef struct {
	Project     string
	ManagedZone string
	Name        string // trailing-dot form
	Type        string // "A" in v1
}

// Record is a point-in-time snapshot of the record's value. In v1 Rrdatas
// carries a single IPv4 address.
type Record struct {
	Rrdatas []string
	TTL     int64
}

// UpsertResult captures what the provider actually did. OldRrdatas is the
// value the provider observed before writing (empty on a create path);
// NewRrdatas is what's in place afterwards. Changed distinguishes a real
// write from a no-op safeguard.
type UpsertResult struct {
	Changed    bool
	OldRrdatas []string
	NewRrdatas []string
}
