---
title: Reference
---

Use this hub when you need the operator-facing details behind the quickstart.
Start with [Getting started](/getting-started/) for the first local run.

## Command And API Reference

- [CLI Reference](/reference/cli/): command behavior for discovery, build,
  render tests, diagnostics, and cache lifecycle.
- [Go API](/reference/go-api/): embedding defaults, result contracts, and
  plugin renderer integration.

## Workflows

- [GitHub Actions](/workflows/github-actions/): setup action, PR action,
  permissions, comments, artifacts, outputs, and caches.
- [Local diffs](/workflows/local-diffs/): compare rendered desired state from a
  current tree against a baseline tree.
- [Output controls](/workflows/output/): text, JSON, markdown, raw diff, and
  HTML diff outputs.

## Concepts

- [Source acquisition](/concepts/source-acquisition/): Git, Helm, remote
  Kustomize, caches, offline behavior, and auth.
- [Repository topologies](/concepts/topologies/): committed Applications,
  app-of-apps, bootstrap manifests, multi-repository sources, and plugins.
- [Changed-only diffs](/concepts/changed-only/): PR-focused selection and
  dependency-aware rendering.

## Detailed References

- [Compatibility](/compatibility/): supported Argo CD behavior and
  runtime-boundary status.
- [ApplicationSets](/docs/applicationsets/): supported generators, fixtures,
  and template parameters.
- [Plugin policy](/plugin-policy/): trusted policy provenance, native
  engines, exec/container rendering, bootstrap entrypoints, cache mounts, and
  schema.
