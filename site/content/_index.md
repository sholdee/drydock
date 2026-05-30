---
title: drydock
---

Inspect your Argo CD fleet without getting wet.

drydock is a fast, single static Go binary and embeddable Go module for
runtime-offline Argo CD desired-state analysis. It discovers, renders, tests,
diffs, and diagnoses GitOps Applications with native Go renderers, no live
cluster, no Argo CD server, and no default shellouts.

Use it locally or in CI to review rendered manifest diffs, catch render
failures, inspect image changes, and validate GitOps repositories before Argo CD
syncs them.

**[Get Started](/getting-started/)** | **[Set Up PR Checks](/workflows/github-actions/)**

## Start Here

- [Getting started](/getting-started/) installs drydock and runs the first
  local render test.
- [GitHub Actions](/workflows/github-actions/) sets up pull request checks
  without Kubernetes or Argo CD credentials.
- [Local diffs](/workflows/local-diffs/) covers local tree and Git ref
  comparisons.
- [Troubleshooting](/troubleshooting/) maps common operator symptoms to the
  first commands to run.

## Core Workflows

```bash
drydock get apps --path ./gitops
drydock test apps --path ./gitops
drydock diff apps --repo . --ref main --ref-orig HEAD~1
drydock diff images --repo . --ref main --ref-orig HEAD~1
drydock diag --path ./gitops
```

## Operating Model

drydock is runtime-offline: render, test, diff, image, and diagnostic commands
do not call live Kubernetes or Argo CD. Declared Git, HTTP Helm, OCI Helm, and
remote Kustomize sources can still be fetched into explicit caches unless
`--offline` is set.

Use these curated pages for day-to-day operation. Use the mounted reference
docs when you need dense details such as full compatibility notes, action
inputs, source acquisition flags, or plugin policy schema.
