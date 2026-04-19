// Package daemon orchestrates one reconciliation pass and drives the polling
// loop under a context.Context.
//
// A tick is:
//  1. Resolve the public IP with quorum. On ErrNoQuorum, log the full
//     per-source report, persist per-record error state for every
//     configured record (so ddns status reflects the failure uniformly),
//     and return the error to the loop (which swallows it until the next
//     tick).
//  2. For every configured record, call reconcileRecord: load prior state
//     (for observability), fetch the live record via the provider (404 is
//     the create path), compare live against desired (quorum IP + TTL).
//     If live already matches desired, log reconcile_noop; otherwise issue
//     Upsert.
//  3. After every per-record outcome (noop | updated | error), persist an
//     updated State so ddns status shows the latest observation and any
//     recent error. State persist failures are LOGGED and NOT propagated —
//     a write failure to the state file should not fail the reconcile.
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
	"github.com/rootwarp/ddns/internal/state"
)

// Daemon holds the dependencies a reconciliation loop needs. Construction
// is done via New; every field is unexported because the daemon is driven
// entirely through ReconcileOnce and Run.
//
// The clock field is injected so tests can advance time without relying on
// wall-clock ticking; default is time.Now.
type Daemon struct {
	cfg      *config.Config
	resolver resolver.IPResolver
	provider dnsprovider.DNSProvider
	store    *state.Store
	log      *slog.Logger
	clock    func() time.Time
}

// New constructs a Daemon with sensible defaults. Phase 3 added the state
// store parameter (Fix I); all cmd/ddns callers were updated in the same
// commit to keep the build green.
func New(
	cfg *config.Config,
	res resolver.IPResolver,
	prov dnsprovider.DNSProvider,
	store *state.Store,
	log *slog.Logger,
) *Daemon {
	return &Daemon{
		cfg:      cfg,
		resolver: res,
		provider: prov,
		store:    store,
		log:      log,
		clock:    time.Now,
	}
}

// ReconcileOnce resolves the public IP once and reconciles Records[0]
// against it. Multi-record fan-out lands in Phase 3 issue 3.6; the
// reconcileRecord helper is already factored out so 3.6 is a one-line
// change to the top-level loop.
//
// Error semantics: any returned error is appropriate for the loop to
// classify — ErrNoQuorum and ErrTransient are "keep going", anything
// else (auth / bug) is terminal.
func (d *Daemon) ReconcileOnce(ctx context.Context) error {
	ip, report, err := d.resolver.Resolve(ctx)
	now := d.clock()
	if err != nil {
		d.log.Info("resolver_no_quorum", "report", report, "err", err.Error())
		rec := d.cfg.Records[0]
		d.persistError(ctx, now, "", err, rec.Name)
		return err
	}
	d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

	rec := d.cfg.Records[0]
	recLog := d.log.With("record", rec.Name)
	return d.reconcileRecord(ctx, ip.String(), rec, recLog)
}

// reconcileRecord runs the per-record reconciliation body. It is extracted
// so the top-level ReconcileOnce can fan out across cfg.Records without
// nesting. Per-record errors are returned so the caller can pick the first
// and still let subsequent records run.
//
// The ip argument is the resolver's quorum IP as a string (already
// canonical). The recLog argument carries the `record` attr so all per-
// record logs are greppable by record name.
func (d *Daemon) reconcileRecord(ctx context.Context, ip string, rec config.RecordConfig, recLog *slog.Logger) error {
	now := d.clock()

	// Load prior state for observability only. Phase 3 does NOT use the
	// state as a fast-path cache that skips the provider Get — the
	// provider is still the source of truth. ErrNotFound (first run for
	// this record) is equivalent to no prior state, not a hard error.
	if _, err := d.store.Load(rec.Name); err != nil && !errors.Is(err, ddnserr.ErrNotFound) {
		// A corrupt state file or unreadable disk: log and press on. We
		// do not fail the reconcile because the authoritative signal is
		// the provider, not our local cache.
		recLog.Warn("state_load_error", "err", err.Error())
	}

	ref := dnsprovider.RecordRef{
		Project:     rec.Project,
		ManagedZone: rec.ManagedZone,
		Name:        rec.Name,
		Type:        rec.Type,
	}
	live, getErr := d.provider.Get(ctx, ref)
	if getErr != nil && !errors.Is(getErr, ddnserr.ErrNotFound) {
		recLog.Error("reconcile_error", "stage", "get", "err", getErr.Error())
		d.persistError(ctx, now, ip, getErr, rec.Name)
		return getErr
	}
	createPath := errors.Is(getErr, ddnserr.ErrNotFound)

	desired := dnsprovider.Record{
		Rrdatas: []string{ip},
		TTL:     rec.TTL,
	}

	// Fix H: the noop gate is based only on LIVE vs desired. The prior
	// state is intentionally not part of the gate — if provider and
	// resolver agree, it's a noop, regardless of whether our local cache
	// is stale. State is refreshed inside persistNoop.
	if !createPath && sameRrdatas(live.Rrdatas, desired.Rrdatas) && live.TTL == desired.TTL {
		recLog.Info("reconcile_noop", "ip", ip)
		d.persistNoop(ctx, now, ip, rec.Name)
		return nil
	}

	res, err := d.provider.Upsert(ctx, ref, desired)
	if err != nil {
		recLog.Error("reconcile_error", "stage", "upsert", "err", err.Error())
		d.persistError(ctx, now, ip, err, rec.Name)
		return err
	}
	recLog.Info("reconcile_updated",
		"old", res.OldRrdatas,
		"new", res.NewRrdatas,
		"ttl", desired.TTL,
	)
	d.persistUpdate(ctx, now, ip, rec.Name)
	return nil
}

// persistNoop writes a "noop" state record. Signature carries recordName up
// front (Fix K) because the helper needs it to compute the state-file path.
func (d *Daemon) persistNoop(_ context.Context, now time.Time, ip, recordName string) {
	d.saveState(state.State{
		LastCheckedAt:  now,
		LastObservedIP: ip,
		LastResult:     "noop",
		RecordName:     recordName,
	})
}

// persistUpdate writes an "updated" state record.
func (d *Daemon) persistUpdate(_ context.Context, now time.Time, ip, recordName string) {
	d.saveState(state.State{
		LastCheckedAt:  now,
		LastObservedIP: ip,
		LastUpdatedAt:  now,
		LastResult:     "updated",
		RecordName:     recordName,
	})
}

// persistError writes an "error" state record. ip may be empty when the
// failure occurred before a quorum was reached (e.g., resolver no-quorum).
func (d *Daemon) persistError(_ context.Context, now time.Time, ip string, err error, recordName string) {
	d.saveState(state.State{
		LastCheckedAt:  now,
		LastObservedIP: ip,
		LastResult:     "error",
		LastError:      err.Error(),
		RecordName:     recordName,
	})
}

// saveState calls store.Save and logs any failure WITHOUT propagating. A
// write failure to the state file must never fail the reconcile: the
// provider is still the source of truth, and next tick we'll re-observe
// and try again.
func (d *Daemon) saveState(st state.State) {
	if err := d.store.Save(st); err != nil {
		d.log.Warn("state_save_error", "record", st.RecordName, "err", err.Error())
	}
}

// sameRrdatas compares two rrdata slices as unordered sets. The daemon
// layer keeps its own copy of this helper rather than sharing with
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
