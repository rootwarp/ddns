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
		Usage: "Dynamic DNS updater for Google Cloud DNS",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "path to config file",
				Sources: cli.EnvVars("DDNS_CONFIG"),
			},
			&cli.StringFlag{
				Name:  "log-format",
				Value: "auto",
				Usage: "log output format: auto|text|json",
			},
		},
		Commands: []*cli.Command{
			{Name: "run", Usage: "run the reconciliation daemon", Action: runAction},
			{Name: "sync", Usage: "run one reconciliation pass and exit", Action: syncAction},
			{Name: "status", Usage: "print last-known state", Action: statusAction},
			{Name: "version", Usage: "print version information", Action: versionAction},
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
