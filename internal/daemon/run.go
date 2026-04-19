package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// Run drives the reconciliation loop until ctx is canceled. The first tick
// fires immediately (not one PollInterval later) so a post-reboot daemon
// reconciles promptly. Subsequent ticks are paced by re-reading
// cfg.PollInterval every iteration via time.After — this is Fix L, which
// makes SIGHUP config reload (issue 4.4) actually change the tick cadence.
// A fixed time.Ticker created once at loop entry would never observe a
// reloaded interval.
//
// Error policy:
//
//   - The FIRST tick's error is inspected: ErrNoQuorum and ErrTransient are
//     "wait and try again", so the loop swallows them and keeps running. Any
//     other error (most importantly ErrAuth or an undetected programmer bug)
//     is propagated — the caller treats it as terminal.
//   - Subsequent ticks never propagate; per-tick errors are already logged
//     inside ReconcileOnce, and the loop continues until cancellation OR
//     the fatal-error ceiling (Fix M) is hit.
//   - Fix M: if ErrAuth or ErrConfig repeats for consecutiveFatalCeiling
//     ticks in a row, Run returns the wrapped error so its supervisor (exit
//     code 3) can restart or alert a human.
//
// Backoff: when a tick returns a transient error, the next tick is delayed
// by min(cfg.PollInterval * 2^(N-1), 30m) where N is the consecutive-
// transient-failure count. ErrNoQuorum neither increments nor resets the
// counter — resolver failures are not provider retriables. A fully
// successful reconcile (nil error) resets both the transient counter and
// the fatal counter (Fix M).
//
// The dryRun parameter is threaded through to ReconcileOnce per issue 4.1.
// Run itself emits dry_run_mode_active at entry when dryRun is true so
// operators see the state of the world up-front.
//
// Shutdown: ctx.Done() causes one structured `shutdown` log and a nil return.
func (d *Daemon) Run(ctx context.Context, dryRun bool) error {
	if dryRun {
		d.log.Info("dry_run_mode_active")
	}

	// First tick fires immediately: post-reboot the user wants a prompt
	// reconcile, not one PollInterval of silence.
	if err := d.ReconcileOnce(ctx, dryRun); err != nil {
		if !errors.Is(err, ddnserr.ErrNoQuorum) && !errors.Is(err, ddnserr.ErrTransient) {
			return err
		}
		if fatalErr := d.updateBackoff(err); fatalErr != nil {
			return fatalErr
		}
	} else {
		_ = d.updateBackoff(nil)
	}

	for {
		interval, backoffActive, notBefore := d.nextIntervalDetails()
		if backoffActive {
			d.log.Info("backoff_applied",
				"skip_until", notBefore.UTC().Format(time.RFC3339Nano),
				"wait", interval.String(),
			)
		}
		select {
		case <-ctx.Done():
			d.log.Info("shutdown", "reason", ctx.Err().Error())
			return nil
		case <-time.After(interval):
			// Per-tick errors are logged inside ReconcileOnce; this loop
			// only propagates them after the fatal-ceiling check inside
			// updateBackoff. A transient tick is recorded and the loop
			// continues; a fatal tick eventually returns (Fix M).
			err := d.ReconcileOnce(ctx, dryRun)
			if fatalErr := d.updateBackoff(err); fatalErr != nil {
				return fatalErr
			}
		}
	}
}

// nextIntervalDetails returns how long to wait before the next tick,
// whether a backoff window is currently extending it, and the computed
// not-before timestamp (zero when no backoff). The returned triple lets
// Run decide whether to emit a backoff_applied log line.
//
// Reading cfg under an RLock is what makes Fix L (SIGHUP reload changes
// the interval) work — the value is freshly read on every iteration.
func (d *Daemon) nextIntervalDetails() (wait time.Duration, backoffActive bool, notBefore time.Time) {
	d.mu.RLock()
	poll := d.cfg.PollInterval
	nextNotBefore := d.backoff.nextTickNotBefore
	d.mu.RUnlock()

	if nextNotBefore.IsZero() {
		return poll, false, time.Time{}
	}
	remaining := nextNotBefore.Sub(d.clock())
	// Backoff has already elapsed: fall through to a normal tick without
	// emitting backoff_applied (the window is not actually suppressing
	// anything).
	if remaining <= poll {
		return poll, false, time.Time{}
	}
	return remaining, true, nextNotBefore
}
