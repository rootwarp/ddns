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
- [[architecture]]
- [[project-plan]]
- [[README]]

# PRD: `ddns` — Dynamic DNS for Google Cloud DNS

## Overview

`ddns` is a single-binary Go daemon that keeps one or more Google Cloud DNS `A` records pointing at the current public IPv4 address of the host it runs on. It is targeted at home-server operators whose ISP assigns public IPs via DHCP, where the assigned address can change without warning on lease renewal, router reboot, or upstream maintenance. The tool polls redundant HTTPS "what is my IP" services, takes a quorum, and — only when the observed IP differs from both local state and the live Cloud DNS record — issues a `Changes.Create` against the target managed zone.

- **Binary:** `ddns`
- **Module:** `github.com/rootwarp/ddns`
- **Target platform:** Linux on amd64 and arm64, delivered as a Docker container. Raspberry Pi 4/5 and generic x86-64 home servers are first-class targets.

## Problem Statement

Residential ISPs virtually always hand out public IPs via DHCP rather than statically. The lease may survive for weeks or may rotate in a matter of hours, and there is no in-protocol notification to hosts behind the router when it changes. Home-server operators who expose services (game servers, self-hosted apps, VPN endpoints, SSH bastions, Jellyfin/Plex, reverse-proxied web apps) therefore need a mechanism that continuously reconciles a stable hostname with the current public IP.

The existing DDNS ecosystem has two weaknesses for this audience:

1. **Provider coverage.** The canonical open-source clients — `ddclient`, `inadyn`, `no-ip-updater` — were written around the DynDNS v3 protocol and the handful of free dynamic-DNS hosts that speak it (No-IP, FreeDNS, DuckDNS, Dynu). Google Cloud DNS is not a DynDNS-protocol endpoint; it is a REST API authenticated with Google Cloud IAM. Bolting Cloud DNS onto these clients requires either a shim that fronts the REST API with a DynDNS-protocol facade, or switching to RFC 2136 dynamic-update mode — neither of which is a clean fit.

2. **Operational posture.** Home-server operators who already manage a Google Cloud project for DNS tend to hand-roll a shell script: `curl https://api.ipify.org` → compare to a file in `/tmp` → `gcloud dns record-sets update` on change, scheduled under cron. These scripts are widespread but share predictable defects: they trust a single IP-echo service (so an outage flips the record to an error HTML page), they cache the last IP in a location that does not survive reboot, they have no retry or backoff on Cloud DNS errors, they log nothing useful, and they silently fail when the service account token expires.

`ddns` is the packaged, correct version of that hand-rolled script — no more, no less. It is a small, focused Go binary rather than a general-purpose multi-provider framework.

## Target Users

1. **Home-lab operators on Google Cloud DNS.** They already host at least one zone in Cloud DNS, typically because they wanted IAM-controlled DNS for personal projects, and they run services at home (Jellyfin, game servers, VPN concentrator, Home Assistant, reverse-proxied web apps) that they want reachable under a stable hostname.
2. **Self-hosted service operators behind CGNAT-free consumer ISPs.** Their ISP hands out a real public IP via DHCP, and that IP rotates often enough that a manual update is a recurring chore.
3. **Developers running remote-access gateways** (Tailscale funnel alternatives, SSH bastions, WireGuard endpoints) from a fixed piece of home hardware and binding a friendly DNS name to it.

Explicit non-user: operators behind carrier-grade NAT. `ddns` cannot help if the ISP-assigned IP is shared, because the public IP echoed back is the ISP's NAT pool, not the host. This is documented but not worked around in v1.

## Goals

- Provide a single static binary that, given a config file and Google Cloud credentials, correctly maintains a Cloud DNS `A` record against the host's public IPv4.
- Never publish a wrong record: the daemon must refuse to write when IP detection cannot reach quorum across the configured echo services.
- Never thrash the record: updates are idempotent and only fire when the resolved IP differs from both the local state file and the live Cloud DNS record.
- Survive reboots cleanly: on startup, reconcile from persistent state rather than blindly overwriting the record on the first tick.
- Keep the command surface small and memorable: `ddns run`, `ddns sync`, `ddns status`, `ddns version`.
- Emit structured, greppable logs at every decision point so that a post-incident review can reconstruct exactly what was seen, when, and why an update did or did not happen.

## Non-Goals

- Not a multi-provider client in v1. Cloudflare, Route 53, AWS, Azure, and RFC 2136 endpoints are explicit out-of-scope for v1; the provider interface is designed to make them tractable later but shipping with more than one provider dilutes the scope of the first release.
- Not an IPv6 updater. `AAAA` records are not supported — IPv4 `A` records only.
- Not a router agent. `ddns` does not talk to the router over UPnP/NAT-PMP/SSDP and does not attempt to drive port-forwarding changes.
- Not a web UI, GUI, or mobile app. CLI + logs + optional health endpoint only.
- Not a Kubernetes operator. Deployment is a Docker container; the binary can also be run directly if the operator manages their own process supervision.
- Not a host-service integration. No systemd unit, no launchd agent shipped in v1 — operators who want OS-level supervision wrap the Docker container with their own unit file.
- Not a general-purpose "run arbitrary command when IP changes" hook framework. A single post-update webhook is a P1 consideration; pluggable scripting is out-of-scope.
- Not a secret manager. Google Cloud credentials are supplied via ADC (file path, env var, or metadata server) — `ddns` does not implement its own credential storage.

## User Stories

- As a home-lab operator, I edit `~/.config/ddns/config.yaml` to list my Cloud DNS project, managed zone, and the hostname `home.example.com`, run `ddns run`, and thereafter `home.example.com` resolves to my current public IP without further intervention.
- As a cron user, I run `ddns sync` from `cron` (or any scheduler) every 10 minutes instead of running a daemon, and the command exits 0 on a successful reconciliation, non-zero on error, and prints a single JSON line I can feed to a log aggregator.
- As an operator debugging a networking issue, I run `ddns status` and immediately see the last-known public IP, the last successful update time, the last error (if any), and the resolved current value of the Cloud DNS record.
- As an operator deploying under Docker, I pass the service-account JSON path via `GOOGLE_APPLICATION_CREDENTIALS` and the config via a bind mount, and the container runs until SIGTERM with no unhandled panics across a full DHCP lease cycle.
- As an operator testing a config change, I run `ddns run --dry-run` and see exactly what update would be sent, with the full `Changes.Create` payload logged, without mutating the zone.

## Functional Requirements

### P0 (must ship in v1)

- **Configuration.** YAML config at `~/.config/ddns/config.yaml` (XDG-respecting, overridable via `--config`). Schema: a top-level `records:` list, each with `project`, `managed_zone`, `name` (FQDN), `ttl` (seconds, default 300), plus global `poll_interval` (default `5m`), `resolver` block (list of echo URLs and quorum threshold), and `state_path` (default `~/.local/state/ddns/state.json`).
- **Google Cloud DNS client.** Authenticates via Application Default Credentials (`google.FindDefaultCredentials`), so the same binary works with a service-account JSON file (`GOOGLE_APPLICATION_CREDENTIALS` env var), a user login via `gcloud auth application-default login`, or GCE metadata server. Reads the current record via `ResourceRecordSets.Get`; writes updates as a single `Changes.Create` with both a deletion of the old RRset and an addition of the new one in the same atomic change.
- **Public IP resolution.** HTTPS GET against at least three configurable echo services. Default set: `https://api.ipify.org`, `https://ifconfig.me/ip`, `https://icanhazip.com`. Responses must parse as a single IPv4 address (trim whitespace, validate with `netip.ParseAddr`). Quorum policy: at least 2 of 3 must return the same value for the result to be accepted; otherwise the tick logs a `resolver_no_quorum` event and takes no action.
- **State persistence.** On every successful reconciliation, write `state.json` (mode 0600) recording: last-checked timestamp, last-observed IP, last-update timestamp, last-update result (`noop` / `updated` / `error`), and last-error message if any. Writes are atomic (temp file + rename) to survive power loss mid-write.
- **Change detection.** Before writing, compare the quorum IP against (a) the cached state value and (b) the live record fetched from Cloud DNS. Skip the update if both match. This double-check guards against state-file drift (e.g., after a manual `gcloud` edit).
- **Daemon loop (`ddns run`).** Runs until SIGTERM/SIGINT. On each tick: resolve public IP, reconcile. On unrecoverable config/auth errors at startup, exit non-zero with a clear diagnostic and do not enter the loop. On transient per-tick errors (resolver timeout, 5xx from Cloud DNS), log and continue; on repeated Cloud DNS errors, back off exponentially up to a cap (default 30 minutes).
- **One-shot mode (`ddns sync`).** Single reconciliation pass. Exit 0 if the record is now correct (whether noop or updated), exit 1 on resolver quorum failure, exit 2 on Cloud DNS error, exit 3 on config/auth error.
- **Status command (`ddns status`).** Prints `state.json` contents in a human-readable form by default; `--json` flag prints the raw state document.
- **Dry-run mode (`--dry-run`).** Available on both `run` and `sync`. Performs IP resolution and comparison but never issues a `Changes.Create`; instead logs the payload it would have sent.
- **Structured logging.** All logs are Go 1.22 `log/slog` records. `--log-format=json|text` flag, default `text` on TTY and `json` when stdout is not a TTY. Every tick logs `tick_start`, `ip_resolved` (with per-source values and quorum outcome), and `reconcile_result`.
- **Signal handling.** `SIGTERM` and `SIGINT` trigger a clean shutdown (finish any in-flight Cloud DNS call, flush state, exit 0). `SIGHUP` re-reads the config file.
- **Docker image.** A reference `Dockerfile` and `docker-compose.yml` are shipped under `deploy/docker/` and are referenced from the README. No systemd unit or launchd plist is shipped.

### P1 (v1.1 follow-up)

- **Post-update webhook.** Single configurable HTTP(S) POST fired after a successful update, with a signed JSON body containing the old IP, new IP, record name, and timestamp. One hook URL per config, not a pluggable scripting surface.
- **Health endpoint.** Optional `--health-addr :8080` flag exposes `GET /healthz` (always 200 when the loop is running), `GET /readyz` (200 only after the first successful reconciliation), and `GET /state` (JSON dump of the state file, redacted to omit any secrets).
- **Prometheus metrics.** `/metrics` on the same health endpoint: `ddns_ticks_total`, `ddns_updates_total`, `ddns_resolver_errors_total{source}`, `ddns_provider_errors_total`, `ddns_last_update_timestamp_seconds`.
- **Provider interface second implementation.** Add Cloudflare as the proof-of-refactoring that the provider interface was not Cloud-DNS-shaped. No promise of operational equality with Cloud DNS; Cloud DNS remains the primary supported provider.

### P2 (post-v1 backlog)

- Route 53 and RFC 2136 provider implementations.
- Router-side IP detection via UPnP/NAT-PMP as a quorum source alongside HTTPS echo.
- Container image publication to a public registry.
- Windows support (currently not an author priority).

## Out of Scope

- Any feature that requires `ddns` to hold long-lived secrets on disk itself. Cloud credentials are always delegated to ADC.
- Any feature that requires a background service other than the `ddns` daemon. For example, a host-level unit that watches `/run/dhclient.leases` is deliberately not how this works — `ddns` is self-contained and poll-driven.
- Replacing or wrapping `gcloud`. `ddns` talks directly to the Cloud DNS REST API via the `google.golang.org/api/dns/v1` client library.

## Assumptions

- The host has at least sporadic outbound HTTPS connectivity. If the network is down, `ddns` correctly logs resolver failures and takes no action.
- The Cloud DNS managed zone already exists and is authoritative for the record's parent domain. `ddns` does not create zones.
- The record `ddns` manages is either pre-existing (of any `rrdatas` value — it will be overwritten on first run) or absent (in which case the first update creates it).
- Clock drift on the host is bounded; state-file timestamps and log timestamps are informational, not used for correctness.

## Success Metrics

- **Correctness.** Over a 30-day soak on the author's home network, zero events where `ddns` wrote a wrong IP to Cloud DNS. "Wrong" means the value did not match the host's actual public IP at the time of the write.
- **Stability.** Zero unhandled panics, zero goroutine leaks, and zero state-file corruption across the soak period.
- **Quiescence.** On an unchanging public IP, the daemon issues zero `Changes.Create` calls — verified by log inspection and by the Cloud DNS audit log.
- **Reaction time.** Median time from actual IP change to Cloud DNS record reflecting the new IP is within `poll_interval + 30s` over the soak period.

## Open Questions

- Should the default poll interval be lower than 5 minutes? Residential DHCP leases sometimes change within 2–3 minutes; 5 minutes is a safe default for API-quota reasons but may be slow for VPN operators. Resolution: ship 5 minutes as the default and accept feedback before v1.1.
- Should `ddns` refuse to update when the observed IP is in a private/loopback/link-local range? The answer is yes — this is a cheap defense against a compromised or spoofed echo service returning `127.0.0.1` — and will be implemented as a P0 sanity check, not deferred. Captured here in Open Questions because the policy was debated during drafting; closed for v1.
