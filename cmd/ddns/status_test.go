package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/state"
)

// seededStatus is the common setup the status tests share: a temp
// state-dir, a minimal config pointing at it, and zero or more pre-seeded
// State records. Returns the config-file path so the test can pass
// --config.
type seededStatus struct {
	configPath string
	stateDir   string
}

func seedStatus(t *testing.T, recs []state.State, extraRecords []string) seededStatus {
	t.Helper()
	stateDir := t.TempDir()
	store := state.NewStore(stateDir)
	for _, r := range recs {
		if err := store.Save(r); err != nil {
			t.Fatalf("seed save: %v", err)
		}
	}

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	var recordsBody strings.Builder
	// Include every record the caller wants to see (both the pre-seeded
	// ones and any extra "no-state-yet" placeholders).
	names := make([]string, 0, len(recs)+len(extraRecords))
	for _, r := range recs {
		names = append(names, r.RecordName)
	}
	names = append(names, extraRecords...)
	for _, n := range names {
		recordsBody.WriteString("  - project: example-proj\n")
		recordsBody.WriteString("    managed_zone: example-zone\n")
		recordsBody.WriteString("    name: " + n + "\n")
		recordsBody.WriteString("    type: A\n")
		recordsBody.WriteString("    ttl: 300\n")
	}
	body := `poll_interval: 1s
state_path: ` + stateDir + `
log_format: json
resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
  quorum: 1
  timeout: 10s
records:
` + recordsBody.String()
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return seededStatus{configPath: cfgPath, stateDir: stateDir}
}

// runStatus invokes statusAction with a mini cli.Command bound to --config
// and (optionally) --json, capturing stdout. Returns stdout as a string
// and the action's error.
func runStatus(t *testing.T, configPath string, asJSON bool) (string, error) {
	t.Helper()

	// Capture stdout.
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

	// Build the exact same cli.Command the test path would receive via
	// cmd.Run — urfave/cli v3 lets us drive the tree through Run([]).
	app := &cli.Command{
		Commands: []*cli.Command{
			{
				Name:   "status",
				Action: statusAction,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config"},
					&cli.BoolFlag{Name: "json"},
				},
			},
		},
	}
	args := []string{"app", "status", "--config", configPath}
	if asJSON {
		args = append(args, "--json")
	}
	runErr := app.Run(context.Background(), args)
	_ = w.Close()
	return <-done, runErr
}

func TestStatus_SingleRecord_Text(t *testing.T) {
	recs := []state.State{{
		RecordName:     "alpha.example.com.",
		LastObservedIP: "192.0.2.42",
		LastResult:     "updated",
		LastCheckedAt:  time.Date(2026, 4, 19, 16, 30, 2, 0, time.UTC),
		LastUpdatedAt:  time.Date(2026, 4, 19, 14, 15, 44, 0, time.UTC),
	}}
	s := seedStatus(t, recs, nil)

	out, err := runStatus(t, s.configPath, false)
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	for _, want := range []string{
		"alpha.example.com.",
		"192.0.2.42",
		"updated",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestStatus_SingleRecord_JSON(t *testing.T) {
	recs := []state.State{{
		RecordName:     "alpha.example.com.",
		LastObservedIP: "192.0.2.42",
		LastResult:     "updated",
		LastCheckedAt:  time.Date(2026, 4, 19, 16, 30, 2, 0, time.UTC),
	}}
	s := seedStatus(t, recs, nil)

	out, err := runStatus(t, s.configPath, true)
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	var decoded []*state.State
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, out)
	}
	if len(decoded) != 1 {
		t.Fatalf("len(decoded) = %d, want 1", len(decoded))
	}
	if decoded[0].RecordName != "alpha.example.com." {
		t.Fatalf("RecordName = %q, want alpha.example.com.", decoded[0].RecordName)
	}
	if decoded[0].LastObservedIP != "192.0.2.42" {
		t.Fatalf("LastObservedIP = %q, want 192.0.2.42", decoded[0].LastObservedIP)
	}
}

func TestStatus_MissingState_Text(t *testing.T) {
	s := seedStatus(t, nil, []string{"no-state.example.com."})

	out, err := runStatus(t, s.configPath, false)
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(out, "no-state.example.com.") {
		t.Fatalf("output missing record name:\n%s", out)
	}
	if !strings.Contains(out, "no state recorded yet") {
		t.Fatalf("output missing 'no state recorded yet':\n%s", out)
	}
}

func TestStatus_MissingState_JSON(t *testing.T) {
	s := seedStatus(t, nil, []string{"no-state.example.com."})

	out, err := runStatus(t, s.configPath, true)
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	// JSON shape: [null] for a record with no state.
	var decoded []*state.State
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, out)
	}
	if len(decoded) != 1 {
		t.Fatalf("len(decoded) = %d, want 1", len(decoded))
	}
	if decoded[0] != nil {
		t.Fatalf("decoded[0] = %+v, want nil (no state recorded)", decoded[0])
	}
}

func TestStatus_MultipleRecords_Text(t *testing.T) {
	recs := []state.State{
		{
			RecordName:     "alpha.example.com.",
			LastObservedIP: "192.0.2.42",
			LastResult:     "updated",
			LastCheckedAt:  time.Date(2026, 4, 19, 16, 30, 0, 0, time.UTC),
		},
		{
			RecordName:     "bravo.example.com.",
			LastObservedIP: "198.51.100.7",
			LastResult:     "noop",
			LastCheckedAt:  time.Date(2026, 4, 19, 16, 30, 0, 0, time.UTC),
		},
	}
	s := seedStatus(t, recs, nil)

	out, err := runStatus(t, s.configPath, false)
	if err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	for _, want := range []string{
		"alpha.example.com.",
		"bravo.example.com.",
		"192.0.2.42",
		"198.51.100.7",
		"updated",
		"noop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestStatus_MissingConfigIsError — unlike no-state-yet (exit 0), a
// missing config file is an error.
func TestStatus_MissingConfigIsError(t *testing.T) {
	_, err := runStatus(t, "/does/not/exist.yaml", false)
	if err == nil {
		t.Fatalf("runStatus: expected error, got nil")
	}
}
