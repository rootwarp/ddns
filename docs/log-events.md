# ddns log events

Canonical list of structured log events emitted by ddns. Each row is the
contract: event name, level, required attrs, and the source file that
emits it. Adding a new event requires adding a row here — doc-drift is
enforced by review, not by automation (see Phase 4 issue 4.5 acceptance
criterion 4).

Every line is a `slog` record. Format (text vs JSON) is chosen by the
`log_format` config / `--log-format` flag; attribute keys are stable
across formats.

## Canonical event taxonomy (v1)

| Event                   | Level | Required attrs                                              | Emitted by                       |
| ----------------------- | ----- | ----------------------------------------------------------- | -------------------------------- |
| `startup`               | INFO  | `poll_interval`, `config`                                   | `cmd/ddns/run.go`                |
| `tick_start`            | INFO  | —                                                           | `internal/daemon/daemon.go`      |
| `ip_resolved`           | INFO  | `ip`, `quorum`                                              | `internal/daemon/daemon.go`      |
| `resolver_no_quorum`    | INFO  | `report`, `err`                                             | `internal/daemon/daemon.go`      |
| `reconcile_noop`        | INFO  | `ip`, `record`                                              | `internal/daemon/daemon.go`      |
| `reconcile_updated`     | INFO  | `old`, `new`, `ttl`, `record`                               | `internal/daemon/daemon.go`      |
| `reconcile_error`       | ERROR | `stage`, `err`, `record`                                    | `internal/daemon/daemon.go`      |
| `reconcile_dry_run`     | INFO  | `would_send_name`, `would_send_type`, `would_send_ttl`, `would_send_old`, `would_send_new`, `record` | `internal/daemon/daemon.go` |
| `dry_run_mode_active`   | INFO  | —                                                           | `cmd/ddns/run.go`, `cmd/ddns/sync.go`, `internal/daemon/run.go` |
| `backoff_applied`       | INFO  | `skip_until`, `wait`                                        | `internal/daemon/run.go`         |
| `backoff_scheduled`     | INFO  | `consecutive_failures`, `delay`                             | `internal/daemon/daemon.go`      |
| `fatal_ceiling_reached` | ERROR | `consecutive_fatal_failures`, `err`                         | `internal/daemon/daemon.go`      |
| `config_reload_ok`      | INFO  | `records`                                                   | `cmd/ddns/run.go`                |
| `config_reload_failed`  | ERROR | `err`                                                       | `cmd/ddns/run.go`                |
| `state_load_error`      | WARN  | `err`, `record`                                             | `internal/daemon/daemon.go`      |
| `state_save_error`      | WARN  | `err`, `record`                                             | `internal/daemon/daemon.go`      |
| `shutdown`              | INFO  | `reason`                                                    | `internal/daemon/run.go`         |

## Notes

- Every record also carries the two default attrs set by
  `internal/logging.NewLogger`: `version` (from `internal/version`) and
  `pid`. They are not listed per-row because they are universal.
- The `record` attr on per-record events (`reconcile_*`, `state_*`) is
  attached via `recLog := d.log.With("record", rec.Name)` inside the
  daemon fan-out, so every downstream event is greppable by record name.
- `dry_run_mode_active` is emitted exactly once per invocation when
  `--dry-run` is active: once in `cmd/ddns/run.go` or `cmd/ddns/sync.go`
  at CLI-wiring time, and once inside `Daemon.Run` for safety in case
  callers build a daemon without going through the CLI.
- `backoff_applied` is emitted at the top of each tick iteration ONLY
  when a backoff window is currently extending the wait beyond
  `cfg.PollInterval`. It is not emitted every iteration; a regular tick
  is silent at that point.
- `tick_start` is emitted at the top of `ReconcileOnce` before the
  resolver call. It bounds each tick's log block so operators can correlate
  resolver / reconcile / state-persist events by position.

## Exit-code mapping (reference)

| Condition                           | Event(s)                             | Exit code |
| ----------------------------------- | ------------------------------------ | --------- |
| Success (including noop)            | `reconcile_noop` or `reconcile_updated` | 0       |
| Resolver could not reach quorum     | `resolver_no_quorum`                 | 1         |
| Provider transient error            | `reconcile_error`                    | 2         |
| Config / auth (fatal) error         | `reconcile_error`, `fatal_ceiling_reached` | 3   |

Exit codes only apply to `ddns sync` (one-shot); `ddns run` exits 0 on
clean shutdown (`shutdown` event) or the same non-zero code when a fatal
error propagates.
