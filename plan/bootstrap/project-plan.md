---
type: document
status: draft
tags:
  - dev/go
  - infra/dns
  - dev/planning
PARA: projects
---

**Connections:**
- [[prd]]
- [[architecture]]
- [[README]]

# Project Plan: `ddns`

## Summary

`ddns` is delivered **walking-skeleton first**: every phase produces a binary that builds, installs, runs, and does progressively more useful work. The first shippable artifact (Phase 1) is a `ddns` executable that parses flags via `urfave/cli/v3`, prints version info, and enters a no-op daemon loop that logs one tick per interval. Subsequent phases replace the no-op interior with real resolver, real Google Cloud DNS provider, real state, and real hardening — in that order. This sequencing guarantees that at any point after Phase 1, the user can do `go install` (or pull a container) and at least have something they can run while the next phase lands.

Phase 0 stands up the Go module, CI, and closes the two remaining research items. Phase 1 ships the **walking-skeleton binary** — `ddns run` / `ddns version` compile and execute, with `urfave/cli/v3` owning flag parsing and subcommand dispatch, but no real reconciliation inside the tick. Phase 2 replaces the no-op tick with a real HTTPS-echo resolver and a real Google Cloud DNS `Changes.Create` — the first version that actually updates a zone, end-to-end, single record. Phase 3 adds state persistence, live-record double-check, `ddns sync`, and `ddns status`. Phase 4 is hardening — dry-run, exponential backoff, sanity-checks on observed IP, `SIGHUP` reload, signal handling polish. Phase 5 writes docs and the Docker deployment shape. Phase 6 releases v1 via GoReleaser.

Default execution model is single-stream, single code-writer — this is a small project and the parallelism available is not worth the coordination cost.

**Shippable-binary milestones:**

| Phase | Binary behavior | User-visible value |
|---|---|---|
| 1 | `ddns version` works; `ddns run` loops and logs no-op ticks | None — walking skeleton, but it runs |
| 2 | `ddns run` actually updates the Cloud DNS record when the IP changes | v0.2 — useful to early adopters |
| 3 | `ddns sync` / `ddns status` work; state survives restart | v0.3 — usable under cron or a container |
| 4 | Dry-run, backoff, SIGHUP, sanity checks, structured logs | v0.9 — release candidate |
| 5 | Docker deployment shape + docs | v1.0-rc |
| 6 | Tagged v1.0.0 | v1.0 |

## Prerequisites

Everything below must be true before Phase 0 begins. These are non-negotiable inputs, not open decisions.

- A development machine with Go 1.22+ stable toolchain.
- A Google Cloud project the author controls, with the Cloud DNS API enabled, one managed zone, and an unused subdomain inside that zone reserved for integration testing.
- A service account with `roles/dns.admin` on the project, JSON key downloaded to `~/.config/gcloud/ddns-integration.json`, path exported via `GOOGLE_APPLICATION_CREDENTIALS` when running integration tests.
- Outbound HTTPS connectivity from the dev machine to `*.ipify.org`, `ifconfig.me`, `icanhazip.com`, `dns.googleapis.com`.
- Decisions already settled by the PRD: Go as the language, Cloud DNS as the only v1 provider, HTTPS echo as the only v1 IP detection method, long-running daemon as the default runtime, state file location under XDG.

## Phase Dependency Graph

```
Phase 0  →  Phase 1  →  Phase 2  →  Phase 3  →  Phase 4  →  Phase 5  →  Phase 6
 scaffold   skeleton    real loop   state+CLI   hardening   docs+deploy  release
                                                                           ↓
                                                                        Phase 7 (v1.1)
```

No latent parallelism worth extracting. Phases are sequential by default. After each phase, `go install github.com/rootwarp/ddns/cmd/ddns@develop` must yield a binary that runs without panicking — this is the invariant that keeps the walking-skeleton discipline honest.

## Phase 0 — Spikes and Scaffold

**Goal.** Answer the remaining implementation-blocking questions and stand up the module skeleton. No binary yet — only `go build ./...` is required to succeed.

### Spikes

- **S0.1 — Cloud DNS write semantics.** Confirm via the Cloud DNS API documentation and a live smoke test: does `Changes.Create` with a delete-then-add pair require both to have identical `name + type`, and does it return the completed `Change` synchronously or require polling `Changes.Get`? Expected answer: synchronous completion for small changes, but the SDK returns a `Change` resource whose status may be `pending`; code must read the returned status rather than assuming `done`. Output: a note under `plan/bootstrap/research/01-cloud-dns-changes-semantics.md`.
- **S0.2 — IP-echo service availability.** Run a 7-day cron against ipify, ifconfig.me, and icanhazip from the author's home network; log HTTP status and response-parse outcome per-hour. Goal is to validate that the 2-of-3 quorum default is sufficient in practice. Output: summary under `plan/bootstrap/research/02-ip-echo-availability.md`.
- **S0.3 — urfave/cli v3 vs v2 ergonomics.** Confirm v3 is stable enough for shipping and that its `cli.Command.Run(ctx, args)` signature works end-to-end with `signal.NotifyContext`. Output: a one-paragraph note under `plan/bootstrap/research/03-urfave-cli-v3.md`.

### Scaffold deliverables

- `go.mod` at `github.com/rootwarp/ddns`, Go 1.22, with `github.com/urfave/cli/v3` and `gopkg.in/yaml.v3` as declared deps from day one.
- Empty package stubs for every package listed in the architecture doc. Each stub compiles.
- `.github/workflows/ci.yml` running `go build ./...`, `go vet`, `go test ./...`, `staticcheck`, and `golangci-lint` on every push and PR into `develop`.
- `Makefile` targets: `build` (produces `bin/ddns`), `test`, `lint`, `install` (runs `go install ./cmd/ddns`), `integration-test` (gated on `DDNS_INTEGRATION_PROJECT`).
- `develop` branch created and set as default.

**Phase 0 exit criteria.** Three research notes land in `plan/bootstrap/research/`; `make build` produces a `bin/ddns` that links successfully (it can be empty-`main`-prints-nothing at this stage); CI is green.

## Phase 1 — Walking-Skeleton Binary

**Goal.** Ship a `ddns` binary that builds, installs via `go install`, runs, responds to `--help`, prints version, and enters a no-op daemon loop under `run`. All CLI scaffolding is owned by `urfave/cli/v3` — any future subcommand or flag adds a struct literal, not a rewrite. This is the first phase where a user can legitimately do `go install` and have something that executes.

The interior of the tick is a placeholder that logs `tick_noop` and advances the clock. No resolver, no provider, no state — those arrive in Phase 2 and later.

### Issues

- **1.1 — `internal/version`: build-time metadata.** Exported `Version`, `Commit`, `BuildDate` strings populated via `-ldflags "-X"`. `Makefile` `build` target passes the right flags. Default values (`"dev"`, `"unknown"`, `"unknown"`) are used when building without ldflags.
- **1.2 — `internal/logging`: minimal slog setup.** `NewLogger(format string, w io.Writer) *slog.Logger` with `auto`/`text`/`json`. TTY detection via `golang.org/x/term.IsTerminal`. Default attrs: `version`, `pid`. No record-name attr yet (no records exist to log against).
- **1.3 — `cmd/ddns`: urfave/cli root + `version` subcommand.** The full `cli.Command` literal from the architecture doc, wired into `main`. `ddns version` prints `version / commit / build_date`. `ddns --help` shows all four subcommands (the other three return a stubbed "not yet implemented" error in this issue). This is the first commit where `make build && ./bin/ddns version` works.
- **1.4 — `cmd/ddns run`: no-op daemon loop.** `runAction` opens a `time.Ticker` at a hard-coded 30-second interval, logs `startup`, logs `tick_noop` per tick, and cleanly shuts down on `SIGINT`/`SIGTERM` via the root context. No config file is read yet; no reconciliation happens.
- **1.5 — `exitCodeFor(err)` mapping.** The small error-to-exit-code helper described in the architecture doc. Wired into `main`. Unit test against sentinel errors.
- **1.6 — Smoke test in CI.** A CI job that runs `./bin/ddns version`, `./bin/ddns --help`, and `timeout 5 ./bin/ddns run` (expecting clean timeout-induced exit) on each push. Catches regressions where the binary fails to launch even though `go test` passes.

**Phase 1 exit criteria.** `go install github.com/rootwarp/ddns/cmd/ddns@develop` produces a working binary on a clean machine; `ddns version` and `ddns --help` render correctly; `ddns run` loops until Ctrl-C without panicking; CI smoke test is green.

## Phase 2 — Real Resolver + Real Google Cloud DNS Update

**Goal.** Replace the no-op tick interior with real logic: resolve the public IP from HTTPS echo services, fetch the Cloud DNS record, and write an update if they differ. By end of phase, `ddns run` against a real managed zone actually keeps the record in sync with the host's public IP. State persistence and the `sync`/`status` subcommands land in Phase 3 — this phase stays narrow to keep the integration test honest.

### Issues

- **2.1 — `internal/config`: minimal YAML load.** Struct definitions, YAML unmarshal via `gopkg.in/yaml.v3`, XDG default path resolution, the subset of validation needed to reject obvious misconfiguration (empty records list, empty project/zone/name). No hot reload yet. The `--config` flag on the root `cli.Command` becomes functional in this issue.
- **2.2 — `internal/resolver`: HTTPS echo + quorum.** Parallel `http.Get` per configured source, per-response parse via `netip.ParseAddr`, quorum arithmetic, `ResolveReport` assembly. Tests against `httptest.Server` covering: all-agree, 2-of-3 agree, total disagreement, timeout of one source, malformed body. The stricter sanity checks (private, CGNAT, link-local reject) land in Phase 4 — this issue ships the happy path plus obvious failures.
- **2.3 — `internal/dnsprovider`: interface types.** `DNSProvider`, `Record`, `RecordRef`, `UpsertResult`. Pure types, no implementations. Existence of this package shape lets `internal/daemon` take a `DNSProvider` instead of a concrete GCP client.
- **2.4 — `internal/dnsprovider/gcp`: `New` + `Get`.** `dns.NewService(ctx, option.WithScopes(...))`, `ResourceRecordSets.Get`, 404 → `ErrNotFound`. Startup-time `New` failure bubbles up as `ErrAuth`. Recorded-cassette tests against captured Cloud DNS response bodies.
- **2.5 — `internal/dnsprovider/gcp`: `Upsert`.** `Changes.Create` with paired delete+add (or add-only on first create). Read the returned `Change.Status`. Recorded-cassette tests for create, update, and 403.
- **2.6 — `internal/daemon`: real reconcile tick (no state yet).** `Daemon.ReconcileOnce(ctx)` wires resolver + provider. Compares quorum IP against the live record's `rrdatas[0]` and calls `Upsert` if they differ. No local state cache yet — Phase 3 adds that for efficiency and restart-survivability. Tests use a fake `DNSProvider` (inlined in `internal/daemon/daemon_test.go` — we do not yet promote it to its own package).
- **2.7 — `cmd/ddns run`: real tick.** `runAction` reads config via `--config`, constructs the real resolver and real GCP provider, and runs the reconcile loop. On config or auth error at startup, returns `ErrConfig` / `ErrAuth` → exit 3. First shippable version.
- **2.8 — Integration test against live Cloud DNS.** `//go:build integration` test that takes `DDNS_INTEGRATION_PROJECT`, `DDNS_INTEGRATION_ZONE`, `DDNS_INTEGRATION_RECORD` env vars, runs one reconciliation, asserts the record now holds the resolved IP, and cleans up. Documented in the README.

**Phase 2 exit criteria.** On the author's home network with a real Cloud DNS zone, running `ddns run --config ~/.config/ddns/config.yaml` for 1 hour correctly keeps the configured record pointed at the host's public IP with zero wrong-IP writes. Integration test green.

## Phase 3 — State, `sync`, `status`

**Goal.** Everything needed to run `ddns` under cron instead of as a daemon, and everything needed for it to survive a restart without re-writing the record unnecessarily. By end of phase, the binary supports all four PRD-promised subcommands.

### Issues

- **3.1 — `internal/fs_atomic`: atomic write helper.** `WriteFile(path, data, mode)` with tempfile + fsync + rename + parent-dir fsync. Tests cover permission, mid-write abort (tempfile cleanup), and survival across simulated power cut.
- **3.2 — `internal/state`: load/save.** `State` struct, `Load` returning `ErrNotFound` for missing file, `Save` via `fs_atomic`. Per-record state file naming (`state/<sha256(name)>.json`) under the configured `state_path`. Round-trip tests.
- **3.3 — `internal/daemon`: state integration.** Reconcile tick now (a) loads state on entry, (b) short-circuits if `quorumIP == state.LastObservedIP == live.Rrdatas[0]`, (c) writes updated state after every outcome including no-op. Tests extend the matrix from Phase 2 with state-file fixtures.
- **3.4 — `cmd/ddns sync`: one-shot mode.** `syncAction` constructs the same daemon and runs exactly one `ReconcileOnce`. Exit 0 on success (noop or updated), 1 on resolver no-quorum, 2 on provider error, 3 on config/auth. Flag `--dry-run` accepted but not yet honored (Phase 4).
- **3.5 — `cmd/ddns status`: print state.** `statusAction` loads all per-record state files under `state_path` and prints a human-readable summary by default; `--json` prints the raw state document. No provider calls — status is read-only against local state.
- **3.6 — Multi-record support in the daemon loop.** Iterate over `cfg.Records` in a single tick; one provider `Get` per record; failures on one record do not abort the others. Extended daemon tests.

**Phase 3 exit criteria.** `ddns sync` invoked by cron (or any scheduler) every 10 minutes keeps the record correct on a host with a changing IP; `ddns status` after a reboot correctly reflects the last-observed state; zero redundant `Changes.Create` calls over a 24-hour soak on an unchanging IP (verified against the Cloud DNS audit log).

## Phase 4 — Hardening

**Goal.** Operational polish. Everything a real deployment needs that the happy-path reconciler does not strictly require.

### Issues

- **4.1 — `--dry-run` honored on `run` and `sync`.** Threaded into `Daemon.ReconcileOnce` as a parameter; in dry-run, the `Upsert` call is replaced by a log of the payload that would have been sent. Tests assert no `Upsert` calls.
- **4.2 — Exponential backoff for provider errors.** Implementation in `internal/daemon`; resets on next successful reconcile only. Fake-clock tests verify the schedule caps at 30 minutes.
- **4.3 — Resolver sanity-reject.** Reject private (RFC 1918), loopback, link-local, CGNAT (100.64.0.0/10), and multicast at the resolver boundary. Per-range tests.
- **4.4 — `SIGHUP` re-reads config.** The loop observes `SIGHUP` and reloads the config; a failed reload logs `config_reload_failed` and keeps the previous config. Tests send signals into a daemon running under a fake clock.
- **4.5 — Log-event taxonomy finalization.** Final pass on event names and attrs (`startup`, `tick_start`, `ip_resolved`, `reconcile_noop`, `reconcile_updated`, `reconcile_error`, `resolver_no_quorum`, `backoff_applied`, `config_reload_ok`, `config_reload_failed`, `shutdown`). A test that greps test log output asserts every event appears in at least one test.
- **4.6 — Help-text pass.** Every `urfave/cli` flag and subcommand has a `Usage:` string that makes sense in isolation; `ddns <cmd> --help` is copy-pasteable into documentation. Manual review gate, no unit test.

**Phase 4 exit criteria.** `staticcheck` and `golangci-lint` clean; all log events in the taxonomy appear under test coverage; dry-run demonstrably makes zero provider writes; 72-hour soak against the author's home network shows zero wrong-IP writes, zero panics, correct backoff schedule on induced 429s.

## Phase 5 — Docs and Deploy

**Goal.** Everything a new user needs to adopt the tool, and everything future-us needs to cut a release with confidence.

### Issues

- **5.1 — `deploy/docker/Dockerfile`.** Distroless `static-debian12` base, statically linked binary, sample `docker-compose.yml` with a bind-mounted config and a bind-mounted service-account JSON. CI job builds the image and runs `docker run --rm ddns version`.
- **5.2 — `README.md` rewrite.** Replace the draft README with the shipped-tool version: install instructions (`go install …`, binary download, Docker quickstart), config example, `--help` reference, troubleshooting section (auth errors, quorum failure, 403 on `Changes.Create`).
- **5.3 — `docs/operations.md`.** Day-two operations: reading logs, forcing a resync, rotating the service-account key, migrating state between hosts.
- **5.4 — Release-gate runbook.** `plan/bootstrap/release-gate-runbook.md` — the exact sequence (build, integration test, 72-hour soak, version bump, tag, push, GitHub release draft review) that precedes a v1 tag.

**Phase 5 exit criteria.** A new contributor can, with only the README, install the binary, write a config, and run a successful `ddns sync` against their own Cloud DNS project.

## Phase 6 — Release v1

**Goal.** Tag and ship.

### Issues

- **6.1 — GoReleaser config.** `.goreleaser.yaml` producing cross-compiled binaries for `linux/amd64` and `linux/arm64`; a multi-arch Docker image published under `ghcr.io/rootwarp/ddns`; a GitHub Release with checksums and SBOM.
- **6.2 — 72-hour soak on the author's home network.** The daemon runs continuously against the production managed zone. Pass criteria: zero panics; zero wrong-IP writes (verified against Cloud DNS audit log and a side-channel `dig` log); zero state-file corruption; ≥99% tick completion rate.
- **6.3 — Tag `v1.0.0`.** Only after 6.2 passes.

**Phase 6 exit criteria.** `v1.0.0` tag pushed; GitHub Release published with binaries and an image; the soak-test log is linked from the release notes.

## Phase 7 — v1.1 Follow-ups (P1)

Best-effort after v1. Not committed deliverables.

- **7.1 — Post-update webhook.** Single configurable HTTP(S) URL; POST a signed JSON body (`old_ip`, `new_ip`, `record`, `at`, `signature`) after every successful update; retries with exponential backoff on non-2xx; explicit out-of-scope for arbitrary script execution.
- **7.2 — Health endpoint.** `--health-addr` flag; `/healthz`, `/readyz`, `/state`. Protected by opt-in only — the flag is empty by default.
- **7.3 — Prometheus metrics.** `/metrics` on the same server; the metric set listed in the PRD.
- **7.4 — Cloudflare provider.** Second `DNSProvider` implementation. API-token auth, `/zones/:zone/dns_records` write path. Parity tests against the same daemon-level integration harness.

## Phase 8 — P2 Backlog

Sequencing notes, not committed work.

- Route 53 and RFC 2136 providers.
- Router-side IP detection via UPnP / NAT-PMP as an additional quorum source.
- Windows support (currently not an author priority; cross-compile works, but the Docker-only deployment shape does not extend to Windows natively).
- Container image signing (Cosign / Sigstore) and SLSA provenance attestation.

## Default Execution Model

- Single code-writer agent working against a single feature branch per issue, rebased onto `develop` on completion.
- `develop` is the integration branch; `main` is release-only and only advances on tag.
- One issue → one PR → one squash-merge. Multi-commit PRs are rejected.
- CI must be green to merge; no `--no-verify`.
- The issue tracker is the `plan/bootstrap/issues/` directory: one markdown file per issue, named `<phase>.<n>-<slug>.md`, owned by the code-writer, checked off as completed. Issue files are populated during each phase's planning pass, not up-front.

## Risk Register

- **Cloud DNS API changes under us.** Low likelihood; mitigated by pinning the `google.golang.org/api` module to a specific version and having an integration test that fails loudly on semantic drift.
- **IP-echo services dying simultaneously.** Addressed by the quorum model and the availability spike (S0.2). If the spike shows 2-of-3 agreement falls below 99% over the sample period, we add a fourth default source before Phase 1 exit.
- **Soak test reveals a pathological DHCP rotation pattern** (e.g., IP flapping every 30 seconds for an hour during a maintenance window) that thrashes the record. Mitigation considered for Phase 3 if observed: a minimum hold-time between consecutive updates, configurable, default 60 seconds. Added to the Phase 3 issue list only if the soak actually reproduces the case.
- **Scope creep toward multi-provider before v1 ships.** Mitigated by an explicit "no second provider before v1.0.0" rule; Cloudflare is Phase 7, not sooner, even if an interested contributor offers a PR.
- **Walking-skeleton discipline erodes.** The invariant that `go install ./cmd/ddns` produces a non-panicking binary at the end of every phase is easy to lose during a rushed PR. Mitigation: the Phase 1 smoke-test CI job (1.6) runs on every PR, not just `develop` merges, so a regression cannot land without noise.
