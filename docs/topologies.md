---
title: Repository Topologies
---

This page helps operators map common GitOps repository shapes to drydock
commands. It is not a second behavior spec; detailed command, source,
ApplicationSet, and plugin rules live in the linked canonical docs.

## Start With The Repository Root

For a repository that commits Argo CD `Application` manifests, run from the
repository root:

```bash
drydock get apps --path .
drydock test apps --path .
drydock diff apps --repo . --ref HEAD --ref-orig main
```

`get apps`, `test apps`, and `diff apps` discover Applications, render desired
state, and report diagnostics without contacting live Argo CD or Kubernetes.
The operator command flow, output formats, selectors, diff behavior, and exit
codes are owned by [`usage.md`](usage.md).

## App-Of-Apps And Rendered Discovery

If the repository commits a bootstrap Application or Helm app-of-apps source,
fleet commands recursively render discovered Applications to find additional
Argo CD objects until discovery converges:

```bash
drydock get apps --path .
drydock test apps --path .
```

Use this topology when committed bootstrap inputs render child `Application`,
`ApplicationSet`, `AppProject`, or Argo CD settings objects. Use
`--discovery-mode static` when you want committed static discovery only, or
`--max-discovery-depth 0` when you want normal static discovery while disabling
recursive rendered fleet discovery. These flags and precedence rules are owned
by [`usage.md`](usage.md).

## Kustomize Bootstrap Inputs

Some repositories store Argo CD bootstrap objects behind Kustomize overlays
instead of committing inflated manifests. Add each local Kustomize entrypoint
explicitly:

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

`--discover-kustomize` paths are relative to `--path`, must stay under that
repository tree, and are rendered with drydock's native Kustomize renderer.
Rendered discovery behavior is owned by [`usage.md`](usage.md); Kustomize
source acquisition and remote reference behavior are owned by
[`source-acquisition.md`](source-acquisition.md).

## Multi-Repository Sources

Applications can reference sources outside the selected repository. Prefer
explicit local mappings in developer workstations and CI jobs:

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
```

Repository source resolution prefers `--repo-map`, then an existing local
source path, then declared Git cache or fetch behavior, then a clear failure.
Use `--offline` when the command must use only local files, repo maps, and
existing cache entries:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

Git, Helm, remote Kustomize, cache, auth, and `--offline` behavior are owned by
[`source-acquisition.md`](source-acquisition.md).

## ApplicationSet Fleets

Repositories that commit `ApplicationSet` manifests can use the same fleet
commands:

```bash
drydock get apps --path .
drydock test apps --path .
```

Local deterministic generators are expanded offline. Provider-backed
generators use explicit local fixture files:

```bash
drydock get apps --path . \
  --appset-provider-fixture fixtures/appset-providers.yaml
```

Supported generators, fixture schema, template parameters, and strict-mode
diagnostics are owned by [`applicationsets.md`](applicationsets.md).

## Config Management Plugin Sources

For repositories that rely on Argo CD config management plugins, start with a
normal render test:

```bash
drydock test apps --path .
```

Safe Kustomize wrapper plugins may render through drydock's native Kustomize
path. Other plugin sources fail closed unless a drydock plugin policy matches
the plugin. Trusted exec policy also requires `--enable-plugins`:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```

Plugin policy provenance, native plugin compatibility, and exec security
controls are owned by [`plugin-policy.md`](plugin-policy.md).

## Pull Request Topologies

For one local repository, compare Git refs without mutating the checkout:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main -o markdown
```

For two prepared trees, compare explicit paths:

```bash
drydock diff apps --path ./current --path-orig ../baseline
drydock diff images --path ./current --path-orig ../baseline -o markdown
```

Changed-only selection, manifest and image outputs, ignore rules,
resource-filter flags, and diff exit codes are owned by [`usage.md`](usage.md).
Source cache placement and source-network behavior during PR checks are owned
by [`source-acquisition.md`](source-acquisition.md).

## What To Verify Per Topology

Run the command that matches the operator workflow first:

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

Use `get apps` to confirm discovery, `test apps` to prove render health without
manifest output, and `diag` to inspect diagnostics and settings summaries. For
source-heavy repositories, add `--offline` after caches and repo maps are in
place so cache-only behavior is exercised. For plugin-heavy repositories, add
the same plugin policy flags used by CI.
