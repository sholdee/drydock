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
`drydock test apps`, renders the desired-state manifest diff, writes full diff
artifacts when differences are found, and comments in trusted same-repository
pull requests. Image diff comments are available as a companion signal. Fork
pull requests skip comments and source-cache save by default.

## Manifest Diff Comment Shape

The manifest diff comment is the main PR review surface. It summarizes changed
Applications and resources, then expands each affected Application into a
reviewable rendered diff:

````markdown
## drydock desired state diff

**Summary:** 1 app, 2 resources, +4/-2.

<details open>
<summary>renovate (+4/-2, 2 resources)</summary>

```diff
--- Application: renovate apps/Deployment: renovate/renovate-operator
+++ Application: renovate apps/Deployment: renovate/renovate-operator
@@ -47,7 +47,7 @@
-          image: ghcr.io/example/renovate:1.0.0
+          image: ghcr.io/example/renovate:1.1.0
```

</details>
````

Use markdown output directly when building a custom workflow, or let
`pr-action` produce the comment:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown
```

## Image Diff Companion Comment

Image comments can be enabled alongside the manifest diff. They are useful for
quickly scanning added and removed rendered image references:

```text
## drydock image diff

**Summary:** 1 added, 1 removed.

| Change | Image |
| --- | --- |
| added | `registry.example.com/app:v2` |
| removed | `registry.example.com/app:v1` |
```

Run image diff markdown directly when building a custom workflow:

```bash
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
```

For all inputs, outputs, permissions, token behavior, and cache details, see
the canonical [GitHub Actions guide](/docs/github-actions/).
