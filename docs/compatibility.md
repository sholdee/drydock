---
title: Argo CD Compatibility Notes
---

`drydock` tracks Argo CD compatibility for runtime-offline desired-state
analysis. Default commands discover, render, test, diff, inspect images, and
diagnose local Argo CD desired state without contacting live Argo CD or
Kubernetes runtime. Declared sources may be fetched into explicit caches unless
`--offline` is set.

## Runtime Model

Supported:

- Direct `Application` CRs.
- Single-source and multi-source Applications.
- Desired-vs-desired manifest and image diffs.
- Render tests with per-Application `PASS`, `FAIL`, and `SKIPPED` status.
- Structured JSON and YAML outputs for list, test, diagnostic, and diff
  commands where supported.
- Stable diagnostic codes in CLI and public API output.
- Render concurrency with deterministic output ordering.
- Partial build results for embedding callers when some selected Applications
  fail to render.

Not reproduced offline:

- Kubernetes API defaulting.
- Admission mutation.
- Live Argo CD server-side diff.
- Live server-side apply field ownership prediction.
- Managed-fields ignores when ownership data exists only on the live cluster.
- Live Argo CD Application health aggregation.
- Live destination cluster existence checks.
- Sync window enforcement.
- Source integrity signature verification.
- Project-scoped cluster Secret enforcement when cluster Secret metadata is not
  present in the analyzed desired state.
- Full Argo CD RBAC/Casbin authorization simulation.

These live-state behaviors must stay explicit runtime-boundary diagnostics, not
silent approximations.

## Applications And ApplicationSets

Supported:

- Direct Application discovery from repository manifests.
- Recursive rendered fleet discovery from desired output, including rendered
  `Application`, `ApplicationSet`, `AppProject`, and Argo CD settings objects.
  Static committed objects take precedence over default rendered fleet
  duplicates.
- Explicit `--discover-kustomize PATH` discovery from rendered local
  Kustomize entrypoints. Explicitly rendered Kustomize objects take precedence
  over committed duplicates because the path is the operator-selected Argo CD
  entrypoint.
- ApplicationSet Git directories, Git files, list, matrix, and merge
  generators.
- ApplicationSet `templatePatch` rendering and strategic merge patching, with
  generated `spec.project` preserved.
- ApplicationSet list `elementsYaml`, including matrix-interpolated
  `elementsYaml`.
- Generator selectors and generator template overrides for supported
  generators.
- Nested matrix/merge combinations where the Argo CD v3 nested JSON API
  permits them.
- Explicit local YAML/JSON fixtures for cluster, clusterDecisionResource, SCM
  provider, pull-request, and plugin ApplicationSet generators.
- Fail-closed fixture diagnostics for invalid provider fixtures, no provider
  matches, and unsupported provider filters.
- Warning diagnostics when a discovered ApplicationSet generates zero
  Applications; `--strict` promotes those diagnostics to errors.

Not supported:

- Live Kubernetes, Argo CD, SCM provider, pull-request, cloud, or plugin
  service API calls for ApplicationSet generation.
- Unsupported generators without diagnostics.

See `docs/applicationsets.md` for generator details and fixture schema.

## Sources And Acquisition

Supported:

- Kustomize, directory, local Helm chart, and chart-only remote Helm sources.
- HTTP(S) and OCI Helm chart fetching by default for chart-only remote Helm
  sources.
- Kustomize `helmCharts` rendered through the shared Go-library Helm path.
- Helm `$ref/...` external value files and file parameters from Git sources.
- HTTP(S) remote Helm value files through the remote-resource cache.
- Remote Kustomize HTTP(S) file refs and Git refs through the remote-resource
  cache.
- Default Git clone/fetch into the Git cache for unmapped path sources missing
  from the local tree.
- `--repo-map URL=PATH` path-source resolution for local external checkouts.
- Explicit Git HTTPS bearer/basic auth and SSH key-file auth.
- Explicit HTTP(S) Helm bearer/basic auth.
- Explicit HTTP(S) remote Kustomize bearer/basic auth.
- Explicit Helm OCI registry config path plumbing.
- Cache event API/reporting for Git, Helm, and remote Kustomize acquisition,
  with redacted targets and errors.
- Local cache lifecycle commands for recognized Git, chart, and remote
  Kustomize cache layouts.

Important boundaries:

- `--offline` disables Git, Helm chart, and remote Kustomize resource network
  fetching and requires cache hits, repo maps, local files, or local chart
  availability.
- `--repo-map` takes precedence over local fallback and network fetching.
- Top-level `--repo` for Git ref diffs supports local repository paths only.
- Missing HTTP(S) and OCI Helm chart dependencies declared in `Chart.yaml` are
  acquired through drydock's native chart cache, without shelling out to
  `helm dependency build` or mutating the source checkout. Local `file://`,
  repository-alias, or otherwise unresolved dependencies must be available
  under `charts/` and fail closed when missing.
- Ambient Git credential helpers, ambient Helm registry config, and secret
  credential fields from discovered repository Secrets are not read.
- Cache lifecycle commands are local filesystem operations only; they do not
  render, fetch, read credential flags, or retry failed acquisitions.

See `docs/source-acquisition.md` for cache, auth, and remote source details.

## Rendering

Supported:

- Argo CD resource tracking metadata injection after rendering and destination
  namespace normalization, without contacting a live application controller.
  The default tracking method is `annotation`, the default instance label key
  is `app.kubernetes.io/instance`, `annotation`, `label`, and
  `annotation+label` modes are supported, `installationID` is applied for
  annotation-based tracking, and CRDs are not stamped.
- Directory rendering that skips values-like YAML documents only when both
  `apiVersion` and `kind` are absent, and fails clearly when exactly one is
  present.
- Directory rendering honors `+argocd:skip-file-rendering`, walks hidden
  directories when `recurse` is enabled, renders Jsonnet through native Go
  libraries with extVars, TLAs, code mode, env substitution, and repo-relative
  libs, and skips drydock cache metadata sidecars.
- Local Kustomize rendering with supported `kustomize.buildOptions`:
  `--enable-helm`, `--helm-api-versions`, and
  `--load-restrictor=LoadRestrictionsRootOnly|LoadRestrictionsNone`.
  Kustomize `helmCharts` render through drydock's Go-library Helm path, so
  chart inflation does not require an external Kustomize binary or CLI
  `--enable-helm`.
- Kustomize Helm `valuesFile` and `additionalValuesFiles` entries outside the
  kustomization directory when the resolved file remains inside the repository
  root.
- Kustomize Helm remote HTTP(S) value files through the same remote-resource
  cache path used by Helm sources.
- Remote Kustomize refs in `resources`, `bases`, `components`,
  `patches.path`, `patchesJson6902.path`, non-inline
  `patchesStrategicMerge`, `generators`, `transformers`, `validators`,
  `configurations`, `crds`, `openapi.path`, `replacements.path`, and generator
  file/env refs.
- Kustomize root Git remote refs such as
  `https://github.com/org/repo?ref=v1` when they resolve to a repository-root
  Kustomization directory.
- Discovered safe Kustomize build CMP definitions interpreted by drydock's
  native Kustomize renderer.
- Bounded warning diagnostics when a discovered sidecar CMP static
  `discover.fileName` or `discover.find.glob` rule matches a native-rendered
  source. drydock does not execute `discover.find.command`.
- Trusted drydock plugin policy entries for native AVP compatibility, native
  plugin overrides, and explicitly enabled exec CMP compatibility.
- Config management plugin source detection with fail-closed diagnostics in
  the CLI and default Go client.
- Injectable in-process plugin renderers, named plugin registry dispatch, and
  plugin timeout controls for embedding callers. Public plugin requests include
  source metadata, `$ref` roots and source metadata, kube version, and API
  versions.

Documented runtime-offline safety boundary:

- Directory rendering rejects symlinked source path components and skips
  symlinked manifest files, even when an Argo CD repo-server could render an
  in-repository symlink target.

Not supported:

- Arbitrary CLI config management plugin execution outside trusted
  `engine: exec` policy plus `--enable-plugins`.
- Shellout plugin adapters or plugin command execution by default.
- Argo CD repo-server sidecar plugin discovery.
- Ambient plugin configuration or environment loading.
- Plugin credential injection.

See `docs/plugin-policy.md` for trusted policy provenance, supported engines,
schema, and exec security controls.

## Diff, Images, And Output

Supported:

- `diff apps` desired-vs-desired manifest diffs.
- `diff app` desired-vs-desired diffs for one Application, including
  add/delete diffs when the Application exists on only one side.
- `diff apps`, `diff app`, and `diff images` against local Git refs through
  temporary local snapshots, without `git` shellouts or checkout mutation.
- Git-style ANSI color for text diff output with
  `--color=auto|always|never`; JSON and YAML payloads remain plain.
- Changed-only Application selection with safe render-all fallback and strict
  failure mode.
- Default diff ignores for common Helm chart/version labels and pod-template
  `checksum/*` annotations.
- `--show-ignored-fields` to include drydock-default ignored fields while
  keeping Argo CD diff customizations active.
- `--strip-attr KEY` normalization for metadata label and annotation keys.
- Application-level `spec.ignoreDifferences[]` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers`.
- Global `resource.customizations.ignoreDifferences.*`,
  `knownTypeFields.*`, selected compare options, resource exclusions, and
  resource inclusions.
- Repeated-resource last-wins behavior inside one Application, with a
  diagnostic.
- `get images` and `diff images` rendered image reference extraction from
  PodSpec container images and scalar fields whose key is exactly `image`.

Not supported:

- Applying `ignoreResourceUpdates` to desired-vs-desired diffs.
- Managed-fields prediction from live state.
- Arbitrary string scanning for image references.

## Diagnostics, Projects, And Settings

Supported:

- `diag --path` repository diagnostics through the render validation path.
- `diag -o json|yaml` structured diagnostic reports.
- `diag --cache-events` optional cache acquisition event reporting.
- `diag --settings -o json|yaml` redacted settings summaries for parsed Argo
  CD settings metadata.
- Local AppProject manifest discovery.
- Rendered AppProject discovery from app-of-apps/bootstrap desired output.
- Offline diagnostics for Application source repository, destination
  server/name/namespace, and source namespace validation from local AppProject
  manifests.
- AppProject RBAC role and policy parsing as reported metadata, without
  simulating authorization.
- Repository credential matching diagnostics based on discovered repository
  Secret metadata only, without reading secret credential fields.
- Cluster Secret metadata parsing for offline named-cluster destination
  diagnostics and project-scoped cluster checks when the desired state includes
  the relevant cluster Secret metadata. Only `name`, `server`, `namespaces`,
  `clusterResources`, and `project` are decoded.
- `argocd-cmd-params-cm` runtime-boundary diagnostics for settings that imply
  live repo-server, controller, or ApplicationSet controller behavior. These
  settings are parsed as metadata and do not mutate render behavior.
- Custom health Lua validation in `test apps` and `test app`, executed offline
  against rendered desired manifests.
- Health and action customization parsing and diagnostics, including
  `useOpenLibs` metadata and redacted SHA-256 Lua summaries.
- Warning diagnostics for version-specific `kustomize.buildOptions.<version>`
  and `kustomize.path.<version>` settings because drydock does not select
  external Kustomize binaries.

Not supported:

- Raw Lua body output in structured settings summaries.
- Resource action Lua execution.
- Live Argo CD health aggregation.
- Live AppProject authorization decisions.

## Public API And Releases

Supported:

- Public Go API in `pkg/drydock` for Application listing, rendering, manifest
  diffs, image diffs, source acquisition hooks, plugin renderer hooks, and
  stable diagnostics.
- `cache path`, `cache list`, `cache prune`, and `cache delete` local cache
  lifecycle commands.
- Benchmarks for repeated local Application rendering and ApplicationSet
  expansion.
- Static binary, setup-action, and GHCR container release shape documented in
  `docs/release.md`.

The module pins Argo CD dependencies. Upgrade Argo CD deliberately and update
compatibility tests and this document in the same change.
