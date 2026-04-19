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

# Phase 5: Docs and Deploy

**Goal.** Everything a new user needs to adopt the tool, and everything future-us needs to cut a release with confidence. One reference deployment shape (Docker), a rewritten README, a day-two operations doc, and the release-gate runbook that Phase 6 follows. No systemd unit or launchd plist ships — operators who want OS-level supervision wrap the Docker container with their own init.

**Entry criteria.** Phase 4 complete — daemon is hardened, log taxonomy finalized, help text polished.

**Exit criteria.** A new contributor can, with only the README, install the binary, write a config, and run a successful `ddns sync` against their own Cloud DNS project. Release-gate runbook committed.

---

## Issue 5.1: `deploy/docker/Dockerfile` and `docker-compose.yml`

**Story points:** 2

**Phase:** 5

**Depends on:** Phase 4 complete

**PRD requirement:** P0 deployment shape (sole deployment shape in v1)

**Architecture modules touched:** none

**Description.** Multi-stage Dockerfile: build stage uses `golang:1.22-alpine` for compilation, final stage is `gcr.io/distroless/static-debian12:nonroot` for the smallest secure runtime surface. The image is explicitly statically linked so the distroless base is sufficient. Also commit a `docker-compose.yml` with a bind-mounted config and a bind-mounted service-account JSON, plus a named volume for state.

**Implementation notes:**
- `deploy/docker/Dockerfile`:
  ```dockerfile
  # syntax=docker/dockerfile:1.6
  FROM golang:1.22-alpine AS build
  WORKDIR /src
  COPY go.mod go.sum ./
  RUN go mod download
  COPY . .
  ARG VERSION=dev
  ARG COMMIT=unknown
  ARG BUILD_DATE=unknown
  RUN CGO_ENABLED=0 GOOS=linux \
      go build -trimpath \
        -ldflags "-s -w \
          -X github.com/rootwarp/ddns/internal/version.Version=${VERSION} \
          -X github.com/rootwarp/ddns/internal/version.Commit=${COMMIT} \
          -X github.com/rootwarp/ddns/internal/version.BuildDate=${BUILD_DATE}" \
        -o /out/ddns ./cmd/ddns

  FROM gcr.io/distroless/static-debian12:nonroot
  COPY --from=build /out/ddns /ddns
  USER nonroot:nonroot
  ENTRYPOINT ["/ddns"]
  CMD ["run", "--config", "/etc/ddns/config.yaml"]
  ```
- `-trimpath` + `-s -w` keep the binary small and reproducible-ish.
- `nonroot` user (uid 65532) is the distroless default.
- `deploy/docker/docker-compose.yml`:
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
- The state volume ensures the per-record state file survives container recreation. The operator's `config.yaml` must set `state_path: /var/lib/ddns` to match.
- Add a CI job: `build image + run ddns version`:
  ```yaml
  - name: Build Docker image
    run: docker build -t ddns:ci -f deploy/docker/Dockerfile .
  - name: Smoke — container version
    run: docker run --rm ddns:ci version
  ```
- No pushing to a registry in this issue — Phase 6 / GoReleaser handles publication.

**Acceptance criteria:**
- [ ] `docker build -f deploy/docker/Dockerfile .` succeeds.
- [ ] The built image is <30 MB (distroless static + a ~15 MB binary; aim for ~20 MB).
- [ ] `docker run --rm ddns:ci version` prints the injected version string.
- [ ] `docker compose up` with a valid config + service-account JSON in the working dir brings the daemon up; `docker logs ddns` shows `startup`.
- [ ] CI image-smoke job green.

**Out of scope.** Multi-arch image (Phase 6 / GoReleaser). Image signing (Phase 8).

---

## Issue 5.2: `README.md` rewrite

**Story points:** 2

**Phase:** 5

**Depends on:** 5.1

**PRD requirement:** P0 docs

**Architecture modules touched:** none

**Description.** Replace the draft README with the shipped-tool version. Sections (in order): one-sentence tagline, install (two options), minimum config example, Docker deployment quickstart, command reference (link to `--help` output), config schema reference, troubleshooting (auth errors, quorum failure, 403 on `Changes.Create`, record not found on first run), FAQ, license note. The tone is operator-first — not hype, not tutorial-for-beginners — the reader is assumed to know what DDNS is and what Cloud DNS is; they want to know how to run the thing.

**Implementation notes:**
- Target length: 200–350 lines. Longer than that and it stops being a README and becomes a manual — split to `docs/` if needed.
- Install section covers two paths:
  1. `go install github.com/rootwarp/ddns/cmd/ddns@latest` — for Go users who want to run the binary directly (without supervision).
  2. `docker run ghcr.io/rootwarp/ddns:latest …` / `docker compose up` — the recommended path.
- No pre-built standalone binary download from GitHub Releases in v1 — the release images are the distribution channel. (`go install` handles the "I already have Go" audience.)
- Config example is a complete, paste-able `config.yaml` matching `testdata/valid.yaml`.
- Deployment section:
  - **Docker Compose** — paste the `docker-compose.yml` example from issue 5.1, walk through config and creds mounts, `docker compose up -d`, `docker logs -f ddns`.
  - **Wrapping with your own init** — a three-line note acknowledging that operators wanting systemd-level supervision can write their own unit pointing at `docker run`; we deliberately don't ship one.
- Troubleshooting section entries match the PRD's error taxonomy:
  - "auth: authentication failed" → ADC not found; bind-mount service-account JSON and set `GOOGLE_APPLICATION_CREDENTIALS`.
  - "resolver: no quorum among echo services" → network issue; check outbound HTTPS to the three echo services.
  - Cloud DNS 403 → IAM permission missing; grant `roles/dns.admin` or `roles/dns.editor`.
  - Cloud DNS 404 on `Get` → normal on first run; ddns creates the record.
  - Private/CGNAT IP observed by all three echo services → behind CGNAT; ddns cannot help.
- FAQ: "why only Google Cloud DNS?" (scope decision), "why no systemd unit?" (scope — wrap Docker yourself), "why no IPv6?" (explicit non-goal), "can I run it on Windows?" (no).
- Connects to `docs/log-events.md` (issue 4.5) and `docs/operations.md` (issue 5.3) via relative links.

**Acceptance criteria:**
- [ ] `README.md` has all nine top-level sections listed above.
- [ ] The config example is syntactically identical to `testdata/valid.yaml`.
- [ ] Every troubleshooting entry names the exact error string a user will see.
- [ ] Links to `deploy/docker/` and `docs/` all resolve.
- [ ] `README.md` renders correctly on GitHub (manual check).

**Out of scope.** A dedicated docs site. Screenshots. Logos.

---

## Issue 5.3: `docs/operations.md` — day-two operations

**Story points:** 1

**Phase:** 5

**Depends on:** 5.1

**PRD requirement:** P0 docs

**Architecture modules touched:** none

**Description.** Commit `docs/operations.md` covering: reading logs, forcing a resync (`ddns sync`), rotating the service-account key, migrating state between hosts, changing the record TTL, adding a new record to an existing deployment. Target audience: an operator 6 months post-install debugging a weird one-off issue.

**Implementation notes:**
- Keep it short and tactical. Each subsection is a "here's the exact command" block, not a narrative.
- Subsections:
  - **Reading logs.** `docker logs -f ddns` (Docker), or if running the binary directly, whatever redirects the operator set up. Grep patterns for the event taxonomy (issue 4.5).
  - **Forcing a resync.** `docker exec ddns /ddns sync --config /etc/ddns/config.yaml` — works even while the daemon is running; the two processes share the state directory via the volume. Alternative: `docker kill -s HUP ddns` to reload config if that's what's wrong.
  - **Rotating the service-account key.** Guide: create a new key in GCP console → download JSON → `cp new.json ./credentials.json` on the host → `docker kill -s HUP ddns`. Delete the old key from GCP.
  - **Migrating state between hosts.** Copy the Docker named volume: `docker run --rm -v ddns-state:/from -v /tmp/out:/to alpine cp -a /from/. /to/`; `rsync /tmp/out/ newhost:/tmp/in/`; on the new host, restore into a fresh `ddns-state` volume. State is portable; no host-specific data is in it.
  - **Changing the TTL.** Edit `config.yaml`, `docker kill -s HUP ddns` or restart the container. Next tick will observe TTL drift and trigger an update.
  - **Adding a new record.** Edit `config.yaml` records list, `docker kill -s HUP ddns`, verify `docker exec ddns /ddns status` now shows the new record.

**Acceptance criteria:**
- [ ] `docs/operations.md` exists with six subsections above.
- [ ] Each subsection ends with an exact command or sequence of commands.
- [ ] Links back to README install sections and to `docs/log-events.md`.

**Out of scope.** Incident-response playbooks. Prometheus alerting examples (Phase 7).

---

## Issue 5.4: Release-gate runbook

**Story points:** 1

**Phase:** 5

**Depends on:** 5.1, 5.2

**PRD requirement:** process — defines what Phase 6 actually does

**Architecture modules touched:** none

**Description.** Commit `plan/bootstrap/release-gate-runbook.md` — the exact sequence of verifications that precede a v1 tag. This is the checklist Phase 6 follows; writing it now (end of Phase 5) means Phase 6 is pure execution, not design.

**Implementation notes:**
- Runbook structure (markdown checklist):
  ```
  # Release Gate: ddns v1.0.0

  ## Pre-flight (24h before tag)
  - [ ] `go test ./...` green on develop head.
  - [ ] `go test -tags=integration ./...` green against the integration Cloud DNS project.
  - [ ] `make lint` clean.
  - [ ] CI smoke tests green.
  - [ ] Dockerfile build green; `docker run --rm ddns:ci version` OK.
  - [ ] Read CHANGELOG; confirm every user-visible change is recorded.

  ## 72-hour soak (start at least 72h before tag)
  - [ ] Deploy the current develop-head image to the author's home network as `ddns-rc` (separate container name from any prod instance).
  - [ ] Configure logging to a separate file so the soak is auditable.
  - [ ] Every 24h during the soak, grep the logs for `reconcile_error` / `resolver_no_quorum`; record counts.
  - [ ] At soak end: zero panics, zero wrong-IP writes, ≥99% tick completion rate.
  - [ ] Verify against a side-channel: `dig +short home.example.com` now matches `curl ifconfig.me`.

  ## Documentation
  - [ ] README reflects the final CLI surface; `ddns --help` output is still accurate.
  - [ ] `docs/operations.md` references are still valid.
  - [ ] Release notes draft in place under `.github/release-notes.md` or direct in GitHub Releases draft.

  ## Tag
  - [ ] On develop: bump `VERSION` (in Makefile if centralized there) to `v1.0.0`; commit.
  - [ ] Open PR develop → main; merge (fast-forward).
  - [ ] Tag `v1.0.0` on main.
  - [ ] Push tag; GoReleaser (Phase 6 workflow) picks it up.

  ## Post-tag
  - [ ] GitHub Release page shows `linux/amd64` and `linux/arm64` binary archives (for `go install` users who want a prebuilt alternative).
  - [ ] `ghcr.io/rootwarp/ddns:v1.0.0` and `:latest` exist as multi-arch manifests.
  - [ ] `docker pull ghcr.io/rootwarp/ddns:v1.0.0 && docker run --rm ghcr.io/rootwarp/ddns:v1.0.0 version` on a clean machine works end-to-end.
  ```
- The runbook is the single source of truth for "what do we verify before v1?" — if something is not on the checklist, Phase 6 does not do it (and if it turns out to matter, add it here and re-run).

**Acceptance criteria:**
- [ ] `plan/bootstrap/release-gate-runbook.md` exists with the four sections above.
- [ ] Every item is an actionable checkbox.
- [ ] The runbook references the exact commands used, not abstract "run tests" instructions.

**Out of scope.** Signing / notarization (Phase 8). Automated release-note generation (the first release is hand-written).
