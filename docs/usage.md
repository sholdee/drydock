# Usage

`argocd-local` currently wires Application discovery, rendered image listing,
all-Application and named-Application build, all-Application and
named-Application render tests, named-Application manifest diffs, image diffs,
and repository diagnostics.

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

Supported local `ApplicationSet` generators are Git directories, Git files,
list, matrix, and merge. Multiple supported top-level generators are expanded
independently and concatenated in manifest order. Unsupported generators emit
diagnostics; non-strict commands keep supported generated Applications, while
`--strict` promotes those diagnostics to errors.

Git files generator matches are sorted by normalized relative path. Include
and exclude patterns are evaluated deterministically, and `exclude: true`
removes a file even if another pattern includes it. Files must stay under the
repository root and must not traverse symlinks. YAML and JSON files must decode
to non-empty mapping documents.

List generators support `elements` and `elementsYaml`. Supported generators
honor generator-level selectors and generator-level template overrides.
Selectors match flattened parameter keys, including nested Go-template maps.
Matrix generators combine exactly two child generators and interpolate the
second child from first-child params, including templated `elementsYaml`.
Merge generators overlay two or more child generators by `mergeKeys` in base
generator order. Matrix and merge children may use list, Git directories, Git
files, and nested matrix/merge combinations where the Argo CD v3 nested JSON
API permits them.

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

When one selected Application fails to render, embedding callers receive a
partial `BuildResult` containing successful manifests, diagnostics, and
per-Application statuses. CLI `build` commands keep stdout parseable; on render
failure they print diagnostics to stderr and do not mix invalid partial manifest
streams into stdout.

Rendering supports directory sources, Kustomize sources, local Helm charts,
Kustomize `helmCharts`, remote Kustomize HTTP(S) files and Git refs, and
Argo CD chart-only remote Helm sources. Public Helm chart fetching is enabled
by default when a render needs chart dependencies. Path-based Git sources use
the local `--path` tree when the source path exists there. Use
`--repo-map URL=PATH` to force a source repo URL to a local checkout, or
`--allow-network` to clone/fetch a missing path source from its `repoURL`.

Supported Kustomize remote refs include:

- `https://github.com/org/repo//path?ref=v1`
- `git::https://github.com/org/repo.git//path?ref=v1`
- `ssh://git@github.com/org/repo.git//path?ref=v1`
- `git@github.com:org/repo.git//path?ref=v1`

Remote Kustomize refs are supported in `resources`, `bases`, `components`,
`patches.path`, `patchesJson6902.path`, non-inline `patchesStrategicMerge`,
`generators`, `transformers`, `validators`, `configurations`, `crds`,
`openapi.path`, `replacements.path`, and ConfigMap/Secret generator
`files`, `envs`, and `env` entries. HTTP(S) refs are treated as single
YAML/JSON files. Directory-shaped fields, including remote bases and
components, must use Git refs that resolve to Kustomization directories. The
renderer copies acquired content into a temporary workspace under generated
`.argocd-local` paths and does not write generated manifests into the source
tree.

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
- `--git-bearer-token TOKEN` authenticates Git HTTPS clone/fetch requests with
  bearer auth and takes precedence over basic auth.
- `--git-username USER` and `--git-password PASS` authenticate Git HTTPS
  clone/fetch requests with basic auth.
- `--git-ssh-key-file PATH` authenticates Git SSH clone/fetch requests.
  `--git-known-hosts-file PATH` is required for SSH in this slice; encrypted
  keys can use `--git-ssh-passphrase PASSPHRASE`.
- Kustomize Git remote refs reuse the explicit `--git-*` credentials, but use
  the remote Kustomize cache and `--offline`/`--refresh-remotes` behavior.
- `--helm-bearer-token TOKEN` authenticates HTTP Helm repository index and
  archive requests with bearer auth and takes precedence over basic auth.
- `--helm-username USER` and `--helm-password PASS` authenticate HTTP Helm
  repository index and archive requests with basic auth.
- `--registry-config PATH` supplies the only Helm OCI registry credentials used
  by this slice. Ambient Helm and Docker registry config is not read.
- `--refresh-remotes` refreshes cached remote Kustomize resources before
  rendering.
- `--remote-cache-dir PATH` overrides the default user cache directory for
  cached remote Kustomize resources.
- `--remote-bearer-token TOKEN` authenticates HTTP(S) remote Kustomize
  resource requests with bearer auth and takes precedence over basic auth.
- `--remote-username USER` and `--remote-password PASS` authenticate HTTP(S)
  remote Kustomize resource requests with basic auth.
- `--skip-kind KIND` omits rendered resources with that Kubernetes kind from
  build output, manifest diffs, image extraction, and render tests. The flag is
  repeatable and matches kind only.
- `--skip-crds` omits rendered `CustomResourceDefinition` resources.
- `--skip-secrets` omits rendered `Secret` resources.

`--allow-network` is not the Helm chart-fetch flag. It only gates Git
repository-source fetching. `--offline` cannot be combined with
`--allow-network`.

Caches must stay outside Git repository trees. A first-class cache inspection
command or structured cache event stream is not implemented yet; that remains
the Phase 1B cache-observability follow-up.

## Go API

Use `github.com/home-operations/argocd-local/pkg/argocdlocal` when embedding
the renderer directly:

```go
client := argocdlocal.NewClient(argocdlocal.Config{
	Path: ".",
	RepoMaps: []argocdlocal.RepoMap{
		{URL: "https://github.com/example/repo", Path: "/work/repo"},
	},
})
result, err := client.Render(ctx)
```

Package-level `Render`, `ListApplications`, `DiffApplications`, and
`DiffImages` functions use the same default network and cache behavior as the
CLI. `NewClient` accepts public Git, chart, and remote-resource acquirer
interfaces so tests and embedding callers can provide fakes without importing
internal packages or performing public network fetches.

Public render results include Applications, manifests, diagnostics, and
per-Application statuses. If one selected Application fails, `Render` returns
the error and still returns the partial successful manifests, stable
diagnostics, and statuses. Set `SkipKinds`, `SkipCRDs`, or `SkipSecrets` on
`argocdlocal.Config` to apply the same rendered-resource filters exposed by
the CLI.

Config management plugin sources are explicit. The CLI and default Go client
do not execute plugin commands; an Application source with `spec.source.plugin`
fails closed with a plugin diagnostic unless an embedding caller supplies
`argocdlocal.Config.PluginRenderer`. Injected plugin renderer output
participates in normal render, diff, image extraction, destination namespace
defaulting, and resource filtering.

## Render Tests

Test every discovered Application without printing manifest bodies:

```bash
argocd-local test apps --path .
```

Test exactly one discovered Application by `metadata.name`:

```bash
argocd-local test app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
argocd-local test app argocd/renovate --path .
```

Default text output prints one status line per selected Application:

```text
PASS argocd/renovate
FAIL argocd/broken Application argocd/broken source[0] path="..." ...
SKIPPED argocd/skipped unsupported ApplicationSet generator ...
```

Status values are `PASS` for Applications that rendered successfully, `FAIL`
for render or planning failures, and `SKIPPED` when an earlier discovery or
expansion precondition prevented safe rendering. `test apps` and `test app`
return exit code `0` only when every selected Application passes; any `FAIL`,
`SKIPPED`, or runtime failure returns exit code `2`.

Structured status output is available with `-o json` and `-o yaml`:

```bash
argocd-local test apps --path . -o json
argocd-local test apps --path . -o yaml
```

Structured test output contains only status records and diagnostics remain on
stderr.

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

Application-level `spec.ignoreDifferences[]` rules and global
`resource.customizations.ignoreDifferences.*` settings from discovered
`argocd-cm` or Helm values `configs.cm` are honored for rendered resource
diffs. Supported ignore fields are `jsonPointers`, `jqPathExpressions`, and
`managedFieldsManagers`. When a matching resource exists on both sides,
`argocd-local` applies the union of matching Application-local and global
settings from the baseline and current trees so a newly added ignore rule can
suppress the intended PR noise immediately.

`jqPathExpressions` are evaluated as Argo CD-style `del(<expression>)` delete
filters. Invalid expressions fail the diff so unsafe normalization does not hide
changes. `managedFieldsManagers` is an offline approximation: it suppresses
fields only when rendered desired manifests include matching
`metadata.managedFields` ownership data.

Discovered `resource.compareoptions` settings are also honored for
`ignoreResourceStatusField` and `ignoreAggregatedRoles`. By default status is
ignored for all resources. Use `ignoreResourceStatusField: none`, `off`, or
`false` when rendered status fields should remain visible in PR diffs.

Rendered-resource filters run before diff comparison. Argo CD core exclusions
and discovered `resource.exclusions`/`resource.inclusions` are applied
automatically. For example, omit CRDs and Secrets from a pull request diff with:

```bash
argocd-local diff apps \
  --path-orig ../base \
  --path ./current \
  --skip-crds \
  --skip-secrets
```

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

- Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
  ApplicationSet generators.
- CLI config management plugin execution, shellout plugin adapters, Argo CD
  repo-server sidecar plugin discovery, ambient plugin configuration, ambient
  plugin environment loading, and plugin credential injection.
- Live cluster and Argo CD API sources.

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
