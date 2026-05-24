# Argo CD Compatibility Notes

`drydock` targets offline desired-vs-desired PR diffs.

Supported in the MVP:

- Direct `Application` CRs
- Git-directory, Git-files, list, matrix, and merge `ApplicationSet` CR
  expansion
- ApplicationSet list `elementsYaml`, including matrix-interpolated
  `elementsYaml`
- ApplicationSet generator selectors and generator template overrides for
  supported generators
- Matrix interpolation for supported local children
- Deterministic merge-key overlays for supported local children
- Nested ApplicationSet matrix/merge combinations where the Argo CD v3 nested
  JSON API permits them
- Single-source and multi-source planning for supported source types
- Kustomize and directory rendering, including repo-root-local Kustomize
  references
- Kustomize `helmCharts` rendered through the shared Go-library Helm path
- Remote Kustomize HTTP(S) file refs and Git refs rendered through the remote
  resource cache
- HTTP(S) Kustomize refs as single YAML/JSON files, with directory-shaped refs
  such as bases and components requiring Git refs to Kustomization directories
- Remote Kustomize `resources`, `bases`, `components`, `patches.path`,
  `patchesJson6902.path`, non-inline `patchesStrategicMerge`, `generators`,
  `transformers`, `validators`, `configurations`, `crds`, `openapi.path`,
  `replacements.path`, and generator file/env refs
- Local Helm chart rendering
- Chart-only remote Helm sources for public HTTP/S and OCI repositories
- Public Helm chart fetching by default for render and diff chart dependencies
- User chart cache entries for acquired charts
- User remote-resource cache entries for acquired Kustomize resources
- Optional cache event API/reporting for Git, Helm, and remote Kustomize
  acquisition, with redacted targets and errors
- `--repo-map URL=PATH` path-source resolution for local external checkouts
- `--allow-network` Git clone/fetch for unmapped path sources missing from the
  local tree
- User Git repository cache entries for fetched path sources
- Explicit Git HTTPS bearer/basic auth and SSH key-file auth
- Explicit HTTP(S) Helm bearer/basic auth
- Explicit HTTP(S) remote Kustomize bearer/basic auth, with Kustomize Git refs
  reusing explicit Git credentials
- Explicit Helm OCI registry config path plumbing
- `diag --path` repository diagnostics through the render validation path
- Stable diagnostic codes in structured CLI and public API diagnostic output
- `diag -o json` and `diag -o yaml` structured diagnostic reports
- `diag --cache-events` optional cache acquisition event reporting in
  structured diagnostic reports
- `cache path`, `cache list`, `cache prune`, and `cache delete` local source
  cache lifecycle commands for recognized Git, chart, and remote Kustomize
  cache layouts
- `get apps` structured table, name, JSON, and YAML output with Kubernetes
  label selector filtering on Application metadata labels
- `get images` structured table, name, JSON, and YAML output using the same
  conservative workload image extraction as `diff images`
- `build app` rendering for one Application by `NAME` or `NAMESPACE/NAME`
- Partial build results for embedding callers when some selected Applications
  fail to render
- `test apps` and `test app` per-Application PASS/FAIL/SKIPPED render status
  output, including JSON and YAML formats
- `diff apps` desired-vs-desired manifest diffs
- `diff app` desired-vs-desired diffs for one Application, including
  add/delete diffs when the Application exists on only one side
- `diff apps` and `diff app` structured JSON and YAML output
- `--strip-attr KEY` diff normalization for metadata label and annotation keys
- Application-level `spec.ignoreDifferences[]` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers` for rendered manifest diffs
- Global `resource.customizations.ignoreDifferences.*` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers` from discovered `argocd-cm`
  and Argo CD Helm values `configs.cm`
- Global `resource.customizations.knownTypeFields.*` normalization for
  desired-vs-desired manifest diffs
- Global `resource.customizations.ignoreResourceUpdates.*` parsing and
  diagnostics, without applying them to desired-vs-desired diffs
- Health and action customization parsing and diagnostics, including
  `useOpenLibs`/Lua metadata, without executing Lua offline
- Discovered `resource.compareoptions.ignoreResourceStatusField` and
  `resource.compareoptions.ignoreAggregatedRoles`
- Argo CD core resource exclusions plus discovered global
  `resource.exclusions` and `resource.inclusions`
- Explicit rendered-resource filters through `--skip-kind`, `--skip-crds`, and
  `--skip-secrets`
- `diff images` conservative workload image diffs
- Repeated-resource last-wins behavior inside one Application
- Public Go API in `pkg/drydock` for Application listing, rendering,
  manifest diffs, image diffs, injectable Git/chart/remote-resource
  acquisition, and injectable config management plugin rendering
- Config management plugin source detection with fail-closed diagnostics in
  the CLI and default Go client when no plugin renderer is injected
- Local `AppProject` manifest discovery
- Offline diagnostics for Application source repository, destination
  server/name/namespace, and source namespace validation from local
  `AppProject` manifests
- AppProject RBAC role and policy parsing as reported metadata, without
  simulating authorization
- `permitOnlyProjectScopedClusters` reporting as deferred metadata, without
  offline project-scoped cluster Secret enforcement
- Repository credential matching diagnostics based on discovered repository
  Secret metadata only, without reading secret credential fields
- Benchmarks for repeated local Application rendering and ApplicationSet
  expansion
- CI documentation for local, library-backed verification without cluster,
  server, or renderer CLI dependencies
- Release and upgrade documentation for static binary/module releases, Argo CD
  dependency upgrades, cache compatibility, and deferred install Action work

Network and cache behavior:

- `--offline` disables Helm chart and remote Kustomize resource network
  fetching. It requires cache hits or local chart availability.
- `--refresh-charts` refreshes cached immutable chart entries.
- `--chart-cache-dir PATH` overrides the default user cache directory.
- `--repo-map URL=PATH` maps a Git repo URL to a local checkout and takes
  precedence over local fallback and network fetching.
- `--allow-network` enables Git clone/fetch for unmapped path sources. It does
  not control Helm chart fetching.
- `--git-cache-dir PATH` overrides the default Git repository cache directory.
- `--refresh-git` fetches cached Git repositories before rendering.
- `--git-bearer-token` takes precedence over `--git-username` and
  `--git-password` for Git HTTPS auth.
- Git SSH auth requires `--git-ssh-key-file` and `--git-known-hosts-file`.
  `ssh://host/...` defaults the SSH user to `git`.
- Kustomize Git remote refs reuse the explicit `--git-*` credentials, but use
  `--remote-cache-dir`, `--refresh-remotes`, and `--offline` for cache and
  network behavior.
- `--helm-bearer-token` takes precedence over `--helm-username` and
  `--helm-password` for HTTP(S) Helm repository auth.
- `--registry-config PATH` is the only OCI registry credential source used by
  this slice; ambient Helm and Docker registry config is not read.
- `--offline` cannot be combined with `--allow-network`.
- `--refresh-remotes` refreshes cached remote Kustomize resources.
- `--remote-cache-dir PATH` overrides the default remote resource cache
  directory.
- `--remote-bearer-token` takes precedence over `--remote-username` and
  `--remote-password` for HTTP(S) remote Kustomize resource auth.
- Git, chart, and remote-resource caches must stay outside Git repository
  trees.
- `--allow-network` is not the Helm chart-fetch flag.
- Cache lifecycle commands are local filesystem operations only. They do not
  render Applications, clone/fetch Git repositories, fetch Helm charts, fetch
  remote Kustomize resources, or read credential flags.
- New cache entries include hidden `.drydock-cache/metadata.json` sidecars
  with redacted target metadata. Older hash-only entries are listed as legacy
  entries when their filesystem layout is recognized.
- Non-dry-run `cache prune` and `cache delete` operations require `--yes`;
  dry-runs do not require confirmation.

Not reproduced offline:

- Kubernetes API defaulting
- Admission mutation
- Live server-side apply field ownership prediction
- Live Argo CD server-side diff
- Managed-fields ignores when ownership data exists only on the live cluster
- Applying `ignoreResourceUpdates` to desired-vs-desired diffs
- Any live integration that has not passed the live integration design gate
- Health or action Lua execution
- Live destination cluster existence checks
- Sync window enforcement
- Source integrity signature verification
- Project-scoped cluster Secret enforcement
- Full Argo CD RBAC/Casbin authorization simulation
- CLI config management plugin execution, shellout plugin adapters, Argo CD
  repo-server sidecar plugin discovery, ambient plugin configuration, ambient
  plugin environment loading, and plugin credential injection
- Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
  ApplicationSet generators
- Live cluster and Argo CD API sources
- Parallel rendering and `--parallelism` controls
- Composite install GitHub Action publishing

Live integration is design-gated in
`docs/reports/2026-05-24-live-integration-design-gate.md`. Future live work
must stay explicitly opt-in and must not change the default offline render/diff
contract.

The tool pins Argo CD dependencies. Upgrade Argo CD dependencies deliberately
and update compatibility tests in the same change.
