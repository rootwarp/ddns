# Day-two operations

Tactical reference for operators already running `ddns`. Each subsection
is a command recipe; narrative lives in
[`getting-started-gcp.md`](getting-started-gcp.md). Log events referenced
below are enumerated in [`log-events.md`](log-events.md).

Commands are shown for the Docker deployment shape
([`deploy/docker/docker-compose.yml`](../deploy/docker/docker-compose.yml));
where the binary path matters, the raw-binary equivalent is given after
the Docker one.

---

## Reading logs

Every log line is a `slog` record. When running under Docker, the
container's stdout is JSON (see `DDNS_LOG_FORMAT: json` in compose) and
`docker logs` is the ground truth:

```sh
# Tail live:
docker logs -f ddns

# Last hour, filtered to reconcile outcomes:
docker logs --since 1h ddns 2>&1 | jq -c 'select(.msg | test("^reconcile_"))'

# Every update event with old/new IPs:
docker logs ddns 2>&1 | jq -c 'select(.msg == "reconcile_updated") | {at: .time, old, new, record}'

# Resolver flakiness, last 24h:
docker logs --since 24h ddns 2>&1 | jq -c 'select(.msg == "resolver_no_quorum")'
```

Under systemd with the raw binary:

```sh
journalctl -u ddns -f
journalctl -u ddns --since "1 hour ago" | rg 'reconcile_updated|reconcile_error'
```

The full event taxonomy — required attrs, emitting package, exit-code
mapping — is in [`log-events.md`](log-events.md). If you are piping logs
into Loki / Elastic / GCP Logging, the `msg` field is the event name and
is stable across releases.

---

## Forcing a resync

Useful after a manual `gcloud` edit, after a network hiccup you want to
confirm recovered from, or to validate a config change before letting the
daemon's own ticker catch up.

`ddns sync` inside the running container shares the state-file volume with
the daemon, so both processes stay consistent:

```sh
# One-shot reconcile (prints exit code, no daemon restart):
docker exec ddns /ddns sync --config /etc/ddns/config.yaml

# Dry-run: see the payload without writing:
docker exec ddns /ddns sync --config /etc/ddns/config.yaml --dry-run

# Reload config in the running daemon (no sync, just re-read the file):
docker kill -s HUP ddns
```

Raw-binary equivalents (same semantics, different invocation):

```sh
ddns sync --config /etc/ddns/config.yaml
kill -HUP $(pidof ddns)
```

`sync` exits `0` on success (noop or updated), `1` on quorum failure, `2`
on provider transient, `3` on config/auth fatal — scriptable under cron or
a post-deploy hook.

---

## Rotating the service-account key

Rotate the key you minted in step 2 of `getting-started-gcp.md` on a
schedule (quarterly is a defensible default) or immediately when the
person who minted it leaves.

```sh
# 1. Mint a new key. Requires the SA-create permission from a workstation:
export PROJECT_ID=my-home-lab
export SA_EMAIL="ddns-writer@${PROJECT_ID}.iam.gserviceaccount.com"
gcloud iam service-accounts keys create ./ddns-key-new.json \
  --iam-account="$SA_EMAIL"

# 2. Copy the new key onto the host, alongside the old one.
scp ./ddns-key-new.json home-server:/etc/ddns/credentials.json.new

# 3. Swap atomically. ddns reads credentials on each request (via ADC), so
#    rename + SIGHUP is enough; no container restart needed.
ssh home-server '
  sudo chown root:root /etc/ddns/credentials.json.new &&
  sudo chmod 0600     /etc/ddns/credentials.json.new &&
  sudo mv /etc/ddns/credentials.json.new /etc/ddns/credentials.json &&
  docker kill -s HUP ddns
'

# 4. Wait one poll_interval, verify success:
docker logs --since 10m ddns | rg 'reconcile_(noop|updated)'

# 5. Delete the old key in GCP (list first to find its id):
gcloud iam service-accounts keys list --iam-account="$SA_EMAIL"
gcloud iam service-accounts keys delete <OLD_KEY_ID> \
  --iam-account="$SA_EMAIL"
```

If step 4 shows `auth: authentication failed`, roll back: the new key was
malformed or the old key was already deleted. Restore the previous JSON
from backup and investigate before retrying.

---

## Migrating state between hosts

The per-record state file (`state/<sha256(name)>.json`) under the
`ddns-state` volume is portable — no host-specific paths, no secrets, no
inode-sensitive fields. Moving a deployment to a new host means copying
the volume and swapping the container.

```sh
# On the old host: export the named volume to a tarball.
docker run --rm \
  -v ddns-state:/from \
  -v "$PWD":/to \
  alpine tar czf /to/ddns-state.tar.gz -C /from .

# Ship it.
rsync -av ddns-state.tar.gz newhost:/tmp/

# On the new host: stop any running ddns, then restore.
docker compose stop ddns
docker volume create ddns-state
docker run --rm \
  -v ddns-state:/to \
  -v /tmp:/from \
  alpine sh -c 'cd /to && tar xzf /from/ddns-state.tar.gz'
docker compose up -d ddns

# Verify the new host reconciles without a redundant write:
docker logs --tail 50 ddns | rg reconcile_
```

You should see `reconcile_noop` on the first tick after the cutover — the
state copy convinced ddns that the record is already correct. If you see
`reconcile_updated` instead, either the old host's state drifted before
export or the record was mutated externally in the interim; either way the
write is correct, just not what you expected.

---

## Changing the TTL

Edit `config.yaml`, reload with SIGHUP, let the next tick observe TTL
drift and trigger a single update.

```sh
# 1. Edit the TTL:
sudo vi /etc/ddns/config.yaml
# ... change `ttl: 300` to `ttl: 600`

# 2. Reload:
docker kill -s HUP ddns

# 3. Wait one poll_interval, confirm the update landed:
docker logs --since 10m ddns | rg reconcile_updated

# 4. Verify externally:
dig +short home.example.com. @8.8.8.8
dig home.example.com. @8.8.8.8 | rg -A0 'ANSWER SECTION' -A2
```

The next reconcile after reload will write the new TTL even if the IP is
unchanged — TTL drift is a real drift condition, not cosmetic.

---

## Adding a new record

New records join the existing fan-out on the next tick; no restart
needed.

```sh
# 1. Append the new record block under `records:` in config.yaml.
sudo vi /etc/ddns/config.yaml
# records:
#   - project: my-home-lab
#     managed_zone: example-com
#     name: home.example.com.
#     type: A
#     ttl: 300
#   - project: my-home-lab            # <-- new
#     managed_zone: example-com       # <-- new
#     name: media.example.com.        # <-- new
#     type: A                         # <-- new
#     ttl: 300                        # <-- new

# 2. Reload:
docker kill -s HUP ddns

# 3. Confirm the reload parsed the new record:
docker logs --since 1m ddns | rg config_reload_ok

# 4. Force a sync so the new record is created now instead of on the next
#    poll_interval boundary:
docker exec ddns /ddns sync --config /etc/ddns/config.yaml

# 5. Confirm ddns now tracks both records:
docker exec ddns /ddns status --config /etc/ddns/config.yaml
```

`ddns status` reads all per-record state files under `state_path` and
prints them. A missing new-record state line after step 4 means the sync
didn't run — check for `config_reload_failed` in the logs and fix the
YAML.

---

## Adding a second provider (v1.1 preview)

**Not available in v1.0.** Multi-provider support — specifically
Cloudflare — is scheduled for Phase 7 / v1.1. When it lands, adding a
second provider will be a per-record config change (`provider:
cloudflare` alongside the existing `project` / `managed_zone` keys for
GCP), not a rewrite. Until v1.1, the provider is always Google Cloud DNS;
attempting to set any other value rejects at config load with a clear
error.

Track progress in
[`plan/bootstrap/project-plan.md`](../plan/bootstrap/project-plan.md)
under "Phase 7 — v1.1 Follow-ups".

---

## Related

- [`getting-started-gcp.md`](getting-started-gcp.md) — first-time setup and
  the full troubleshooting table.
- [`log-events.md`](log-events.md) — structured-log event taxonomy and
  exit-code mapping.
- [`../README.md`](../README.md) — install paths and command reference.
- [`../deploy/docker/docker-compose.yml`](../deploy/docker/docker-compose.yml) —
  the compose file every command here assumes.
