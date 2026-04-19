package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/daemon"
	"github.com/rootwarp/ddns/internal/dnsprovider"
	"github.com/rootwarp/ddns/internal/dnsprovider/fake"
	"github.com/rootwarp/ddns/internal/dnsprovider/gcp"
	"github.com/rootwarp/ddns/internal/logging"
	"github.com/rootwarp/ddns/internal/resolver"
	"github.com/rootwarp/ddns/internal/state"
)

// providerEnvVar is a TEST-ONLY escape hatch. When set to "fake", runAction
// uses the in-memory dnsprovider/fake instead of constructing a real Google
// Cloud DNS client via gcp.New. This keeps the Phase 1 smoke test (which
// spawns the binary for 2s under SIGTERM without ADC) viable after Phase 2
// makes runAction require a config file and a working provider.
//
// The env var is intentionally NOT documented in user-facing help. Only the
// smoke test (cmd/ddns/run_smoke_test.go) and the CI workflow set it. Any
// value other than "" and "fake" is rejected to avoid silent test-shape
// regressions.
const providerEnvVar = "DDNS_FAKE_PROVIDER"

// runAction loads config, builds the resolver / provider / daemon wiring,
// installs a SIGHUP handler for live config reload (issue 4.4), and enters
// the reconcile loop. Errors returned here propagate to main() which maps
// them through exitCodeFor — config errors exit 3, auth errors exit 3,
// transient provider errors exit 2, etc.
//
// --dry-run (issue 4.1) is read here and threaded into Daemon.Run. When
// set, the daemon emits a prominent dry_run_mode_active banner and never
// calls provider.Upsert for the lifetime of the process.
func runAction(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return err
	}

	// No `record` attr on the base logger: issue 3.6's multi-record
	// support attaches per-record attrs inside reconcileRecord so each
	// log line is scoped correctly.
	logger := logging.NewLogger(cfg.LogFormat, os.Stdout)

	logger.Info("startup",
		"poll_interval", cfg.PollInterval.String(),
		"config", cfg.Path(),
	)

	provider, err := buildProvider(ctx)
	if err != nil {
		return err
	}

	res := resolver.New(cfg.Resolver)
	store := state.NewStore(cfg.StatePath)
	d := daemon.New(cfg, res, provider, store, logger)

	// Install the SIGHUP handler (issue 4.4). The goroutine exits when
	// ctx is cancelled via the outer signal.NotifyContext; signal.Stop
	// is deferred so we do not leak the registration on a panic path.
	hupStop := installSIGHUP(ctx, d, cfg.Path(), logger)
	defer hupStop()

	dryRun := cmd.Bool("dry-run")
	return d.Run(ctx, dryRun)
}

// installSIGHUP wires a SIGHUP listener that reloads the config from
// cfgPath on every signal. On success, it swaps the daemon's cfg and
// resolver (Daemon.UpdateConfig) and logs config_reload_ok; on failure,
// it logs config_reload_failed and keeps running with the old config.
//
// Returns a stop function that the caller must defer — it both un-
// registers the signal handler AND closes the signal channel so the
// goroutine exits when the outer context is cancelled.
func installSIGHUP(ctx context.Context, d *daemon.Daemon, cfgPath string, logger *slog.Logger) func() {
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-hupCh:
				if !ok {
					return
				}
				newCfg, err := config.Load(cfgPath)
				if err != nil {
					logger.Error("config_reload_failed", "err", err.Error())
					continue
				}
				d.UpdateConfig(newCfg)
				logger.Info("config_reload_ok", "records", len(newCfg.Records))
			}
		}
	}()

	return func() {
		// Order matters: Stop un-registers the handler so no further
		// signals will be sent to hupCh. Only THEN do we close the
		// channel; closing before Stop could race with a signal
		// dispatch. Finally wait for the goroutine to exit.
		signal.Stop(hupCh)
		close(hupCh)
		<-done
	}
}

// buildProvider returns either the real GCP provider or — when
// DDNS_FAKE_PROVIDER=fake — an in-memory fake. See the comment on
// providerEnvVar for why this seam exists.
func buildProvider(ctx context.Context) (dnsprovider.DNSProvider, error) {
	switch v := os.Getenv(providerEnvVar); v {
	case "":
		return gcp.New(ctx)
	case "fake":
		return fake.New(), nil
	default:
		return nil, fmt.Errorf("invalid %s=%q (only %q or unset allowed)", providerEnvVar, v, "fake")
	}
}
