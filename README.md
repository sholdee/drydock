# argocd-local

`argocd-local` renders and diffs Argo CD GitOps repositories locally.

The first supported workflow is desired-vs-desired pull request diffing:
compare a current repository tree with a baseline tree and inspect the rendered
Kubernetes manifests that changed.

This project is early implementation work. See
`docs/superpowers/specs/2026-05-22-argocd-local-design.md` for the approved MVP
design.
