# argocd-local

`argocd-local` is an early Go CLI for local Argo CD GitOps repository analysis.

The MVP goal is desired-vs-desired pull request diffing: compare a current
repository tree with a baseline tree and inspect the rendered Kubernetes
manifests that changed. The currently wired commands are `get apps` for
Application discovery, `build apps` for local rendering, `diff apps` for
desired-vs-desired manifest diffs, and `diff images` for conservative workload
image diffs.

This project is early implementation work. See
`docs/superpowers/specs/2026-05-22-argocd-local-design.md` for the approved MVP
design.

## Quick Start

```bash
go run ./cmd/argocd-local get apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local build apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local diff apps \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  --exit-code=false
```

Render and diff commands fetch public Helm charts by default for Kustomize
`helmCharts` and Argo CD chart-only sources. Use `--offline` to require cached
or local chart availability, `--refresh-charts` to refresh cached immutable
chart entries, and `--chart-cache-dir` to choose the chart cache directory.
`--allow-network` is not the Helm chart-fetch flag; it is reserved for future
Git/repository-source fetching.

Maintainers with a local `home-ops` checkout can also run the optional
Renovate smoke script:

```bash
scripts/home-ops-renovate-smoke.sh
```

## Current MVP Limits

- Desired-vs-desired only; no live cluster diff.
- Git-directory ApplicationSet generator only.
- No config management plugins.
- No required shellouts in default workflows.
- Server-side diff/apply settings are reported as offline limitations.

See `docs/usage.md` for command examples and `docs/compatibility.md` for
offline Argo CD compatibility notes.
