---
type: document
status: decided (live-verification deferred)
PARA: projects
tags:
  - dev/go
  - dev/research
---

# Spike 0.1: Cloud DNS `Changes.Create` write semantics

## Status

**Decided based on API reference + known behavior.** Live three-experiment smoke call is deferred until a GCP integration project is provisioned (tracked as a pre-release task before Phase 2.8 integration test runs). Decisions below are grounded in the Cloud DNS REST API documentation; if any is contradicted by a live experiment, the implementation in issues 2.4 and 2.5 will be adjusted.

## Questions and decisions

### Q1. Does `changes.create` with paired `deletions + additions` on the same `name+type` atomically swap the RRset?

**Decision:** Yes, treat it as atomic. A `dns.Change` resource is a single transaction on the managed zone; all `deletions` and `additions` in one `Change` apply together or not at all.

**Source:** Cloud DNS REST API documentation for `Change` resource — a `Change` is defined as an atomic unit of work. The `changes.create` endpoint commits the entire set or returns an error without partial application.

**Implementation consequence (issue 2.5):** `Upsert` constructs one `Change` with both `Deletions` (the old RRset payload obtained from a preceding `Get`) and `Additions` (the new RRset). A single API call performs the swap.

### Q2. Does the returned `Change` come back with `status = "done"` synchronously for a single-record swap, or is `"pending"` the common case?

**Decision:** Treat `"done"` as the expected response; accept `"pending"` as also-success (do not block or poll `changes.get`). The next reconcile tick re-observes live state and self-corrects if needed.

**Source:** The Cloud DNS `Change.status` field has two terminal semantics: `pending` (the Change has been accepted but propagation has not completed across the provider's servers) and `done` (propagation complete). For a single-record A-record swap, in practice the response is `done` within the API call — but the API contract allows `pending` and callers must accept both as "request accepted."

**Implementation consequence (issue 2.5):** `Upsert` reads the returned `Change.Status` and treats `pending` + `done` identically. We do not poll `Changes.Get`. If live monitoring in Phase 6 soak shows frequent `pending`, a Phase-8 hardening item adds optional polling.

### Q3. What does the API return when `deletions.rrdatas` does not exactly match the live record — 400, 412, or silent no-op?

**Decision:** Expect HTTP 412 Precondition Failed (or 409 Conflict, depending on API version) when the `rrdatas` in a deletion does not match the live record. Not a silent no-op; not 400. `Upsert` must therefore fetch the current RRset via `Get` immediately before building the `Change`, and pass that payload verbatim into `Deletions` to avoid the optimistic-concurrency window.

**Source:** Cloud DNS treats paired deletion payloads as precondition assertions. The documented behavior of `changes.create` is: deletions are evaluated against the current zone state; any mismatch fails the whole `Change`. Silent no-op is not part of the API contract.

**Implementation consequence (issues 2.4, 2.5):**
- `Get` → `Upsert` sequence in `Daemon.ReconcileOnce` is inherently racy (another actor could edit the record between the two calls). This is acceptable for v1: if the race triggers, `Upsert` fails with a precondition error; we retry on the next tick and the `Get` in that tick observes the new state and builds a fresh `Change`. No special optimistic-retry logic needed in v1.
- `Upsert`'s own internal `Get` (issue 2.5 flow step 1) uses the same pattern and accepts the same race window. Tight enough in practice.

## Live-verification plan (deferred to pre-release)

Before the v1.0.0 release gate (Phase 6 issue 6.3 soak), execute the three experiments against the integration zone:

1. Single pairing create-then-swap-then-delete against `spike-$(date +%s).<zone>` — confirm atomicity and `status` behavior in the response.
2. `Changes.Create` with a paired deletion whose `rrdatas` does not match the live record — confirm the exact error code.
3. Immediate round-trip `Get` after `Changes.Create` — confirm propagation latency.

Results commit back to this file as an "Observed" section. If any decision is contradicted, issues 2.4/2.5 ship with an amendment or a v1.0.1 follow-up.

## Recommendation

Proceed to Phase 2 with the decisions above. `Upsert` does:
1. Internal `Get` to read current rrdatas (if any).
2. Build `Change{Deletions: [current], Additions: [desired]}`.
3. `Changes.Create(project, zone, change)`.
4. Return success on any terminal status (`done` | `pending`).
5. Map 412/409 → `ErrTransient` (the daemon's next tick re-observes).
6. Map 404 → `ErrNotFound` (zone-level not-found is terminal config error).
7. Map 403 → `ErrAuth`.
8. Map 429/503/504 → `ErrTransient`.
