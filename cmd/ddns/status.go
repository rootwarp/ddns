package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rootwarp/ddns/internal/config"
	"github.com/rootwarp/ddns/internal/ddnserr"
	"github.com/rootwarp/ddns/internal/state"
)

// statusAction prints the last-known state for every configured record.
// Read-only against local state: no provider calls, no network I/O.
//
// Output shape:
//
//   - default (text): one human-readable block per record, including the
//     record name, last observed IP, last checked / last updated
//     timestamps (with "X ago" relative suffix), and last result.
//   - --json: a single JSON array with one element per record. Elements
//     are the *State struct, or null when no state file exists yet.
//
// A record with no state yet is NOT an error: first-run is normal. The
// text path prints a "no state recorded yet" hint and moves on. Only a
// genuine I/O error on state.Load propagates.
func statusAction(_ context.Context, cmd *cli.Command) error {
	cfg, err := config.Load(cmd.String("config"))
	if err != nil {
		return err
	}
	store := state.NewStore(cfg.StatePath)
	asJSON := cmd.Bool("json")

	if asJSON {
		return emitJSON(os.Stdout, store, cfg.Records)
	}
	return emitText(os.Stdout, store, cfg.Records)
}

// emitText walks records and prints a human-friendly block for each. The
// timestamps are both absolute (RFC3339) and relative ("3m12s ago") so
// operators at a glance can decide "is this fresh?".
//
// Write errors are captured into a sticky variable so every Fprintf is
// still called (no partial lines on an ErrShortWrite mid-block), but the
// first error is propagated to the caller.
func emitText(w io.Writer, store *state.Store, records []config.RecordConfig) error {
	now := time.Now()
	var werr error
	write := func(format string, args ...any) {
		if werr != nil {
			return
		}
		_, werr = fmt.Fprintf(w, format, args...)
	}
	for _, rec := range records {
		st, err := store.Load(rec.Name)
		if errors.Is(err, ddnserr.ErrNotFound) {
			write("record: %s\n", rec.Name)
			write("  (no state recorded yet — ddns run / ddns sync has not completed a tick)\n")
			continue
		}
		if err != nil {
			return err
		}
		write("record: %s\n", rec.Name)
		write("  last_observed_ip: %s\n", st.LastObservedIP)
		write("  last_checked:    %s%s\n", fmtTime(st.LastCheckedAt), fmtAgo(st.LastCheckedAt, now))
		if !st.LastUpdatedAt.IsZero() {
			write("  last_updated:    %s%s\n", fmtTime(st.LastUpdatedAt), fmtAgo(st.LastUpdatedAt, now))
		}
		write("  last_result:     %s\n", st.LastResult)
		if st.LastError != "" {
			write("  last_error:      %s\n", st.LastError)
		}
	}
	return werr
}

// emitJSON builds a single array of per-record State pointers (nil for no-
// state-yet) and writes it to w. Pretty-printed so `jq .` and human
// eyeballs agree.
func emitJSON(w io.Writer, store *state.Store, records []config.RecordConfig) error {
	out := make([]*state.State, 0, len(records))
	for _, rec := range records {
		st, err := store.Load(rec.Name)
		if errors.Is(err, ddnserr.ErrNotFound) {
			out = append(out, nil)
			continue
		}
		if err != nil {
			return err
		}
		// Take the address of a local copy — the loop variable would
		// otherwise alias across iterations.
		local := st
		out = append(out, &local)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// fmtTime formats t as RFC3339 (the same form the state file uses on
// disk), or "(never)" for a zero time. A zero LastCheckedAt in practice
// only happens if someone hand-crafts a state file without one; we still
// produce legible output in that case.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "(never)"
	}
	return t.UTC().Format(time.RFC3339)
}

// fmtAgo returns " (3m12s ago)" or "" for a zero time. The leading space
// and parens let callers concat this onto an RFC3339 timestamp without
// worrying about formatting branches.
func fmtAgo(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf(" (%s ago)", now.Sub(t).Round(time.Second))
}
