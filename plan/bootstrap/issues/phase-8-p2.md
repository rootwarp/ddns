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

# Phase 8: P2 Backlog

**Goal.** Sequencing notes for post-v1.1 work, not committed deliverables. These items may or may not ever ship. Estimates are deliberately absent — revisit and size when the work is actually scheduled.

---

## 8.1 — Route 53 provider

Third `DNSProvider` implementation. AWS API auth is `aws-sdk-go-v2`'s default credentials chain; the hot-path call is `ChangeResourceRecordSets` with an `UPSERT` action (Route 53's atomic upsert is a first-class primitive, slightly nicer than Cloud DNS's paired delete+add). Risk: the `aws-sdk-go-v2` module tree is large — adding it doubles the binary size. Consider whether this is acceptable before starting.

---

## 8.2 — RFC 2136 / `nsupdate` provider

Standards-based dynamic update against BIND, Knot, PowerDNS, and other authoritative servers that support TSIG-signed DNS updates. Useful for operators who run their own DNS rather than using a cloud provider. Implementation is a pure Go RFC 2136 client — avoid shelling to `nsupdate(8)`. The TSIG key management is the tricky part; stick to shared HMAC-SHA256 keys and skip GSS-TSIG.

---

## 8.3 — Router-side IP detection (UPnP / NAT-PMP)

Add a third quorum source type that asks the home router for its WAN IP via UPnP's `GetExternalIPAddress` or NAT-PMP's external-address request. Complements the HTTPS echo services: if all echo services are down but the router is reachable, we can still reconcile. Complicated by the reality that UPnP implementations vary wildly across router vendors; test on at least three different routers before claiming support.

---

## 8.4 — Container image signing (Cosign / Sigstore)

Sign the `ghcr.io/rootwarp/ddns` images with Cosign keyless signing (OIDC-driven). Adds a `--signed` pull verification path in the deploy docs. Useful for supply-chain posture; not urgent for a single-maintainer home-server tool.

---

## 8.5 — Windows support

Currently out of scope. Go cross-compiles to Windows fine, but the Docker-only deployment model doesn't extend to Windows natively — we'd need either a Windows Service wrapper around the binary or documentation for running it under a scheduled task. Not an author priority; revisit if a user requests it.

---

## 8.6 — `ddns exec <name> -- <cmd>` subcommand

Run `<cmd>` with `DDNS_CURRENT_IP` env var set to the last-observed IP, or block on a fresh reconcile before running. Useful for scripts that want to pipeline "figure out my IP, then do X." Similar to `kubectx exec` but for DDNS. Questionable value for a single-binary tool where `ddns status --json | jq` is already one pipe.

---

## 8.7 — Multi-provider redundancy

Write the same record to two providers simultaneously so that DNS resolution survives one provider's outage (e.g., Cloud DNS + Cloudflare as primary/secondary authoritative). This is DNS-level anycast territory and opens several worms (NS record management, zone transfer semantics). Almost certainly out of scope for ddns; if it ever becomes a goal, it's probably a separate tool.

---

## 8.8 — Per-record poll interval override

Allow each `records:` entry to have its own `poll_interval`. The default stays at the top-level global. Motivation: a home-lab operator with a rapidly-changing WireGuard endpoint and a slow-changing Jellyfin hostname might want different cadences. Complexity: the single-ticker loop becomes a ticker-per-record loop; care needed to stop goroutine leaks on SIGHUP config reload. Easy to add if ever requested.

---

## 8.9 — Shell completions (bash / zsh / fish)

GoReleaser can emit completion scripts for urfave/cli via an `after:` hook. Cheap addition; skipped in v1.0 because neither the author nor the PRD target users asked for it.

---

## 8.10 — Debug log level

`--debug` flag to promote logs from INFO to DEBUG level. Primarily useful for diagnosing resolver edge cases (per-source timing, DNS lookup times, TLS handshake failures). Out of scope for v1 because we don't currently emit DEBUG-level events; adding them is a separate refactor of the logging call sites.

---

## Prioritization Notes

If v1.2 is the next release after v1.1:

- Most-requested external feature is almost always Cloudflare or Route 53 — 8.1 and 7.5 cover that.
- Most-requested internal feature is almost always shell completions (8.9) — trivial once GoReleaser is in place.
- 8.3 (UPnP) is high-effort, niche-audience — deprioritize unless a CGNAT victim shows up with a real use case.

No item in this backlog should ship without writing an issue file against the relevant architecture module and adding a changelog entry — same rigor as v1.x issues.
