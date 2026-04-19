# Contributing

- One feature branch per issue.
- Branch from `develop`; name branches `feat/<issue-id>-<short-description>` (or `fix/`, `chore/`, `docs/`).
- Open a pull request into `develop`; it is squash-merged on review approval + green CI.
- `main` is release-only: it advances exclusively through fast-forward merges from `develop` at tag time.
- Releases are cut by pushing a `v*.*.*` tag on `main`, which drives the release workflow.
