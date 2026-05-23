# Argo CD Compatibility Notes

`argocd-local` targets offline desired-vs-desired PR diffs.

Supported in the MVP:

- Direct `Application` CRs
- Git-directory `ApplicationSet` CRs
- Single-source and multi-source planning for supported source types
- Kustomize and directory rendering, including repo-root-local Kustomize
  references
- Kustomize `helmCharts` rendered through the shared Go-library Helm path
- Local Helm chart rendering
- Chart-only remote Helm sources for public HTTP/S and OCI repositories
- Public Helm chart fetching by default for render and diff chart dependencies
- User chart cache entries for acquired charts
- `diff apps` desired-vs-desired manifest diffs
- `diff images` conservative workload image diffs
- Repeated-resource last-wins behavior inside one Application

Chart cache behavior:

- `--offline` disables Helm chart network fetching and requires cache hits or
  local chart availability.
- `--refresh-charts` refreshes cached immutable chart entries.
- `--chart-cache-dir PATH` overrides the default user cache directory.
- The chart cache is outside the Git repository tree by default.
- `--allow-network` is not the Helm chart-fetch flag; it is reserved for future
  Git/repository-source fetching.

Not reproduced offline:

- Kubernetes API defaulting
- Admission mutation
- Server-side apply field ownership
- Managed fields ignores
- Live Argo CD server-side diff
- Project/RBAC/destination validation
- Authenticated/private Helm chart repositories
- Remote Kustomize refs
- Git/repository-source fetching

The tool pins Argo CD dependencies. Upgrade Argo CD dependencies deliberately
and update compatibility tests in the same change.
