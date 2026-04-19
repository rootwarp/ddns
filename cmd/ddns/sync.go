package main

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/logging"
	"github.com/rootwarp/ddns/internal/resolver"
	"github.com/rootwarp/ddns/internal/state"
)

// syncAction runs exactly one reconciliation pass and exits. Exit-code
// discipline is provided by the same exitCodeFor mapping used by runAction:
//
//   - config load error (wraps ErrConfig) → 3
//   - provider construction (wraps ErrAuth) → 3
//   - ReconcileOnce returns ErrNoQuorum → 1
//   - ReconcileOnce returns provider error (wraps ErrTransient) → 2
//   - success → 0
//
// Phase 4 issue 4.1 makes --dry-run a first-class feature: when set, the
// daemon emits reconcile_dry_run with the would-be payload instead of
// calling provider.Upsert, and persists state with last_result=noop so a
// subsequent non-dry-run sync still issues the update.
func syncAction(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return err
	}

	// No `record` attr on the base logger: multi-record support (issue
	// 3.6) attaches per-record attrs inside reconcileRecord so each log
	// line is scoped correctly. A base-level `record` attr would shadow
	// the per-record one in some handlers, producing duplicate keys.
	logger := logging.NewLogger(cfg.LogFormat, os.Stdout)

	provider, err := buildProvider(ctx)
	if err != nil {
		return err
	}

	res := resolver.New(cfg.Resolver)
	store := state.NewStore(cfg.StatePath)
	d := daemon.New(cfg, res, provider, store, logger)

	dryRun := cmd.Bool("dry-run")
	if dryRun {
		// Prominent startup banner so operators immediately see that
		// this sync will NOT mutate DNS. Documented in docs/log-events.md.
		logger.Info("dry_run_mode_active")
	}
	return d.ReconcileOnce(ctx, dryRun)
}
