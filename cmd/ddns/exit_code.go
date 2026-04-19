package main

import (
	"errors"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// exitCodeFor maps a top-level error returned from cmd.Run into a process
// exit code per the PRD's error taxonomy.
//
// cli.ExitCoder is intentionally checked BEFORE the sentinel switch: a
// subcommand Action that returns `cli.Exit("...", 7)` must produce exit 7,
// even though that value would otherwise fall through to the default branch.
// Checking ExitCoder first preserves urfave/cli's contract.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var exitCoder cli.ExitCoder
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	switch {
	case errors.Is(err, ddnserr.ErrNoQuorum):
		return 1
	case errors.Is(err, ddnserr.ErrConfig), errors.Is(err, ddnserr.ErrAuth):
		return 3
	case errors.Is(err, ddnserr.ErrTransient):
		return 2
	default:
		return 2
	}
}
