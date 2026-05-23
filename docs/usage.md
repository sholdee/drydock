# Usage

`argocd-local` currently wires the all-Application discovery, build, manifest
diff, and image diff paths. Commands that target one named Application and the
repository diagnostic command are still placeholders.

## Application Discovery

List discovered direct `Application` CRs and supported generated
`ApplicationSet` Applications:

```bash
argocd-local get apps --path .
```

## Rendering

Build every discovered Application:

```bash
argocd-local build apps --path .
```

Rendering supports directory sources, Kustomize sources, local Helm charts,
Kustomize `helmCharts`, and Argo CD chart-only remote Helm sources. Public Helm
chart fetching is enabled by default when a render needs chart dependencies.

Chart cache flags:

- `--offline` disables Helm chart network fetching and requires a cached or
  local chart to already be available.
- `--refresh-charts` refreshes cached immutable chart entries before rendering.
- `--chart-cache-dir PATH` overrides the default user cache directory for
  acquired charts.

`--allow-network` is not the Helm chart-fetch flag. It is parsed for future
Git/repository-source fetching, which is not wired yet.

## Manifest Diffs

Diff all affected Applications between two repository trees:

```bash
argocd-local diff apps --path ./current --path-orig ../base
```

`diff apps` renders the baseline and current trees, then prints
desired-vs-desired manifest diffs. It uses changed-only selection by default:
if changed files can be mapped to Application inputs, only affected
Applications render; if any changed file is unowned, non-strict mode warns and
renders all Applications. Use `--changed-only=false` to render all
Applications explicitly, or `--strict-changed-only` to fail on incomplete input
ownership.

For local inspection, keep the command successful even when a diff exists:

```bash
argocd-local diff apps \
  --path-orig ../base \
  --path ./current \
  --exit-code=false
```

## Image Diffs

Diff conservative workload container images from rendered manifests:

```bash
argocd-local diff images --path ./current --path-orig ../base
```

This projection is intentionally conservative and does not report arbitrary
`image` keys from ConfigMaps or CRDs.

## Deferred Commands And Sources

These commands and source paths are not wired in the current MVP:

- `argocd-local diff app NAME --path ./current --path-orig ../base`
- `argocd-local diag --path .`
- Remote Kustomize refs.
- Git/repository-source fetching, including `--allow-network` behavior.
- Authenticated or private Helm chart repositories.

`--repo-map` is parsed as future command surface, but the current E2E build and
diff paths do not use it yet.

## Optional Home-Ops Smoke

Run the optional home-ops Renovate smoke:

```bash
RENOVATE_CHART_TO=4.8.2 scripts/home-ops-renovate-smoke.sh
```

The smoke script is optional, targets maintainers with a local `home-ops`
checkout, uses temporary worktrees, and does not mutate the real checkout. It
detects the current Renovate chart version from `apps/renovate/kustomization.yaml`
and requires `RENOVATE_CHART_TO` to name the simulated target version.
Committed tests and portable fixtures do not depend on `home-ops`.

Run the optional home-ops pattern smoke:

```bash
scripts/home-ops-pattern-smoke.sh
```

The pattern smoke applies small synthetic changes across representative
`home-ops` app patterns in temporary worktrees. It is optional, may fetch
public charts, and accepts `RENOVATE_CHART_TO` or `EXTERNAL_SECRETS_CHART_TO`
when maintainers want to choose explicit chart target versions.
