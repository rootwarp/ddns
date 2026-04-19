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

# Phase 7: v1.1 Follow-ups (P1)

**Goal.** Land the PRD P1 items as v1.1: post-update webhook, health endpoint, Prometheus metrics, a second DNS provider (Cloudflare) as the proof-of-refactoring that the provider interface wasn't Cloud-DNS-shaped.

**Status.** Best-effort after v1.0.0 ships. Not committed deliverables. Estimates carry story points for sizing but are not promises.

**Entry criteria.** `v1.0.0` tagged and in use for ≥2 weeks on the author's home network without a user-reported P0 bug.

**Exit criteria.** `v1.1.0` tagged and released via the same GoReleaser workflow.

---

## Issue 7.1: Post-update webhook

**Story points:** 2

**Phase:** 7

**Depends on:** v1.0.0 released

**PRD requirement:** P1

**Architecture modules touched:** `internal/daemon`, `internal/config`

**Description.** After every successful `reconcile_updated`, optionally POST a signed JSON body to a configured URL. The hook is a single URL (not a scripting surface — deliberately). Retries use exponential backoff with a cap; a failing hook does not fail the reconcile (the DNS update already succeeded; the hook is informational).

**Implementation notes:**
- `Config` gains:
  ```yaml
  webhook:
    url: https://example.com/ddns-hook
    secret: env:DDNS_WEBHOOK_SECRET  # or an inline secret — env prefix hides it
    timeout: 10s
  ```
- Payload:
  ```json
  {
    "record": "home.example.com.",
    "type": "A",
    "old_ip": "198.51.100.7",
    "new_ip": "192.0.2.42",
    "at": "2026-05-01T12:00:00Z",
    "signature": "hex(hmac-sha256(secret, body_without_signature_field))"
  }
  ```
- Signature: compute over the canonicalized JSON body with the `signature` field removed / zero — standard webhook convention. Document the recipe in `docs/webhook.md`.
- Retries: 3 attempts, exponential backoff (1s, 4s, 16s), skip retry on 4xx (those are "your request is wrong, retrying won't help" signals).
- A queued hook must not block the next reconcile; hook delivery runs in its own goroutine with its own context.

**Acceptance criteria:**
- [ ] `webhook.url` configured triggers a POST on every `reconcile_updated`.
- [ ] The hook body includes a valid HMAC-SHA256 signature.
- [ ] `env:` prefix on `webhook.secret` reads from the env var.
- [ ] A failing hook logs `webhook_failed` but does not increment the backoff counter or fail the reconcile.
- [ ] `docs/webhook.md` documents the payload and signature algorithm.

---

## Issue 7.2: Health endpoint

**Story points:** 2

**Phase:** 7

**Depends on:** v1.0.0 released

**PRD requirement:** P1

**Architecture modules touched:** `internal/health` (new package, stubbed in Phase 1)

**Description.** Optional HTTP server behind a `--health-addr :8080` flag. Endpoints: `GET /healthz` (200 when the loop is active), `GET /readyz` (200 after first successful reconcile), `GET /state` (state-file contents, redacted). The flag is empty by default — no port opens unless the operator asks for it.

**Implementation notes:**
- `internal/health/server.go` with a small `Server` type backed by a `*http.Server`. Graceful shutdown on root-context cancellation.
- `/state` returns the same JSON as `ddns status --json` for the current configured records.
- `/metrics` is added by issue 7.3 on the same server.
- Security posture: bind to `127.0.0.1:port` by default; user can override to `0.0.0.0` by editing the flag. Document the risk of publicly exposing `/state` in `docs/operations.md`.
- Tests use `httptest.NewServer` and assert on each endpoint.

**Acceptance criteria:**
- [ ] `--health-addr 127.0.0.1:8080` opens the endpoint.
- [ ] `curl localhost:8080/healthz` returns `200 OK` while the daemon is live.
- [ ] `curl localhost:8080/readyz` returns `503` before the first tick, `200` after.
- [ ] `curl localhost:8080/state` returns the current state as JSON.
- [ ] `--health-addr ""` (default) opens no port.

---

## Issue 7.3: Prometheus metrics

**Story points:** 2

**Phase:** 7

**Depends on:** 7.2

**PRD requirement:** P1

**Architecture modules touched:** `internal/health`, `internal/daemon`

**Description.** Add a `/metrics` endpoint to the health server with Prometheus text format. Metric set (from PRD): `ddns_ticks_total`, `ddns_updates_total`, `ddns_resolver_errors_total{source}`, `ddns_provider_errors_total`, `ddns_last_update_timestamp_seconds`. Use `prometheus/client_golang` as the metrics library.

**Implementation notes:**
- Counter for ticks, updates, provider errors. Counter vector labeled `source` for per-echo-service resolver errors.
- Gauge `ddns_last_update_timestamp_seconds` set to `time.Now().Unix()` on every `reconcile_updated`.
- Integration test scrapes `/metrics`, asserts metric names appear with expected types.
- No custom histograms — tick duration is not worth the buckets at this scale; log timestamps cover it.

**Acceptance criteria:**
- [ ] `/metrics` returns valid Prometheus text format.
- [ ] All five metrics are present after a single reconcile.
- [ ] `ddns_resolver_errors_total{source="https://api.ipify.org"}` increments when that source fails.
- [ ] `ddns_last_update_timestamp_seconds` is close to `time.Now().Unix()` after an `updated` reconcile.

---

## Issue 7.4: Cloudflare provider implementation

**Story points:** 3

**Phase:** 7

**Depends on:** v1.0.0 released (so the refactoring cost is paid against a stable daemon surface)

**PRD requirement:** P1

**Architecture modules touched:** `internal/dnsprovider/cloudflare` (new), `internal/config`, `cmd/ddns`

**Description.** Second `DNSProvider` implementation against Cloudflare's API. API token authentication (not the legacy API key). Record writes via `POST /zones/:zone/dns_records` and `PUT /zones/:zone/dns_records/:id`. This is the existence proof that the `DNSProvider` interface survived contact with a second backend — if it didn't, refactor before shipping.

**Implementation notes:**
- `config.RecordConfig` gains `provider: string` (values: `"gcp"`, `"cloudflare"`). Default `"gcp"` for backwards compatibility.
- `internal/dnsprovider/cloudflare/cloudflare.go` implements `DNSProvider`. Auth via `CLOUDFLARE_API_TOKEN` env var. `New(ctx)` returns `ErrAuth` if the env var is missing.
- Cloudflare's API models records differently: each record has an ID, and updates use `PUT /zones/:zone/dns_records/:id` rather than a generic `changes.create`. `Upsert` therefore does: `GET /zones/:zone/dns_records?type=A&name=<name>` → if result list has one record, `PUT` to update it; if empty, `POST` to create.
- Error mapping: 4xx → `ErrAuth` / `ErrNotFound` per code; 5xx / 429 → `ErrTransient`.
- Integration test harness parallels the GCP one: `DDNS_INTEGRATION_CF_TOKEN`, `DDNS_INTEGRATION_CF_ZONE`, `DDNS_INTEGRATION_CF_RECORD` env vars.
- Parity tests: run the daemon against both providers back-to-back with the same config (substituting `provider`) and assert identical log event sequences. If the test passes, the interface held up.

**Acceptance criteria:**
- [ ] `config` validates `provider: cloudflare`.
- [ ] `DDNS_INTEGRATION_CF_*` integration test passes.
- [ ] Daemon parity test passes.
- [ ] README documents Cloudflare setup (API token scope, zone ID).
- [ ] The `DNSProvider` interface was not modified — only implemented. If this turned out to be false, note what changed and why.

**Out of scope.** Cloudflare proxy mode (`proxied: true`) — ddns targets direct DNS resolution, not Cloudflare's CDN. If a user wants proxy mode, they configure it out-of-band in the Cloudflare dashboard; ddns does not touch it.

---

## Issue 7.5: v1.1.0 tag

**Story points:** 1

**Phase:** 7

**Depends on:** 7.1, 7.2, 7.3, 7.4 (any subset the user wants to ship)

**PRD requirement:** release

**Architecture modules touched:** none

**Description.** Cut a v1.1.0 release using the same Phase 6 runbook. 72-hour soak is optional for a minor release if no Phase-7 feature touches the existing reconcile path (only 7.4 does — if only 7.1/7.2/7.3 shipped, skip the soak and tag after a 24-hour smoke). Document the decision in the release notes.

**Acceptance criteria:**
- [ ] `v1.1.0` tag on main; GitHub Release published.
- [ ] Release notes list every included Phase-7 issue.
- [ ] If the soak was skipped, the release notes explain why.
