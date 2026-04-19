package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

// runAction is the reconciliation daemon entry point. Phase 1 leaves it as a
// stub; issue 1.4 replaces the body with a no-op ticker loop, and Phase 2
// wires it to the real resolver/provider/state chain.
func runAction(_ context.Context, _ *cli.Command) error {
	return cli.Exit("not yet implemented", 1)
}
