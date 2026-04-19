//go:build integration

package gcp_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	dnsapi "google.golang.org/api/dns/v1"
	"google.golang.org/api/option"

	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/gcp"
)

// TestIntegration_FullReconcile runs one full reconcile cycle against a live
// Google Cloud DNS managed zone, then cleans up behind itself via a
// pure-deletion Changes.Create.
//
// Prerequisites (all three must be set or the test self-skips):
//
//   - DDNS_INTEGRATION_PROJECT: GCP project ID that owns the managed zone.
//   - DDNS_INTEGRATION_ZONE:    managed-zone name (not the DNS domain).
//   - DDNS_INTEGRATION_RECORD:  FQDN inside the zone, trailing-dot form
//     (e.g., "ddns-integration.example.com.").
//
// Credentials come from Application Default Credentials (the daemon's normal
// path). See the README section "Integration tests" for the manual-run
// rationale and safety guarantees.
//
// The test uses addresses from the TEST-NET-3 reserved range (203.0.113.0/24,
// RFC 5737) so no value written to the record ever directs traffic at a real
// host: if the cleanup step races, the worst case is a stale record pointing
// at documentation-only IP space.
func TestIntegration_FullReconcile(t *testing.T) {
	project := os.Getenv("DDNS_INTEGRATION_PROJECT")
	zone := os.Getenv("DDNS_INTEGRATION_ZONE")
	record := os.Getenv("DDNS_INTEGRATION_RECORD")
	if project == "" || zone == "" || record == "" {
		t.Skip("DDNS_INTEGRATION_PROJECT/ZONE/RECORD unset; skipping integration test")
	}
	if !strings.HasSuffix(record, ".") {
		t.Fatalf("DDNS_INTEGRATION_RECORD %q must be FQDN with trailing dot", record)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	provider, err := gcp.New(ctx)
	if err != nil {
		t.Fatalf("gcp.New: %v", err)
	}

	ref := dnsprovider.RecordRef{
		Project:     project,
		ManagedZone: zone,
		Name:        record,
		Type:        "A",
	}

	// Always attempt cleanup, even if an assertion fails. The deletion path
	// is tolerant of "record not found" so it is safe to run unconditionally.
	t.Cleanup(func() {
		cleanupRecord(t, project, zone, record)
	})

	// Step 1: Upsert with a known TEST-NET-3 IP. Accepts both create and
	// update paths; we don't care which — we only care that the resulting
	// state matches what we wrote.
	newIP := "203.0.113.42"
	res, err := provider.Upsert(ctx, ref, dnsprovider.Record{
		Rrdatas: []string{newIP},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert(first write): %v", err)
	}
	if !res.Changed {
		t.Fatalf("Upsert(first write): Changed=false, expected an actual write")
	}

	// Step 2: Get should return the value we just wrote.
	got, err := provider.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if len(got.Rrdatas) != 1 || got.Rrdatas[0] != newIP {
		t.Fatalf("Get Rrdatas = %v, want [%s]", got.Rrdatas, newIP)
	}
	if got.TTL != 300 {
		t.Fatalf("Get TTL = %d, want 300", got.TTL)
	}

	// Step 3: Idempotent upsert — same IP, same TTL. Provider's no-op
	// safeguard should fire; no Changes.Create issued, Changed=false.
	res2, err := provider.Upsert(ctx, ref, dnsprovider.Record{
		Rrdatas: []string{newIP},
		TTL:     300,
	})
	if err != nil {
		t.Fatalf("Upsert(idempotent): %v", err)
	}
	if res2.Changed {
		t.Fatalf("Upsert(idempotent): Changed=true, expected no-op")
	}
}

// cleanupRecord removes the integration-test record via a pure-deletion
// Changes.Create. It tolerates "not found" so re-running the test leaves no
// residual state regardless of whether the previous run completed.
func cleanupRecord(t *testing.T, project, zone, record string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	provider, err := gcp.New(ctx)
	if err != nil {
		t.Logf("cleanup: gcp.New: %v", err)
		return
	}

	ref := dnsprovider.RecordRef{
		Project:     project,
		ManagedZone: zone,
		Name:        record,
		Type:        "A",
	}
	existing, err := provider.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, ddnserr.ErrNotFound) {
			return // already clean
		}
		t.Logf("cleanup: Get: %v", err)
		return
	}

	// Issue a raw Changes.Create with deletions only — the provider package
	// does not expose a "delete" operation (Upsert is the only write path),
	// and we deliberately avoid a second Upsert so audit-log readers see
	// exactly two change calls per test run (one write, one cleanup).
	svc, err := dnsapi.NewService(ctx, option.WithScopes(dnsapi.NdevClouddnsReadwriteScope))
	if err != nil {
		t.Logf("cleanup: dns.NewService: %v", err)
		return
	}
	change := &dnsapi.Change{
		Deletions: []*dnsapi.ResourceRecordSet{{
			Name:    ref.Name,
			Type:    ref.Type,
			Ttl:     existing.TTL,
			Rrdatas: existing.Rrdatas,
		}},
	}
	if _, err := svc.Changes.Create(project, zone, change).Context(ctx).Do(); err != nil {
		t.Logf("cleanup: Changes.Create(delete): %v", err)
	}
}
