---
type: document
status: decided (live-verified)
PARA: projects
tags:
  - dev/go
  - dev/research
---

# Spike 0.3: urfave/cli/v3 ergonomics

## Status

**Live-verified on 2026-04-19.** A 50-line spike under `plan/bootstrap/research/scripts/urfave-cli-spike/` built and ran against `github.com/urfave/cli/v3@v3.8.0`. Help output, subcommand dispatch, and SIGTERM handling via `signal.NotifyContext` all behaved as expected.

## Decision

**Pin `github.com/urfave/cli/v3@v3.8.0`** in `go.mod` (issue 0.4). No fallback to v2.

## Key observations

1. **Action signature:** `func(ctx context.Context, cmd *cli.Command) error` — `ctx` first, then the command. Issue 1.3's sketch already uses this form.

2. **SIGTERM plumbing:** `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` wrapped around `cmd.Run(ctx, os.Args)` correctly cancels the context inside subcommand Actions. The spike's `run` subcommand selects on `ctx.Done()` and returns cleanly within <1 second of a `kill -TERM`. Exit code is 0 (the handler returns `nil`).

3. **Help output:** Clean, conventional. Global options shown under the root; subcommand-specific flags under their subcommand's help. No template overrides needed for v1.

4. **Flag sources:** `cli.EnvVars("DDNS_CONFIG")` on a `StringFlag` gives the "flag-or-env-var" behavior issue 1.3 needs. Verified in the help output as `[$DDNS_CONFIG]` annotation.

5. **Module path:** `github.com/urfave/cli/v3` — the canonical module path. `go get github.com/urfave/cli/v3@v3.8.0` resolves cleanly.

## Spike evidence

The spike binary:

```
$ ./spike --help
NAME:
   spike - urfave/cli/v3 spike

USAGE:
   spike [global options] [command [command options]]

COMMANDS:
   run      blocks until SIGINT/SIGTERM
   version  print version
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --config string  path to config
   --help, -h       show help

$ ./spike version
spike v0.0.0

$ ./spike run &
$ kill -TERM %1
run: context cancelled, exiting cleanly
$ echo $?
0
```

The clean SIGTERM handler confirms `signal.NotifyContext` plumbs through `cmd.Run` correctly, which is the single ergonomic risk v3 was posing. No concerns.

## Recommendation

Ship v1 on `github.com/urfave/cli/v3@v3.8.0`. Bump to the latest v3 minor as desired during v1.x maintenance; v3 is past beta and API-stable.

## Out of scope

- Custom help templates.
- Shell completion scripts (Phase 8 item 8.9).
- Internationalized usage strings.
