---
title: Compatibility
---

drydock targets Argo CD desired-state analysis that can run without live Argo
CD or Kubernetes. It supports the common runtime-offline paths operators need
for local inspection, CI validation, and pull request review.

## Supported At A Glance

- Direct Argo CD `Application` resources.
- Single-source and multi-source Applications.
- Supported local ApplicationSet generators and fixture-backed provider
  generators.
- Desired-vs-desired manifest and image diffs.
- Native directory, Kustomize, local Helm, remote Helm, and remote Kustomize
  rendering paths.
- Structured JSON and YAML output where commands support it.
- Markdown manifest and image diff output for review comments.
- Stable diagnostics in CLI and public API output.

## Runtime Boundary

drydock does not reproduce live cluster or Argo CD server behavior such as API
defaulting, admission mutation, server-side diff, live managed-fields
ownership, live Application health aggregation, sync windows, source signature
verification, or full RBAC simulation.

Use this page for the shape of support. Use the canonical
[Argo CD compatibility notes](/docs/compatibility/) when you need the detailed
matrix and exact boundaries.
