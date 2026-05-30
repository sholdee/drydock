---
title: drydock
---

# drydock

Offline desired-state analysis for Argo CD GitOps repositories.

drydock discovers Argo CD Applications, renders desired manifests, compares
Git refs, inspects image changes, and reports diagnostics without requiring a
running Argo CD instance or Kubernetes cluster.

## Start Here

- [Getting started](/getting-started/) covers the first local commands and the
  runtime-offline model.
- [GitHub Actions](/workflows/github-actions/) shows the current setup and pull
  request actions for CI.
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
