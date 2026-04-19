package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// Run drives the reconciliation loop until ctx is canceled. The first tick
// fires immediately (not one PollInterval later) so a post-reboot daemon
// reconciles promptly; subsequent ticks are paced by a time.Ticker using
// cfg.PollInterval.
//
// Error policy:
//
//   - The FIRST tick's error is inspected: ErrNoQuorum and ErrTransient are
//     "wait and try again", so the loop swallows them and keeps running. Any
//     other error (most importantly ErrAuth or an undetected programmer bug)
//     is propagated — the caller treats it as terminal.
//   - Subsequent ticks never propagate; per-tick errors are already logged
//     inside ReconcileOnce, and the loop continues until cancellation.
//
// Shutdown: ctx.Done() causes one structured `shutdown` log and a nil return.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.ReconcileOnce(ctx); err != nil {
		if !errors.Is(err, ddnserr.ErrNoQuorum) && !errors.Is(err, ddnserr.ErrTransient) {
			return err
		}
	}

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("shutdown", "reason", ctx.Err().Error())
			return nil
		case <-ticker.C:
			// Per-tick errors are logged inside ReconcileOnce; intentionally
			// ignored here so a transient flake does not kill the daemon.
			_ = d.ReconcileOnce(ctx)
		}
	}
}
