---
title: Pull Request Checks
---

## Use The PR Action

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
          version: vX.Y.Z
          comment-mode: both
          skip-secrets: "true"
```

## Own The Commands

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0
- uses: sholdee/drydock/setup-action@main
  with:
    version: vX.Y.Z
- run: drydock test apps --path .
- run: >-
    drydock diff apps --repo . --ref HEAD
    --ref-orig origin/${{ github.base_ref }}
- run: >-
    drydock diff images --repo . --ref HEAD
    --ref-orig origin/${{ github.base_ref }} -o markdown
```

## Tighten The Gate

```yaml
with:
  strict: "true"
  strict-changed-only: "true"
  fail-on-diff: "true"
```

Use `pr-action` for the standard render test, manifest diff, image diff,
artifacts, source cache, and sticky comments. Use `setup-action` when workflow
YAML should own every drydock command.

Full inputs, outputs, permissions, and token behavior are in the
[GitHub Actions guide](/docs/github-actions/).
