package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

// statusAction prints the last-known state. Phase 1 stub; implemented in
// Phase 3 (issue 3.5).
func statusAction(_ context.Context, _ *cli.Command) error {
	return cli.Exit("not yet implemented", 1)
}
