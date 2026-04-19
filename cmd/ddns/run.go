package main

import (
	"context"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/logging"
)

// noopInterval is the hard-coded tick interval for the Phase-1 no-op loop.
// Phase 2 (issue 2.6) replaces this with the config's poll_interval.
const noopInterval = 30 * time.Second

// runAction is the reconciliation daemon entry point. In Phase 1 it is a
// no-op loop: it logs startup, ticks every 30s emitting tick_noop, and exits
// cleanly on SIGINT/SIGTERM (ctx cancellation).
//
// Phase 2 replaces the body with the real reconcile loop wired to the
// resolver, provider, and state store; the test smoke shape remains the same
// so the CI smoke step is stable across phases.
func runAction(ctx context.Context, cmd *cli.Command) error {
	logger := logging.NewLogger(cmd.String("log-format"), os.Stdout)
	logger.Info("startup", "interval", noopInterval.String(), "mode", "no-op")

	ticker := time.NewTicker(noopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown", "reason", ctx.Err().Error())
			return nil
		case t := <-ticker.C:
			logger.Info("tick_noop", "at", t.Format(time.RFC3339))
		}
	}
}
