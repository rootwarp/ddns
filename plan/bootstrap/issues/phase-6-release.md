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

# Phase 6: Release v1

**Goal.** Tag `v1.0.0` and ship. GoReleaser drives the release workflow: cross-compiled Linux binaries for `amd64` and `arm64`; a multi-arch Docker image published to `ghcr.io/rootwarp/ddns` (primary distribution channel); GitHub Release page with checksums and an SBOM. The 72-hour soak gates the tag.

**Entry criteria.** Phase 5 complete — docs, deploy units, release-gate runbook in place.

**Exit criteria.** `v1.0.0` tag pushed; GitHub Release published with Linux amd64/arm64 binary archives and a multi-arch image on ghcr.io; soak-test log linked from the release notes.

---

## Issue 6.1: `.goreleaser.yaml` — cross-compile + Docker publish

**Story points:** 2

**Phase:** 6

**Depends on:** 5.3 (Dockerfile)

**PRD requirement:** P0 distribution

**Architecture modules touched:** none

**Description.** Commit `.goreleaser.yaml` that produces: (1) cross-compiled Linux `ddns` binaries for `amd64` and `arm64` — bundled as tarballs with the README and LICENSE; (2) a multi-arch Docker image at `ghcr.io/rootwarp/ddns` tagged `vX.Y.Z` and `latest`; (3) checksums and an SBOM (CycloneDX); (4) a GitHub Release draft with release notes from a changelog. No Homebrew tap in v1 (Phase 8 consideration). No `darwin` builds — macOS users who want the binary can `go install`; the release artifacts are Linux-only because the supported deployment shape is Linux Docker.

**Implementation notes:**
- `.goreleaser.yaml`:
  ```yaml
  version: 2

  before:
    hooks:
      - go mod tidy

  builds:
    - id: ddns
      main: ./cmd/ddns
      binary: ddns
      env:
        - CGO_ENABLED=0
      goos: [linux]
      goarch: [amd64, arm64]
      flags: [-trimpath]
      ldflags:
        - -s -w
        - -X github.com/rootwarp/ddns/internal/version.Version={{.Version}}
        - -X github.com/rootwarp/ddns/internal/version.Commit={{.ShortCommit}}
        - -X github.com/rootwarp/ddns/internal/version.BuildDate={{.Date}}

  archives:
    - id: tarball
      formats: [tar.gz]
      name_template: "ddns_{{.Version}}_{{.Os}}_{{.Arch}}"
      files:
        - README.md
        - LICENSE
        - deploy/**/*

  dockers:
    - image_templates:
        - "ghcr.io/rootwarp/ddns:{{.Version}}-amd64"
      goarch: amd64
      use: buildx
      dockerfile: deploy/docker/Dockerfile
      build_flag_templates:
        - --platform=linux/amd64
        - --label=org.opencontainers.image.source=https://github.com/rootwarp/ddns
        - --build-arg=VERSION={{.Version}}
        - --build-arg=COMMIT={{.ShortCommit}}
        - --build-arg=BUILD_DATE={{.Date}}
    - image_templates:
        - "ghcr.io/rootwarp/ddns:{{.Version}}-arm64"
      goarch: arm64
      use: buildx
      dockerfile: deploy/docker/Dockerfile
      build_flag_templates:
        - --platform=linux/arm64
        - --label=org.opencontainers.image.source=https://github.com/rootwarp/ddns
        - --build-arg=VERSION={{.Version}}
        - --build-arg=COMMIT={{.ShortCommit}}
        - --build-arg=BUILD_DATE={{.Date}}

  docker_manifests:
    - name_template: "ghcr.io/rootwarp/ddns:{{.Version}}"
      image_templates:
        - "ghcr.io/rootwarp/ddns:{{.Version}}-amd64"
        - "ghcr.io/rootwarp/ddns:{{.Version}}-arm64"
    - name_template: "ghcr.io/rootwarp/ddns:latest"
      image_templates:
        - "ghcr.io/rootwarp/ddns:{{.Version}}-amd64"
        - "ghcr.io/rootwarp/ddns:{{.Version}}-arm64"

  checksum:
    name_template: "checksums.txt"

  sboms:
    - artifacts: archive

  changelog:
    use: github
    groups:
      - title: Features
        regexp: "^.*feat[(\\w)]*:+.*$"
      - title: Fixes
        regexp: "^.*fix[(\\w)]*:+.*$"
      - title: Other
  ```
- Note the Dockerfile is shared with Phase 5 issue 5.1; the `--build-arg` values ensure the Docker image carries the same injected version as the tarball binary.
- Test locally before the release: `goreleaser release --snapshot --clean --skip=publish` — produces artifacts in `dist/` without pushing. Confirm both tarballs, both per-arch images, and the multi-arch manifest exist.

**Acceptance criteria:**
- [ ] `goreleaser check` passes.
- [ ] `goreleaser release --snapshot --clean --skip=publish` succeeds locally.
- [ ] The snapshot `dist/` contains 2 Linux tarballs (`amd64`, `arm64`), 2 per-arch Docker images, a `checksums.txt`, and an SBOM.
- [ ] `dist/ddns_*_linux_amd64.tar.gz` includes `deploy/docker/Dockerfile` and `deploy/docker/docker-compose.yml` (confirms the `files:` glob).
- [ ] Both Docker images contain a binary that runs `ddns version` and prints the version from the tag.

**Out of scope.** Cosign signing (Phase 8). Homebrew tap (Phase 8). Cross-publish to other registries (Docker Hub: not required).

---

## Issue 6.2: `.github/workflows/release.yml` — tag-driven release

**Story points:** 1

**Phase:** 6

**Depends on:** 6.1

**PRD requirement:** P0 distribution automation

**Architecture modules touched:** none

**Description.** GitHub Actions workflow that runs GoReleaser on tag push. Tag convention: `v*.*.*`. Needs `contents: write` (for the release page) and `packages: write` (for ghcr.io push) permissions.

**Implementation notes:**
- `.github/workflows/release.yml`:
  ```yaml
  name: release

  on:
    push:
      tags:
        - "v*.*.*"

  permissions:
    contents: write
    packages: write
    id-token: write

  jobs:
    goreleaser:
      runs-on: ubuntu-latest
      steps:
        - name: Checkout
          uses: actions/checkout@v4
          with:
            fetch-depth: 0
        - name: Set up Go
          uses: actions/setup-go@v5
          with:
            go-version: "1.22.x"
        - name: Set up QEMU
          uses: docker/setup-qemu-action@v3
        - name: Set up Buildx
          uses: docker/setup-buildx-action@v3
        - name: Login to GHCR
          uses: docker/login-action@v3
          with:
            registry: ghcr.io
            username: ${{ github.actor }}
            password: ${{ secrets.GITHUB_TOKEN }}
        - name: GoReleaser
          uses: goreleaser/goreleaser-action@v6
          with:
            version: latest
            args: release --clean
          env:
            GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  ```
- Test: push a `v0.9.0-rc1` tag on the `develop` branch (not main) to dry-run the release. Verify GHCR pushes succeed and the GitHub Release page is created as a prerelease (GoReleaser auto-flags anything with a hyphen as prerelease).
- Delete the `v0.9.0-rc1` tag and the prerelease afterward.

**Acceptance criteria:**
- [ ] Workflow file is valid YAML (`actionlint` passes).
- [ ] An `rc1` tag push triggers the workflow; it completes green.
- [ ] The prerelease page exists with binaries, checksums, and SBOM.
- [ ] `ghcr.io/rootwarp/ddns:v0.9.0-rc1` exists and `docker run --rm ghcr.io/rootwarp/ddns:v0.9.0-rc1 version` prints `v0.9.0-rc1`.
- [ ] After cleanup, the `rc1` tag and prerelease are removed so the registry state is clean for v1.

**Out of scope.** Release-candidate promotion workflow (manual for v1). Slack/Discord release announcements (manual post).

---

## Issue 6.3: 72-hour soak test on home network

**Story points:** 1 (+ 72h wall time)

**Phase:** 6

**Depends on:** 6.2

**PRD requirement:** success-metric gate from project-plan §Phase 6 exit

**Architecture modules touched:** none (runtime verification)

**Description.** Deploy the head-of-develop `ddns` container on the author's home server (via `docker compose up -d` using the reference compose file). Let it run for 72 hours against the production Cloud DNS managed zone with the real production record. Pass criteria: zero panics (`docker logs ddns-rc | grep panic:` returns nothing), zero wrong-IP writes (cross-check every `reconcile_updated` log event against a `dig +short` / `curl ifconfig.me` log captured at the same time), zero state-file corruption (`docker exec ddns-rc /ddns status --json | jq .` succeeds at any point), ≥99% tick completion rate (count of `tick_start` vs count of `reconcile_*` outcomes).

**Implementation notes:**
- Set up a parallel side-channel logger before the soak starts:
  ```bash
  while true; do
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $(curl -s https://api.ipify.org) $(dig +short home.example.com @8.8.8.8)" >> /var/log/ddns-soak.log
    sleep 60
  done &
  ```
  This gives a ground-truth log of (what is my IP, what does DNS resolve) at 1-minute granularity, independent of ddns.
- During the soak, resist the urge to redeploy. If a bug is found, note it, let the soak continue if the bug is non-critical, and address it in a follow-up patch release (v1.0.1).
- At soak end:
  1. `docker logs ddns-rc | grep panic:` → expected: zero hits.
  2. `docker logs ddns-rc | grep reconcile_updated` → for each event, look up the ddns timestamp in the side-channel log and confirm the public IP and the DNS resolution were both the new IP within 5 minutes.
  3. `docker exec ddns-rc /ddns status --json | jq .` → expected: valid JSON.
  4. Count `tick_start` events and `reconcile_{noop,updated,error}` events; the ratio is completion rate.
- File the findings under `plan/bootstrap/release-gate-runbook.md` as a "Soak Run Log" appendix, or in a separate `plan/bootstrap/soak-<date>.md` — either is fine; the point is the record exists.

**Acceptance criteria:**
- [ ] Soak runs for ≥72 hours uninterrupted.
- [ ] Zero `panic:` events in the log.
- [ ] Every `reconcile_updated` event is corroborated by the side-channel log.
- [ ] `ddns status --json` is valid JSON throughout.
- [ ] Tick completion rate ≥99%.
- [ ] Soak findings document committed.

**Out of scope.** Multi-host soak. Induced-failure soak (e.g., pulling the network cable) — that's a targeted test, separate from the steady-state soak.

---

## Issue 6.4: Tag `v1.0.0`

**Story points:** 1

**Phase:** 6

**Depends on:** 6.3

**PRD requirement:** release

**Architecture modules touched:** none

**Description.** Mechanical: open a `develop` → `main` PR (fast-forward), merge, tag `v1.0.0` on `main`, push the tag. The release workflow (issue 6.2) takes it from there.

**Implementation notes:**
- Verify one more time: the release-gate runbook checklist (issue 5.6) is fully checked.
- On `develop`: if `CHANGELOG.md` exists and is used, finalize the v1.0.0 section. (If not using a changelog, GoReleaser generates one from commit messages — issue 6.1 configures that.)
- `gh pr create --base main --head develop --title "Release v1.0.0" --body "$(cat plan/bootstrap/release-gate-runbook.md)"` or equivalent.
- Review the PR (self-review is fine for a single-owner project; the point is the explicit gate, not the second pair of eyes).
- Merge as fast-forward (no squash, no rebase-merge) — develop and main share a linear history.
- `git checkout main && git pull && git tag -a v1.0.0 -m "v1.0.0" && git push origin v1.0.0`.
- Watch `.github/workflows/release.yml` run. If it fails, the fix is: figure out why (almost always a permission / secret issue), amend, re-tag as `v1.0.0+build2` or delete-and-retag (if nothing published yet).
- After the tag is pushed and the release workflow succeeds:
  - Verify GitHub Release page has the two Linux tarballs, checksums, SBOM.
  - Verify `ghcr.io/rootwarp/ddns:v1.0.0` and `:latest` are pushed.
  - Edit the release notes to link to the soak log from issue 6.3.
  - `go install github.com/rootwarp/ddns/cmd/ddns@v1.0.0` on a clean machine; confirm it works.

**Acceptance criteria:**
- [ ] `v1.0.0` tag exists on `main`.
- [ ] GitHub Release page is public, not a draft.
- [ ] Both Linux binaries are downloadable from the release page.
- [ ] `ghcr.io/rootwarp/ddns:v1.0.0` and `:latest` are publicly pullable.
- [ ] `go install github.com/rootwarp/ddns/cmd/ddns@v1.0.0` on a clean VM produces a working binary.
- [ ] Release notes link to the soak log.

**Out of scope.** Post-release announcements (blog post, social media — at the user's discretion).
