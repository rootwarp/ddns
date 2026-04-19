// Command ddns is the Dynamic DNS updater CLI.
//
// The root command and subcommand registration live in this file; each
// subcommand Action has its own file (run.go, sync.go, status.go,
// version.go). Error-to-exit-code mapping lives in exit_code.go.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"
)

func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:  "ddns",
		Usage: "Dynamic DNS updater for Google Cloud DNS (poll echo services, reconcile an A record)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "path to the YAML config file (required for run/sync/status)",
				Sources: cli.EnvVars("DDNS_CONFIG"),
			},
			&cli.StringFlag{
				Name:  "log-format",
				Value: "auto",
				Usage: "log output format: auto (TTY→text, else JSON), text, or json",
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "run",
				Usage:  "run the reconciliation daemon (long-lived, polls and reconciles)",
				Action: runAction,
				Description: "Run ddns as a long-lived daemon. The --config flag (or DDNS_CONFIG env) is required. " +
					"The daemon polls the configured HTTPS echo services every poll_interval, compares the resolved IP " +
					"against local state and the live Cloud DNS record, and issues an update only when both comparisons " +
					"show drift. SIGHUP re-reads the config without restarting; SIGTERM/SIGINT shut down cleanly. " +
					"For cron-style one-shot execution, use \"ddns sync\" instead.",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "do not call the provider; log the payload that would be sent",
					},
				},
			},
			{
				Name:   "sync",
				Usage:  "run one reconciliation pass and exit (for cron-style use)",
				Action: syncAction,
				Description: "Run a single reconcile tick and exit with a discrete code (0 success / noop, 1 no quorum, " +
					"2 transient provider error, 3 config or auth error). The --config flag (or DDNS_CONFIG env) is " +
					"required. Pair with cron or a systemd timer to get scheduled updates without a long-lived process. " +
					"For continuous polling, use \"ddns run\" instead.",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "do not call the provider; log the payload that would be sent",
					},
				},
			},
			{
				Name:   "status",
				Usage:  "print last-known per-record state from the local store",
				Action: statusAction,
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "json",
						Usage: "emit the state as a JSON array (one element per record, null for no-state-yet)",
					},
				},
			},
			{Name: "version", Usage: "print the version, commit, and build date", Action: versionAction},
		},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := newRootCommand()
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "ddns:", err)
		os.Exit(exitCodeFor(err))
	}
}
