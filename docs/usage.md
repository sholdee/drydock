# Usage

`argocd-local` currently wires Application discovery, all-Application and
named-Application build, all-Application and named-Application manifest diffs,
and image diffs. The repository diagnostic command is still a placeholder.

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

Build exactly one discovered Application by `metadata.name`:

```bash
argocd-local build app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
argocd-local build app argocd/renovate --path .
```

`build app` errors when no discovered Application matches. The unqualified
`NAME` form must identify exactly one Application.

Rendering supports directory sources, Kustomize sources, local Helm charts,
Kustomize `helmCharts`, safe single-file HTTP(S) Kustomize `resources:`, and
Argo CD chart-only remote Helm sources. Public Helm chart fetching is enabled
by default when a render needs chart dependencies.

Network and cache flags:

- `--offline` disables Helm chart and remote Kustomize resource network
  fetching. It requires cached or local charts and cached remote Kustomize
  resources.
- `--refresh-charts` refreshes cached immutable chart entries before rendering.
- `--chart-cache-dir PATH` overrides the default user cache directory for
  acquired charts.
- `--refresh-remotes` refreshes cached remote Kustomize resources before
  rendering.
- `--remote-cache-dir PATH` overrides the default user cache directory for
  cached remote Kustomize resources.

`--allow-network` is not the Helm chart-fetch flag. It is parsed for future
Git/repository-source fetching, which is not wired yet.

Caches must stay outside Git repository trees.

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

Diff one requested Application by `metadata.name`:

```bash
argocd-local diff app renovate --path-orig ../base --path .
```

Use `NAMESPACE/NAME` to disambiguate:

```bash
argocd-local diff app argocd/renovate --path-orig ../base --path .
```

`diff app` selects the requested Application directly in each tree and does not
use changed-only Git path filtering. If the Application exists only in current,
the diff shows additions; if it exists only in baseline, the diff shows
deletions. If it is absent from both trees, the command errors.

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

- `argocd-local diag --path .`
- Remote Kustomize Git refs, bases, components, patches, generators,
  transformers, validators, `crds`, `openapi`, and replacements.
- Git/repository-source fetching, including `--allow-network` behavior.
- Authenticated remote resources.
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
