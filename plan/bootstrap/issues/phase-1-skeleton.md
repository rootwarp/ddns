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

# Phase 1: Walking-Skeleton Binary

**Goal.** Ship a `ddns` binary that builds, installs via `go install`, runs, responds to `--help`, prints version, and enters a no-op daemon loop under `run`. All CLI scaffolding is owned by `urfave/cli/v3`. After Phase 1, any new subcommand or flag is a struct-literal addition, not a rewrite.

**Entry criteria.** Phase 0 complete — module scaffold, CI green, Makefile in place, urfave/cli v3 version pinned in `go.mod`.

**Exit criteria.** `go install github.com/rootwarp/ddns/cmd/ddns@develop` on a clean machine produces a working binary; `ddns version` and `ddns --help` render correctly; `ddns run` loops and logs `tick_noop` entries until Ctrl-C; CI smoke test (new in 1.6) green.

---

## Issue 1.1: `internal/version` — build-time metadata constants

**Story points:** 1

**Phase:** 1

**Depends on:** 0.5 (Makefile LDFLAGS already reference these symbols)

**PRD requirement:** P0 `ddns version` subcommand

**Architecture modules touched:** `internal/version`

**Description.** Populate `internal/version` with three exported `string` variables — `Version`, `Commit`, `BuildDate` — that the Makefile's `-ldflags "-X"` injects at build time. Defaults for go-run / `go install`-without-ldflags builds are `"dev"`, `"unknown"`, `"unknown"`. This is the first issue that produces user-visible output from the binary.

**Implementation notes:**
- `internal/version/version.go`:
  ```go
  package version

  var (
      Version   = "dev"
      Commit    = "unknown"
      BuildDate = "unknown"
  )

  // String returns a human-readable one-liner for `ddns version`.
  func String() string {
      return fmt.Sprintf("ddns %s (commit %s, built %s)", Version, Commit, BuildDate)
  }
  ```
- Do NOT use `const` — ldflags-injection only works against package-level `var`.
- Unit test in `internal/version/version_test.go`:
  - `TestStringDefaults` asserts `String()` contains "dev" when no ldflags override.
  - `TestStringWithInjection` uses `t.Setenv` or an init hook — actually, ldflags injection can't be tested in-process; instead, the test asserts that mutating `Version` in the test temporarily produces the expected string, proving the format. (Tests revert the mutation in `t.Cleanup`.)

**Acceptance criteria:**
- [ ] `internal/version/version.go` exports `Version`, `Commit`, `BuildDate` as `var`.
- [ ] `String()` returns a format matching `"ddns <version> (commit <short-sha>, built <iso-8601>)"`.
- [ ] `make build` produces a binary where `strings bin/ddns | rg 'ddns v\d'` finds the injected version string (confirming ldflags took effect).
- [ ] `go run ./cmd/ddns` (no Makefile) shows `"dev"` — confirming the defaults work.

**Out of scope.** The `ddns version` subcommand itself (issue 1.3). Any version-comparison logic.

---

## Issue 1.2: `internal/logging` — minimal slog setup

**Story points:** 2

**Phase:** 1

**Depends on:** 0.4

**PRD requirement:** P0 structured logging

**Architecture modules touched:** `internal/logging`

**Description.** Provide `NewLogger(format string, w io.Writer) *slog.Logger` with three format modes: `"text"` (forces `slog.NewTextHandler`), `"json"` (forces `slog.NewJSONHandler`), `"auto"` (picks text on a TTY, JSON otherwise). Default attrs attached to the returned logger: `version` (from `internal/version.Version`), `pid` (from `os.Getpid()`). Level is hard-coded to `INFO` for v1; a `--debug` flag is Phase 7 P1.

**Implementation notes:**
- `internal/logging/logger.go`:
  ```go
  package logging

  import (
      "io"
      "log/slog"
      "os"

      "golang.org/x/term"
      "github.com/rootwarp/ddns/internal/version"
  )

  func NewLogger(format string, w io.Writer) *slog.Logger {
      var handler slog.Handler
      switch resolveFormat(format, w) {
      case "json":
          handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
      default:
          handler = slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
      }
      return slog.New(handler).With("version", version.Version, "pid", os.Getpid())
  }

  func resolveFormat(format string, w io.Writer) string {
      if format == "text" || format == "json" {
          return format
      }
      if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
          return "text"
      }
      return "json"
  }
  ```
- The `w io.Writer` parameter makes the logger testable without redirecting `os.Stdout`.
- `TestNewLogger_Text`: write to a `bytes.Buffer`, assert output matches `key=value` shape.
- `TestNewLogger_JSON`: write to a `bytes.Buffer`, parse as JSON, assert `version` and `pid` attrs are present.
- `TestNewLogger_AutoNonTTY`: pass a `bytes.Buffer` (not a file), confirm JSON handler selected.
- The `auto` TTY branch is not unit-tested directly because faking a TTY `*os.File` is fragile; rely on manual verification via `./bin/ddns run` in a terminal vs piped.

**Acceptance criteria:**
- [ ] `NewLogger("text", buf)` emits `key=value` lines.
- [ ] `NewLogger("json", buf)` emits line-delimited JSON with `version` and `pid` attrs.
- [ ] `NewLogger("auto", buf)` where `buf` is a `*bytes.Buffer` (non-TTY) selects JSON.
- [ ] All unit tests pass on both `ubuntu-latest` and `macos-latest` CI jobs.

**Out of scope.** `--debug` level support (Phase 7). A panic hook that redacts (no secrets in v1 logs — the only sensitive-ish field is the IP itself, and we log that deliberately).

---

## Issue 1.3: `cmd/ddns` — urfave/cli root + `version` subcommand

**Story points:** 2

**Phase:** 1

**Depends on:** 0.3 (version pin), 1.1, 1.2

**PRD requirement:** P0 `ddns --help`, `ddns version`

**Architecture modules touched:** `cmd/ddns`

**Description.** Wire the root `cli.Command` struct literal from the architecture doc into `cmd/ddns/main.go`. Define all four subcommands (`run`, `sync`, `status`, `version`) but only implement `version`; the others return a `cli.Exit("not yet implemented", 1)` stub. This is the first commit where `./bin/ddns version` prints real output and `./bin/ddns --help` shows the full command tree.

**Implementation notes:**
- File layout under `cmd/ddns/`:
  - `main.go` — the `cli.Command` struct literal, `signal.NotifyContext` wrapping, `cmd.Run` call, `exitCodeFor` wiring.
  - `version.go` — `versionAction(ctx context.Context, cmd *cli.Command) error`, prints `version.String()` to stdout.
  - `run.go`, `sync.go`, `status.go` — stubs returning `cli.Exit("not yet implemented", 1)`.
- `main.go` skeleton:
  ```go
  package main

  import (
      "context"
      "fmt"
      "os"
      "syscall"

      "github.com/urfave/cli/v3"
      "os/signal"
  )

  func main() {
      cmd := &cli.Command{
          Name:  "ddns",
          Usage: "Dynamic DNS updater for Google Cloud DNS",
          Flags: []cli.Flag{
              &cli.StringFlag{
                  Name:    "config",
                  Usage:   "path to config file",
                  Sources: cli.EnvVars("DDNS_CONFIG"),
              },
              &cli.StringFlag{
                  Name:  "log-format",
                  Value: "auto",
                  Usage: "auto|text|json",
              },
          },
          Commands: []*cli.Command{
              {Name: "run", Usage: "run the reconciliation daemon", Action: runAction},
              {Name: "sync", Usage: "run one reconciliation pass and exit", Action: syncAction},
              {Name: "status", Usage: "print last-known state", Action: statusAction},
              {Name: "version", Usage: "print version information", Action: versionAction},
          },
      }
      ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
      defer stop()
      if err := cmd.Run(ctx, os.Args); err != nil {
          fmt.Fprintln(os.Stderr, "ddns:", err)
          os.Exit(exitCodeFor(err))
      }
  }
  ```
- `version.go`:
  ```go
  func versionAction(ctx context.Context, cmd *cli.Command) error {
      fmt.Println(version.String())
      return nil
  }
  ```
- `exitCodeFor` lives in `main.go` as a small helper; it's a placeholder that returns `1` for everything in this issue; issue 1.5 fills in the real mapping.
- If urfave/cli v3 uses a slightly different action signature (e.g., `func(*cli.Command, context.Context) error`), adjust. The spike in issue 0.3 records the exact signature — match it.

**Acceptance criteria:**
- [ ] `./bin/ddns --help` prints usage with all four subcommands listed.
- [ ] `./bin/ddns version` prints a line like `ddns v0.1.0 (commit abc1234, built 2026-04-19T...)`.
- [ ] `./bin/ddns run`, `./bin/ddns sync`, `./bin/ddns status` each exit non-zero with the stub message.
- [ ] `./bin/ddns --config /tmp/missing.yaml version` succeeds — `version` does not require a config file (validated by the handler not reading the flag).
- [ ] `DDNS_CONFIG=/tmp/x.yaml ./bin/ddns version` also succeeds — env-var source is wired but not required.

**Out of scope.** `run`/`sync`/`status` implementations (issues 1.4, 3.4, 3.5). Real exit-code mapping (issue 1.5).

---

## Issue 1.4: `ddns run` — no-op daemon loop

**Story points:** 2

**Phase:** 1

**Depends on:** 1.2, 1.3

**PRD requirement:** P0 `ddns run` (placeholder interior)

**Architecture modules touched:** `cmd/ddns`, `internal/logging`

**Description.** Replace the `runAction` stub with a real no-op loop: build a `slog.Logger` from the root command's `--log-format` flag, log `startup`, start a `time.Ticker` at a hard-coded 30-second interval, log `tick_noop` per tick, and exit cleanly on root-context cancellation (SIGINT / SIGTERM). No config file reading, no resolver, no provider — those are Phase 2.

**Implementation notes:**
- `cmd/ddns/run.go`:
  ```go
  func runAction(ctx context.Context, cmd *cli.Command) error {
      logger := logging.NewLogger(cmd.String("log-format"), os.Stdout)
      logger.Info("startup", "interval", "30s", "mode", "no-op")
      ticker := time.NewTicker(30 * time.Second)
      defer ticker.Stop()
      for {
          select {
          case <-ctx.Done():
              logger.Info("shutdown", "reason", ctx.Err().Error())
              return nil
          case t := <-ticker.C:
              logger.Info("tick_noop", "at", t.Format(time.RFC3339))
          }
      }
  }
  ```
- The initial tick fires after 30s in this no-op version; a firing-immediately variant (`firstTickNow`) is deferred to issue 2.7 when the real reconciler wants to reconcile on startup.
- Tests against `runAction` are fragile because they require driving the real `time.Ticker`; instead, add a small integration test in `cmd/ddns/run_smoke_test.go` that:
  - Builds the binary with `go build -o bin/ddns-test ./cmd/ddns`.
  - `exec.Command("./bin/ddns-test", "run")` with `Cancel: func() error { return p.Signal(syscall.SIGTERM) }`.
  - Waits 1 second, sends SIGTERM, asserts the process exits within 2 seconds with exit code 0.
  - Asserts stderr/stdout contain the string `"startup"`.
- The smoke test is `//go:build integration`-tagged or guarded by `testing.Short()` so `go test ./...` on every developer machine is fast.

**Acceptance criteria:**
- [ ] `./bin/ddns run` emits a `startup` log record within 100ms.
- [ ] Pressing Ctrl-C causes `./bin/ddns run` to exit within 2 seconds with exit 0, emitting a `shutdown` log record.
- [ ] Leaving `./bin/ddns run` alone for 35 seconds produces at least one `tick_noop` log record.
- [ ] `./bin/ddns --log-format=json run` emits line-delimited JSON records.
- [ ] The smoke test passes in CI (verified by issue 1.6).

**Out of scope.** Config loading (issue 2.1). Real reconcile (issue 2.6). Backoff, SIGHUP (Phase 4).

---

## Issue 1.5: `exitCodeFor(err)` — error-to-exit-code mapping

**Story points:** 1

**Phase:** 1

**Depends on:** 1.3

**PRD requirement:** P0 exit-code discipline (partial — Phase 3 completes the table)

**Architecture modules touched:** `cmd/ddns`

**Description.** Implement the `exitCodeFor(err)` helper in `cmd/ddns/main.go` per the architecture doc's error taxonomy. This issue introduces the sentinel-error plumbing even though most of the sentinel types don't yet have callers — wiring the mapping now means Phase 2, 3, and 4 issues add cases, not redesign the structure.

**Implementation notes:**
- Sentinel errors live in a new package `internal/ddnserr/errors.go`:
  ```go
  package ddnserr

  import "errors"

  var (
      ErrNoQuorum  = errors.New("resolver: no quorum among echo services")
      ErrNotFound  = errors.New("provider: record not found")
      ErrTransient = errors.New("provider: transient error, will retry")
      ErrConfig    = errors.New("config: validation failed")
      ErrAuth      = errors.New("provider: authentication failed")
  )
  ```
  (Package name `ddnserr` avoids collision with the stdlib `errors`.)
- `cmd/ddns/main.go`:
  ```go
  func exitCodeFor(err error) int {
      switch {
      case err == nil:
          return 0
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
  ```
- Unit tests in `cmd/ddns/exit_code_test.go` cover each case and an `fmt.Errorf("wrap: %w", ddnserr.ErrAuth)` wrapping case.
- `urfave/cli/v3` has its own `cli.Exit(msg, code)` type; `exitCodeFor` must also check `cli.ExitCoder`:
  ```go
  var exitCoder cli.ExitCoder
  if errors.As(err, &exitCoder) {
      return exitCoder.ExitCode()
  }
  ```
  so stub handlers returning `cli.Exit("not yet implemented", 1)` still produce exit 1.

**Acceptance criteria:**
- [ ] `exitCodeFor(nil) == 0`.
- [ ] `exitCodeFor(ddnserr.ErrNoQuorum) == 1`.
- [ ] `exitCodeFor(fmt.Errorf("wrapped: %w", ddnserr.ErrAuth)) == 3`.
- [ ] `exitCodeFor(cli.Exit("x", 7)) == 7`.
- [ ] `exitCodeFor(errors.New("random")) == 2`.
- [ ] All cases covered by unit tests.

**Out of scope.** Full error taxonomy (Phase 4). Per-subcommand exit-code tests (issue 3.4 covers `sync`).

---

## Issue 1.6: CI smoke test — binary actually runs

**Story points:** 1

**Phase:** 1

**Depends on:** 1.3, 1.4, 1.5

**PRD requirement:** infrastructure (enforces the walking-skeleton invariant from project-plan §Phase Dependency Graph)

**Architecture modules touched:** none

**Description.** Extend `.github/workflows/ci.yml` with a post-`make build` step that actually runs the binary. The pre-existing CI jobs confirm `make lint` and `make test` pass; this step catches regressions where the binary compiles but fails to launch (for example: unresolvable dependency at runtime, panicking init, flag parsing broken). Catches real bugs that unit tests miss.

**Implementation notes:**
- New `smoke` step after `make build` in each CI job:
  ```yaml
  - name: Smoke — version
    run: ./bin/ddns version
  - name: Smoke — help
    run: ./bin/ddns --help
  - name: Smoke — run (5s timeout)
    run: timeout --preserve-status --signal=TERM 5 ./bin/ddns run
  ```
  The `timeout` command on macOS needs `coreutils` (via `brew install coreutils` → `gtimeout`) OR we can use a shell-level equivalent:
  ```yaml
  - name: Smoke — run
    run: |
      ./bin/ddns run &
      PID=$!
      sleep 2
      kill -TERM $PID
      wait $PID
      STATUS=$?
      if [ $STATUS -ne 0 ] && [ $STATUS -ne 143 ]; then
        # 143 = 128 + 15 (SIGTERM); some shells report it, our handler should return 0.
        echo "Unexpected exit: $STATUS"
        exit 1
      fi
  ```
  Prefer the shell variant — it's portable across Ubuntu and macOS runners without extra deps.
- The `run` smoke test asserts the process:
  - starts and does not panic,
  - responds to SIGTERM within 2s,
  - exits with 0 (handler-induced clean shutdown) or 143 (external SIGTERM before handler registered — acceptable for a 2-second run).

**Acceptance criteria:**
- [ ] Both CI jobs (`ubuntu-latest`, `macos-latest`) include the three smoke steps.
- [ ] Intentionally breaking `main.go` (e.g., adding `panic("boom")` on startup) fails the smoke step.
- [ ] Intentionally breaking signal handling (e.g., removing `signal.NotifyContext`) causes the smoke step to time out and fail — proving the test actually exercises the shutdown path.
- [ ] The smoke step adds <10 seconds to total CI wall time.

**Out of scope.** Integration tests against Cloud DNS (Phase 2 issue 2.8). Cross-compile smoke tests (Phase 6 GoReleaser).
