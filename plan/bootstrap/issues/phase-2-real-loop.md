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

# Phase 2: Real Resolver + Real Google Cloud DNS Update

**Goal.** Replace the Phase 1 no-op tick with real logic: resolve the public IP from HTTPS echo services, fetch the live Cloud DNS record, and issue a `Changes.Create` if they differ. By end of phase, `ddns run` against a real managed zone actually keeps the record in sync with the host's public IP. State persistence and `sync`/`status` land in Phase 3 — this phase stays narrow to keep the integration test honest.

**Entry criteria.** Phase 1 complete — walking-skeleton binary runs, urfave/cli wired, logging in place, exit-code helper in place.

**Exit criteria.** `ddns run --config ~/.config/ddns/config.yaml` against the author's integration managed zone, run for 1 hour, correctly keeps the configured record pointed at the host's public IP; integration test green; zero wrong-IP writes verified against the Cloud DNS audit log.

---

## Issue 2.1: `internal/config` — minimal YAML load + validation

**Story points:** 2

**Phase:** 2

**Depends on:** 0.4 (scaffold), 1.3 (`--config` flag already wired)

**PRD requirement:** P0 config schema

**Architecture modules touched:** `internal/config`

**Description.** Implement `Load(path string) (*Config, error)` that reads YAML, populates defaults, and validates. Full struct layout from architecture §2. Validation aggregates errors and returns one `*ConfigError` with all issues, so a user fixing three problems sees all three in one run. No hot reload (Phase 4), no secret redaction (no secrets in config), no migrations.

**Implementation notes:**
- `internal/config/config.go`:
  ```go
  type Config struct {
      PollInterval time.Duration  `yaml:"poll_interval"`
      StatePath    string         `yaml:"state_path"`
      LogFormat    string         `yaml:"log_format"`
      Resolver     ResolverConfig `yaml:"resolver"`
      Records      []RecordConfig `yaml:"records"`
      HealthAddr   string         `yaml:"health_addr"` // Phase 7 P1; unused in v1
  }
  type ResolverConfig struct {
      Sources []string      `yaml:"sources"`
      Quorum  int           `yaml:"quorum"`
      Timeout time.Duration `yaml:"timeout"`
  }
  type RecordConfig struct {
      Project     string `yaml:"project"`
      ManagedZone string `yaml:"managed_zone"`
      Name        string `yaml:"name"`
      TTL         int64  `yaml:"ttl"`
      Type        string `yaml:"type"`
  }
  ```
- `Load(path)`:
  1. `os.ReadFile(path)`.
  2. `yaml.Unmarshal` into a zero-value `Config`.
  3. Populate defaults via `applyDefaults(&cfg)`:
     - `PollInterval` default `5m`.
     - `StatePath` default `~/.local/state/ddns`.
     - `LogFormat` default `"auto"`.
     - `Resolver.Sources` default `["https://api.ipify.org", "https://ifconfig.me/ip", "https://icanhazip.com"]`.
     - `Resolver.Quorum` default `2`.
     - `Resolver.Timeout` default `10s`.
     - Per-record: `TTL` default `300`, `Type` default `"A"`.
  4. Validate; aggregate errors.
- Validation rules (from architecture §2):
  - `Records` non-empty.
  - Each `Project`, `ManagedZone`, `Name` non-empty; `Name` parses via `net/url` or a manual FQDN check — normalize to trailing-dot.
  - `Resolver.Quorum` in `[1, len(Sources)]`; `len(Sources) >= 2` when `Quorum >= 2`.
  - `TTL` in `[30, 86400]`.
  - `Type == "A"` — reject any other value with a v1-explicit message: `"record type %q not supported in v1; only A records are supported"`.
- `ConfigError` aggregates via `errors.Join` (Go 1.20+) and wraps `ddnserr.ErrConfig`.
- `Load` does `os.UserHomeDir()` expansion for `~/...` in `StatePath`; no other path magic.
- Tests: table-driven with 10+ cases: happy path, missing records list, empty project, invalid TTL, invalid type, missing sources, quorum > sources, duplicate record names (reject), trailing-dot-vs-no-dot name normalization.

**Acceptance criteria:**
- [ ] `Load("testdata/valid.yaml")` returns a populated `*Config` with no error.
- [ ] `Load("testdata/invalid-ttl.yaml")` returns an error that wraps `ddnserr.ErrConfig`.
- [ ] An invalid config with three problems produces an error whose message lists all three.
- [ ] Records with names like `home.example.com` and `home.example.com.` normalize to the same trailing-dot form.
- [ ] `go test ./internal/config/...` has ≥90% line coverage.

**Out of scope.** Hot reload (Phase 4 issue 4.4). Migration between schema versions (not needed in v1). IPv6 / `AAAA` records (explicit non-goal).

---

## Issue 2.2: `internal/resolver` — HTTPS echo + quorum

**Story points:** 2

**Phase:** 2

**Depends on:** 2.1 (`ResolverConfig`)

**PRD requirement:** P0 public-IP resolution

**Architecture modules touched:** `internal/resolver`

**Description.** Implement `IPResolver` per architecture §2: parallel `http.Get` against each configured source, per-response parse via `netip.ParseAddr`, quorum arithmetic, `ResolveReport` assembly. In this phase, reject only `!addr.Is4()` and parse failures; the full sanity-reject (private, loopback, CGNAT, link-local, multicast) lands in Phase 4 issue 4.3.

**Implementation notes:**
- `internal/resolver/resolver.go`:
  ```go
  type Resolver struct {
      sources []string
      quorum  int
      timeout time.Duration
      client  *http.Client
  }
  func New(cfg config.ResolverConfig) *Resolver {
      return &Resolver{
          sources: cfg.Sources,
          quorum:  cfg.Quorum,
          timeout: cfg.Timeout,
          client: &http.Client{
              Transport: &http.Transport{
                  TLSHandshakeTimeout: 5 * time.Second,
                  DisableKeepAlives:   true,
              },
          },
      }
  }
  func (r *Resolver) Resolve(ctx context.Context) (netip.Addr, ResolveReport, error) {
      // fan-out, read bounded 64 bytes, parse, collect, tally
  }
  ```
- Fan-out: one goroutine per source under `errgroup.WithContext` with `ctx, cancel := context.WithTimeout(ctx, r.timeout)`.
- Per-source read: `io.LimitReader(resp.Body, 64)`, trim whitespace, `netip.ParseAddr`, reject `!addr.Is4()` with reason `"not_ipv4"`, reject parse error with reason `"parse_err"`, reject non-2xx response with reason `"http_<status>"`.
- Tally: group per-IP; pick the IP with the highest count; if that count < `quorum`, return `(netip.Addr{}, report, ddnserr.ErrNoQuorum)`.
- `ResolveReport` includes `ElapsedMs`.
- Tests against `httptest.Server`:
  - `TestResolver_AllAgree` — 3 servers return same IP → quorum 3.
  - `TestResolver_TwoOfThree` — 2 return same IP, 1 returns different → quorum 2 for majority IP.
  - `TestResolver_TotalDisagreement` — 3 different IPs → `ErrNoQuorum`.
  - `TestResolver_OneTimeout` — 1 server sleeps past timeout → majority of remaining 2 still works.
  - `TestResolver_MalformedBody` — 1 server returns "not an ip" → skipped, remaining 2 fall to `ErrNoQuorum` boundary (if `quorum == 2`).
  - `TestResolver_5xx` — 1 server returns 503 → skipped.
  - `TestResolver_IPv6Response` — 1 server returns an IPv6 address → rejected as `not_ipv4`.

**Acceptance criteria:**
- [ ] All seven unit tests pass.
- [ ] `go test -race ./internal/resolver/...` passes (fan-out goroutines).
- [ ] The `ResolveReport` returned on quorum failure lists every source and its individual outcome — verified by a test assertion.
- [ ] Coverage ≥85% for `internal/resolver`.

**Out of scope.** Private/loopback/CGNAT/link-local rejection (Phase 4 issue 4.3). Router-side (UPnP / NAT-PMP) detection (Phase 8). IPv6 echo sources (IPv6 is an explicit non-goal).

---

## Issue 2.3: `internal/dnsprovider` — interface types

**Story points:** 1

**Phase:** 2

**Depends on:** 2.1

**PRD requirement:** P0 provider abstraction (pre-req for 2.4, 2.5)

**Architecture modules touched:** `internal/dnsprovider`

**Description.** Define the provider-agnostic interface and value types, without any concrete implementation. Lets issue 2.6 (daemon reconcile) take a `DNSProvider` parameter and unit-test against a fake, while issues 2.4 and 2.5 fill in the real Google Cloud DNS implementation.

**Implementation notes:**
- `internal/dnsprovider/dnsprovider.go` (package-level types, no implementation):
  ```go
  package dnsprovider

  type DNSProvider interface {
      Get(ctx context.Context, r RecordRef) (Record, error)
      Upsert(ctx context.Context, r RecordRef, new Record) (UpsertResult, error)
  }

  type RecordRef struct {
      Project     string
      ManagedZone string
      Name        string // trailing-dot form
      Type        string // "A" in v1
  }

  type Record struct {
      Rrdatas []string
      TTL     int64
  }

  type UpsertResult struct {
      Changed    bool
      OldRrdatas []string
      NewRrdatas []string
  }
  ```
- Also define a fake under `internal/dnsprovider/fake/fake.go`:
  ```go
  package fake

  type Provider struct {
      mu      sync.Mutex
      records map[string]dnsprovider.Record
      GetErr  error
      UpsertErr error
  }
  ```
  The fake is not imported by `main` — it's only used by `internal/daemon` tests.
- No tests on the interface package itself (it's just types); the fake gets a round-trip `TestFakeProvider_GetUpsertGet` test.

**Acceptance criteria:**
- [ ] `go build ./internal/dnsprovider/...` succeeds.
- [ ] The interface signature matches architecture §2 verbatim.
- [ ] `internal/dnsprovider/fake` compiles and has a minimal round-trip test.

**Out of scope.** Real Google Cloud DNS implementation (2.4, 2.5).

---

## Issue 2.4: `internal/dnsprovider/gcp` — `New` + `Get`

**Story points:** 2

**Phase:** 2

**Depends on:** 2.3, 0.1 (Cloud DNS semantics spike)

**PRD requirement:** P0 provider `Get`

**Architecture modules touched:** `internal/dnsprovider/gcp`

**Description.** Implement `New(ctx) (*Provider, error)` and `Get(ctx, ref) (Record, error)`. `New` builds `*dns.Service` via ADC; `Get` translates 404 → `ddnserr.ErrNotFound` and wraps other errors with status-code context.

**Implementation notes:**
- `internal/dnsprovider/gcp/gcp.go`:
  ```go
  package gcp

  type Provider struct {
      svc *dns.Service
  }

  func New(ctx context.Context) (*Provider, error) {
      svc, err := dns.NewService(ctx, option.WithScopes(dns.NdevClouddnsReadwriteScope))
      if err != nil {
          return nil, fmt.Errorf("%w: %v", ddnserr.ErrAuth, err)
      }
      return &Provider{svc: svc}, nil
  }

  func (p *Provider) Get(ctx context.Context, r dnsprovider.RecordRef) (dnsprovider.Record, error) {
      resp, err := p.svc.ResourceRecordSets.Get(r.Project, r.ManagedZone, r.Name, r.Type).Context(ctx).Do()
      if err != nil {
          var gErr *googleapi.Error
          if errors.As(err, &gErr) && gErr.Code == http.StatusNotFound {
              return dnsprovider.Record{}, ddnserr.ErrNotFound
          }
          return dnsprovider.Record{}, fmt.Errorf("%w: Get: %v", ddnserr.ErrTransient, err)
      }
      return dnsprovider.Record{Rrdatas: resp.Rrdatas, TTL: resp.Ttl}, nil
  }
  ```
- Recorded-cassette tests under `internal/dnsprovider/gcp/gcp_test.go`:
  - Test helpers accept a fake `http.Client` injected via `option.WithHTTPClient(...)`.
  - Fixture JSON under `testdata/responses/get-ok.json`, `get-404.json`, `get-403.json`.
  - `TestGet_OK`, `TestGet_NotFound`, `TestGet_PermissionDenied`.
- `New` without ADC in env must return an error that `errors.Is(err, ddnserr.ErrAuth)`. Test via `t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/does/not/exist")` — the underlying ADC chain will fail.

**Acceptance criteria:**
- [ ] `New(ctx)` returns a valid `*Provider` when ADC is present.
- [ ] `New(ctx)` returns an error wrapping `ddnserr.ErrAuth` when ADC is absent.
- [ ] `Get` returns `ErrNotFound` on 404.
- [ ] `Get` returns an error wrapping `ddnserr.ErrTransient` on 5xx.
- [ ] All three `TestGet_*` cases pass.

**Out of scope.** `Upsert` (issue 2.5). Retries / backoff on 429 (Phase 4 issue 4.2).

---

## Issue 2.5: `internal/dnsprovider/gcp` — `Upsert`

**Story points:** 3

**Phase:** 2

**Depends on:** 2.4

**PRD requirement:** P0 provider `Upsert`

**Architecture modules touched:** `internal/dnsprovider/gcp`

**Description.** Implement `Upsert(ctx, ref, new) (UpsertResult, error)`. Construct a `Changes.Create` that deletes the old RRset (if one exists) and adds the new one in the same atomic change. Use a preceding `Get` to produce the precise `deletions` entry, so we don't trip the 412/400 behavior captured in the 0.1 spike. Read the returned `Change.Status`; treat `"done"` and `"pending"` as success (the spike confirmed `"pending"` is rare and ddns does not block on polling — the next tick will observe the new state).

**Implementation notes:**
- `Upsert` flow:
  1. Call the internal `Get` to fetch the current record. If `ErrNotFound`, set `old = nil` (create path). Other errors bubble up.
  2. Idempotency: if `old != nil && sameRrdatas(old, new) && old.TTL == new.TTL`, return `UpsertResult{Changed: false, OldRrdatas: old.Rrdatas, NewRrdatas: old.Rrdatas}` without any write. This is the "no-op safeguard" — the daemon's own pre-check already handles this, but a second line of defense at the provider boundary guards against future callers.
  3. Build `change := &dns.Change{}`. If `old != nil`, add `change.Deletions = []*dns.ResourceRecordSet{{Name: ref.Name, Type: ref.Type, Ttl: old.TTL, Rrdatas: old.Rrdatas}}`. Always add `change.Additions = []*dns.ResourceRecordSet{{Name: ref.Name, Type: ref.Type, Ttl: new.TTL, Rrdatas: new.Rrdatas}}`.
  4. Call `p.svc.Changes.Create(ref.Project, ref.ManagedZone, change).Context(ctx).Do()`.
  5. On success, return `UpsertResult{Changed: true, OldRrdatas: old.Rrdatas, NewRrdatas: new.Rrdatas}`.
  6. On googleapi error: 404 → `ddnserr.ErrNotFound` (zone deleted mid-flight), 403 → `ddnserr.ErrAuth`, 429/503/504 → `ddnserr.ErrTransient`, other 4xx → `fmt.Errorf("provider upsert: %w", err)`.
- Invariants (assertions, not graceful errors):
  - `new.Rrdatas` non-empty, each parses as IPv4 (matches `Type == "A"`).
  - `ref.Type == "A"` in v1.
  - If either fails, return an error that starts with `"ddns bug:"` — these are programmer errors, not user errors.
- Recorded-cassette tests:
  - `TestUpsert_Create` — `Get` returns 404; `Changes.Create` returns 200 `done`.
  - `TestUpsert_Update` — `Get` returns existing record; swap payload asserted; response `done`.
  - `TestUpsert_NoOp` — `Get` returns same IP as requested; no `Changes.Create` call made (assert on mock).
  - `TestUpsert_PermissionDenied` — `Changes.Create` returns 403; error wraps `ddnserr.ErrAuth`.
  - `TestUpsert_RateLimited` — `Changes.Create` returns 429; error wraps `ddnserr.ErrTransient`.
  - `TestUpsert_PendingStatus` — response is `status: pending`; treated as success, `Changed: true`.
- The `sameRrdatas` helper sorts both slices before comparison — Cloud DNS does not guarantee order on `Get`.

**Acceptance criteria:**
- [ ] All six test cases pass.
- [ ] `TestUpsert_NoOp` verifies zero `Changes.Create` calls via a request counter on the mock HTTP client.
- [ ] The paired deletion payload in `TestUpsert_Update` is asserted field-by-field: `Name`, `Type`, `Ttl`, `Rrdatas`.
- [ ] A record with TTL changed but same rrdatas is detected as `Changed: true` (by the `old.TTL == new.TTL` part of the no-op check).

**Out of scope.** Polling `Changes.Get` when `status: pending` lingers (Phase 8 if ever seen in prod). Retries on transient (Phase 4 issue 4.2).

---

## Issue 2.6: `internal/daemon` — real reconcile tick (no state yet)

**Story points:** 2

**Phase:** 2

**Depends on:** 2.2, 2.3, 2.5

**PRD requirement:** P0 reconcile logic (state integration deferred to Phase 3)

**Architecture modules touched:** `internal/daemon`

**Description.** Implement `Daemon` and `ReconcileOnce(ctx) error`. A tick fetches quorum IP, fetches the live record, compares, and calls `Upsert` if they differ. No local state cache yet — Phase 3 adds that. Multi-record handling arrives in Phase 3 issue 3.6; this issue is single-record.

**Implementation notes:**
- `internal/daemon/daemon.go`:
  ```go
  type Daemon struct {
      cfg      *config.Config
      resolver resolver.IPResolver
      provider dnsprovider.DNSProvider
      log      *slog.Logger
  }

  func (d *Daemon) ReconcileOnce(ctx context.Context) error {
      ip, report, err := d.resolver.Resolve(ctx)
      if err != nil {
          d.log.Info("resolver_no_quorum", "report", report)
          return err
      }
      d.log.Info("ip_resolved", "ip", ip.String(), "quorum", report.Quorum)

      rec := d.cfg.Records[0] // single-record in 2.6; multi lands in 3.6
      ref := dnsprovider.RecordRef{Project: rec.Project, ManagedZone: rec.ManagedZone, Name: rec.Name, Type: rec.Type}
      live, err := d.provider.Get(ctx, ref)
      if err != nil && !errors.Is(err, ddnserr.ErrNotFound) {
          return err
      }
      desired := dnsprovider.Record{Rrdatas: []string{ip.String()}, TTL: rec.TTL}
      if errors.Is(err, ddnserr.ErrNotFound) {
          // create path handled by provider.Upsert
      } else if sameRrdatas(live.Rrdatas, desired.Rrdatas) && live.TTL == desired.TTL {
          d.log.Info("reconcile_noop", "ip", ip.String())
          return nil
      }
      res, err := d.provider.Upsert(ctx, ref, desired)
      if err != nil {
          d.log.Error("reconcile_error", "err", err.Error())
          return err
      }
      d.log.Info("reconcile_updated", "old", res.OldRrdatas, "new", res.NewRrdatas)
      return nil
  }
  ```
- Tests use an inline `fakeResolver` and the `dnsprovider/fake` package:
  - `TestReconcileOnce_NoOp` — quorum IP equals live record; no `Upsert` call.
  - `TestReconcileOnce_CreatePath` — provider `Get` returns `ErrNotFound`; `Upsert` called with `additions` only (implicit — the fake's `Upsert` records whether `old` was nil).
  - `TestReconcileOnce_UpdatePath` — quorum IP differs from live; `Upsert` called; log event asserted.
  - `TestReconcileOnce_ResolverNoQuorum` — resolver returns `ErrNoQuorum`; no provider calls; error propagated.
  - `TestReconcileOnce_ProviderTransient` — provider returns `ErrTransient`; error propagated (backoff is Phase 4).
- `sameRrdatas` is a private helper in `internal/daemon` — identical to the one in `gcp` package but duplicated deliberately; these are different layers, don't share.

**Acceptance criteria:**
- [ ] All five test cases pass.
- [ ] `TestReconcileOnce_NoOp` asserts zero `Upsert` calls on the fake.
- [ ] `TestReconcileOnce_ResolverNoQuorum` asserts zero `Get` / `Upsert` calls.
- [ ] Coverage ≥85% for `internal/daemon`.

**Out of scope.** `Daemon.Run(ctx)` loop (issue 2.7). State persistence (Phase 3 issue 3.3). Multi-record (Phase 3 issue 3.6). Dry-run (Phase 4 issue 4.1). Backoff (Phase 4 issue 4.2).

---

## Issue 2.7: `cmd/ddns run` — wire real reconcile

**Story points:** 2

**Phase:** 2

**Depends on:** 2.1, 2.2, 2.4, 2.5, 2.6

**PRD requirement:** P0 `ddns run` with real updates

**Architecture modules touched:** `cmd/ddns`, `internal/daemon`

**Description.** Replace the Phase 1 no-op `runAction` body with real wiring: load config, build resolver, build provider, build daemon, run a `for`-loop over `time.Ticker(cfg.PollInterval)` calling `ReconcileOnce` on each tick. Also add `Daemon.Run(ctx)` to `internal/daemon` as the caller-friendly loop entry point. First tick fires immediately (`firstTickNow: true` semantics — not a flag, just how the loop is structured) so a post-reboot reconcile does not wait a full interval.

**Implementation notes:**
- `internal/daemon/run.go`:
  ```go
  func (d *Daemon) Run(ctx context.Context) error {
      // first tick now
      if err := d.ReconcileOnce(ctx); err != nil && !errors.Is(err, ddnserr.ErrNoQuorum) && !errors.Is(err, ddnserr.ErrTransient) {
          return err
      }
      ticker := time.NewTicker(d.cfg.PollInterval)
      defer ticker.Stop()
      for {
          select {
          case <-ctx.Done():
              d.log.Info("shutdown", "reason", ctx.Err().Error())
              return nil
          case <-ticker.C:
              _ = d.ReconcileOnce(ctx)
              // per-tick errors are logged inside ReconcileOnce; do not propagate
          }
      }
  }
  ```
  - The first-tick error is swallowed for `ErrNoQuorum` / `ErrTransient` (those are "wait and try again" cases) but a config/auth error returned from `ReconcileOnce` bubbles up and the loop exits. The current `ReconcileOnce` signature does not distinguish config/auth from transient — but `New` catches auth before the loop starts, and config is validated in `Load`, so in practice only resolver and provider errors reach here.
- `cmd/ddns/run.go` updated:
  ```go
  func runAction(ctx context.Context, cmd *cli.Command) error {
      cfg, err := config.Load(cmd.String("config"))
      if err != nil {
          return err // exitCodeFor maps to 3
      }
      logger := logging.NewLogger(cfg.LogFormat, os.Stdout).With("record", cfg.Records[0].Name)
      provider, err := gcp.New(ctx)
      if err != nil {
          return err // ErrAuth → exit 3
      }
      d := daemon.New(cfg, resolver.New(cfg.Resolver), provider, logger)
      return d.Run(ctx)
  }
  ```
- Integration-test-adjacent unit test at `cmd/ddns/run_wiring_test.go`:
  - Build the binary with `go build`.
  - Set `DDNS_CONFIG` to a testdata config that points at a fake HTTP server the test spins up as a stand-in for `dns.googleapis.com`.
  - Hard to do without intercepting the Google API client's base URL — rely on the real integration test (2.8) for end-to-end verification, and keep this test as a smoke-only "binary starts and exits cleanly under SIGTERM with a real config."

**Acceptance criteria:**
- [ ] `./bin/ddns run --config testdata/example.yaml` against a fake DNS server (if rigged) exits cleanly on SIGTERM.
- [ ] With a valid real config and ADC, `./bin/ddns run` issues exactly one `reconcile_noop` or `reconcile_updated` log line on startup (verifies first-tick-now).
- [ ] With `--config /does/not/exist`, the binary exits 3 within 200ms.
- [ ] With no ADC and a valid config, the binary exits 3 with an auth-error message.

**Out of scope.** `sync` and `status` subcommands (Phase 3). Dry-run (Phase 4). SIGHUP config reload (Phase 4).

---

## Issue 2.8: Integration test against live Cloud DNS

**Story points:** 2

**Phase:** 2

**Depends on:** 2.7

**PRD requirement:** validation — proves Phase 2 exit criterion

**Architecture modules touched:** none (new test package)

**Description.** Add `//go:build integration`-tagged test `internal/dnsprovider/gcp/gcp_integration_test.go` that takes `DDNS_INTEGRATION_PROJECT`, `DDNS_INTEGRATION_ZONE`, `DDNS_INTEGRATION_RECORD` env vars, runs one full reconcile cycle end-to-end, and cleans up. Documented in the README as `make integration-test`.

**Implementation notes:**
- Test file header:
  ```go
  //go:build integration
  package gcp_test

  func TestIntegration_FullReconcile(t *testing.T) {
      project := os.Getenv("DDNS_INTEGRATION_PROJECT")
      zone := os.Getenv("DDNS_INTEGRATION_ZONE")
      record := os.Getenv("DDNS_INTEGRATION_RECORD")
      if project == "" || zone == "" || record == "" {
          t.Skip("integration env vars unset")
      }
      // ...
  }
  ```
- Test flow:
  1. Build a provider via `gcp.New(ctx)`.
  2. Ensure the record is either absent or points at a known-wrong value `192.0.2.1` (TEST-NET).
  3. Call `Upsert` with a new IP (use `203.0.113.42` from TEST-NET-3 — guaranteed not a real IP).
  4. Call `Get`; assert rrdata is `["203.0.113.42"]`.
  5. Call `Upsert` again with the same IP; assert `UpsertResult.Changed == false`.
  6. Cleanup: call the raw Cloud DNS API to delete the RRset via `Changes.Create` with a pure deletion; assert success.
- Add a README note under `## Integration tests` explaining the env vars and that the test uses TEST-NET-3 addresses so no real traffic is ever directed at the record under test.
- Add an environment-variable guard in `Makefile`'s `integration-test` target if not already present (issue 0.5 should have done this).

**Acceptance criteria:**
- [ ] `make integration-test` with all three env vars set and valid ADC runs the test and passes.
- [ ] The test leaves no residual record behind (cleanup step verified).
- [ ] Running the test twice in a row both succeed (idempotent cleanup).
- [ ] README includes a paragraph under `## Integration tests` explaining setup.
- [ ] The Cloud DNS audit log for the project shows exactly two `dns.changes.create` calls per test run (one update, one delete-cleanup) — not three or more.

**Out of scope.** Running integration tests in CI (they remain local-only). Daemon-level (`Run`) integration test — the 1-hour soak at Phase 2 exit is a manual QA step, not an automated test.
