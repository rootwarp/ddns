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

# Phase 4: Hardening

**Goal.** Operational polish. Everything a real deployment needs that the happy-path reconciler does not strictly require: dry-run, backoff on provider errors, stricter resolver sanity, SIGHUP config reload, finalized log-event taxonomy, help-text pass.

**Entry criteria.** Phase 3 complete — all four subcommands work, state survives restart, multi-record is supported.

**Exit criteria.** `staticcheck` and `golangci-lint` clean; every log event in the taxonomy appears under test coverage; dry-run provably makes zero provider writes; 72-hour soak against the author's home network shows zero wrong-IP writes, zero panics, correct backoff schedule on induced 429s.

---

## Issue 4.1: `--dry-run` honored on `run` and `sync`

**Story points:** 1

**Phase:** 4

**Depends on:** 3.4

**PRD requirement:** P0 `--dry-run`

**Architecture modules touched:** `internal/daemon`, `cmd/ddns`

**Description.** Thread a `dryRun bool` into `Daemon.ReconcileOnce` and `Daemon.Run`. When true, the provider `Upsert` call is replaced by a log line `reconcile_dry_run` carrying the full payload that would have been sent. `--dry-run` is already accepted as a flag on the `sync` subcommand in Phase 3 (but ignored); this issue wires it through end-to-end and also adds it to `run`.

**Implementation notes:**
- Add `DryRun bool` to the `Daemon` struct, or thread it as a parameter — thread it as a parameter, since `run` and `sync` can each set it independently:
  ```go
  func (d *Daemon) ReconcileOnce(ctx context.Context, dryRun bool) error
  func (d *Daemon) Run(ctx context.Context, dryRun bool) error
  ```
- In `reconcileRecord`, before calling `d.provider.Upsert(ctx, ref, desired)`:
  ```go
  if dryRun {
      recLog.Info("reconcile_dry_run",
          "would_send_name", ref.Name,
          "would_send_type", ref.Type,
          "would_send_ttl", desired.TTL,
          "would_send_old", live.Rrdatas,
          "would_send_new", desired.Rrdatas,
      )
      d.persistNoop(ctx, d.clock(), ip.String(), rec.Name)
      return nil
  }
  ```
- `cmd/ddns/sync.go`: `d.ReconcileOnce(ctx, cmd.Bool("dry-run"))`.
- `cmd/ddns/run.go`: add `--dry-run` flag to the `run` subcommand definition in `main.go`, pass through to `d.Run(ctx, cmd.Bool("dry-run"))`.
- Tests:
  - `TestReconcileOnce_DryRun_NoProviderUpsert` — dry-run with resolver IP != live IP; assert `fake.Provider.UpsertCallCount == 0`.
  - `TestReconcileOnce_DryRun_StillWritesStateAsNoop` — dry-run writes state with `last_result=noop` (not "updated" — the record did not in fact update).
  - `TestReconcileOnce_DryRun_LogsWouldSendPayload` — assert the `reconcile_dry_run` event appears with the expected attrs.

**Acceptance criteria:**
- [ ] `./bin/ddns sync --dry-run --config …` reconciles without calling the provider (verified against a counting fake).
- [ ] `./bin/ddns run --dry-run --config …` loops indefinitely without calling the provider.
- [ ] Dry-run state writes use `last_result=noop`, not `updated` — so a subsequent non-dry-run run correctly updates.
- [ ] All three new tests pass.

**Out of scope.** A "diff" output mode that pretty-prints the would-be change (the log line is enough; this is not a GUI tool).

---

## Issue 4.2: Exponential backoff for provider errors

**Story points:** 2

**Phase:** 4

**Depends on:** 3.6

**PRD requirement:** P0 backoff on transient errors

**Architecture modules touched:** `internal/daemon`

**Description.** When `Upsert` or `Get` returns an error that wraps `ddnserr.ErrTransient`, delay the next tick by `min(cfg.PollInterval * 2^(N-1), 30m)` where `N` is the consecutive-failure count. The counter resets to zero on the next fully-successful reconcile (not on a successful resolve — a resolver success with a provider failure still counts as a failure for backoff purposes). Injectable clock for tests.

**Implementation notes:**
- `Daemon` gains a `clock` dependency (already added in issue 3.3) and a `backoff` field:
  ```go
  type backoffState struct {
      consecutiveFailures int
      nextTickNotBefore   time.Time // zero = no backoff active
  }
  ```
- Refactor `Daemon.Run` to consult the backoff state:
  ```go
  for {
      select {
      case <-ctx.Done():
          return nil
      case now := <-d.tickerChan():
          if now.Before(d.backoff.nextTickNotBefore) {
              d.log.Info("backoff_applied", "skip_until", d.backoff.nextTickNotBefore)
              continue
          }
          err := d.ReconcileOnce(ctx, dryRun)
          d.updateBackoff(err)
      }
  }
  ```
- `updateBackoff(err)`:
  ```go
  if err == nil {
      d.backoff.consecutiveFailures = 0
      d.backoff.nextTickNotBefore = time.Time{}
      return
  }
  if !errors.Is(err, ddnserr.ErrTransient) {
      // non-transient errors (ErrAuth, ErrConfig) should have aborted startup;
      // if we see them here, they're unexpected — still back off to avoid tight loop.
  }
  d.backoff.consecutiveFailures++
  delay := d.cfg.PollInterval << (d.backoff.consecutiveFailures - 1)
  if delay > 30*time.Minute {
      delay = 30 * time.Minute
  }
  d.backoff.nextTickNotBefore = d.clock().Add(delay)
  d.log.Info("backoff_scheduled", "consecutive_failures", d.backoff.consecutiveFailures, "delay", delay.String())
  ```
- The `tickerChan()` indirection is there so tests can inject a fake ticker. Alternative: use a `clockwork.Clock` (but avoid the dependency) or a `chan time.Time` that tests drive. Prefer the latter.
- Tests (with a fake clock driven by a channel):
  - `TestBackoff_FirstTransientFailure_SchedulesNextTickAtInterval` — one failure; `nextTickNotBefore = now + PollInterval`.
  - `TestBackoff_ConsecutiveFailures_ExponentialDoubling` — three failures → delays `5m, 10m, 20m` (or `PollInterval*1`, `*2`, `*4`).
  - `TestBackoff_CapAt30Min` — six failures → delay caps at 30m.
  - `TestBackoff_SuccessResetsCounter` — failure, failure, success, failure → last delay is back to `PollInterval * 1`.
  - `TestBackoff_SuccessResetsOnResolveNoQuorum` — resolver fails, then succeeds (no provider error) → backoff resets. (Confirm this is desired behavior: yes — the whole tick succeeded, so we reset; ErrNoQuorum is not a provider transient).
  - Wait — on re-reading the intent: ErrNoQuorum from the resolver is not an `ErrTransient`, so `updateBackoff` treats it as "not transient" and does not apply backoff. But should the counter increment? Per the project plan: "resets on next successful reconcile, not on a successful resolve." So a resolver failure should NOT reset the counter. Update the test accordingly — `ErrNoQuorum` neither increments nor resets.

**Acceptance criteria:**
- [ ] All backoff test cases pass.
- [ ] `TestBackoff_CapAt30Min` confirms 30m ceiling.
- [ ] `TestBackoff_SuccessResetsCounter` confirms counter returns to 0 after one successful reconcile.
- [ ] Log events `backoff_scheduled` and `backoff_applied` appear with the right attrs.

**Out of scope.** Jitter (random fraction added to each backoff delay) — not needed for single-host; revisit if we later find ourselves stampeding API quota.

---

## Issue 4.3: Resolver sanity-reject (private, loopback, CGNAT, link-local, multicast)

**Story points:** 1

**Phase:** 4

**Depends on:** 2.2

**PRD requirement:** P0 (promoted from Open Questions in the PRD — private-range IP rejection is a cheap defense against compromised echo services).

**Architecture modules touched:** `internal/resolver`

**Description.** Before accepting a per-source response, reject any address in: RFC 1918 private (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), loopback (`127.0.0.0/8`), link-local (`169.254.0.0/16`), CGNAT (`100.64.0.0/10`), multicast (`224.0.0.0/4`), or benchmark (`198.18.0.0/15`). Per-response rejection is logged with a `sanitize_reject` reason attribute; the overall quorum arithmetic continues over the remaining non-rejected responses.

**Implementation notes:**
- New helper `internal/resolver/sanitize.go`:
  ```go
  var rejectedPrefixes = []netip.Prefix{
      netip.MustParsePrefix("10.0.0.0/8"),
      netip.MustParsePrefix("172.16.0.0/12"),
      netip.MustParsePrefix("192.168.0.0/16"),
      netip.MustParsePrefix("127.0.0.0/8"),
      netip.MustParsePrefix("169.254.0.0/16"),
      netip.MustParsePrefix("100.64.0.0/10"),
      netip.MustParsePrefix("224.0.0.0/4"),
      netip.MustParsePrefix("198.18.0.0/15"),
  }
  func sanitize(addr netip.Addr) (ok bool, reason string) {
      for _, p := range rejectedPrefixes {
          if p.Contains(addr) {
              return false, "in_" + p.String()
          }
      }
      return true, ""
  }
  ```
- Called in `Resolver.fetchOne` after successful parse, before adding to the tally.
- Per-source `SourceResult.Error` on rejection becomes e.g. `"sanitize_reject:in_192.168.0.0/16"`.
- Tests (extend existing `resolver_test.go`):
  - `TestResolver_Reject_Private_192_168` — one source returns `192.168.1.1`; rejected; other two agree on a valid public IP; quorum 2 still works.
  - `TestResolver_Reject_Loopback_127` — `127.0.0.1`; rejected.
  - `TestResolver_Reject_CGNAT_100_64` — `100.64.0.5`; rejected.
  - `TestResolver_Reject_LinkLocal_169_254` — `169.254.1.1`; rejected.
  - `TestResolver_Reject_Multicast_224` — `224.0.0.1`; rejected.
  - `TestResolver_AllSourcesRejected_NoQuorum` — all three sources return private addresses → `ErrNoQuorum` with all three reported as `sanitize_reject`.

**Acceptance criteria:**
- [ ] All six new test cases pass.
- [ ] The `ResolveReport` carries the `sanitize_reject` reason on the affected sources.
- [ ] Adding a new prefix (e.g., `240.0.0.0/4` Class E future-use) is a one-line change to the `rejectedPrefixes` slice.
- [ ] Coverage for `internal/resolver` remains ≥85%.

**Out of scope.** User-configurable accept/reject prefixes (not needed; the defaults cover every sane home-server case). IPv6 sanity-rejection is not applicable — `ddns` does not support `AAAA` records.

---

## Issue 4.4: `SIGHUP` re-reads config

**Story points:** 2

**Phase:** 4

**Depends on:** 2.7

**PRD requirement:** P0 signal handling

**Architecture modules touched:** `internal/daemon`, `cmd/ddns`

**Description.** While `ddns run` is active, sending `SIGHUP` triggers a config reload. On success, the new config takes effect on the next tick (resolver sources, poll interval, TTLs, record list all replaceable live). On failure, the previous config is retained and `config_reload_failed` is logged with the reason. This lets operators edit the config without restarting the daemon.

**Implementation notes:**
- `cmd/ddns/run.go` installs a second signal handler for SIGHUP alongside the existing SIGINT/SIGTERM:
  ```go
  hupCh := make(chan os.Signal, 1)
  signal.Notify(hupCh, syscall.SIGHUP)
  defer signal.Stop(hupCh)
  ```
- A new goroutine watches `hupCh`:
  ```go
  go func() {
      for range hupCh {
          newCfg, err := config.Load(cfg.Path())
          if err != nil {
              logger.Error("config_reload_failed", "err", err.Error())
              continue
          }
          d.UpdateConfig(newCfg)
          logger.Info("config_reload_ok", "records", len(newCfg.Records))
      }
  }()
  ```
- Add `Daemon.UpdateConfig(newCfg *config.Config)` — takes a mutex, swaps the config, also rebuilds the resolver if the resolver block changed (a bit of work but keeps the `Resolver` internally immutable, which we want):
  ```go
  func (d *Daemon) UpdateConfig(newCfg *config.Config) {
      d.mu.Lock()
      defer d.mu.Unlock()
      d.cfg = newCfg
      d.resolver = resolver.New(newCfg.Resolver)
  }
  ```
- Every read of `d.cfg` / `d.resolver` in the hot path takes `d.mu.RLock()`. Keep the lock scope minimal.
- `config.Load` needs to retain the path it was loaded from so the reload knows where to go. Simplest: store the path on the `*config.Config` itself (unexported field, set by `Load`):
  ```go
  type Config struct {
      // ... existing fields
      path string // unexported, set by Load; persisted across reloads
  }
  func (c *Config) Path() string { return c.path }
  ```
- Tests:
  - `TestSIGHUP_ReloadsPollInterval` — send SIGHUP with a new config that changes `poll_interval`; verify subsequent ticks use the new interval. Requires the fake clock + subprocess infrastructure; consider instead a unit test on `Daemon.UpdateConfig` directly.
  - `TestDaemon_UpdateConfig_SwapsResolverSources` — verify the resolver now queries the new sources.
  - `TestDaemon_UpdateConfig_InvalidConfigKeepsOld` — `Load` of invalid path returns error; daemon config unchanged.

**Acceptance criteria:**
- [ ] `kill -HUP $(pgrep ddns)` on a running daemon with a modified config results in `config_reload_ok` in the logs.
- [ ] The same with a syntactically invalid config yields `config_reload_failed` and the daemon keeps running.
- [ ] Unit tests for `UpdateConfig` pass; manual QA documented in the operations runbook (Phase 5).
- [ ] Concurrent reload + tick exhibits no race (`go test -race ./internal/daemon/...` green).

**Out of scope.** Partial reload (e.g., only records changed, keep resolver) — always do a full swap. Live add/remove of records without SIGHUP (no API surface for that in v1).

---

## Issue 4.5: Log-event taxonomy finalization

**Story points:** 1

**Phase:** 4

**Depends on:** all prior Phase 4 issues

**PRD requirement:** P0 structured logging at every decision point

**Architecture modules touched:** `internal/daemon`, `cmd/ddns`

**Description.** Final pass on log event names and attributes. Canonical list: `startup`, `tick_start`, `ip_resolved`, `resolver_no_quorum`, `reconcile_noop`, `reconcile_updated`, `reconcile_error`, `reconcile_dry_run`, `backoff_applied`, `backoff_scheduled`, `config_reload_ok`, `config_reload_failed`, `shutdown`. Document the event-name → attrs map in `docs/log-events.md`. A test grep asserts every event name appears in at least one test log capture.

**Implementation notes:**
- Walk every `logger.*` call site in `cmd/ddns/` and `internal/daemon/` and normalize to the canonical names above. Any stray event name (e.g., `tick_noop` from Phase 1 that became `reconcile_noop` here) gets renamed.
- New file `docs/log-events.md`:
  ```
  # ddns log events

  | Event | Level | Required attrs | Emitted by |
  |---|---|---|---|
  | startup | INFO | version, record | cmd/ddns/run.go
  | tick_start | INFO | — | internal/daemon/daemon.go
  | ip_resolved | INFO | ip, quorum | internal/daemon/daemon.go
  | resolver_no_quorum | WARN | report | internal/daemon/daemon.go
  | reconcile_noop | INFO | ip, record | internal/daemon/daemon.go
  | reconcile_updated | INFO | old, new, record | internal/daemon/daemon.go
  | reconcile_error | ERROR | err, record | internal/daemon/daemon.go
  | reconcile_dry_run | INFO | would_send_new, would_send_old, record | internal/daemon/daemon.go
  | backoff_applied | INFO | skip_until | internal/daemon/run.go
  | backoff_scheduled | INFO | consecutive_failures, delay | internal/daemon/run.go
  | config_reload_ok | INFO | records | cmd/ddns/run.go
  | config_reload_failed | ERROR | err | cmd/ddns/run.go
  | shutdown | INFO | reason | cmd/ddns/run.go
  ```
- Add `internal/daemon/log_events_test.go` with a table-driven test:
  ```go
  func TestAllLogEventsCovered(t *testing.T) {
      events := []string{"startup", "tick_start", "ip_resolved", /* … */}
      for _, ev := range events {
          t.Run(ev, func(t *testing.T) {
              // Run a test scenario that should emit this event, capture the
              // logger output, assert the event name appears.
          })
      }
  }
  ```
  Practically, this is split across the daemon, resolver, and `cmd/ddns` test files — a single meta-test greps all package-test stdouts after a `go test ./...` run, or the canonical list is verified by an `errcheck`-style custom lint. Simplest: one unit test per event, asserting that event is emitted under some path, collected via a shared `testLogger` helper. Don't over-engineer.

**Acceptance criteria:**
- [ ] `docs/log-events.md` exists and lists every event.
- [ ] Every event name in the table appears in at least one test assertion (grep check or explicit test).
- [ ] No stray event names remain in the codebase — verified by `rg 'Info\("' internal/ cmd/` against the canonical list.
- [ ] Adding a new event requires adding a row to the doc — enforced by review, not by automation (doc-drift is accepted risk for this scale).

**Out of scope.** Log-shipping-specific formatting (e.g., ecs-style). A `--debug` log level (Phase 7 P1).

---

## Issue 4.6: Help-text pass

**Story points:** 1

**Phase:** 4

**Depends on:** all prior Phase 4 issues

**PRD requirement:** P0 usability

**Architecture modules touched:** `cmd/ddns`

**Description.** Audit every `urfave/cli` flag and subcommand for its `Usage:` string. Every flag's usage must make sense in isolation (i.e., readable from `./bin/ddns run --help` without having seen the other subcommands). Subcommand `Usage:` must answer "what does this do in one line?" Also add `Description:` fields for the `run` and `sync` subcommands covering the multi-line context (when to use one vs the other, long-running container vs one-shot cron invocation). This is a human-review issue; no automated assertion.

**Implementation notes:**
- Read the full `urfave/cli` help output at each level:
  - `./bin/ddns --help`
  - `./bin/ddns run --help`
  - `./bin/ddns sync --help`
  - `./bin/ddns status --help`
  - `./bin/ddns version --help`
- Rewrite any usage string that's less than crisp.
- Add `Description` fields where a single line isn't enough:
  ```go
  {
      Name:  "run",
      Usage: "run the reconciliation daemon",
      Description: `Run ddns as a long-lived daemon. Polls configured HTTPS echo services every poll_interval, compares the resolved IP against local state and the live Cloud DNS record, and issues an update when they differ. Handles SIGHUP for config reload and SIGTERM/SIGINT for clean shutdown. For cron-style one-shot execution, use "ddns sync" instead.`,
      Action: runAction,
      Flags: runFlags,
  },
  ```
- Add top-level examples via `cli.Command.CustomAppHelpTemplate` only if needed; prefer to keep help output close to urfave's default.
- Verify the `--help` of the root command lists all four subcommands in the expected order.

**Acceptance criteria:**
- [ ] Manual review: every flag's usage string is a readable one-liner.
- [ ] `run` and `sync` have `Description` fields that distinguish their use cases.
- [ ] `./bin/ddns --help | wc -l` is bounded (not so verbose it's useless) — target <40 lines.
- [ ] `./bin/ddns run --help` explicitly mentions config-file-required.
- [ ] A new operator reading `./bin/ddns --help` for the first time can pick between `run` and `sync` without asking.

**Out of scope.** Shell completions (Phase 7). Man pages (not shipping).
