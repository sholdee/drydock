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
| ApplicationSets | Native and supported with input | Git directories, Git files, OCI directories and files, list, matrix, merge, template overrides, `templatePatch`, and fixture-backed provider generators. | No live Kubernetes, SCM, pull-request, cloud, or plugin provider API calls. |
| Sources | Native and supported with input | Local paths, Git cache/fetch, `--repo-map`, HTTP(S) Helm, OCI Helm, OCI artifact sources, remote Kustomize, external Helm value files, and cache lifecycle commands. | `--offline` requires local files, repo maps, or cache hits. Ambient Git helpers and Helm registry config are not read. Private OCI artifact registries use the explicit `--oci-*` credential flags — one global set for every OCI registry in a run. |
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
| Remote Helm or schemeless OCI charts (`repoURL: ghcr.io/org/charts` plus `chart:`) | Let drydock fetch into its chart cache, or pre-populate the cache and use `--offline`. |
| First-class OCI artifact sources (every `repoURL: oci://...`) | Use normal commands; drydock resolves the tag or constraint to a digest and renders the artifact content at `path` (default `.`), as Argo CD's repo-server does. `chart:` on such a source is ignored and reported as the advisory `source.oci-chart-ignored` warning. Warm the cache online with `--oci-cache-dir` before `--offline` runs. |
| Private Git, Helm, OCI artifact, or remote Kustomize sources | Pass explicit auth flags (`--registry-config` supplies per-registry credentials for OCI artifact sources and OCI chart registries) or local repo maps. drydock does not read ambient credential helpers. Git SSH is the exception: the runner's ssh-agent and `~/.ssh` identities are used when no `--git-ssh-key-file` is given. Host keys are always verified against known_hosts. |
| Kustomize with Helm charts | Use the native renderer. `kustomize.buildOptions: --enable-helm` is honored without shelling out to Kustomize. |
| Config management plugins | Prefer native compatibility paths. Use trusted plugin policy plus `--enable-plugins` only when exec or container command simulation is truly needed. |
| Pull request review | Use `diff apps`, `diff images`, or the GitHub PR action for markdown comments and artifacts. |

## Detail By Area

### Applications and ApplicationSets

drydock discovers committed Applications and can recursively discover rendered
Applications, ApplicationSets, AppProjects, and Argo CD settings from desired
output. Static committed objects take precedence over default rendered fleet
duplicates.

Supported ApplicationSet generators are deterministic. The OCI generator reads
an artifact acquired like an `oci://` Application source, so it follows the
same cache and `--offline` rules. Provider-style generators are fixture-backed
so CI can validate generated output without calling live provider APIs.

Template render errors are scoped to the failing ApplicationSet as warning
diagnostics, and that ApplicationSet contributes zero generated Applications,
matching the Argo CD controller's `ErrorOccurred` condition behavior.

drydock checks the sources of each Application as the Argo CD controller
does. A single source needs `path` or `chart`, a multi-source entry needs
`path`, `chart`, or `ref`, and a `chart` needs `targetRevision`. If a check
fails, Argo CD sets an `InvalidSpecError` condition and renders nothing, and
drydock fails the Application with the same text. An empty `spec.project`
is the `default` project.

For an Application with `spec.sourceHydrator`, drydock renders the sync
source (`syncSource.path` at `syncSource.targetBranch`), because that is
what Argo CD compares. It reads that branch from the local tree only when
the self-repo rule of every other source tracks the branch there, else from
a `--repo-map` Git repository, the Git cache, or a fetch. drydock cannot run the hydrator offline, so it warns
`source.hydrator-sync-source`: a change to the dry source shows only after
the hydrator commits it.

When drydock reads an `argocd-cmd-params-cm` that names its namespace, it
applies `application.namespaces` and `applicationsetcontroller.namespaces`. Argo CD
never reconciles an Application or an ApplicationSet outside those
namespaces and the Argo CD namespace, so drydock skips it with the
`discovery.namespace-not-enabled` warning.

See [ApplicationSet support](/docs/applicationsets/) and
[repository topologies](/concepts/topologies/).

### Sources and rendering

drydock renders directory, Kustomize, Helm, and Jsonnet sources through native
Go paths. It can fetch declared Git, HTTP(S) Helm, OCI Helm, OCI artifact,
and remote Kustomize resources into explicit caches. `--offline` turns those
source network fetches into cache/local-only requirements.

The build environment variables `ARGOCD_APP_REVISION`,
`ARGOCD_APP_REVISION_SHORT`, and `ARGOCD_APP_REVISION_SHORT_8` carry the
revision that the Argo CD repo-server resolves. A Git source gets the commit
SHA of the tree that drydock renders: the fetched commit, the HEAD commit of
the `--path` tree or of a `--repo-map` directory, or the snapshot commit of
each side of a `--ref` diff. In a multi-source Application a Git source gets
the `git ls-remote` form, as in Argo CD: an abbreviated SHA stays as written,
and a tag gives the commit SHA. An OCI artifact source gets the
manifest digest, and a chart from a Helm repository gets the chart version. When the tree is
not the top of a Git repository, drydock cannot tell the commit and keeps
the spec `targetRevision`. A source that renders the local tree reports the
local commit, which differs from the commit that Argo CD sees after a push
or a merge.

For a source without an explicit type, drydock picks the type as the Argo
CD repo-server does. The last file name in the source directory, in name
order, that ends in `Chart.yaml` or is a kustomization file decides it. So
a Kustomize directory that also holds `traefik-HelmChart.yaml` renders as a
Helm chart.

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

AppProject destination checks follow Argo CD 3.6.1: a cluster Secret scoped to
a project adds that project's implicit destinations, a server-only destination
at `https://kubernetes.default.svc` or a name-only `in-cluster` destination
with no cluster Secret is the built-in `in-cluster` cluster (denied when
argocd-cm `cluster.inClusterEnabled` is `"false"`), and a check that depends on
a cluster name, server, or project scoping drydock cannot resolve offline is
deferred rather than denied. argocd-cm `globalProjects` are not merged. The
[render parity smoke](/concepts/argocd-render-parity/) checks these
destination rules against a live Argo CD.

It does not predict live API defaulting, admission mutation, server-side apply
ownership, live health aggregation, sync windows, orphaned resources, source
signature verification, destination service account impersonation, or full RBAC
authorization.

See [output workflows](/workflows/output/), [local diffs](/workflows/local-diffs/),
and [troubleshooting](/troubleshooting/).
