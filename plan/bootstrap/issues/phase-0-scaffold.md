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

# Phase 0: Spikes & Scaffold

**Goal.** Resolve the three open research items with recorded-artifact answers and stand up the Go module, CI, and Makefile. No user-facing functionality; every later phase builds on this.

**Entry criteria.** Go 1.22 toolchain installed; a GCP project with Cloud DNS API enabled; a reserved managed zone + test subdomain for integration work; `github.com/rootwarp/ddns` empty repo exists with push access; service-account JSON downloaded locally for integration tests.

**Exit criteria.** Three research notes landed under `plan/bootstrap/research/`; `make build` produces an empty-but-linkable `bin/ddns`; `go build ./...`, `go vet ./...`, `go test ./...`, `staticcheck ./...`, `golangci-lint run` all green on CI.

---

## Issue 0.1: Spike — Cloud DNS `Changes.Create` write semantics

**Story points:** 1

**Phase:** 0

**Depends on:** none

**PRD requirement:** research spike (feeds `internal/dnsprovider/gcp.Upsert` design in Phase 2)

**Architecture modules touched:** `dnsprovider/gcp` (design input only — no code)

**Description.** Confirm from the Cloud DNS REST API documentation and one live smoke call: (a) does `changes.create` with a paired `deletions + additions` for the same `name + type` atomically swap the RRset? (b) Does the returned `Change` object come back with `status = "done"` synchronously for a one-record swap, or is `status = "pending"` the common case, requiring the caller to poll `changes.get`? (c) What does the API return on a `deletions` entry whose `rrdatas` does not exactly match the live record — a 400, a 412, or a silent no-op? The answers decide whether `Upsert` must poll, must strict-match the old rrdatas from a preceding `Get`, and whether an optimistic-concurrency bug is possible between our `Get` and `changes.create`.

**Implementation notes:**
- Drive from the dev laptop with `gcloud` or a 20-line Go `main.go` against the integration zone. No `internal/` code is written for this spike.
- Issue one create-then-swap-then-delete cycle against a throwaway subdomain (e.g., `spike-$(date +%s).<zone>`).
- For the third subquestion, intentionally construct a `deletions` entry with wrong `rrdatas` and record the error code.
- Output: `plan/bootstrap/research/01-cloud-dns-changes-semantics.md` with: (1) the full JSON request/response for each of the three experiments, (2) a one-line policy decision for each of the three subquestions, (3) a recommendation on whether `Upsert` should poll `changes.get` or trust the first response's status (expected: trust it, but note the polling fallback as a Phase 4 hardening item if the spike sees any `pending`).

**Acceptance criteria:**
- [ ] `plan/bootstrap/research/01-cloud-dns-changes-semantics.md` exists and includes the three experiment transcripts.
- [ ] Each of the three subquestions has a one-line policy decision.
- [ ] A reviewer other than the author can read the doc and implement `Upsert` in Phase 2 without reopening the Cloud DNS API reference.

**Out of scope.** Any Go code inside the `internal/` tree (all live in Phase 2). Long-term monitoring of the spike subdomain (delete after the experiment).

---

## Issue 0.2: Spike — 7-day IP-echo service availability measurement

**Story points:** 1

**Phase:** 0

**Depends on:** none

**PRD requirement:** research spike (validates the 2-of-3 quorum default in `internal/resolver` before Phase 2 writes the code)

**Architecture modules touched:** `resolver` (design input only)

**Description.** Run a 7-day cron on the author's home network polling `https://api.ipify.org`, `https://ifconfig.me/ip`, and `https://icanhazip.com` once per hour. Log per-request: timestamp, HTTP status, response body (first 64 bytes), parse outcome (`ok`/`wrong-family`/`parse-err`). Compute per-service availability and the count of hours where ≥2 of 3 services returned the same valid IPv4. Decision output: (a) do we need a fourth default source before Phase 2? (b) is 2-of-3 quorum sufficient, or should we default to 3-of-3? (c) are any of the default sources flaky enough to demote to "optional"?

**Implementation notes:**
- One-file shell script under `plan/bootstrap/research/scripts/ip-echo-sample.sh`; logs to a TSV.
- Scheduled via `crontab -e` at `0 * * * *` on the author's home Linux box.
- After 7 days (168 samples per service), stop the cron, copy the TSV back, and summarize in `plan/bootstrap/research/02-ip-echo-availability.md`.
- Summary must include: per-service error count, per-service error breakdown (HTTP 5xx vs timeout vs parse error), hours with 3/3 agreement, hours with 2/3 agreement, hours with <2/3 agreement.

**Acceptance criteria:**
- [ ] `plan/bootstrap/research/02-ip-echo-availability.md` exists with per-service availability percentages across the 7-day window.
- [ ] The doc contains an explicit decision: "2-of-3 is / is not sufficient as the v1 default."
- [ ] If 2-of-3 is not sufficient, the doc names the fourth source to add (expected fallback: `https://checkip.amazonaws.com`) and updates the architecture doc's resolver default list accordingly.
- [ ] Raw TSV is checked in under `plan/bootstrap/research/data/ip-echo-<date>.tsv`.

**Out of scope.** Building the real resolver. Monitoring past the 7-day window.

---

## Issue 0.3: Spike — urfave/cli v3 ergonomics confirmation

**Story points:** 1

**Phase:** 0

**Depends on:** none

**PRD requirement:** research spike (confirms the dependency choice for `cmd/ddns` before Phase 1 builds on it)

**Architecture modules touched:** `cmd/ddns` (design input only)

**Description.** Confirm that `github.com/urfave/cli/v3` is appropriate for ddns's CLI surface by writing a 40-line throwaway `main.go` that: (a) defines a root command with a global `--config` flag, (b) defines two subcommands (`run`, `version`), (c) wires a `signal.NotifyContext(SIGINT, SIGTERM)` into `cmd.Run(ctx, os.Args)`, (d) verifies that Ctrl-C during `run` cleanly cancels the subcommand's context, and (e) verifies `--help` output looks sensible. Decision output: v3 goes forward, or we fall back to v2 if v3's module path (`v3`) or struct-literal pattern creates friction.

**Implementation notes:**
- Throwaway code lives under `plan/bootstrap/research/scripts/urfave-cli-spike/` and is not imported by the real module.
- Write the spike against v3's module path (`github.com/urfave/cli/v3`). If `go get` fails (e.g., v3 is still prerelease at the time of the spike), fall back to v2 and document the reason.
- Run `./spike run` and press Ctrl-C; confirm the handler returns within 1s and the process exits 0.
- Output: `plan/bootstrap/research/03-urfave-cli-v3.md` with: (1) the module version chosen (`v3.x.y`), (2) the 40-line spike code pasted inline, (3) confirmation that the signal-context plumbing works, (4) a one-line recommendation.

**Acceptance criteria:**
- [ ] `plan/bootstrap/research/03-urfave-cli-v3.md` exists with the module version and the spike code.
- [ ] Doc explicitly confirms: "Ctrl-C during `ddns run` cancels the root context and the handler returns within 1 second."
- [ ] Doc explicitly names the version to pin in `go.mod` (e.g., `v3.0.0-beta11` or `v3.1.5`).
- [ ] If v3 was rejected, the doc explains why and names the v2 version to pin instead.

**Out of scope.** The real `cmd/ddns` wiring (Phase 1 issue 1.3).

---

## Issue 0.4: Go module scaffold + package stubs

**Story points:** 2

**Phase:** 0

**Depends on:** 0.3 (urfave/cli version decision)

**PRD requirement:** infrastructure (feeds every subsequent phase)

**Architecture modules touched:** all (`cmd/ddns`, `internal/config`, `internal/resolver`, `internal/dnsprovider`, `internal/dnsprovider/gcp`, `internal/state`, `internal/daemon`, `internal/logging`, `internal/fs_atomic`, `internal/version`)

**Description.** Initialize the Go module at `github.com/rootwarp/ddns`, create package skeletons for every package in the architecture doc, and pin the v1 dependency set. Each package is an empty `doc.go` with a one-line package comment matching the architecture doc's "responsibility" column. No types, no functions — this issue is about compile-ability and `go mod tidy` cleanliness.

**Implementation notes:**
- `go mod init github.com/rootwarp/ddns`.
- `go.mod` declares `go 1.22` (the minimum the project targets).
- Run `go get` for the core deps at the versions chosen in the spikes:
  - `github.com/urfave/cli/v3@<version from 0.3>`
  - `gopkg.in/yaml.v3@latest`
  - `google.golang.org/api@latest` (this pulls the Cloud DNS client `google.golang.org/api/dns/v1`)
  - `golang.org/x/oauth2@latest` (transitive but explicit)
  - `golang.org/x/term@latest` (for TTY detection in `internal/logging`)
- Create `cmd/ddns/main.go` with a bare `package main; func main() {}` — the walking skeleton proper lands in Phase 1.
- Create `doc.go` under every package listed in the architecture doc:
  - `internal/config/doc.go`
  - `internal/resolver/doc.go`
  - `internal/dnsprovider/doc.go`
  - `internal/dnsprovider/gcp/doc.go`
  - `internal/state/doc.go`
  - `internal/daemon/doc.go`
  - `internal/logging/doc.go`
  - `internal/fs_atomic/doc.go`
  - `internal/version/doc.go`
- Each `doc.go` is:
  ```go
  // Package <name> <one-sentence responsibility from architecture §2>.
  package <name>
  ```
- Run `go mod tidy` and confirm the resulting `go.sum` only pins the explicit deps (plus transitive).
- Run `go build ./...` — must succeed.

**Acceptance criteria:**
- [ ] `go build ./...` succeeds at commit time.
- [ ] `go vet ./...` succeeds.
- [ ] `ls internal/` shows exactly the nine package directories listed above.
- [ ] Every `doc.go` contains a one-line package comment that matches the architecture doc's responsibility wording (verbatim-enough that a reviewer can map them 1:1).
- [ ] `go.mod` pins `github.com/urfave/cli/v3` at the version named by issue 0.3.
- [ ] `go.mod` pins `google.golang.org/api` at the version that resolves the `dns/v1` subpackage.

**Out of scope.** Any type or function definitions beyond empty package comments. The `Makefile` (issue 0.5). CI (issue 0.6).

---

## Issue 0.5: Makefile with `build`, `test`, `lint`, `install`, `integration-test` targets

**Story points:** 1

**Phase:** 0

**Depends on:** 0.4

**PRD requirement:** infrastructure

**Architecture modules touched:** none (build tooling)

**Description.** Commit a `Makefile` at repo root that is the single entry point developers and CI both use. Targets: `build` (produces `bin/ddns` with ldflags-injected version), `test` (`go test ./...`), `lint` (runs `go vet`, `staticcheck`, `golangci-lint`), `install` (`go install ./cmd/ddns`), `integration-test` (runs `go test -tags=integration ./...` only when `DDNS_INTEGRATION_PROJECT` is set), `clean` (removes `bin/`). The `build` target populates `internal/version.Version` / `Commit` / `BuildDate` via `-ldflags "-X"` — the stub constants land in Phase 1 issue 1.1, but the Makefile is written now with the intended `-X` paths so Phase 1 is a one-line addition.

**Implementation notes:**
- Use `:=` for variables, tabs for command indentation (required).
- `VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`.
- `COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)`.
- `BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)`.
- `LDFLAGS := -X github.com/rootwarp/ddns/internal/version.Version=$(VERSION) -X github.com/rootwarp/ddns/internal/version.Commit=$(COMMIT) -X github.com/rootwarp/ddns/internal/version.BuildDate=$(BUILD_DATE)`.
- `build: @mkdir -p bin; go build -ldflags "$(LDFLAGS)" -o bin/ddns ./cmd/ddns`.
- `install: go install -ldflags "$(LDFLAGS)" ./cmd/ddns`.
- `test: go test ./...`.
- `lint: go vet ./...; staticcheck ./...; golangci-lint run`.
- `integration-test:` guard with `test -n "$$DDNS_INTEGRATION_PROJECT" || (echo "DDNS_INTEGRATION_PROJECT unset; skipping" && exit 0)` then `go test -tags=integration ./...`.
- `clean: rm -rf bin/`.
- Also add `.PHONY: build test lint install integration-test clean`.

**Acceptance criteria:**
- [ ] `make build` produces `bin/ddns` on a clean checkout (the binary is effectively empty at this point but must link).
- [ ] `make test` runs (passes trivially with "no test files" output).
- [ ] `make lint` runs — may flag the empty `main` function; that's fine if `go vet` is clean and the linters are downloaded.
- [ ] `make integration-test` without `DDNS_INTEGRATION_PROJECT` exits 0 with a skip message.
- [ ] The `LDFLAGS` references point at the `internal/version` package paths that Phase 1 issue 1.1 will populate.

**Out of scope.** Release automation (Phase 6 / GoReleaser). Docker build (Phase 5).

---

## Issue 0.6: GitHub Actions CI — build, vet, test, staticcheck, golangci-lint

**Story points:** 1

**Phase:** 0

**Depends on:** 0.4, 0.5

**PRD requirement:** infrastructure

**Architecture modules touched:** none

**Description.** Commit `.github/workflows/ci.yml` that runs on every push and every PR into `develop`. Jobs: one `test` job on `ubuntu-latest` (plus `macos-latest` in the matrix) running `make lint` and `make test`. No integration tests in CI — those remain local-only (`make integration-test` with the env var set).

**Implementation notes:**
- Workflow triggers: `push` on any branch, `pull_request` targeting `develop` or `main`.
- Matrix: `os: [ubuntu-latest, macos-latest]`, `go: ["1.22.x"]`.
- Steps per job:
  1. `actions/checkout@v4`.
  2. `actions/setup-go@v5` with `go-version: ${{ matrix.go }}`.
  3. Cache module downloads via `actions/cache@v4` keyed on `go.sum`.
  4. `go install honnef.co/go/tools/cmd/staticcheck@latest`.
  5. `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`.
  6. `make lint`.
  7. `make test`.
  8. `make build` (confirms the binary still links — this is the walking-skeleton guard rail from project-plan §Phase 1, but wired at Phase 0 so the habit starts before it matters).
- Timeout: 10 minutes per job.
- Add a minimal `.golangci.yml` enabling `errcheck`, `govet`, `staticcheck`, `gosimple`, `ineffassign`, `unused`, `gofmt` — a conservative set that we tighten in Phase 4.

**Acceptance criteria:**
- [ ] `.github/workflows/ci.yml` is valid YAML (actions-lint or `actionlint` passes).
- [ ] Opening a test PR triggers both `ubuntu-latest` and `macos-latest` jobs.
- [ ] Both jobs green on the Phase-0 scaffold commit.
- [ ] Intentionally introducing an unused import in `cmd/ddns/main.go` fails the `make lint` step — proving the linters are actually invoked.
- [ ] `.golangci.yml` exists at repo root with the enabled-linter set above.

**Out of scope.** Release workflow (Phase 6). Docker-image CI (Phase 5).

---

## Issue 0.7: `develop` branch + branch protection

**Story points:** 1

**Phase:** 0

**Depends on:** 0.6

**PRD requirement:** infrastructure

**Architecture modules touched:** none (repo configuration)

**Description.** Create `develop` branch, push it, configure it as the default branch on GitHub. Add branch protection rules: require the CI `test` matrix to pass before merge; require one approving review; disallow force-push. `main` stays protected and only advances on tag (Phase 6). This issue locks the workflow convention described in project-plan §Default Execution Model.

**Implementation notes:**
- From a clean checkout of the Phase-0 scaffold commit: `git checkout -b develop && git push -u origin develop`.
- On GitHub: Settings → Branches → set `develop` as default.
- Branch protection for `develop`:
  - Require a pull request before merging.
  - Require approvals: 1.
  - Require status checks: `test (ubuntu-latest, 1.22.x)`, `test (macos-latest, 1.22.x)`.
  - Require conversation resolution.
  - Do not allow force-push.
- Branch protection for `main`:
  - Require a pull request before merging.
  - Require status checks (same as develop).
  - Restrict who can push to `main` to repo admins — releases cut tags from develop via a fast-forward PR.
- Document in a new `CONTRIBUTING.md` (five lines): one feature branch per issue, branched from `develop`, squash-merged back into `develop` on review, `main` is release-only.

**Acceptance criteria:**
- [ ] `develop` is the default branch on GitHub.
- [ ] Branch protection on `develop` requires both CI jobs to pass and one review.
- [ ] Branch protection on `main` requires both CI jobs to pass.
- [ ] `CONTRIBUTING.md` exists with the five-line workflow note.
- [ ] A test push to a feature branch and PR into `develop` demonstrates the CI gates work end-to-end.

**Out of scope.** CODEOWNERS (single-owner project; unnecessary). Issue/PR templates (Phase 5).
