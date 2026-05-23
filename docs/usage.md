# Usage

`argocd-local` currently wires Application discovery, rendered image listing,
all-Application and named-Application build, all-Application and
named-Application manifest diffs, image diffs, and repository diagnostics.

## Application Discovery

List discovered direct `Application` CRs and supported generated
`ApplicationSet` Applications:

```bash
argocd-local get apps --path .
```

`get apps` defaults to table output and supports `-o table`, `-o name`,
`-o json`, and `-o yaml`. Use `-l`/`--selector` with Kubernetes label selector
syntax to match `Application.metadata.labels`:

```bash
argocd-local get apps --path . -l 'env in (prod,stage),tier!=test'
```

List conservative workload container images from rendered Applications:

```bash
argocd-local get images --path . -o name
```

`get images` supports the same structured output formats as `get apps`.
Diagnostics are printed to stderr for both commands.

Supported local `ApplicationSet` generators are top-level Git directories, Git
files, and list generators. Multiple supported top-level generators are
expanded independently and concatenated in manifest order. Unsupported
generators emit diagnostics; non-strict commands keep supported generated
Applications, while `--strict` promotes those diagnostics to errors.

Git files generator matches are sorted by normalized relative path. Include
and exclude patterns are evaluated deterministically, and `exclude: true`
removes a file even if another pattern includes it. Files must stay under the
repository root and must not traverse symlinks. YAML and JSON files must decode
to non-empty mapping documents.

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
by default when a render needs chart dependencies. Path-based Git sources use
the local `--path` tree when the source path exists there. Use
`--repo-map URL=PATH` to force a source repo URL to a local checkout, or
`--allow-network` to clone/fetch a missing path source from its `repoURL`.

Network and cache flags:

- `--offline` disables Helm chart and remote Kustomize resource network
  fetching. It requires cached or local charts and cached remote Kustomize
  resources.
- `--refresh-charts` refreshes cached immutable chart entries before rendering.
- `--chart-cache-dir PATH` overrides the default user cache directory for
  acquired charts.
- `--repo-map URL=PATH` maps a Git repository URL to a local checkout and wins
  over local source-path fallback and network fetching.
- `--allow-network` enables Git clone/fetch for unmapped path sources whose
  paths are not present in `--path`.
- `--git-cache-dir PATH` overrides the default user cache directory for cached
  Git repositories.
- `--refresh-git` fetches cached Git repositories before rendering.
- `--refresh-remotes` refreshes cached remote Kustomize resources before
  rendering.
- `--remote-cache-dir PATH` overrides the default user cache directory for
  cached remote Kustomize resources.

`--allow-network` is not the Helm chart-fetch flag. It only gates Git
repository-source fetching. `--offline` cannot be combined with
`--allow-network`.

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

Manifest diffs default to unified diff output. `diff apps` and `diff app`
also support `-o json` and `-o yaml`, which serialize the structured
`[]diff.Result` payload. Diagnostics remain on stderr so stdout stays valid
JSON or YAML. `-o name` is not supported for manifest diffs.

Use repeatable `--strip-attr KEY` to remove matching keys from
`metadata.labels` and `metadata.annotations` before comparing rendered
manifests and generating diffs:

```bash
argocd-local diff apps \
  --path-orig ../base \
  --path ./current \
  --strip-attr helm.sh/chart \
  --strip-attr app.kubernetes.io/version
```

If stripped attributes are the only rendered difference for a resource, no diff
result is emitted for that resource.

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

## Diagnostics

Run repository diagnostics without printing rendered manifests:

```bash
argocd-local diag --path .
```

`diag` uses the same discovery, ApplicationSet expansion, source resolution,
and render validation path as `build apps`. It prints diagnostics to stderr and
returns an error when runtime failures or error-severity diagnostics are found.
Use `--strict` to promote warnings to errors.

## Deferred Commands And Sources

These source paths are not wired in the current MVP:

- Cluster, SCM provider, pull-request, plugin, matrix, and merge
  ApplicationSet generators.
- Remote Kustomize Git refs, bases, components, patches, generators,
  transformers, validators, `crds`, `openapi`, and replacements.
- Authenticated/private Git repositories.
- Authenticated remote resources.
- Authenticated or private Helm chart repositories.

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
