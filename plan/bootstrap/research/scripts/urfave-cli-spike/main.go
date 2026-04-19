// Spike for issue 0.3: confirm urfave/cli/v3 ergonomics.
// Throwaway — not imported by the real module.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "spike",
		Usage: "urfave/cli/v3 spike",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Usage: "path to config"},
		},
		Commands: []*cli.Command{
			{
				Name:   "run",
				Usage:  "blocks until SIGINT/SIGTERM",
				Action: runAction,
			},
			{
				Name:   "version",
				Usage:  "print version",
				Action: versionAction,
			},
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
}

func runAction(ctx context.Context, _ *cli.Command) error {
	fmt.Println("run: waiting for SIGINT/SIGTERM (press Ctrl-C)")
	select {
	case <-ctx.Done():
		fmt.Println("run: context cancelled, exiting cleanly")
		return nil
	case <-time.After(30 * time.Second):
		fmt.Println("run: 30s elapsed with no signal, exiting")
		return nil
	}
}

func versionAction(_ context.Context, _ *cli.Command) error {
	fmt.Println("spike v0.0.0")
	return nil
}
