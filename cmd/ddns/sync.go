package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

// syncAction runs one reconciliation pass and exits. Phase 1 stub;
// implemented in Phase 3 (issue 3.4).
func syncAction(_ context.Context, _ *cli.Command) error {
	return cli.Exit("not yet implemented", 1)
}
