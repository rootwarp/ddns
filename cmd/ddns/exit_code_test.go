package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

func TestExitCodeFor_Nil(t *testing.T) {
	if got := exitCodeFor(nil); got != 0 {
		t.Fatalf("exitCodeFor(nil) = %d, want 0", got)
	}
}

func TestExitCodeFor_NoQuorum(t *testing.T) {
	if got := exitCodeFor(ddnserr.ErrNoQuorum); got != 1 {
		t.Fatalf("exitCodeFor(ErrNoQuorum) = %d, want 1", got)
	}
}

func TestExitCodeFor_Config(t *testing.T) {
	if got := exitCodeFor(ddnserr.ErrConfig); got != 3 {
		t.Fatalf("exitCodeFor(ErrConfig) = %d, want 3", got)
	}
}

func TestExitCodeFor_Auth(t *testing.T) {
	if got := exitCodeFor(ddnserr.ErrAuth); got != 3 {
		t.Fatalf("exitCodeFor(ErrAuth) = %d, want 3", got)
	}
}

func TestExitCodeFor_Transient(t *testing.T) {
	if got := exitCodeFor(ddnserr.ErrTransient); got != 2 {
		t.Fatalf("exitCodeFor(ErrTransient) = %d, want 2", got)
	}
}

func TestExitCodeFor_WrappedAuth(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", ddnserr.ErrAuth)
	if got := exitCodeFor(wrapped); got != 3 {
		t.Fatalf("exitCodeFor(wrapped ErrAuth) = %d, want 3", got)
	}
}

func TestExitCodeFor_WrappedNoQuorum(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", ddnserr.ErrNoQuorum)
	if got := exitCodeFor(wrapped); got != 1 {
		t.Fatalf("exitCodeFor(wrapped ErrNoQuorum) = %d, want 1", got)
	}
}

func TestExitCodeFor_Random(t *testing.T) {
	if got := exitCodeFor(errors.New("random")); got != 2 {
		t.Fatalf("exitCodeFor(random) = %d, want 2", got)
	}
}

// TestExitCodeFor_CLIExit is the Fix-A regression test: cli.Exit("...", 7) must
// return 7, which only works when the cli.ExitCoder branch is checked BEFORE
// the sentinel switch. If the branches are reordered (as in the issue doc's
// original sketch), this case falls through to default and returns 2.
func TestExitCodeFor_CLIExit(t *testing.T) {
	if got := exitCodeFor(cli.Exit("x", 7)); got != 7 {
		t.Fatalf("exitCodeFor(cli.Exit(\"x\", 7)) = %d, want 7", got)
	}
}

// A cli.Exit with code 1 should still override the sentinel-default path.
func TestExitCodeFor_CLIExitOne(t *testing.T) {
	if got := exitCodeFor(cli.Exit("not yet implemented", 1)); got != 1 {
		t.Fatalf("exitCodeFor(cli.Exit msg 1) = %d, want 1", got)
	}
}
