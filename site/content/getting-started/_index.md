---
title: Getting Started
---

Use drydock when you want to inspect Argo CD desired state from repository
contents before a controller sees it. The default commands do not call a live
Argo CD server or Kubernetes cluster.

## First Commands

Run these from the GitOps repository you want to inspect:

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

`get apps` shows discovered Applications. `test apps` renders them without
printing manifest bodies. `diag` uses the same discovery and render validation
path, then reports repository diagnostics.

## Compare Changes

Compare the current tree against a baseline worktree:

```bash
drydock diff apps --path . --path-orig ../baseline
drydock diff images --path . --path-orig ../baseline -o markdown
```

Or compare local Git refs without creating a second checkout:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main -o markdown
```

## Runtime Offline Does Not Mean Source Offline

drydock is runtime-offline: it does not depend on live Kubernetes or Argo CD.
Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources may still be
fetched into explicit caches during render commands.

Use `--offline` when a run must use only local files, repo maps, and existing
cache entries:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

For full command coverage, see the [CLI usage guide](/docs/usage/). For source
fetching, cache, and auth details, see [Source acquisition](/docs/source-acquisition/).
