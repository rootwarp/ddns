// Package daemon orchestrates one reconciliation pass and drives the polling
// loop under a context.Context.
//
// Phase 2 scope is deliberately narrow: a single record, no local state
// cache, no backoff, no dry-run. The multi-record fan-out, state integration,
// and backoff policy arrive in Phases 3 and 4.
//
// A tick is:
//  1. Resolve the public IP with quorum. On ErrNoQuorum, log the full
//     per-source report and return the error to the loop (which swallows it
//     until the next tick).
//  2. Fetch the live record via the provider. A 404 (ErrNotFound) is the
//     create path — the provider's Upsert will add the record; any other
//     error bubbles out.
//  3. Compare the live record against the desired record (quorum IP + the
//     configured TTL). If equal (order-independent for rrdatas, because Cloud
//     DNS does not promise order on Get), log reconcile_noop and return.
//     Otherwise, issue Upsert and log reconcile_updated on success or
//     reconcile_error on failure.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/resolver"
)

// Daemon holds the dependencies a reconciliation loop needs. Construction is
// done via New; every field is unexported because the daemon is driven
// entirely through ReconcileOnce and Run.
//
// The clock field is injected so Phase 3 / 4 tests can advance time without
// relying on wall-clock ticking; default is time.Now.
type Daemon struct {
	cfg      *config.Config
	resolver resolver.IPResolver
	provider dnsprovider.DNSProvider
	log      *slog.Logger
	clock    func() time.Time
}

// New constructs a Daemon with sensible defaults. The Phase 3 state store
// will extend this signature; for Phase 2 we stay at four dependencies.
func New(cfg *config.Config, res resolver.IPResolver, prov dnsprovider.DNSProvider, log *slog.Logger) *Daemon {
	return &Daemon{
		cfg:      cfg,
		resolver: res,
		provider: prov,
		log:      log,
		clock:    time.Now,
	}
}

// ReconcileOnce runs exactly one reconcile pass for Records[0]. Multi-record
// support lands in Phase 3 issue 3.6.
//
// Error semantics: any returned error is appropriate for the loop to classify
// — ErrNoQuorum and ErrTransient are "keep going", anything else (auth /
// bug) is terminal.
func (d *Daemon) ReconcileOnce(ctx context.Context) error {
	ip, report, err := d.resolver.Resolve(ctx)
	if err != nil {
		d.log.Info("resolver_no_quorum", "report", report, "err", err.Error())
		return err
	}
	d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

	rec := d.cfg.Records[0]
	ref := dnsprovider.RecordRef{
		Project:     rec.Project,
		ManagedZone: rec.ManagedZone,
		Name:        rec.Name,
		Type:        rec.Type,
	}

	live, getErr := d.provider.Get(ctx, ref)
	if getErr != nil && !errors.Is(getErr, ddnserr.ErrNotFound) {
		d.log.Error("reconcile_error", "stage", "get", "err", getErr.Error())
		return getErr
	}
	createPath := errors.Is(getErr, ddnserr.ErrNotFound)

	desired := dnsprovider.Record{
		Rrdatas: []string{ip.String()},
		TTL:     rec.TTL,
	}

	if !createPath && sameRrdatas(live.Rrdatas, desired.Rrdatas) && live.TTL == desired.TTL {
		d.log.Info("reconcile_noop", "ip", ip.String())
		return nil
	}

	res, err := d.provider.Upsert(ctx, ref, desired)
	if err != nil {
		d.log.Error("reconcile_error", "stage", "upsert", "err", err.Error())
		return err
	}
	d.log.Info("reconcile_updated",
		"old", res.OldRrdatas,
		"new", res.NewRrdatas,
		"ttl", desired.TTL,
	)
	return nil
}

// sameRrdatas compares two rrdata slices as unordered sets. The daemon layer
// keeps its own copy of this helper rather than sharing with
// internal/dnsprovider/gcp — they are different boundaries and should not
// develop a shared lower-level dep.
func sameRrdatas(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
