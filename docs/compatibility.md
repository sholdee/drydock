# Argo CD Compatibility Notes

`argocd-local` targets offline desired-vs-desired PR diffs.

Supported in the MVP:

- Direct `Application` CRs
- Git-directory `ApplicationSet` CRs
- Single-source and multi-source planning for supported source types
- Kustomize and directory rendering, including repo-root-local Kustomize
  references
- Kustomize `helmCharts` rendered through the shared Go-library Helm path
- Safe single-file HTTP(S) Kustomize `resources:` rendered through the remote
  resource cache
- Local Helm chart rendering
- Chart-only remote Helm sources for public HTTP/S and OCI repositories
- Public Helm chart fetching by default for render and diff chart dependencies
- User chart cache entries for acquired charts
- User remote-resource cache entries for acquired Kustomize resources
- `--repo-map URL=PATH` path-source resolution for local external checkouts
- `--allow-network` Git clone/fetch for unmapped path sources missing from the
  local tree
- User Git repository cache entries for fetched path sources
- `diag --path` repository diagnostics through the render validation path
- `get apps` structured table, name, JSON, and YAML output with Kubernetes
  label selector filtering on Application metadata labels
- `get images` structured table, name, JSON, and YAML output using the same
  conservative workload image extraction as `diff images`
- `build app` rendering for one Application by `NAME` or `NAMESPACE/NAME`
- `diff apps` desired-vs-desired manifest diffs
- `diff app` desired-vs-desired diffs for one Application, including
  add/delete diffs when the Application exists on only one side
- `diff apps` and `diff app` structured JSON and YAML output
- `--strip-attr KEY` diff normalization for metadata label and annotation keys
- `diff images` conservative workload image diffs
- Repeated-resource last-wins behavior inside one Application

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
- `--offline` cannot be combined with `--allow-network`.
- `--refresh-remotes` refreshes cached remote Kustomize resources.
- `--remote-cache-dir PATH` overrides the default remote resource cache
  directory.
- Chart and remote-resource caches must stay outside Git repository trees.
- `--allow-network` is not the Helm chart-fetch flag.

Not reproduced offline:

- Kubernetes API defaulting
- Admission mutation
- Server-side apply field ownership
- Managed fields ignores
- Live Argo CD server-side diff
- Project/RBAC/destination validation
- Authenticated/private Helm chart repositories
- Authenticated/private Git repositories
- Authenticated remote resources
- Remote Kustomize Git refs, bases, components, patches, generators,
  transformers, validators, `crds`, `openapi`, and replacements

The tool pins Argo CD dependencies. Upgrade Argo CD dependencies deliberately
and update compatibility tests in the same change.
