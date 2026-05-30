---
title: drydock
---

# drydock

Offline desired-state analysis for Argo CD GitOps repositories.

drydock discovers Argo CD Applications, renders desired manifests, compares
Git refs, inspects image changes, and reports diagnostics without requiring a
running Argo CD instance or Kubernetes cluster.

## Start Here

- [Usage](docs/usage/) covers discovery, rendering, diffs, image reports,
  diagnostics, and local verification commands.
- [GitHub Actions](docs/github-actions/) shows the setup action and pull
  request action inputs, permissions, comments, and outputs.
- [Compatibility](docs/compatibility/) defines the supported Argo CD behavior
  and runtime boundary.

## Core Workflows

```bash
drydock get apps --path ./gitops
drydock test apps --path ./gitops
drydock diff apps --repo . --ref main --ref-orig HEAD~1
drydock diff images --repo . --ref main --ref-orig HEAD~1
drydock diag --path ./gitops
```

## Operating Model

drydock is built for offline desired-state inspection. It can fetch declared
Git, Helm, OCI Helm, and remote Kustomize sources into explicit caches unless
`--offline` is set, but default analysis does not depend on live Kubernetes or
Argo CD APIs.
