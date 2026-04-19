package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

func TestVersionAction_PrintsVersionString(t *testing.T) {
	out := captureStdout(t, func() {
		if err := versionAction(context.Background(), &cli.Command{}); err != nil {
			t.Fatalf("versionAction: %v", err)
		}
	})
	if !strings.HasPrefix(strings.TrimSpace(out), "ddns ") {
		t.Fatalf("versionAction stdout = %q, want prefix %q", out, "ddns ")
	}
}

func assertExitCodeErr(t *testing.T, err error, wantCode int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var ec cli.ExitCoder
	if !errors.As(err, &ec) {
		t.Fatalf("expected cli.ExitCoder, got %T: %v", err, err)
	}
	if got := ec.ExitCode(); got != wantCode {
		t.Fatalf("ExitCode() = %d, want %d", got, wantCode)
	}
}

func TestRunAction_StubBeforeLoop(t *testing.T) {
	// We deliberately don't test the runAction loop in-process: that path is
	// exercised by the smoke test. This slot is reserved for a future mockable
	// version of runAction (Phase 2 when config+resolver land).
	t.Skip("runAction loop is covered by TestRunSmoke")
}

func TestSyncAction_ReturnsStubExit1(t *testing.T) {
	err := syncAction(context.Background(), &cli.Command{})
	assertExitCodeErr(t, err, 1)
}

func TestStatusAction_ReturnsStubExit1(t *testing.T) {
	err := statusAction(context.Background(), &cli.Command{})
	assertExitCodeErr(t, err, 1)
}

func TestNewRootCommand_DeclaresFourSubcommands(t *testing.T) {
	cmd := newRootCommand()
	if cmd.Name != "ddns" {
		t.Fatalf("Name = %q, want ddns", cmd.Name)
	}
	if got := len(cmd.Commands); got != 4 {
		t.Fatalf("len(Commands) = %d, want 4", got)
	}
	want := map[string]bool{"run": false, "sync": false, "status": false, "version": false}
	for _, sub := range cmd.Commands {
		if _, ok := want[sub.Name]; !ok {
			t.Fatalf("unexpected subcommand %q", sub.Name)
		}
		want[sub.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("subcommand %q not declared", name)
		}
	}
	// Global flags must include --config (with DDNS_CONFIG env) and --log-format.
	sawConfig, sawLogFormat := false, false
	for _, f := range cmd.Flags {
		switch f.Names()[0] {
		case "config":
			sawConfig = true
		case "log-format":
			sawLogFormat = true
		}
	}
	if !sawConfig {
		t.Fatalf("root command missing --config flag")
	}
	if !sawLogFormat {
		t.Fatalf("root command missing --log-format flag")
	}
}
