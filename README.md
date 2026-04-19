# ddns

Dynamic DNS updater for Google Cloud DNS. `ddns run` reconciles one or more
`A` records against the host's current public IPv4 on a polling interval.
`ddns sync` is the one-shot variant suitable for `cron`. `ddns status`
prints the last-observed state.

- **Status:** walking-skeleton-through-hardening complete on `develop`; `v1.0.0`
  release is Phase 5–6 away.
- **Supported providers:** Google Cloud DNS (Cloudflare in v1.1).
- **Supported record types:** `A` (IPv4 only; IPv6 is an explicit non-goal).

## Documentation

- [Getting started with ddns and Google Cloud DNS](docs/getting-started-gcp.md) —
  set up a managed zone, a service account, install the binary, write the
  config, and run the first reconcile.
- [Log event taxonomy](docs/log-events.md) — canonical list of every
  structured log event ddns emits, with required attributes and exit-code
  mapping.

## Quick start

```sh
# 1. Install (requires Go 1.26+)
go install github.com/rootwarp/ddns/cmd/ddns@latest

# 2. Authenticate (one-time, for local testing)
gcloud auth application-default login

# 3. Write /etc/ddns/config.yaml (see getting-started-gcp.md)

# 4. Dry-run: see what would be written without writing
ddns sync --config /etc/ddns/config.yaml --dry-run

# 5. First real reconcile
ddns sync --config /etc/ddns/config.yaml

# 6. As a long-lived daemon with SIGHUP reload
ddns run --config /etc/ddns/config.yaml
```

Full step-by-step walk-through: [`docs/getting-started-gcp.md`](docs/getting-started-gcp.md).

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
