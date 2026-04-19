---
type: document
status: action-required
PARA: projects
tags:
  - dev/ops
  - dev/planning
---

# Branch Protection Setup (manual, GitHub UI)

Branch protection must be configured in the GitHub web UI or via the GitHub API
after the Phase-0 scaffold lands and the first CI run on `develop` registers
the status check names. This document records the exact settings a repo admin
must apply.

## Default branch

Set `develop` as the repository default branch.

GitHub → Settings → General → Default branch → switch to `develop`.

## Required status checks (shared by `develop` and `main`)

These check names are what the Phase-0 CI workflow produces. They only appear
in the branch-protection UI after CI has run at least once on the repo, so
land Phase 0 first, push it to trigger CI, then configure protection.

- `test (ubuntu-latest, 1.26.x)`
- `test (macos-latest, 1.26.x)`

## Branch protection for `develop`

GitHub → Settings → Branches → Add branch protection rule → Branch name
pattern: `develop`.

- Require a pull request before merging: **enabled**
  - Require approvals: **1**
  - Dismiss stale pull request approvals when new commits are pushed: optional
- Require status checks to pass before merging: **enabled**
  - Require branches to be up to date before merging: **enabled**
  - Required status checks:
    - `test (ubuntu-latest, 1.26.x)`
    - `test (macos-latest, 1.26.x)`
- Require conversation resolution before merging: **enabled**
- Do not allow bypassing the above settings: **enabled**
- Allow force pushes: **disabled**
- Allow deletions: **disabled**

## Branch protection for `main`

GitHub → Settings → Branches → Add branch protection rule → Branch name
pattern: `main`.

- Require a pull request before merging: **enabled**
  - Require approvals: **1**
- Require status checks to pass before merging: **enabled**
  - Require branches to be up to date before merging: **enabled**
  - Required status checks:
    - `test (ubuntu-latest, 1.26.x)`
    - `test (macos-latest, 1.26.x)`
- Require conversation resolution before merging: **enabled**
- Restrict who can push to matching branches: **enabled** — repo admins only
  (releases advance `main` via a fast-forward PR from `develop` and then a
  `v*.*.*` tag push).
- Allow force pushes: **disabled**
- Allow deletions: **disabled**

## Verification

After applying the rules, a feature branch push + PR into `develop` should:

- Trigger both `test` matrix jobs.
- Block merge until both are green and one approval is on file.
- Reject force-push attempts to `develop` and `main` from non-admins.

Once verified, update Issue 0.7's acceptance checklist to "done" and close
the issue.
