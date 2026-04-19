---
type: document
status: draft
PARA: projects
tags:
  - dev/go
  - dev/planning
  - dev/release
---

**Connections:**
- [[project-plan]]
- [[architecture]]
- [[prd]]

# Release Gate: ddns v1.0.1

**About this version.** `v1.0.0` was cut on `main` ahead of the Phase 5
docs-and-deploy work landing, so the first release that follows this
runbook end-to-end is `v1.0.1` — the packaging release containing Phase 5
(Dockerfile, README rewrite, `docs/operations.md`, this runbook) and
Phase 6 (GoReleaser, soak test, tag). Everywhere below where `v1.0.1`
appears, substitute the target version on subsequent releases.

Every item is a checkbox. Work top-to-bottom; do not skip steps because
"that one doesn't seem to matter." If a step turns out not to matter,
remove it from the runbook in a follow-up PR — do not silently ignore it
in this pass.

---

## 1. Pre-flight (24 hours before tag)

- [ ] On `develop` HEAD: `git fetch origin && git status` — working tree
      clean, branch tracking up-to-date.
- [ ] `go build ./...` succeeds.
- [ ] `go vet ./...` clean.
- [ ] `make test` green (unit + fake-provider).
- [ ] `make coverage-check` green at the configured threshold
      (`COVERAGE_MIN=70` default).
- [ ] `make lint` clean — `staticcheck` and `golangci-lint` both silent.
- [ ] Integration test green against the author's Cloud DNS project:
      ```sh
      export DDNS_INTEGRATION_PROJECT=my-home-lab
      export DDNS_INTEGRATION_ZONE=example-com
      export DDNS_INTEGRATION_RECORD=ddns-integration.example.com.
      make integration-test
      ```
- [ ] CI is green on `develop` for the last five commits (visual check:
      GitHub Actions badge + three most-recent runs).
- [ ] Docker image builds and smokes locally:
      ```sh
      docker build -t ddns:rc -f deploy/docker/Dockerfile \
        --build-arg VERSION=v1.0.1-rc \
        --build-arg COMMIT=$(git rev-parse --short HEAD) \
        --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
        .
      docker run --rm ddns:rc version
      ```
      Expect output starting `ddns v1.0.1-rc ` and an exit code of 0.
- [ ] Docker image size under 30 MB:
      `docker images ddns:rc --format '{{.Size}}'`
- [ ] CHANGELOG reviewed: every user-visible change since the previous
      tag has a line under an `## Unreleased` (or `## v1.0.1`) heading.
      If CHANGELOG.md does not yet exist, create it now with the first
      release notes in place (blocked on Phase 6 if that is still open).
- [ ] `ddns --help` output sanity-checked against `README.md#command-reference`
      and `docs/getting-started-gcp.md`. Any drift gets a docs PR before
      tag, not after.

---

## 2. 72-hour soak (start at least 72 hours before tag)

Runs against the author's home network with the `v1.0.1-rc` image from
the Pre-flight step. Must be a separate container (`container_name:
ddns-rc`) from any production `ddns` instance so the soak is independent.

- [ ] Deploy `ddns:rc` to the author's home network under the name
      `ddns-rc`:
      ```sh
      docker run -d \
        --name ddns-rc \
        --restart unless-stopped \
        -e DDNS_LOG_FORMAT=json \
        -e GOOGLE_APPLICATION_CREDENTIALS=/etc/ddns/credentials.json \
        -v $PWD/config.rc.yaml:/etc/ddns/config.yaml:ro \
        -v $PWD/credentials.json:/etc/ddns/credentials.json:ro \
        -v ddns-rc-state:/var/lib/ddns \
        ddns:rc run --config /etc/ddns/config.yaml
      ```
- [ ] `config.rc.yaml` points at a dedicated soak hostname
      (e.g., `rc.home.example.com.`) — NOT the production hostname, so
      production is unaffected if the RC misbehaves.
- [ ] Logs routed to a file for post-hoc review:
      ```sh
      docker logs -f ddns-rc >> ~/ddns-rc-soak.log 2>&1 &
      ```
      Archive `~/ddns-rc-soak.log` after the soak ends; it is evidence for
      the release notes.
- [ ] T+24h: grep the soak log; record the counts for the tracking sheet:
      ```sh
      rg -c resolver_no_quorum  ~/ddns-rc-soak.log
      rg -c reconcile_error     ~/ddns-rc-soak.log
      rg -c reconcile_updated   ~/ddns-rc-soak.log
      rg -c reconcile_noop      ~/ddns-rc-soak.log
      rg -c panic               ~/ddns-rc-soak.log   # must be 0
      ```
- [ ] T+48h: repeat the grep pass; deltas match expected per-day rates.
- [ ] T+72h: final grep pass. Acceptance criteria:
  - [ ] `panic` count is exactly 0.
  - [ ] Every `reconcile_updated` matches a real ISP IP change (spot-check
        3 of them against `dig` + `curl ifconfig.me` side-channel logs).
  - [ ] `(reconcile_noop + reconcile_updated) / tick_start` ≥ 0.99
        (tick-completion rate).
  - [ ] `reconcile_error` bursts (>3 in a 30-min window) all have an
        identifiable external cause (ISP outage, GCP 5xx period).
- [ ] Side-channel correctness check:
      ```sh
      dig +short rc.home.example.com. @8.8.8.8
      curl -sS https://api.ipify.org
      ```
      The two must agree at soak end.

If any acceptance fails, halt release, file an issue, fix on `develop`,
restart the 72h clock on the next RC.

---

## 3. Documentation

- [ ] `README.md` reflects the final CLI surface: each subcommand listed
      in the command-reference section matches `ddns --help` verbatim
      (subcommand names and one-sentence usage).
- [ ] `docs/getting-started-gcp.md` still contains accurate commands —
      no stale `go install ...@develop` or stale flag names.
- [ ] `docs/operations.md` command recipes all point at the correct
      compose file path and the correct image tag reference.
- [ ] `docs/log-events.md` table matches the events emitted by the code
      — grep the repo for `.Info("` / `.Warn("` / `.Error("` and confirm
      every event name is in the table.
- [ ] `config.example.yaml` field defaults match the code-level defaults
      in `internal/config`.
- [ ] Release notes drafted in `.github/release-notes/v1.0.1.md` (or a
      GitHub Releases draft). Sections: Highlights, Install, Upgrade,
      Breaking changes (none expected for a packaging release), Fixes,
      Docs, Known issues.
- [ ] Release notes reference the soak-log archive (commit it to the
      repo under `plan/bootstrap/soak/v1.0.1.log` or attach to the GH
      release — not both).

---

## 4. Tag

- [ ] On `develop`: final commit is the release-notes / CHANGELOG bump
      for `v1.0.1`. Push to origin.
- [ ] Open PR `develop` → `main`, titled `release: v1.0.1`.
- [ ] PR body contains the release-notes draft verbatim (so reviewers
      see exactly what ships).
- [ ] Merge fast-forward only (no squash, no merge commit). If
      fast-forward isn't possible, rebase `develop` on `main` first.
- [ ] On `main`, after merge:
      ```sh
      git pull --ff-only origin main
      git tag -a v1.0.1 -m "Release v1.0.1"
      git push origin v1.0.1
      ```
- [ ] Confirm the tag points at the right commit:
      ```sh
      git rev-parse v1.0.1
      git log -1 v1.0.1
      ```
- [ ] GoReleaser workflow (Phase 6 deliverable) picks up the tag. Watch
      the Actions run; it must complete cleanly before moving to Post-tag.

---

## 5. Post-tag

- [ ] GitHub Release page for `v1.0.1` is visible and published (not
      draft).
- [ ] Release assets include at least:
  - [ ] `ddns_v1.0.1_linux_amd64.tar.gz`
  - [ ] `ddns_v1.0.1_linux_arm64.tar.gz`
  - [ ] `checksums.txt` (SHA256 over the above)
  - [ ] (Phase 8 — deferred) `checksums.txt.sig` signed artifact.
- [ ] `ghcr.io/rootwarp/ddns:v1.0.1` and `ghcr.io/rootwarp/ddns:latest`
      exist as multi-arch manifests:
      ```sh
      docker buildx imagetools inspect ghcr.io/rootwarp/ddns:v1.0.1
      # Expect: linux/amd64, linux/arm64.
      ```
- [ ] End-to-end install smoke test on a clean machine (a fresh VM or
      `docker run` on a different host):
      ```sh
      docker pull ghcr.io/rootwarp/ddns:v1.0.1
      docker run --rm ghcr.io/rootwarp/ddns:v1.0.1 version
      # Expect: "ddns v1.0.1 (commit <sha>, built <date>)"
      ```
- [ ] `go install github.com/rootwarp/ddns/cmd/ddns@v1.0.1` on a clean
      host produces a working binary; `ddns version` reports `v1.0.1`.
- [ ] Production `ddns` instance (on the author's home network) bumped
      from the previous version to `v1.0.1` without incident. One full
      poll interval passes without a `reconcile_error`.
- [ ] Close the release milestone in the tracker. Move any unfinished
      items to the next milestone; do not silently drop.
- [ ] Announcement (if applicable): single paragraph to whatever channel
      existing users watch. Omit on a packaging release if nothing
      user-visible changed — CHANGELOG is enough.

---

## Deviations and backout

If anything on this runbook fails after tag push:

- [ ] Do **not** delete the tag from `origin`. Deleting published tags
      breaks downstream consumers who already pulled.
- [ ] Cut `v1.0.2` with the fix instead. Mark `v1.0.1` as yanked in the
      GitHub Release description and link to `v1.0.2`.
- [ ] File an issue titled `postmortem: v1.0.1` with the symptom, the
      root cause, and which runbook step should have caught it. Update
      this runbook in the same PR as the fix.
