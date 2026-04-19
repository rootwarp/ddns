# ddns

Dynamic DNS updater for Google Cloud DNS. One Go binary, one config file,
one reconcile loop — keep an `A` record pointed at your home's current
public IPv4 and stop hand-rolling `curl | gcloud` scripts.

[![CI](https://github.com/rootwarp/ddns/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/rootwarp/ddns/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/rootwarp/ddns.svg)](https://pkg.go.dev/github.com/rootwarp/ddns)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

---

## Why ddns

`ddns` is the packaged, correct version of the `curl api.ipify.org | gcloud
dns record-sets update` shell script that every home-lab operator
eventually writes: it polls three redundant HTTPS echo services, takes a
quorum, fetches the live Cloud DNS record, and issues a single atomic
`Changes.Create` only when both a local state file and the live record
disagree with the observed IP. It is **narrow on purpose** — Google Cloud
DNS only, IPv4 `A` records only, single binary, no plugins. If you want a
multi-provider framework, look elsewhere; if you already run Cloud DNS and
want a correct, observable, scope-locked DDNS updater for it, this is the
tool.

- **Providers:** Google Cloud DNS only (Cloudflare is Phase 7 / v1.1).
- **Record types:** `A` only. IPv6 / `AAAA` is an explicit non-goal.
- **Platforms:** Linux `amd64` and `arm64`. No Windows, no BSD.
- **Audience:** operators who already know what DDNS and Cloud DNS are.

---

## Install

Pick the path that matches your audience:

### Via `go install` — for operators who already have Go

```sh
go install github.com/rootwarp/ddns/cmd/ddns@latest
```

Requires Go 1.26.0 or newer. Installs to `$GOBIN` (default `$HOME/go/bin`).

### Via Docker — recommended deployment

```sh
docker pull ghcr.io/rootwarp/ddns:latest
docker run --rm ghcr.io/rootwarp/ddns:latest version
```

See [Docker deployment](#docker-deployment) below for the compose layout.

### From source — for contributors

```sh
git clone https://github.com/rootwarp/ddns.git
cd ddns
make build             # produces ./bin/ddns
./bin/ddns version
```

---

## Quick start

```sh
# 1. Authenticate (one-time, local testing)
gcloud auth application-default login

# 2. Copy the annotated template and edit it
curl -fsSL https://raw.githubusercontent.com/rootwarp/ddns/develop/config.example.yaml \
    -o /etc/ddns/config.yaml

# 3. Dry-run: see the reconcile without writing
ddns sync --config /etc/ddns/config.yaml --dry-run

# 4. First real reconcile (creates the record on first run)
ddns sync --config /etc/ddns/config.yaml

# 5. Run as a daemon
ddns run --config /etc/ddns/config.yaml
```

Full step-by-step walk-through — GCP zone, service-account key, config,
first reconcile, cron vs daemon, troubleshooting — lives in
[`docs/getting-started-gcp.md`](docs/getting-started-gcp.md). Read that
before your first deployment; the rest of this README is reference.

---

## Minimum config example

The smallest config that passes validation:

```yaml
# /etc/ddns/config.yaml
records:
  - project: my-home-lab
    managed_zone: example-com
    name: home.example.com.
```

Defaults fill in `poll_interval: 5m`, `state_path: ~/.local/state/ddns`,
`log_format: auto`, the three default echo sources with quorum 2,
`type: A`, and `ttl: 300`. For the full annotated template — every key,
every default, every range — copy [`config.example.yaml`](config.example.yaml).

---

## Docker deployment

Recommended shape, pasted from
[`deploy/docker/docker-compose.yml`](deploy/docker/docker-compose.yml):

```yaml
services:
  ddns:
    image: ghcr.io/rootwarp/ddns:latest
    container_name: ddns
    restart: unless-stopped
    environment:
      DDNS_LOG_FORMAT: json
      GOOGLE_APPLICATION_CREDENTIALS: /etc/ddns/credentials.json
    volumes:
      - ./config.yaml:/etc/ddns/config.yaml:ro
      - ./credentials.json:/etc/ddns/credentials.json:ro
      - ddns-state:/var/lib/ddns
    command: ["run", "--config", "/etc/ddns/config.yaml"]

volumes:
  ddns-state:
```

Layout in the working directory:

```
./docker-compose.yml       (the file above)
./config.yaml              (bind-mounted read-only)
./credentials.json         (GCP service-account JSON, 0600)
```

Set `state_path: /var/lib/ddns` inside `config.yaml` so per-record state
writes land in the named volume and survive container recreation.

Bring it up:

```sh
docker compose up -d
docker logs -f ddns
```

Reload config without a restart:

```sh
docker kill -s HUP ddns
```

Operators who want OS-level supervision wrap the container with their own
systemd unit pointing at `docker run` — ddns deliberately does not ship a
unit file. Day-two ops (resync, key rotation, state migration) are
documented in [`docs/operations.md`](docs/operations.md).

---

## Command reference

Four subcommands, each one-sentence-describable:

- **`ddns run`** — long-lived daemon. Reconciles on startup and every
  `poll_interval`. Honors `SIGHUP` (config reload), `SIGTERM`/`SIGINT`
  (clean shutdown). Applies exponential backoff on provider transients.
- **`ddns sync`** — one-shot reconcile. Exit 0 on success (noop or
  updated), 1 on resolver quorum failure, 2 on provider transient, 3 on
  config/auth fatal. Suitable under `cron`.
- **`ddns status`** — prints the per-record state file contents. Reads
  local state only, never calls Cloud DNS. `--json` emits the raw state
  document.
- **`ddns version`** — prints version, commit, build date. Does not read
  config; safe on any host.

Both `run` and `sync` accept `--dry-run`, which performs resolution and
comparison but logs the payload it *would* have sent instead of issuing
`Changes.Create`. Use this to validate config changes without mutating
the zone.

Full flag surface: `ddns --help` and `ddns <subcommand> --help`. Exit
codes and their mapping to log events are enumerated in
[`docs/log-events.md`](docs/log-events.md).

---

## Troubleshooting

Common failure modes and the one-line fix. The full table with worked
examples lives in
[`docs/getting-started-gcp.md#9-troubleshooting`](docs/getting-started-gcp.md#9-troubleshooting).

| You see                                        | Cause                              | Fix                                                                 |
|------------------------------------------------|------------------------------------|---------------------------------------------------------------------|
| `auth: authentication failed`                   | ADC unreachable                    | Bind-mount the SA JSON and set `GOOGLE_APPLICATION_CREDENTIALS`.   |
| `resolver: no quorum among echo services`       | Outbound HTTPS blocked             | Verify `curl -sS https://api.ipify.org` and siblings work.         |
| `provider upsert: … code=403`                   | SA missing `roles/dns.admin`       | Re-grant via `gcloud projects add-iam-policy-binding …`.           |
| `provider upsert: … code=404`                   | `managed_zone` typo (not DNS name) | `gcloud dns managed-zones list`; first column is the value needed. |
| `record type "AAAA" not supported in v1`        | IPv6 is out of scope               | v1 only updates `A`. See non-goals.                                |

For log grep patterns and the full event taxonomy, see
[`docs/log-events.md`](docs/log-events.md).

---

## FAQ

**Why only Google Cloud DNS?**
Scope. v1 focuses on making one provider correct rather than making many
providers shallow. Cloudflare lands in Phase 7 as the proof-of-refactoring
that the `DNSProvider` interface was not Cloud-DNS-shaped.

**Why no systemd unit or launchd plist?**
Operators who want OS-level supervision already have opinions about unit
files, user/group, `ProtectSystem`, `StateDirectory`, journald routing,
etc. Shipping a unit file that's half-right for everyone is worse than
shipping none. Wrap `docker run` or the installed binary with your own
unit; see
[`docs/getting-started-gcp.md#option-b--long-lived-daemon-ddns-run`](docs/getting-started-gcp.md#option-b--long-lived-daemon-ddns-run)
for a worked example.

**Why no IPv6 (`AAAA`)?**
Explicit non-goal. Residential IPv6 deployment is sparse enough, and
IPv6-reachable home services rare enough, that the feature's carrying
cost outweighs its value at v1. Re-evaluated in v2 if the landscape
changes.

**Can I run it on Windows?**
No. Cross-compile works (`GOOS=windows go build`) but none of the
integration tests or the deployment shape (Docker) target Windows and
nothing is validated there. If this matters to you, file an issue with a
concrete use case.

**How do I contribute?**
Read [`CONTRIBUTING.md`](CONTRIBUTING.md) and
[`plan/bootstrap/project-plan.md`](plan/bootstrap/project-plan.md) to see
where the project is in its phase sequence. Open issues against the phase
you're touching; PRs come from `feature/<phase>-<slug>` branches onto
`develop`, never `main`.

---

## Integration tests

A live-Cloud-DNS integration test is maintained under
`internal/dnsprovider/gcp/gcp_integration_test.go`, gated behind the
`integration` Go build tag so it never runs as part of `go test ./...` or in
CI. The test performs one full reconcile cycle (create/update -> get ->
idempotent upsert), then cleans up via a pure-deletion `Changes.Create`.

### Prerequisites

All three environment variables must be set or the test self-skips:

| Variable | Meaning |
|---|---|
| `DDNS_INTEGRATION_PROJECT` | GCP project ID that owns the managed zone. |
| `DDNS_INTEGRATION_ZONE` | Managed-zone name (not the DNS domain). |
| `DDNS_INTEGRATION_RECORD` | FQDN inside the zone, trailing-dot form (e.g., `ddns-integration.example.com.`). |

Credentials come from Application Default Credentials (ADC) — the same path
the daemon uses. The easiest local setup is `gcloud auth
application-default login`.

### Running

```sh
export DDNS_INTEGRATION_PROJECT=my-home-lab
export DDNS_INTEGRATION_ZONE=example-com
export DDNS_INTEGRATION_RECORD=ddns-integration.example.com.
make integration-test
```

### Safety

The test writes addresses from the TEST-NET-3 reserved range (`203.0.113.0/24`,
RFC 5737). No value written to the record ever directs traffic at a real
host, so even if cleanup races the worst-case residual state points at
documentation-only IP space. The cleanup is idempotent, so running the test
twice in a row works: the second pass observes "already absent" and proceeds.

Running the test produces exactly two `dns.changes.create` audit-log entries
per run — one update, one delete — which is the signal to check in the
Cloud Console audit log after a run to confirm behavior end-to-end.

---

## License

[MIT](LICENSE). The `ddns` project is maintained by Joonkyo Kim
([@rootwarp](https://github.com/rootwarp)).
