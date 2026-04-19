package main

import (
	"context"
	"fmt"
	"os"

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
// and enters the reconcile loop. Errors returned here propagate to main()
// which maps them through exitCodeFor — config errors exit 3, auth errors
// exit 3, transient provider errors exit 2, etc.
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
	return d.Run(ctx)
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
