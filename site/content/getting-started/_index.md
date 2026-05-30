---
title: Getting Started
---

Use drydock when you want to inspect Argo CD desired state from repository
contents before a controller sees it. The default commands do not call a live
Argo CD server or Kubernetes cluster.

## Install

Install the CLI locally with Go:

```bash
go install github.com/sholdee/drydock/cmd/drydock@latest
```

For CI installs, use the drydock setup action in GitHub Actions instead of
installing by hand in each workflow.

## First Commands

Run these from the GitOps repository you want to inspect:

```bash
drydock test apps --path .
drydock get apps --path .
drydock diag --path .
```

Start with `drydock test apps --path .`. It discovers Applications and renders
them without printing manifest bodies, which makes it the fastest first check
for render failures. `get apps` shows discovered Applications. `diag` uses the
same discovery and render validation path, then reports repository diagnostics.

If you are setting this up for CI, go to [GitHub Actions](/workflows/github-actions/)
next.

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
