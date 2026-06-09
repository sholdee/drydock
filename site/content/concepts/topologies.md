---
title: Repository Topologies
aliases:
  - /docs/topologies/
---

Choose the command shape before tuning flags. The examples below map common
GitOps repository layouts to drydock commands; detailed CLI behavior lives in
the [reference](/reference/), source resolution lives in
[Source acquisition](/concepts/source-acquisition/), ApplicationSet behavior
lives in the [ApplicationSet reference](/docs/applicationsets/), and plugin
controls live in [Plugin policy](/plugin-policy/).

`get apps`, `test apps`, `diff apps`, and the verification flows on this page
are [runtime-offline](/concepts/runtime-offline/): they discover, render, diff,
and report diagnostics without contacting live Argo CD or Kubernetes.
`--offline` is narrower; it also requires source acquisition to use only local
files, repo maps, and existing caches.

## Choose The Shape

- Committed `Application` manifests: run from the repository root.
- Bootstrap or app-of-apps inputs: use fleet commands and let rendered
  discovery find generated Argo CD objects.
- Bootstrap objects behind Kustomize: add each `--discover-kustomize`
  entrypoint.
- Applications with external sources: add `--repo-map` or pre-populated
  source caches, then verify `--offline` if CI must be source-offline.
- ApplicationSet provider generators: use local fixture files.
- Config management plugins: prefer native Kustomize compatibility, then use a
  trusted policy with `--enable-plugins` only when command-backed rendering is
  required.
- Pull requests: compare Git refs in one checkout or compare two prepared
  trees explicitly.

## Committed Applications

```bash
drydock get apps --path .
drydock test apps --path .
drydock diff apps --repo . --ref HEAD --ref-orig main
```

Use this when Argo CD `Application` manifests are committed directly. Start at
the repository root so repository-relative paths, source fallback, and
changed-only selection line up with the checkout under review.

## App-Of-Apps And Rendered Discovery

```bash
drydock get apps --path .
drydock test apps --path .
```

By default, fleet commands recursively render bootstrap Applications and
app-of-apps sources to discover rendered `Application`, `ApplicationSet`,
`AppProject`, and Argo CD settings objects until discovery converges.

Use `--discovery-mode static` when only committed static objects should count.
Use `--max-discovery-depth 0` when normal static discovery should still run but
recursive rendered fleet discovery should be disabled:

```bash
drydock get apps --path . --discovery-mode static
drydock get apps --path . --max-discovery-depth 0
```

## Kustomize Bootstrap

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

Use this when Argo CD objects live behind Kustomize overlays instead of
committed inflated YAML. Each `--discover-kustomize` path is relative to
`--path`, must stay under that selected repository tree, and is rendered with
drydock's native Kustomize renderer.

## Multi-Repository Sources

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

Use `--repo-map` for adjacent checkouts and CI jobs that prepare dependencies
explicitly. Repository source resolution prefers `--repo-map`, then an existing
local source path, then declared Git cache or fetch behavior, then a clear
failure.

Use `--offline` after local files, repo maps, and caches are in place. In
offline mode, drydock does not fetch missing Git, Helm, OCI Helm, or remote
Kustomize sources.

## ApplicationSets

```bash
drydock get apps --path .
drydock test apps --path .
drydock get apps --path . \
  --appset-provider-fixture fixtures/appset-providers.yaml
```

Local generators expand offline. Provider-backed generators need explicit
fixture files so discovery stays deterministic and does not contact live
Kubernetes, SCM provider, pull-request, cloud, or plugin-service APIs.

## Plugin Sources

```bash
drydock test apps --path .
drydock test apps --path . --plugin-policy-ref main --enable-plugins
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```

Start with native compatibility paths. Kustomize wrapper plugins should render
through drydock's native Kustomize renderer when possible.

Use trusted plugin policy only when the repository depends on command-backed
exec or container rendering, and pass `--enable-plugins` for those engines. For
Docker sidecar CMP descriptors, copy only safe descriptor metadata into policy:
the plugin name as the policy key, trusted static discovery, and optional
generate argv metadata. Prefer `engine: native-kustomize` for wrapper plugins.
For trusted container policy, use digest-pinned images, explicit Application
parameter allowlists, and repository-copy includes for any trusted sibling
paths the plugin must read. Add `bootstrap.entrypoints` when plugin-rendered
output creates fleet `Application` or `ApplicationSet` objects.

## Pull Request Topologies

Compare Git refs without mutating the checkout:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main -o markdown
```

Compare two prepared trees when CI has already checked out current and
baseline paths:

```bash
drydock diff apps --path ./current --path-orig ../baseline
drydock diff images --path ./current --path-orig ../baseline -o markdown
```

Changed-only selection, manifest and image outputs, ignore rules, resource
filters, and diff exit codes are covered by the [reference](/reference/) and
[local diff workflow](/workflows/local-diffs/).

## Verify The Topology

Use `get apps` to confirm discovery, `test apps` to prove render health without
manifest output, and `diag` to inspect diagnostics and settings summaries:

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

Reuse the same topology flags during verification:

| Topology | Verification additions |
| --- | --- |
| App-of-apps | Add `--discovery-mode static` or `--max-discovery-depth 0` only when verifying those reduced discovery modes. |
| Kustomize bootstrap | Add every `--discover-kustomize` entrypoint. |
| Multi-repository sources | Add the same `--repo-map` entries; add `--offline` after caches are populated. |
| ApplicationSets | Add the same `--appset-provider-fixture` files used by CI. |
| Plugins | Add the same policy ref and `--enable-plugins` flags used by CI. |
| Pull requests | Use the same `diff apps` ref or path comparison that the review workflow runs. |
