# Argo CD Compatibility Notes

`argocd-local` targets offline desired-vs-desired PR diffs.

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
- Discovered `resource.compareoptions.ignoreResourceStatusField` and
  `resource.compareoptions.ignoreAggregatedRoles`
- Argo CD core resource exclusions plus discovered global
  `resource.exclusions` and `resource.inclusions`
- Explicit rendered-resource filters through `--skip-kind`, `--skip-crds`, and
  `--skip-secrets`
- `diff images` conservative workload image diffs
- Repeated-resource last-wins behavior inside one Application
- Public Go API in `pkg/argocdlocal` for Application listing, rendering,
  manifest diffs, image diffs, and injectable Git/chart/remote-resource
  acquisition

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
- Chart and remote-resource caches must stay outside Git repository trees.
- `--allow-network` is not the Helm chart-fetch flag.
- Cache hit/miss inspection is not a first-class command or structured output
  surface yet; that is tracked as Phase 1B cache observability.

Not reproduced offline:

- Kubernetes API defaulting
- Admission mutation
- Live server-side apply field ownership prediction
- Live Argo CD server-side diff
- Managed-fields ignores when ownership data exists only on the live cluster
- Global `resource.customizations` `ignoreResourceUpdates`,
  `knownTypeFields`, health, actions, and Lua settings
- Project/RBAC/destination validation
- Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
  ApplicationSet generators
- Live cluster and Argo CD API sources

The tool pins Argo CD dependencies. Upgrade Argo CD dependencies deliberately
and update compatibility tests in the same change.
