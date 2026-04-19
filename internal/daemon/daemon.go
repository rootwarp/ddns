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
	"sync"
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
//
// Concurrency model: cfg and resolver are hot-reloadable via UpdateConfig
// on SIGHUP (issue 4.4). Every read that could race with UpdateConfig goes
// through an RLock on mu. Hold time is kept to a single field read so the
// hot path is not serialized on the mutex. backoff is only touched from
// the Run goroutine (single-writer), but UpdateConfig can run concurrently
// with tick processing, so we place it behind mu as well to keep things
// simple.
type Daemon struct {
	mu sync.RWMutex

	cfg      *config.Config
	resolver resolver.IPResolver
	provider dnsprovider.DNSProvider
	store    *state.Store
	log      *slog.Logger
	clock    func() time.Time

	backoff backoffState
}

// backoffState tracks exponential backoff across ticks.
//
// consecutiveFailures counts consecutive transient (ErrTransient) or other
// unexpected-but-not-fatal errors from ReconcileOnce. ErrNoQuorum does NOT
// count — a resolver failure is not a provider retriable, so it does not
// schedule backoff. A nil-err reconcile resets this counter.
//
// consecutiveFatalFailures (Fix M) counts consecutive ErrAuth / ErrConfig
// errors observed inside Run's tick loop. When it reaches
// consecutiveFatalCeiling, Run returns the error so a supervisor (systemd,
// compose, k8s) can notice and restart / page. Any nil-err reconcile
// resets this counter too.
//
// nextTickNotBefore holds the wall-clock time before which the next tick
// should be skipped. Zero means "no backoff active". It's derived from
// consecutiveFailures and cfg.PollInterval inside updateBackoff.
type backoffState struct {
	consecutiveFailures      int
	consecutiveFatalFailures int
	nextTickNotBefore        time.Time
}

// Backoff tuning. Kept unexported; not configurable in v1 because the
// research doc (project plan, Open Questions) decided jitter + knobs are a
// Phase 7 concern.
const (
	backoffCeiling          = 30 * time.Minute
	consecutiveFatalCeiling = 5 // Fix M: exit after N consecutive ErrAuth/ErrConfig
)

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

// ReconcileOnce resolves the public IP once and reconciles every
// configured record against it. A failure on one record logs and persists
// that record's error state but does NOT abort the fan-out — "all records
// get their chance" per the PRD. The returned error is the first error
// encountered (or nil), so ddns sync still exits non-zero if any record
// failed.
//
// The resolver call is factored out of the per-record loop: all records
// share the same observed IP per tick, which is what we want (a single
// public IP maps to many hostnames).
//
// When dryRun is true, the daemon never calls provider.Upsert; instead it
// emits a reconcile_dry_run log line carrying the would-be payload, and
// persists state with last_result=noop (the record was not actually
// updated). See issue 4.1 for the full contract.
//
// The resolver call is always real: dry-run affects writes, not reads.
//
// Error semantics: any returned error is appropriate for the loop to
// classify — ErrNoQuorum and ErrTransient are "keep going", anything
// else (auth / bug) is terminal.
func (d *Daemon) ReconcileOnce(ctx context.Context, dryRun bool) error {
	d.log.Info("tick_start")

	// Snapshot cfg and resolver under the read lock so a concurrent
	// UpdateConfig (SIGHUP, issue 4.4) cannot tear a tick in progress.
	// The snapshot is taken once and passed down — no other field reads
	// under the mutex happen inside this tick.
	d.mu.RLock()
	resolverSnap := d.resolver
	records := append([]config.RecordConfig(nil), d.cfg.Records...)
	d.mu.RUnlock()

	ip, report, err := resolverSnap.Resolve(ctx)
	now := d.clock()
	if err != nil {
		d.log.Info("resolver_no_quorum", "report", report, "err", err.Error())
		// Every record gets an error state so ddns status reflects the
		// failure uniformly, not just for the first record.
		for _, rec := range records {
			d.persistError(ctx, now, "", err, rec.Name)
		}
		return err
	}
	d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

	var firstErr error
	for _, rec := range records {
		recLog := d.log.With("record", rec.Name)
		if rerr := d.reconcileRecord(ctx, ip.String(), rec, recLog, dryRun); rerr != nil && firstErr == nil {
			firstErr = rerr
		}
	}
	return firstErr
}

// reconcileRecord runs the per-record reconciliation body. It is extracted
// so the top-level ReconcileOnce can fan out across cfg.Records without
// nesting. Per-record errors are returned so the caller can pick the first
// and still let subsequent records run.
//
// The ip argument is the resolver's quorum IP as a string (already
// canonical). The recLog argument carries the `record` attr so all per-
// record logs are greppable by record name. When dryRun is true, the
// Upsert call is replaced by a reconcile_dry_run log line and the record
// is NOT written to the provider — state is refreshed with last_result=
// noop because the on-disk record remains unchanged.
func (d *Daemon) reconcileRecord(ctx context.Context, ip string, rec config.RecordConfig, recLog *slog.Logger, dryRun bool) error {
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

	// Dry-run: log the payload that WOULD be sent, skip the provider
	// write, and persist state as a noop. The record remains unchanged
	// on the provider side so last_result=noop reflects reality. Issue
	// 4.1 acceptance criterion: a subsequent non-dry-run tick must still
	// perform the update, which is why we do not record it as "updated".
	if dryRun {
		var oldRrdatas []string
		if !createPath {
			oldRrdatas = live.Rrdatas
		}
		recLog.Info("reconcile_dry_run",
			"would_send_name", ref.Name,
			"would_send_type", ref.Type,
			"would_send_ttl", desired.TTL,
			"would_send_old", oldRrdatas,
			"would_send_new", desired.Rrdatas,
		)
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

// UpdateConfig swaps the live config and rebuilds the resolver under the
// write lock. It is the SIGHUP-driven hot-reload hook (issue 4.4). The
// resolver is rebuilt unconditionally because internal/resolver.Resolver
// is designed to be immutable after New — swapping the pointer is the
// simplest correct approach, and it lets a reloaded resolver block
// (sources/quorum/timeout) take effect on the next tick without dancing
// around in-flight requests.
//
// Callers must invoke this only with a fully-validated *config.Config
// (i.e., the result of config.Load). Passing nil or a partially-populated
// struct would leave the daemon in an inconsistent state; we guard with
// a nil-check but trust the caller beyond that.
//
// The provider is intentionally NOT rebuilt here: provider construction
// requires ADC discovery which can fail (ErrAuth), and we do not want a
// SIGHUP to be able to crash a running daemon. Provider reload is a
// Phase 7 concern.
func (d *Daemon) UpdateConfig(newCfg *config.Config) {
	if newCfg == nil {
		return
	}
	newResolver := resolver.New(newCfg.Resolver)

	d.mu.Lock()
	d.cfg = newCfg
	d.resolver = newResolver
	d.mu.Unlock()
}

// updateBackoff inspects the outcome of a reconcile tick and adjusts the
// backoff window accordingly. It is called from Run after every tick
// (including the very first). The returned error is non-nil only when the
// fatal-ceiling (Fix M) has been reached — Run then propagates it to the
// supervisor.
//
// Rules:
//   - err == nil → reset both counters, clear nextTickNotBefore.
//   - err wraps ErrNoQuorum → do not touch either counter; resolver
//     failures are not provider-transient, and the project plan specifies
//     they neither schedule backoff nor reset it.
//   - err wraps ErrAuth or ErrConfig → increment fatal counter. If it has
//     hit consecutiveFatalCeiling, return the error so Run exits 3. Do
//     not update transient backoff here — these are not retriable.
//   - any other err → increment transient counter, compute
//     min(PollInterval << (N-1), 30m), set nextTickNotBefore to
//     clock()+delay, log backoff_scheduled.
//
// All writes to backoff happen under mu.Lock() so a concurrent
// UpdateConfig or nextInterval read cannot see a torn value.
func (d *Daemon) updateBackoff(err error) error {
	if err == nil {
		d.mu.Lock()
		d.backoff.consecutiveFailures = 0
		d.backoff.consecutiveFatalFailures = 0
		d.backoff.nextTickNotBefore = time.Time{}
		d.mu.Unlock()
		return nil
	}

	// ErrNoQuorum: resolver failed; the provider was never involved. Per
	// project plan, this neither schedules backoff nor resets it.
	if errors.Is(err, ddnserr.ErrNoQuorum) {
		return nil
	}

	// Fix M: ErrAuth and ErrConfig are fatal-class errors. They should
	// have aborted startup; seeing them here means the supervisor needs
	// to notice. Tight-looping forever at the 30m cap would hide a real
	// problem.
	if errors.Is(err, ddnserr.ErrAuth) || errors.Is(err, ddnserr.ErrConfig) {
		d.mu.Lock()
		d.backoff.consecutiveFatalFailures++
		fatal := d.backoff.consecutiveFatalFailures
		d.mu.Unlock()
		if fatal >= consecutiveFatalCeiling {
			d.log.Error("fatal_ceiling_reached",
				"consecutive_fatal_failures", fatal,
				"err", err.Error(),
			)
			return err
		}
		return nil
	}

	// Everything else (ErrTransient, or an unexpected wrapped error):
	// exponential backoff. PollInterval is read under the lock because
	// UpdateConfig could be swapping it concurrently (issue 4.4).
	d.mu.Lock()
	d.backoff.consecutiveFailures++
	n := d.backoff.consecutiveFailures
	poll := d.cfg.PollInterval
	// Use multiplication to avoid wrap-around on very large N. 2^62 ns
	// exceeds a year, so the shift path is fine in practice, but we
	// cap at ceiling unconditionally which makes the math robust.
	delay := poll
	for i := 1; i < n; i++ {
		delay *= 2
		if delay >= backoffCeiling {
			delay = backoffCeiling
			break
		}
	}
	if delay > backoffCeiling {
		delay = backoffCeiling
	}
	d.backoff.nextTickNotBefore = d.clock().Add(delay)
	d.mu.Unlock()

	d.log.Info("backoff_scheduled",
		"consecutive_failures", n,
		"delay", delay.String(),
	)
	return nil
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
