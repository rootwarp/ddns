---
type: document
status: decided (7-day sample deferred)
PARA: projects
tags:
  - dev/go
  - dev/research
---

# Spike 0.2: IP-echo service availability

## Status

**Decided based on published reliability data and production use at other open-source projects.** The 7-day continuous sampling run is deferred because it would serialize the whole project behind a week of wall-clock time on a single decision (is 2-of-3 enough?). Instead, we ship v1 with 2-of-3 quorum over three default sources, treat the sanity-reject prefix list from issue 4.3 as the compensating control against compromised echo services, and use the Phase 6 72-hour soak as the first real production data point. If the soak sees any quorum failure, Phase 8 revisits.

## Questions and decisions

### Q1. Do we need a fourth default source before Phase 2?

**Decision:** No. Three sources is sufficient if 2-of-3 quorum is the default.

**Reasoning:** `api.ipify.org`, `ifconfig.me`, and `icanhazip.com` are all operated by separate providers with independent infrastructure. Public availability numbers and community reports place each in the 99.5–99.9% range. Simultaneous outage probability is extremely low; the failure mode that matters is correlated outage (single upstream network event affecting all three — in which case the author's home network itself is almost certainly impaired and reconciling is moot).

### Q2. Is 2-of-3 quorum sufficient as the v1 default, or should we default to 3-of-3?

**Decision:** 2-of-3 is the v1 default. 3-of-3 is too brittle — any single service blip causes `ErrNoQuorum` and the daemon enters backoff.

**Reasoning:** The threat model for quorum is "one of the sources is compromised or stale and returns a wrong IP." 2-of-3 defeats this: an attacker would need to compromise two independent providers simultaneously. The complementary defense — the sanity-reject prefix list from issue 4.3 — filters out the most common compromise signatures (a compromised endpoint returning a private/CGNAT IP).

### Q3. Are any of the default sources flaky enough to demote to "optional"?

**Decision:** No demotion in v1. All three stay as defaults.

**Reasoning:** Shipping fewer than three services would force 2-of-2 quorum (brittle) or 1-of-2 (no quorum at all). Keep all three; operators who find one flaky can override `resolver.sources` in config.

## Fallback source (in case Phase 6 soak surfaces issues)

`https://checkip.amazonaws.com` is the pre-approved fourth source if the soak observes frequent quorum failures. Adding it is a one-line change to `DefaultResolverSources` in `internal/config`.

## 7-day sampling plan (deferred)

The sampling script is written but not scheduled:

- `plan/bootstrap/research/scripts/ip-echo-sample.sh` — polls all three services hourly, logs to TSV.
- Can be run post-v1 on the author's home network as part of long-term observability without blocking the release.

If executed, results land in:
- `plan/bootstrap/research/data/ip-echo-<date>.tsv` — raw sample
- An "Observed" section appended to this doc.

## Recommendation

Proceed to Phase 2 with:

```yaml
resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
    - https://icanhazip.com
  quorum: 2
  timeout: 10s
```

Defer empirical sampling. Use Phase 6 soak as first real-world validation.
