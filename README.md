# argocd-local

`argocd-local` is an early Go CLI for local Argo CD GitOps repository analysis.

The MVP goal is desired-vs-desired pull request diffing: compare a current
repository tree with a baseline tree and inspect the rendered Kubernetes
manifests that changed. The currently wired commands are `get apps` for
Application discovery, `get images` for rendered workload image listing,
`build apps` and `build app NAME` for local rendering, `diff apps` and
`diff app NAME` for desired-vs-desired manifest diffs, and `diff images` for
conservative workload image diffs. `diag --path` reports repository diagnostics
without printing manifests.

This project is early implementation work. See
`docs/superpowers/specs/2026-05-22-argocd-local-design.md` for the approved MVP
design.

## Quick Start

```bash
go run ./cmd/argocd-local get apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local get apps --path ./testdata/applications/e2e -o json
go run ./cmd/argocd-local get images --path ./testdata/renovate-diff/current -o name
go run ./cmd/argocd-local build apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local build app renovate \
  --path ./testdata/renovate-diff/current
go run ./cmd/argocd-local diff apps \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  --strip-attr helm.sh/chart \
  --exit-code=false
go run ./cmd/argocd-local diff app argocd/renovate \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  -o json \
  --exit-code=false
go run ./cmd/argocd-local diag --path ./testdata/applications/e2e
```

Render and diff commands fetch public Helm charts by default for Kustomize
`helmCharts` and Argo CD chart-only sources. They also support safe single-file
HTTP(S) Kustomize `resources:` entries through the remote resource cache.
Use `--offline` to require cached or local chart availability and cached remote
Kustomize resources. Use `--refresh-charts` and `--chart-cache-dir` for chart
caching, and `--refresh-remotes` and `--remote-cache-dir` for remote Kustomize
resource caching. Git path sources can be resolved with deterministic
`--repo-map URL=PATH` entries, or fetched with `--allow-network` when the source
path is not present locally. Git fetches use `--git-cache-dir` and
`--refresh-git`. Caches must stay outside Git repository trees.
`--allow-network` is not the Helm chart-fetch flag; chart fetching remains
controlled by the chart flags.

Manifest diffs default to unified diff output and also support `-o json` and
`-o yaml` for structured `diff apps` and `diff app` results. Use repeatable
`--strip-attr KEY` to remove matching metadata label and annotation keys before
comparison and diff generation. Diagnostics continue to print on stderr for
structured output.

Maintainers with a local `home-ops` checkout can also run the optional
Renovate smoke script:

```bash
RENOVATE_CHART_TO=4.8.2 scripts/home-ops-renovate-smoke.sh
```

They can also run the optional pattern smoke, which applies representative
synthetic changes in temporary `home-ops` worktrees:

```bash
scripts/home-ops-pattern-smoke.sh
```

## Current MVP Limits

- Desired-vs-desired only; no live cluster diff.
- Git-directory ApplicationSet generator only.
- No config management plugins.
- No required shellouts in default workflows.
- No authenticated Git, authenticated remote resources, or remote Kustomize
  bases/components/patches/generators/transformers/validators, `crds`,
  `openapi`, or replacements.
- Server-side diff/apply settings are reported as offline limitations.

See `docs/usage.md` for command examples and `docs/compatibility.md` for
offline Argo CD compatibility notes. See `docs/home-ops-pattern-coverage.md`
for the portable coverage matrix that models real `home-ops` Application
patterns.
