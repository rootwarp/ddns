# Getting started with ddns and Google Cloud DNS

End-to-end setup: from an empty Google Cloud project to a `ddns` daemon keeping
an `A` record pointed at your current public IPv4. Written for operators who
already know what DDNS and Cloud DNS are and want to run the thing.

This guide assumes:

- You have billing enabled on a Google Cloud project.
- You own (or control) a DNS domain.
- You have Go 1.26+ installed on the host that will run `ddns`.
- The host has unrestricted outbound HTTPS to the three echo services
  (`api.ipify.org`, `ifconfig.me`, `icanhazip.com`) and to `dns.googleapis.com`.

Takes ~15 minutes.

---

## 1. Prepare the Cloud DNS managed zone

Skip this section if you already have a public Cloud DNS zone authoritative
for your domain.

```sh
# One-time — set your project and zone naming.
export PROJECT_ID=my-home-lab
export ZONE_NAME=example-com            # lowercase, hyphens; NOT the DNS name
export DNS_NAME=example.com.            # trailing dot is required
export RECORD=home.example.com.         # the hostname ddns will update

gcloud config set project "$PROJECT_ID"

# Enable the Cloud DNS API.
gcloud services enable dns.googleapis.com

# Create the public managed zone.
gcloud dns managed-zones create "$ZONE_NAME" \
  --dns-name="$DNS_NAME" \
  --description="Public zone managed by ddns" \
  --visibility=public

# Print the NS records and point your registrar at them.
gcloud dns managed-zones describe "$ZONE_NAME" \
  --format="value(nameServers)"
```

Update your registrar's name servers to the four Cloud DNS servers printed
above, then wait for propagation (5 minutes to a few hours). You don't have
to wait before continuing — `ddns` will work as soon as it can talk to the
Cloud DNS API, and the record will be live once propagation catches up.

> **Note.** `ddns` in v1 **only updates existing zones**; it does not create
> zones. It *will* create the `A` record inside an existing zone on first run
> if the record is absent.

---

## 2. Create a service account for `ddns`

A dedicated service account with the narrowest Cloud DNS role. The record
lives inside one zone; we grant access to the whole project (IAM does not
scope finer than project for Cloud DNS admin roles in v1 of that API).

```sh
export SA_NAME=ddns-writer
export SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

gcloud iam service-accounts create "$SA_NAME" \
  --display-name="ddns record updater"

# dns.admin is the canonical write role. roles/dns.editor also works if you
# prefer to withhold IAM-modification inside the DNS scope.
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="roles/dns.admin"
```

---

## 3. Hand the service account's credentials to `ddns`

Pick the option that matches where you're running `ddns`.

### Option A — Your laptop, for a first test

```sh
gcloud auth application-default login
```

This writes an ADC credential file under `~/.config/gcloud/` that `ddns`
picks up automatically through the Google Cloud Go client's default chain.
No service-account JSON needed; the login uses your human identity. Use this
only for local testing — do not deploy it.

### Option B — A home server or VM (JSON key)

```sh
# One-time on the workstation with the SA create permission:
gcloud iam service-accounts keys create ./ddns-key.json \
  --iam-account="$SA_EMAIL"

# Copy ./ddns-key.json to the target host, 0600, root-owned:
scp ./ddns-key.json user@home-server:/etc/ddns/credentials.json
ssh user@home-server "sudo chown root:root /etc/ddns/credentials.json \
                   && sudo chmod 0600 /etc/ddns/credentials.json"

# On the target host, set the env var so ADC picks it up:
export GOOGLE_APPLICATION_CREDENTIALS=/etc/ddns/credentials.json
```

Rotate the key when the person who minted it leaves, or on a schedule.
`gcloud iam service-accounts keys list/delete` handles rotation.

### Option C — GCE VM

If `ddns` runs on a Compute Engine instance, attach the service account to
the VM and omit the JSON file entirely. ADC auto-discovers the metadata
server:

```sh
gcloud compute instances create home-server \
  --service-account="$SA_EMAIL" \
  --scopes="https://www.googleapis.com/auth/ndev.clouddns.readwrite"
```

No `GOOGLE_APPLICATION_CREDENTIALS` environment variable needed; no key file
to rotate.

---

## 4. Install `ddns`

```sh
# Requires Go 1.26.0+.
go install github.com/rootwarp/ddns/cmd/ddns@latest

# Or build from source:
git clone https://github.com/rootwarp/ddns.git
cd ddns
make build                               # produces ./bin/ddns
sudo install -m 0755 bin/ddns /usr/local/bin/ddns
```

Verify:

```sh
ddns version
# ddns v0.x.x (commit abc1234, built 2026-04-19T12:00:00Z)

ddns --help
```

> Docker images will ship with `v1.0.0` (Phase 5). For now, `go install` is
> the supported install path.

---

## 5. Write the config

Save this as `/etc/ddns/config.yaml` (root-owned, `0644`). Edit `project`,
`managed_zone`, and the record's `name` to match your setup.

```yaml
# /etc/ddns/config.yaml

# How often the daemon reconciles. 5m is the default; tighten only if your
# ISP rotates IPs aggressively. Each tick makes 3 outbound HTTPS calls plus
# 1 Cloud DNS Get (sometimes + 1 Changes.Create on drift).
poll_interval: 5m

# Where per-record state files live. Persists across restarts so a reboot
# doesn't cause a spurious write. 0700 dir, 0600 files.
state_path: /var/lib/ddns

# "auto" picks text on a TTY, JSON when piped. "json" forces JSON for
# log aggregators.
log_format: auto

resolver:
  # The default three; override if one is blocked from your network.
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
    - https://icanhazip.com
  quorum: 2            # 2-of-3 is the safe default. 3-of-3 is too brittle.
  timeout: 10s

records:
  - project: my-home-lab           # your GCP project ID
    managed_zone: example-com      # Cloud DNS managed-zone name (NOT the DNS name)
    name: home.example.com.        # trailing dot required
    type: A
    ttl: 300                       # seconds; 30–86400 accepted
```

### Validate the config without doing anything

```sh
ddns sync --config /etc/ddns/config.yaml --dry-run
```

On success you'll see something like:

```
INFO startup poll_interval=5m config=/etc/ddns/config.yaml
INFO dry_run_mode_active
INFO tick_start
INFO ip_resolved ip=198.51.100.42 quorum=2
INFO reconcile_dry_run record=home.example.com. would_send_name=home.example.com. \
     would_send_type=A would_send_ttl=300 would_send_old=<nil> would_send_new=[198.51.100.42]
```

and the process exits 0. No Cloud DNS write happens — the daemon logs exactly
what it would have done. If you see an error:

- `config: validation failed` → fix the YAML and retry (see §8).
- `auth: authentication failed` → ADC isn't reachable (see §3).
- `resolver: no quorum among echo services` → the host can't reach the
  three public IP services (see §8).

---

## 6. Do the first real reconcile

With the config valid:

```sh
ddns sync --config /etc/ddns/config.yaml
```

This runs one tick and exits. On a fresh zone with no existing record, you'll
see `reconcile_updated` with `old=<nil>` — meaning the record was created.
On subsequent runs with an unchanged IP, you'll see `reconcile_noop`.

Confirm from the outside:

```sh
dig +short home.example.com. @8.8.8.8
# 198.51.100.42
```

DNS propagation may take up to the record's TTL (300s with the default config)
after the initial change.

---

## 7. Run as a daemon

Two options depending on your operational preference.

### Option A — cron + `ddns sync` (simplest)

`ddns sync` is idempotent: if the record already matches, it exits 0 without
touching the provider. Run it under `cron` every 5 minutes:

```cron
# /etc/cron.d/ddns
*/5 * * * * root GOOGLE_APPLICATION_CREDENTIALS=/etc/ddns/credentials.json \
            /usr/local/bin/ddns sync --config /etc/ddns/config.yaml \
            >> /var/log/ddns.log 2>&1
```

Exit code discipline (per `docs/log-events.md`):

| Exit | Meaning                                      |
|------|----------------------------------------------|
| 0    | Success (noop or updated)                    |
| 1    | Resolver could not reach quorum              |
| 2    | Provider transient error (retry next tick)   |
| 3    | Config or auth error (operator must fix)     |

Cron will silently keep running on exit 1 / 2 (these are self-healing).
Exit 3 is worth alerting on.

### Option B — long-lived daemon (`ddns run`)

Use this if you want SIGHUP reload and in-process backoff rather than cron's
"try every 5 minutes regardless" semantics.

```sh
ddns run --config /etc/ddns/config.yaml
```

The daemon:

- Reconciles on startup (first tick is immediate).
- Reconciles every `poll_interval`.
- Applies exponential backoff on provider transients (capped at 30 min).
- Exits non-zero after 5 consecutive `auth`/`config` errors (supervisor restart).
- Reloads config on `SIGHUP` (no restart needed).
- Exits cleanly on `SIGTERM` / `SIGINT`.

Wrap with whatever supervisor you prefer. Minimal systemd unit:

```ini
# /etc/systemd/system/ddns.service
[Unit]
Description=ddns — dynamic DNS updater for Google Cloud DNS
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ddns run --config /etc/ddns/config.yaml
Environment=GOOGLE_APPLICATION_CREDENTIALS=/etc/ddns/credentials.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
# Tighten these if your systemd is recent enough:
DynamicUser=no
User=ddns
Group=ddns
StateDirectory=ddns
ReadWritePaths=/var/lib/ddns
ProtectSystem=strict
ProtectHome=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

`state_path: /var/lib/ddns` in the config must match `StateDirectory=ddns`
and the user the daemon runs as.

> v1 ships no systemd unit or launchd plist; the above is an example, not
> shipped. Phase 5 will add a Docker deployment; until then, this is the
> reference.

---

## 8. Verify and inspect

```sh
# What does ddns think the current state is?
ddns status --config /etc/ddns/config.yaml

# Machine-readable:
ddns status --config /etc/ddns/config.yaml --json | jq .

# Force a resync (useful after manual DNS edits):
ddns sync --config /etc/ddns/config.yaml

# Under systemd:
journalctl -u ddns -f
systemctl reload ddns         # sends SIGHUP → config reload without restart
systemctl status ddns
```

`ddns status` only reads local state files — it does not call Cloud DNS.
For ground-truth comparison use `dig`.

---

## 9. Troubleshooting

| Symptom                                                  | Cause                                  | Fix |
|---|---|---|
| `auth: authentication failed`                             | ADC not found or wrong               | §3: verify `GOOGLE_APPLICATION_CREDENTIALS` points at a readable JSON, or run `gcloud auth application-default login`. |
| `resolver: no quorum among echo services`                 | Outbound HTTPS blocked               | `curl -sS https://api.ipify.org` and the other two must return a public IP. Proxy / captive portal / firewall is the usual culprit. |
| `provider upsert: … code=403`                             | SA missing `roles/dns.admin`         | §2: re-grant. |
| `provider upsert: … code=404`                             | `managed_zone` name wrong in config  | `gcloud dns managed-zones list` — the first column is the value `config.records[].managed_zone` needs. |
| `record type "AAAA" not supported in v1`                  | IPv6 is explicitly out of scope      | v1 only updates `A` records. |
| `resolver_no_quorum` but one source returned a valid IP   | Results disagreed; quorum needs 2+   | Usually transient; next tick recovers. Persistent → one source is returning a stale / wrong answer; drop it from `resolver.sources` in config. |
| All sources return a private / CGNAT IP                   | You're behind CGNAT                  | `ddns` cannot help on CGNAT (your ISP does not give you a routable IP). |
| `ddns status` shows `last_result=error` repeatedly        | Read the `last_error` field          | Maps directly to one of the rows above. |
| The record updates but `dig` still shows the old IP       | Downstream resolver caching          | Wait up to TTL (300s default). Check `dig @ns-cloud-a1.googledomains.com.` for Cloud DNS's own view. |

### Reading the logs

Every log line is a structured `slog` record. Full taxonomy in
[`log-events.md`](log-events.md). Quick greps:

```sh
# What IP did we last observe and write?
journalctl -u ddns | rg reconcile_updated | tail -5

# Has the resolver been flaky?
journalctl -u ddns | rg resolver_no_quorum

# Config reload in flight?
journalctl -u ddns | rg config_reload

# Backoff currently engaged?
journalctl -u ddns | rg backoff_applied
```

---

## 10. What `ddns` does not do

To calibrate expectations, v1 is scoped tightly. The following are **not**
supported:

- IPv6 (`AAAA` records) — explicit non-goal.
- Non-Google Cloud DNS providers — Cloudflare lands in v1.1 (Phase 7).
- Creating zones — `ddns` only operates inside an existing managed zone.
- Route 53, RFC 2136, DynDNS-compatible protocols — all in the Phase 8 backlog.
- Running on Windows — not in scope for v1.
- A webhook / health endpoint / Prometheus metrics — all Phase 7 (v1.1).

If one of these is a blocker, file an issue against the relevant PRD line.

---

## 11. Next steps

- Read [`docs/log-events.md`](log-events.md) if you plan to hook ddns into a
  log aggregator (Loki, Elastic, GCP Logging).
- See [`plan/bootstrap/issues/phase-2-real-loop.md`](../plan/bootstrap/issues/phase-2-real-loop.md)
  §2.8 for the integration-test harness if you want to run ddns against a
  throwaway zone in CI.
- Phase 5 (upcoming) adds a reference Docker Compose deployment; watch the
  `develop` branch.
