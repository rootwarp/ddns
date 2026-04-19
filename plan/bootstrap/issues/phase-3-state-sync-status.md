---
type: document
status: draft
PARA: projects
tags:
  - dev/go
  - dev/planning
---

**Connections:**
- [[project-plan]]
- [[architecture]]

# Phase 3: State, `sync`, `status`

**Goal.** Everything needed to run `ddns` under cron instead of as a daemon, and everything needed for it to survive a restart without re-writing the record unnecessarily. By end of phase, the binary supports all four PRD-promised subcommands and multiple records in one config.

**Entry criteria.** Phase 2 complete — `ddns run` against a real Cloud DNS managed zone keeps a single record in sync.

**Exit criteria.** `ddns sync` invoked by cron (or any scheduler) every 10 minutes keeps the record correct on a host with a changing IP; `ddns status` after a reboot correctly reflects last-observed state; zero redundant `Changes.Create` calls over a 24-hour soak on an unchanging IP (verified against the Cloud DNS audit log).

---

## Issue 3.1: `internal/fs_atomic` — atomic write helper

**Story points:** 1

**Phase:** 3

**Depends on:** 0.4

**PRD requirement:** P0 safe state-file writes

**Architecture modules touched:** `internal/fs_atomic`

**Description.** Single public function `WriteFile(path string, data []byte, mode fs.FileMode) error`. Writes via tempfile + `fsync` + `os.Rename` + parent-dir `fsync`. Guards the state file (and any future config export path) against power-loss corruption and torn writes.

**Implementation notes:**
- `internal/fs_atomic/atomic.go`:
  ```go
  func WriteFile(path string, data []byte, mode fs.FileMode) error {
      dir := filepath.Dir(path)
      if err := os.MkdirAll(dir, 0o700); err != nil {
          return fmt.Errorf("fs_atomic: mkdir %q: %w", dir, err)
      }
      tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
      if err != nil {
          return fmt.Errorf("fs_atomic: create temp: %w", err)
      }
      defer os.Remove(tmp.Name()) // safe: rename removes it from the dir
      if _, err := tmp.Write(data); err != nil {
          _ = tmp.Close()
          return fmt.Errorf("fs_atomic: write: %w", err)
      }
      if err := tmp.Chmod(mode); err != nil {
          _ = tmp.Close()
          return fmt.Errorf("fs_atomic: chmod: %w", err)
      }
      if err := tmp.Sync(); err != nil {
          _ = tmp.Close()
          return fmt.Errorf("fs_atomic: fsync temp: %w", err)
      }
      if err := tmp.Close(); err != nil {
          return fmt.Errorf("fs_atomic: close: %w", err)
      }
      if err := os.Rename(tmp.Name(), path); err != nil {
          return fmt.Errorf("fs_atomic: rename: %w", err)
      }
      // fsync the directory so the rename is durable.
      d, err := os.Open(dir)
      if err != nil {
          return nil // non-fatal; rename already succeeded
      }
      defer d.Close()
      _ = d.Sync()
      return nil
  }
  ```
- Tests in `internal/fs_atomic/atomic_test.go`:
  - `TestWriteFile_HappyPath` — write, read back, verify bytes and mode.
  - `TestWriteFile_OverwriteExisting` — pre-populate the file; WriteFile with new content; verify final content and mode.
  - `TestWriteFile_Permissions` — verify `0600` survives the rename.
  - `TestWriteFile_NoTempLeak` — after a successful write, no `*.tmp-*` files remain in the directory.
  - `TestWriteFile_MidWriteAbortNoLeak` — inject a write failure via a custom `io.Writer`? Not directly possible with this API (data is a `[]byte`). Instead, simulate a disk-full by writing to a path inside a tmpfs-mounted directory pre-filled to the brim; optional, skip if tricky. At minimum, a test that kills the process mid-write is out of scope here — covered by manual QA.
- Parent-dir fsync errors are intentionally non-fatal; logging them would require importing `slog` into `fs_atomic` which we avoid (keep it dependency-free except stdlib).

**Acceptance criteria:**
- [ ] All four unit tests pass.
- [ ] `TestWriteFile_NoTempLeak` runs on both Linux and macOS (different `os.CreateTemp` patterns).
- [ ] Package has no non-stdlib imports.
- [ ] Coverage ≥95% (it's a tiny function).

**Out of scope.** Crash-injection testing (manual). Windows support.

---

## Issue 3.2: `internal/state` — load + save

**Story points:** 2

**Phase:** 3

**Depends on:** 3.1, 2.1

**PRD requirement:** P0 state persistence

**Architecture modules touched:** `internal/state`

**Description.** Implement the `State` struct, `Load(path) (State, error)`, and `Save(path, state) error`. Per-record state-file naming: under `cfg.StatePath`, each record gets `state/<sha256(name)[:16]>.json`. Load returns `(State{}, ErrNotFound)` when the file does not exist — callers treat that as equivalent to first run.

**Implementation notes:**
- `internal/state/state.go`:
  ```go
  type State struct {
      LastCheckedAt  time.Time `json:"last_checked_at"`
      LastObservedIP string    `json:"last_observed_ip"`
      LastUpdatedAt  time.Time `json:"last_updated_at,omitempty"`
      LastResult     string    `json:"last_result"` // "noop" | "updated" | "error"
      LastError      string    `json:"last_error,omitempty"`
      RecordName     string    `json:"record_name"`
  }

  type Store struct {
      dir string // the base state directory (cfg.StatePath)
  }

  func NewStore(dir string) *Store { return &Store{dir: dir} }

  func (s *Store) Load(recordName string) (State, error) { /* … */ }
  func (s *Store) Save(state State) error { /* record name is inside State */ }
  ```
- `pathFor(name string)` returns `filepath.Join(s.dir, "state", sha256hex(name)[:16]+".json")` — the hash prefix avoids filesystems' filename-length limits and makes sure a name like `home.example.com.` and `home.example.com` (trailing-dot normalization) map to the same file.
- `Load` returns `ddnserr.ErrNotFound` on `os.IsNotExist`. Unmarshal errors return a wrapped error.
- `Save` uses `json.Marshal` (pretty-printed for human debugging) and `fs_atomic.WriteFile` with mode `0600`.
- Tests in `internal/state/state_test.go`:
  - `TestStore_RoundTrip` — save, load, assert equality.
  - `TestStore_LoadMissing` — `ErrNotFound`.
  - `TestStore_SavePermissions` — stat the file, verify `0600`.
  - `TestStore_NameHashingCollision` — two records with different names produce different paths.
  - `TestStore_TrailingDotNormalization` — `home.example.com` and `home.example.com.` load the same file (verified by `Save(name)` then `Load(name+".")` returning the saved state).

**Acceptance criteria:**
- [ ] All five tests pass.
- [ ] `Save` then `Load` round-trips cleanly.
- [ ] State file on disk is valid JSON and has mode `0600`.
- [ ] The hash-prefix scheme is documented with a comment in `pathFor`.

**Out of scope.** Compaction / rotation (state files are small, never grow). Migration between state schemas (bump in v2 if needed).

---

## Issue 3.3: `internal/daemon` — state integration

**Story points:** 2

**Phase:** 3

**Depends on:** 3.2, 2.6

**PRD requirement:** P0 state-aware reconcile

**Architecture modules touched:** `internal/daemon`

**Description.** Extend `ReconcileOnce` to (a) load per-record state at the top of the tick, (b) short-circuit when `quorumIP == state.LastObservedIP == live.Rrdatas[0]`, (c) write updated state after every tick outcome (`noop`, `updated`, `error`). This is the optimization that makes `ddns sync` under cron not thrash the provider API with `Get` calls when nothing has changed — the state file is the fast-path cache.

**Implementation notes:**
- `Daemon` now holds a `state.Store`:
  ```go
  type Daemon struct {
      cfg      *config.Config
      resolver resolver.IPResolver
      provider dnsprovider.DNSProvider
      store    *state.Store
      log      *slog.Logger
      clock    func() time.Time // injectable for tests; defaults to time.Now
  }
  ```
- Updated `ReconcileOnce`:
  ```go
  func (d *Daemon) ReconcileOnce(ctx context.Context) error {
      ip, report, err := d.resolver.Resolve(ctx)
      now := d.clock()
      if err != nil {
          d.log.Info("resolver_no_quorum", "report", report)
          d.persistError(ctx, now, "", err)
          return err
      }
      d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

      rec := d.cfg.Records[0]
      st, err := d.store.Load(rec.Name)
      if err != nil && !errors.Is(err, ddnserr.ErrNotFound) {
          return err
      }

      ref := dnsprovider.RecordRef{Project: rec.Project, ManagedZone: rec.ManagedZone, Name: rec.Name, Type: rec.Type}
      live, err := d.provider.Get(ctx, ref)
      if err != nil && !errors.Is(err, ddnserr.ErrNotFound) {
          d.persistError(ctx, now, ip.String(), err)
          return err
      }

      desired := dnsprovider.Record{Rrdatas: []string{ip.String()}, TTL: rec.TTL}
      if errors.Is(err, ddnserr.ErrNotFound) {
          // create
      } else if sameRrdatas(live.Rrdatas, desired.Rrdatas) && live.TTL == desired.TTL && st.LastObservedIP == ip.String() {
          d.log.Info("reconcile_noop", "ip", ip.String())
          d.persistNoop(ctx, now, ip.String(), rec.Name)
          return nil
      }

      res, err := d.provider.Upsert(ctx, ref, desired)
      if err != nil {
          d.log.Error("reconcile_error", "err", err.Error())
          d.persistError(ctx, now, ip.String(), err)
          return err
      }
      d.log.Info("reconcile_updated", "old", res.OldRrdatas, "new", res.NewRrdatas)
      d.persistUpdate(ctx, now, ip.String(), rec.Name)
      return nil
  }
  ```
- Three small helpers: `persistNoop`, `persistUpdate`, `persistError` — each builds a `State` and calls `d.store.Save`. State persistence errors are logged but not propagated (a write failure to the state file should not fail the reconcile — the next tick will re-observe and write state again).
- Tests extend the Phase 2 matrix with state-file fixtures:
  - `TestReconcileOnce_StateMatchesLive_FastPathNoop` — state says IP=X, provider says IP=X, resolver says IP=X → no `Upsert`.
  - `TestReconcileOnce_StateStale_ProviderSays_DifferentIP` — state says IP=X, provider says IP=Y, resolver says IP=Y → no `Upsert` (both agree on Y; we update state to Y but skip the provider write).
  - `TestReconcileOnce_StateStale_ResolverSays_DifferentIP` — state says IP=X, provider says IP=X, resolver says IP=Y → `Upsert` fires with Y.
  - `TestReconcileOnce_StateWritten_OnError` — induce resolver no-quorum; state file written with `last_result=error`.

**Acceptance criteria:**
- [ ] All new tests pass.
- [ ] `TestReconcileOnce_StateStale_ProviderSays_DifferentIP` confirms the provider is still the source of truth (guards against the case where someone edits the record via `gcloud` while `ddns` is running).
- [ ] State-file-write failures do not fail the reconcile (tested by injecting a `store.Save` error).

**Out of scope.** Multi-record state (issue 3.6). State file migration.

---

## Issue 3.4: `ddns sync` — one-shot subcommand

**Story points:** 1

**Phase:** 3

**Depends on:** 3.3

**PRD requirement:** P0 `ddns sync`

**Architecture modules touched:** `cmd/ddns`

**Description.** Replace the Phase 1 stub `syncAction` with a real implementation that constructs the same daemon as `run` and calls `ReconcileOnce` exactly once. Exit code mapping per the PRD: 0 on success (noop or updated), 1 on resolver no-quorum, 2 on provider error, 3 on config/auth. Flag `--dry-run` is accepted here but not yet honored; Phase 4 issue 4.1 wires it through.

**Implementation notes:**
- `cmd/ddns/sync.go`:
  ```go
  func syncAction(ctx context.Context, cmd *cli.Command) error {
      cfg, err := config.Load(cmd.String("config"))
      if err != nil {
          return err
      }
      logger := logging.NewLogger(cfg.LogFormat, os.Stdout).With("record", cfg.Records[0].Name)
      provider, err := gcp.New(ctx)
      if err != nil {
          return err
      }
      d := daemon.New(cfg, resolver.New(cfg.Resolver), provider, state.NewStore(cfg.StatePath), logger)
      return d.ReconcileOnce(ctx)
  }
  ```
- Add `--dry-run` flag to the `sync` subcommand in `cmd/ddns/main.go`:
  ```go
  {
      Name:  "sync",
      Usage: "run one reconciliation pass and exit",
      Action: syncAction,
      Flags: []cli.Flag{
          &cli.BoolFlag{Name: "dry-run", Usage: "do not call the provider; log the payload that would be sent"},
      },
  },
  ```
  The flag is read but unused in this issue. Phase 4 issue 4.1 passes it into `ReconcileOnce`.
- Test strategy: reuse the Phase 2 integration-test scaffolding by invoking `./bin/ddns sync` as a subprocess with `DDNS_INTEGRATION_*` env vars set, assert exit code based on resolver/provider outcomes. Leave the detailed error-path tests to the `daemon` package unit tests — the `cmd/ddns` layer is thin enough that a smoke test is sufficient.

**Acceptance criteria:**
- [ ] `./bin/ddns sync --config testdata/valid.yaml` exits 0 against a mocked provider when the record already matches.
- [ ] `./bin/ddns sync --config /does/not/exist` exits 3.
- [ ] `./bin/ddns sync --config testdata/unreachable-resolver.yaml` exits 1.
- [ ] `--dry-run` flag appears in `./bin/ddns sync --help` but does not yet change behavior (verified by a test assertion that an `Upsert` call is still attempted).

**Out of scope.** Honoring `--dry-run` (Phase 4 issue 4.1). Sync across multiple records (issue 3.6 covers the daemon side; `sync` picks that up for free).

---

## Issue 3.5: `ddns status` — print last-known state

**Story points:** 1

**Phase:** 3

**Depends on:** 3.2

**PRD requirement:** P0 `ddns status`

**Architecture modules touched:** `cmd/ddns`

**Description.** Replace the Phase 1 stub `statusAction` with a real implementation that loads all per-record state files under `cfg.StatePath` and prints a human-readable summary by default, or the raw state JSON under `--json`. Read-only against local state; no provider calls. Intended for operators debugging "why does my record still point at the wrong IP?" — the last-observed state and last-error are the first things to inspect.

**Implementation notes:**
- `cmd/ddns/status.go`:
  ```go
  func statusAction(ctx context.Context, cmd *cli.Command) error {
      cfg, err := config.Load(cmd.String("config"))
      if err != nil {
          return err
      }
      store := state.NewStore(cfg.StatePath)
      jsonOut := cmd.Bool("json")
      for _, rec := range cfg.Records {
          st, err := store.Load(rec.Name)
          if errors.Is(err, ddnserr.ErrNotFound) {
              printNoStateYet(rec.Name, jsonOut)
              continue
          }
          if err != nil {
              return err
          }
          printState(st, jsonOut)
      }
      return nil
  }
  ```
- Default (text) output format:
  ```
  record: home.example.com.
    last_observed_ip: 192.0.2.42
    last_checked:    2026-04-19T16:30:02Z (3m12s ago)
    last_updated:    2026-04-19T14:15:44Z (2h17m ago)
    last_result:     updated
  ```
- `--json` output is a single JSON array of `State` objects (one per record), pretty-printed.
- When a record has no state yet, text output is:
  ```
  record: home.example.com.
    (no state recorded yet — ddns run / ddns sync has not completed a tick)
  ```
  and JSON output includes a `state: null` entry.
- `printNoStateYet` and `printState` helpers in the same file.
- Tests in `cmd/ddns/status_test.go`:
  - `TestStatus_SingleRecord_Text` — seed state; run status; assert text contains the IP and the record name.
  - `TestStatus_SingleRecord_JSON` — `--json`; parse output as JSON; assert structure.
  - `TestStatus_MissingState` — no state file; assert the "no state recorded" message.
  - `TestStatus_MultipleRecords` — two seeded records; both appear in the output.

**Acceptance criteria:**
- [ ] `./bin/ddns status --config testdata/valid.yaml` prints a sensible summary.
- [ ] `./bin/ddns status --config testdata/valid.yaml --json | jq .` produces valid JSON.
- [ ] When no state file exists yet, status exits 0 (not an error — first-run case is normal).
- [ ] Tests use a temp directory for `StatePath` so they don't depend on the developer's actual `~/.local/state/ddns`.

**Out of scope.** Fetching the live Cloud DNS record to show drift (status is explicitly local-only). A `--watch` or `-f` mode (not needed for v1).

---

## Issue 3.6: Multi-record support in the daemon loop

**Story points:** 2

**Phase:** 3

**Depends on:** 3.3, 3.4

**PRD requirement:** P0 — config-level promise that `records:` is a list

**Architecture modules touched:** `internal/daemon`

**Description.** `ReconcileOnce` iterates over every `cfg.Records[i]` and reconciles each independently. A failure on one record logs and updates that record's state but does not abort the loop — the other records still get their chance. The returned error is the first error encountered, so `ddns sync` still exits non-zero if any record failed.

**Implementation notes:**
- Extract the single-record body of `ReconcileOnce` into `func (d *Daemon) reconcileRecord(ctx, rec RecordConfig) error`.
- New top-level `ReconcileOnce`:
  ```go
  func (d *Daemon) ReconcileOnce(ctx context.Context) error {
      ip, report, err := d.resolver.Resolve(ctx)
      if err != nil {
          d.log.Info("resolver_no_quorum", "report", report)
          for _, rec := range d.cfg.Records {
              d.persistError(ctx, d.clock(), "", err, rec.Name)
          }
          return err
      }
      d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

      var firstErr error
      for _, rec := range d.cfg.Records {
          recLog := d.log.With("record", rec.Name)
          if rerr := d.reconcileRecord(ctx, ip, rec, recLog); rerr != nil && firstErr == nil {
              firstErr = rerr
          }
      }
      return firstErr
  }
  ```
- The resolver call is factored out of the per-record loop — all records share the same observed IP per tick, which is what we want (a single public IP maps to many hostnames).
- Per-record logger scoping via `d.log.With("record", rec.Name)` means log records carry the record-name attr without the daemon having to repeat it everywhere.
- Tests:
  - `TestReconcileOnce_MultipleRecords_AllSucceed` — two records, both noop.
  - `TestReconcileOnce_MultipleRecords_OneFailsOthersContinue` — inject `Upsert` failure for record A; verify record B still reconciles; verify `ReconcileOnce` returns record A's error.
  - `TestReconcileOnce_MultipleRecords_ResolverFailFailsAll` — resolver `ErrNoQuorum`; verify both records get state writes with `last_result=error`.

**Acceptance criteria:**
- [ ] All three new tests pass.
- [ ] `./bin/ddns sync` with two records in the config calls `Upsert` on each independently (verified via a counting fake).
- [ ] Failure on one record does not prevent state-file writes for the others.
- [ ] Log output contains the `record` attr on every reconcile-related event.

**Out of scope.** Per-record poll interval (not promised by PRD; all records tick together). Concurrent reconcile (current loop is sequential; Cloud DNS call latencies are sub-second so concurrency is not worth the complexity).
