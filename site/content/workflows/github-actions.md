---
title: GitHub Actions
---

drydock publishes two composite GitHub Actions:

- `sholdee/drydock/setup-action`: install a released drydock binary.
- `sholdee/drydock/pr-action`: install drydock, run PR validation, upload
  artifacts, and optionally maintain sticky PR comments.

Use `setup-action` when you want to own the CLI commands. Use `pr-action` when
you want the standard render test, markdown manifest diff, image diff, source
cache, artifacts, and pull request comment workflow.

## Manual CLI Workflow

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: sholdee/drydock/setup-action@main
      - run: drydock test apps --path .
      - run: drydock diff apps --repo . --ref HEAD --ref-orig origin/${{ github.base_ref }}
      - run: >-
          drydock diff images --repo . --ref HEAD
          --ref-orig origin/${{ github.base_ref }} -o markdown
```

## Pull Request Action

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read
  pull-requests: write

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: sholdee/drydock/pr-action@main
        with:
          version: v0.1.12
          comment-mode: both
          skip-secrets: "true"
          changed-only-include: |
            apps/**
          changed-only-ignore: |
            .github/**
```

The PR action checks out the pull request, fetches the base ref, runs
`drydock test apps`, renders the desired-state manifest diff, writes full diff
artifacts when differences are found, and comments in trusted same-repository
pull requests. Image diff comments are available as a companion signal. Fork
pull requests skip comments and source-cache restore/save by default.

## Full Rendered Diff View

The PR action posts a compact markdown summary in the pull request. When a
manifest diff exists and artifact upload is enabled, the summary links
reviewers to a standalone Full Rendered Diff View artifact:

{{< pr-comment-example >}}

Use it to review the desired state Argo CD would reconcile after merge. drydock
renders the PR and base refs, then compares the results without requiring Argo
CD or Kubernetes credentials.

Artifacts follow the workflow's retention settings. For a permanent sample:

[Full Rendered Diff View](/examples/full-rendered-diff-view.html)

## Reporting And Gating

By default, `pr-action` fails render errors and reports manifest or image diffs
through comments and artifacts without failing the workflow. To make diffs a
gate, set `strict`, `strict-changed-only`, `fail-on-diff`, and optionally
`fail-on-image-diff`.

`changed-only-include` and `changed-only-ignore` are optional newline-delimited
globs passed to manifest and image diffs. They keep known non-GitOps paths from
forcing a full-fleet changed-only fallback. Keep them narrow; ignored paths
cannot trigger Application renders. They do not affect `test apps`.

Use markdown output directly when building a custom workflow, or let
`pr-action` produce the comment:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown
```

## Image Diff Companion Comment

Image comments can be enabled alongside the manifest diff. They are useful for
quickly scanning added and removed rendered image references:

{{< image-comment-example >}}

Run image diff markdown directly when building a custom workflow:

```bash
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
```

For all inputs, outputs, permissions, token behavior, and cache details, see
the canonical [GitHub Actions guide](/docs/github-actions/).
