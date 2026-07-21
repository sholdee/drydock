---
title: Compatibility
aliases:
  - /docs/compatibility/
---

drydock analyzes Argo CD desired state without a running Argo CD instance or
Kubernetes cluster. It can still fetch declared Git, Helm, OCI, and remote
Kustomize sources unless `--offline` is set.

Use this page to quickly judge whether drydock fits your repository shape and
which boundaries are intentionally runtime-offline.

## Status Legend

| Status | Meaning |
| --- | --- |
| Native | Implemented with drydock logic or Go libraries in the static binary. |
| Supported with input | Works when you provide explicit local inputs such as fixtures, repo maps, caches, credentials, or plugin policy. |
| Runtime boundary | Intentionally not simulated because it depends on live Argo CD or Kubernetes behavior. |
| Not supported | Known unsupported behavior that drydock should fail closed or report clearly. |

## At A Glance

| Area | Status | What works | Boundary |
| --- | --- | --- | --- |
| Applications | Native | Direct `Application` manifests, single-source and multi-source apps, rendered app-of-apps discovery. | No live application-controller state or sync behavior. |
| ApplicationSets | Native and supported with input | Git directories, Git files, list, matrix, merge, template overrides, `templatePatch`, and fixture-backed provider generators. | No live Kubernetes, SCM, pull-request, cloud, or plugin provider API calls. |
| Sources | Native and supported with input | Local paths, Git cache/fetch, `--repo-map`, HTTP(S) Helm, OCI Helm, OCI artifact sources, remote Kustomize, external Helm value files, and cache lifecycle commands. | `--offline` requires local files, repo maps, or cache hits. Ambient Git helpers and Helm registry config are not read. Private OCI registries are not yet supported for OCI artifact sources. |
| Rendering | Native | Directory, Kustomize, Helm, Jsonnet, Kustomize Helm charts, Argo CD tracking metadata, namespace normalization, and custom health Lua validation. | No `kubectl`, `argocd`, Helm CLI, Kustomize CLI, repo-server wrapper, or live API defaulting. |
| Plugins | Supported with input | Native safe Kustomize CMP compatibility, argocd-vault-plugin placeholder compatibility, in-process public API renderers, trusted exec or container policy with `--enable-plugins`, and `bootstrap.entrypoints` for plugin-rendered app discovery. | No default sidecar plugin execution, ambient plugin loading, or plugin credential injection. |
| Diffs and images | Native | Desired-vs-desired manifest diffs, Git ref diffs, ignored-field normalization, markdown diff previews, and image extraction from PodSpecs and exact `image` keys. | No live managed-fields prediction, server-side diff, or arbitrary string image scanning. |
| Projects and settings | Native and supported with input | Local `AppProject` checks, rendered project discovery, rendered-resource project policy checks, repository Secret metadata, cluster Secret metadata, Argo CD settings metadata, and structured diagnostics. | No full RBAC/Casbin simulation, sync-window scheduling, orphan detection, source signature verification, sync impersonation, live cluster existence checks, or secret credential reads. |
| API and release shape | Native | Public Go API, stable diagnostics, cache event hooks, cache commands, static binary, setup action, PR action, and container image. | Argo CD dependency updates are deliberate compatibility work. |

## Can drydock handle my repo?

| Repository shape | Recommended starting point |
| --- | --- |
| Direct committed `Application` manifests | Run `drydock test apps` from the repository root. |
| ApplicationSet-generated Applications | Use native supported generators; provide fixtures for provider-backed generators. |
| App-of-apps or bootstrap manifests | Let recursive rendered fleet discovery find rendered Applications, use `--discover-kustomize PATH` for explicit Kustomize entrypoints, or use trusted PluginPolicy `bootstrap.entrypoints` when bootstrap apps are plugin-rendered. |
| Multi-source Applications | Use normal commands; add `--repo-map URL=PATH` when external Git sources are already checked out locally. |
| Remote Helm or OCI charts | Let drydock fetch into its chart cache, or pre-populate the cache and use `--offline`. |
| First-class OCI artifact sources (`oci://` + `path:`) | Use normal commands; drydock resolves the tag or constraint to a digest and renders the artifact content. Warm the cache online before `--offline` runs. |
| Private Git, Helm, or remote Kustomize sources | Pass explicit auth flags or local repo maps. drydock does not read ambient credential helpers. |
| Kustomize with Helm charts | Use the native renderer. `kustomize.buildOptions: --enable-helm` is honored without shelling out to Kustomize. |
| Config management plugins | Prefer native compatibility paths. Use trusted plugin policy plus `--enable-plugins` only when exec or container command simulation is truly needed. |
| Pull request review | Use `diff apps`, `diff images`, or the GitHub PR action for markdown comments and artifacts. |

## Detail By Area

### Applications and ApplicationSets

drydock discovers committed Applications and can recursively discover rendered
Applications, ApplicationSets, AppProjects, and Argo CD settings from desired
output. Static committed objects take precedence over default rendered fleet
duplicates.

Supported ApplicationSet generators are local and deterministic. Provider-style
generators are fixture-backed so CI can validate generated output without
calling live provider APIs.

Template render errors are scoped to the failing ApplicationSet as warning
diagnostics, and that ApplicationSet contributes zero generated Applications,
matching the Argo CD controller's `ErrorOccurred` condition behavior.

See [ApplicationSet support](/docs/applicationsets/) and
[repository topologies](/concepts/topologies/).

### Sources and rendering

drydock renders directory, Kustomize, Helm, and Jsonnet sources through native
Go paths. It can fetch declared Git, HTTP(S) Helm, OCI Helm, OCI artifact,
and remote Kustomize resources into explicit caches. `--offline` turns those
source network fetches into cache/local-only requirements.

Rendering intentionally does not call live Argo CD, Kubernetes, `kubectl`,
`argocd`, Helm CLI, or Kustomize CLI. That keeps local and CI analysis fast,
portable, and reproducible.

Covered rendering semantics are validated against real Argo CD through the
[render parity smoke](/concepts/argocd-render-parity/).

See [source acquisition](/concepts/source-acquisition/),
[runtime-offline design](/concepts/runtime-offline/), and
[Argo CD render parity](/concepts/argocd-render-parity/).

### Plugins

Native compatibility paths cover safe Kustomize CMP definitions and
argocd-vault-plugin placeholder redaction. Policy can also declare trusted
exec or container entries, explicit native overrides, and
`bootstrap.entrypoints` for plugin-rendered fleet discovery inputs.

This is a validation compatibility layer, not Argo CD sidecar auto-discovery.
Plugin command execution is never enabled by default. Bootstrap entrypoints are
disabled by static discovery mode, are not disabled by
`--max-discovery-depth 0`, and require trusted static discovery for the
referenced plugin.

See [plugin policy](/plugin-policy/).

### Diffs, images, diagnostics, and settings

drydock compares desired state to desired state. It supports manifest diffs,
image diffs, markdown output for review comments, structured diagnostics,
AppProject checks from local manifests, rendered-resource project policy
checks, and redacted Argo CD settings metadata.

Project diagnostics are render-aware when they need rendered resources. The
default `--project-diagnostics=actionable` mode keeps known local denials
visible and hides AppProject findings that cannot be conclusively enforced
offline. Use `--project-diagnostics=all` for full audit output, or
`--project-diagnostics=off` to suppress AppProject diagnostics. `test` and
`build` print visible AppProject warnings and strict failures on stderr. `diff`
uses stderr for non-markdown output and embeds successful diagnostics in
markdown reports; `diag --render -o json` exposes the same visible findings with
stable diagnostic codes for CI.

It does not predict live API defaulting, admission mutation, server-side apply
ownership, live health aggregation, sync windows, orphaned resources, source
signature verification, destination service account impersonation, or full RBAC
authorization.

See [output workflows](/workflows/output/), [local diffs](/workflows/local-diffs/),
and [troubleshooting](/troubleshooting/).
