package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/version"
)

// versionAction prints the build-injected version string to stdout. It is the
// only Phase-1 subcommand that does real work; the other three return stubs.
func versionAction(_ context.Context, _ *cli.Command) error {
	fmt.Println(version.String())
	return nil
}
