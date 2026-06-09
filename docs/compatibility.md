---
title: Argo CD Compatibility Notes
---

`drydock` tracks Argo CD compatibility for runtime-offline desired-state
analysis. Default commands discover, render, test, diff, inspect images, and
diagnose local Argo CD desired state without contacting live Argo CD or
Kubernetes runtime. Declared sources may be fetched into explicit caches unless
`--offline` is set.

## Render Parity Validation

Argo CD remains the semantic reference for generated desired manifests. drydock
keeps normal commands runtime-offline, then validates compatibility work with
an isolated Argo CD render parity smoke.

The smoke installs the pinned Argo CD version into kind, serves the
`testdata/argocd-parity` fixture repository to Argo CD, renders the same
Applications with drydock, and compares generated desired manifests. It runs
manually for maintainer validation and selectively in CI when render parity
fixtures or semantic-rendering dependencies change. See `docs/release.md` for
maintainer workflow details.

It also runs a thin live AppProject Application-spec policy sanity check for
source repository, destination, and source namespace outcomes against the real
Argo CD instance.

This check is scoped to generated desired state. Live-only runtime behavior
such as Kubernetes defaulting, admission mutation, server-side diff, managed
fields, health aggregation, sync behavior, and controller state remains outside
drydock's runtime-offline boundary. It is not broad live authorization parity
and excludes RBAC/Casbin, sync windows, orphaning, signatures, sync
impersonation, and rendered-resource policy.

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
- Orphaned resource detection.
- Source integrity signature verification.
- Destination service account sync impersonation.
- Project-scoped cluster Secret enforcement when cluster Secret metadata is not
  present in the analyzed desired state.
- Full Argo CD RBAC/Casbin authorization simulation.

These live-state behaviors must stay explicit runtime boundaries, not silent
approximations. drydock may report deferred diagnostics when it can identify a
specific unevaluated policy decision without creating strict-mode noise for
valid runtime-only configuration.

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

See `site/content/docs/applicationsets.md` for generator details and fixture
schema.

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

See `site/content/concepts/source-acquisition.md` for cache, auth, and remote
source details.

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
  plugin overrides, and explicitly enabled exec/container CMP compatibility.
- Trusted plugin policy bootstrap entrypoints for plugin-rendered Argo CD
  bootstrap objects.
- Trusted plugin policy `configManagementPlugin` seeds for static
  `discover.fileName` / `discover.find.glob` metadata and optional
  `generate.command` / `generate.args` compatibility metadata. Unnamed
  Application plugin sources may match these trusted static rules.
- Trusted exec/container plugin policies that gate Application plugin
  parameters through `parameters.allow`, including string argv substitution and
  constrained path parameters.
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
  `engine: exec` or `engine: container` policy plus `--enable-plugins`.
- Shellout plugin adapters or plugin command execution by default.
- Argo CD repo-server sidecar plugin discovery.
- Ambient plugin configuration or environment loading.
- Untrusted CMP descriptor execution, unallowlisted Application plugin
  parameters, or Application plugin env for policy-backed native engines.
- Plugin credential injection.

See `site/content/docs/plugin-policy/` for trusted policy provenance, supported
engines, schema, and command security controls.

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

- `diag --path` repository diagnostics through static discovery, ApplicationSet
  expansion, and settings metadata.
- `diag --render` render-backed diagnostic reports.
- `diag -o json|yaml` structured diagnostic reports.
- `diag --cache-events` optional render-backed cache acquisition event
  reporting.
- `diag --plugin-executions` optional render-backed plugin execution metadata.
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
- Project diagnostic modes. The default `actionable` mode reports local
  AppProject denials that drydock can enforce offline and hides deferred or
  metadata-only AppProject diagnostics. Use `--project-diagnostics=all` for full
  compatibility audits, or `--project-diagnostics=off` to suppress AppProject
  diagnostics.
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

### AppProject Field Semantics

This matrix is audited against Argo CD `v3.4.3`
(`1801122b4391cad4961301f787006dc9a88c2dd3`). It records current drydock
behavior. Strict render tests promote visible project and repository warnings
after the selected project diagnostic mode is applied.

| Field | Upstream owner | drydock status | Current diagnostic | Offline input and next phase |
| --- | --- | --- | --- | --- |
| `spec.sourceRepos` | `types.go`, `IsSourcePermitted`, Argo repo metadata merge | Supported | `project` warning for denied sources; `repository` warning for mismatched repository Secret project metadata | Application sources plus redacted repository metadata; parity fixtures present, future gaps Phase 3 |
| `spec.destinations` | `types.go`, `IsDestinationPermitted`, `isDestinationMatched` | Supported with named-cluster metadata caveats | `project` warning for denied destinations or unresolved name-only server policy | Application destination plus redacted cluster metadata; parity fixtures present, deferred metadata cases Phase 3 |
| `spec.description` | `types.go` | Metadata-only | None | Decoded as typed metadata |
| `spec.sourceNamespaces` | `types.go`, `IsAppNamespacePermitted` | Supported | `project` warning for denied Application source namespace | Application namespace plus controller namespace assumption; parity fixtures present |
| `spec.permitOnlyProjectScopedClusters` | `types.go`, `IsDestinationPermitted` project-cluster check | Deferred when cluster metadata is unavailable; supported when redacted project-scoped cluster metadata is present | `project` warning for deferred metadata or known denial | Redacted cluster Secret `project` metadata; without it drydock reports a runtime-boundary deferral rather than a hard decision |
| `spec.clusterResourceWhitelist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources | Rendered objects plus offline scope knowledge; unknown custom-resource scope defers with `project.resource-scope-deferred` only when possible scopes change or defer the policy outcome |
| `spec.clusterResourceBlacklist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources | Rendered objects plus offline scope knowledge; unknown custom-resource scope defers with `project.resource-scope-deferred` only when possible scopes change or defer the policy outcome |
| `spec.namespaceResourceWhitelist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources; `project.resource-destination-denied` for rendered object namespace denials | Rendered objects plus destination namespace normalization; discovery-only workflows do not evaluate rendered resource policy |
| `spec.namespaceResourceBlacklist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources; `project.resource-destination-denied` for rendered object namespace denials | Rendered objects plus destination namespace normalization; discovery-only workflows do not evaluate rendered resource policy |
| `spec.roles` | `types.go`, `ValidateProject`, `ProjectPoliciesString`, Argo RBAC runtime | Metadata-only | `project.rbac-metadata-only` warning when roles are present | Role presence only; no Casbin authorization, claims, scopes, or token-status simulation |
| `spec.syncWindows` | `types.go`, `SyncWindow.Matches`, `SyncWindows.CanSync`, Argo sync runtime | Runtime-bound; documented only | None | Depends on clock, timezone, destination, and manual vs automated operation context |
| `spec.orphanedResources` | `types.go`, Argo application controller live orphan scan | Unsupported live-cluster behavior | None | Requires live namespace inventory, ownership graph, orphan exclusions, and controller state |
| `spec.signatureKeys` | `types.go`, Argo GPG verification in `controller/state.go` and repo-server verification | Unsupported runtime verification | None | Requires GPG keyring state, repo-server verification result, and resolved commit metadata |
| `spec.destinationServiceAccounts` | `types.go`, `ValidateProject`, `controller/sync.go` sync impersonation runtime | Runtime-bound metadata | None | Sync impersonation is applied during controller sync; drydock does not mutate REST config or validate live service account authorization |

### AppProject Current Behavior Traceability

This table maps the current drydock code paths and tests to the semantics above.
Future audit phases may replace current-behavior tests as deferred fields are
implemented or documented as runtime boundaries.

| Behavior | Current code path | Test coverage | Disposition |
| --- | --- | --- | --- |
| Local AppProject discovery | `discovery.Scan`, `appendDiscoveredProjects` | `TestScanDiscoversAppProjects`, `TestScanPreservesDocumentIdentityForTypedObjects` | No follow-up |
| Rendered AppProject discovery | `applyExplicitKustomizeDiscovery`, `discoverRenderedFleet`, bootstrap discovery | Existing explicit-rendered and fleet discovery tests | Phase 2 fixture expansion |
| Duplicate or conflicting projects | `mergeProjects`, `resolveDiscoveryConflict`, same-scan `projectIndex` last-wins | Existing rendered precedence coverage | Discovery-focused follow-up |
| Implicit default project | `effectiveProject`, `implicitDefaultProject` | `TestValidateApplicationsAllowsImplicitDefaultProject`, `TestValidateApplicationsCurrentBehaviorAllowsImplicitDefaultProjectWhenOtherLocalProjectsExist` | No follow-up |
| Missing non-default project | `ValidateApplications`, `projectIndex`, `applicationProject` | `TestValidateApplicationsCurrentBehaviorReportsMissingNonDefaultProject` | No follow-up |
| Source repository matching | `validateSources`, Argo `IsSourcePermitted` | Existing source policy tests, source parity fixtures, `TestValidateApplicationsCurrentBehaviorReportsDeniedMultiSourceRepository` | Phase 3 remediation for any future parity gaps |
| Destination matching | `validateDestination`, `destinationCluster`, Argo `IsDestinationPermitted` | Existing destination and project-scoped cluster tests, destination parity fixtures | Phase 3 remediation for deferred metadata cases |
| Cluster Secret metadata | `clusterSecretSettings`, `projectScopedClusters` | Cluster parser tests, project-scoped destination tests, metadata integration fixtures | Phase 3 remediation for deferred metadata cases |
| Repository Secret project metadata | `repositorySecretSettings`, `effectiveProject`, `repositoryMetadataDiagnostics` | Repository metadata tests, project-scoped repository tests, metadata integration fixtures | No follow-up |
| Source namespace checks | `validateSourceNamespace` | Existing source namespace tests, source namespace parity fixtures, `TestValidateApplicationsCurrentBehaviorPhase3SourceNamespacesEmptyListDeniesNonControllerNamespace` | No follow-up |
| RBAC role metadata | `rbacMetadataDiagnostics` | Existing source namespace and RBAC metadata test | Metadata-only warning; authorization is not simulated |
| Resource policy fields | `ValidateRenderedResourcePolicy`, `renderOneApplication`, shared manifest scope helpers | Resource policy evaluator tests and `orchestrator_project_resource_policy_test.go` | Supported for render-backed workflows; discovery-only paths remain unevaluated |
| Runtime-bound fields | No sync window, orphan, signature, or impersonation simulation | `TestValidateApplicationsRuntimeBoundFieldsDoNotSimulateLivePolicy` | Intentionally documented runtime boundary |

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
