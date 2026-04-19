package main

import (
	"bytes"
	"strings"
	"testing"
)

// Issue 4.6: help-text pass.
//
// These tests codify the human-review acceptance criteria as machine-
// checkable assertions:
//   - ./bin/ddns --help is <= 40 lines.
//   - ./bin/ddns run --help mentions config-file-required.
//   - Both run and sync have Description fields that distinguish them.

func TestHelp_RootUnder40Lines(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	code, out := runDdns(t, []string{"--help"}, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nout:\n%s", code, out)
	}
	lines := bytes.Count([]byte(out), []byte{'\n'})
	if lines > 40 {
		t.Fatalf("--help output is %d lines, want <= 40:\n%s", lines, out)
	}
}

func TestHelp_RunMentionsConfigRequired(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	code, out := runDdns(t, []string{"run", "--help"}, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nout:\n%s", code, out)
	}
	low := strings.ToLower(out)
	// "required" appears in the --config flag usage and/or in the
	// Description text; either is fine for this assertion.
	if !strings.Contains(low, "required") {
		t.Fatalf("run --help does not mention config-required:\n%s", out)
	}
	if !strings.Contains(low, "config") {
		t.Fatalf("run --help does not mention config:\n%s", out)
	}
}

func TestHelp_RunAndSyncHaveDescriptions(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	for _, sub := range []string{"run", "sync"} {
		code, out := runDdns(t, []string{sub, "--help"}, nil)
		if code != 0 {
			t.Fatalf("%s --help exit = %d\n%s", sub, code, out)
		}
		if !strings.Contains(out, "DESCRIPTION:") {
			t.Errorf("%s --help lacks DESCRIPTION section:\n%s", sub, out)
		}
	}
}

func TestHelp_RunAndSyncMentionEachOther(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test skipped in -short mode")
	}
	{
		code, out := runDdns(t, []string{"run", "--help"}, nil)
		if code != 0 {
			t.Fatalf("run --help exit = %d", code)
		}
		if !strings.Contains(out, "ddns sync") {
			t.Errorf("run --help should point users to ddns sync for one-shot use:\n%s", out)
		}
	}
	{
		code, out := runDdns(t, []string{"sync", "--help"}, nil)
		if code != 0 {
			t.Fatalf("sync --help exit = %d", code)
		}
		if !strings.Contains(out, "ddns run") {
			t.Errorf("sync --help should point users to ddns run for continuous polling:\n%s", out)
		}
	}
}
