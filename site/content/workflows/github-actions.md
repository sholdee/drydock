---
title: GitHub Actions
---

drydock publishes two repository-local composite actions:

- `sholdee/drydock/setup-action`: install a released drydock binary.
- `sholdee/drydock/pr-action`: install drydock, run PR validation, upload
  artifacts, and optionally maintain sticky PR comments.

Use `setup-action` when you want to own the CLI commands. Use `pr-action` when
you want the standard render test, manifest diff, image diff, source cache, and
comment workflow.

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
          comment-mode: both
          skip-secrets: "true"
```

The PR action checks out the pull request, fetches the base ref, runs
`drydock test apps`, runs manifest and image diffs, writes artifacts when
differences are found, and comments in trusted same-repository pull requests.
Fork pull requests skip comments and source-cache save by default.

## Image Diff Comment Shape

The action uses drydock markdown output for image comments. A typical comment
looks like this:

```markdown
## drydock image diff

**Summary:** 1 added, 1 removed.

| Change | Image |
| --- | --- |
| added | `registry.example.com/app:v2` |
| removed | `registry.example.com/app:v1` |
```

For a no-change image diff, the comment stays compact:

```markdown
## drydock image diff

**Summary:** 0 added, 0 removed.

No rendered image differences detected.
```

Use `-o markdown` directly when building a custom workflow, or let `pr-action`
produce the comment:

```bash
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
```

For all inputs, outputs, permissions, token behavior, and cache details, see
the canonical [GitHub Actions guide](/docs/github-actions/).
