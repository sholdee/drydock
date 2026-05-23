# Argo CD Compatibility Notes

`argocd-local` targets offline desired-vs-desired PR diffs.

Supported in the MVP:

- Direct `Application` CRs
- Git-directory `ApplicationSet` CRs
- Single-source and multi-source planning for supported source types
- Kustomize and directory rendering
- Local Helm chart rendering
- Repeated-resource last-wins behavior inside one Application

Not reproduced offline:

- Kubernetes API defaulting
- Admission mutation
- Server-side apply field ownership
- Managed fields ignores
- Live Argo CD server-side diff
- Project/RBAC/destination validation

The tool pins Argo CD dependencies. Upgrade Argo CD dependencies deliberately
and update compatibility tests in the same change.
