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
// The --dry-run flag is declared here so the help text documents it, but
// Phase 3 does NOT honor it — Phase 4 issue 4.1 threads it into
// ReconcileOnce so the daemon logs the would-be payload instead of calling
// Upsert.
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

	// --dry-run is read here so urfave/cli doesn't complain about an
	// unused declared flag, and so future readers can find the exact
	// spot where Phase 4 will plumb it through.
	_ = cmd.Bool("dry-run")

	return d.ReconcileOnce(ctx)
}
