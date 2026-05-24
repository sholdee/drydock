# Usage

`drydock` currently wires Application discovery, rendered image listing,
all-Application and named-Application build, all-Application and
named-Application render tests, named-Application manifest diffs, image diffs,
repository diagnostics, and local source-cache lifecycle commands.

## Application Discovery

List discovered direct `Application` CRs and supported generated
`ApplicationSet` Applications:

```bash
drydock get apps --path .
```

`get apps` defaults to table output and supports `-o table`, `-o name`,
`-o json`, and `-o yaml`. Use `-l`/`--selector` with Kubernetes label selector
syntax to match `Application.metadata.labels`:

```bash
drydock get apps --path . -l 'env in (prod,stage),tier!=test'
```

List conservative workload container images from rendered Applications:

```bash
drydock get images --path . -o name
```

`get images` supports the same structured output formats as `get apps`.
Diagnostics are printed to stderr for both commands.

Supported local `ApplicationSet` generators are Git directories, Git files,
list, matrix, merge, and explicit fixture-backed provider generators. Multiple
supported top-level generators are expanded independently and concatenated in
manifest order. Unsupported generators emit diagnostics; non-strict commands
keep supported generated Applications, while `--strict` promotes those
diagnostics to errors.

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
files, fixture-backed provider generators, and nested matrix/merge combinations
where the Argo CD v3 nested JSON API permits them.

Provider-backed `ApplicationSet` generators are supported only from explicit
local fixture files. The CLI never contacts Kubernetes, Argo CD, SCM provider,
pull-request, cloud, or plugin-service APIs for these generators. Supply one
or more fixtures with the repeatable flag:

```bash
drydock get apps --path . --appset-provider-fixture fixtures/appset-providers.yaml
drydock diff apps --path . --path-orig ../base --appset-provider-fixture fixtures/appset-providers.yaml
```

Fixture files are strict YAML or JSON documents. Unknown fields, duplicate
identities, URL-like fixture paths, and malformed files produce
`appset.provider-fixture-invalid`. If fixtures are supplied but no entries
match a provider generator, drydock emits `appset.provider-no-match`. Filters
that cannot be evaluated from fixture data fail closed with
`appset.provider-unsupported-filter`.

Fixture schema:

```yaml
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
    project: platform
    labels:
      environment: prod
    annotations:
      owner: platform
    values:
      region: home

clusterDecisions:
  - configMapRef: placement-config
    resourceName: placement-a
    labels:
      placement: edge
    matchKey: clusterName
    statusListKey: clusters
    decisions:
      - clusterName: prod-a
        placement: edge
    values:
      tier: edge

scmRepositories:
  - provider: github
    organization: example-org
    repository: example-repo
    repositoryID: repo-123
    branch: main
    sha: abcdef1234567890
    url: https://github.com/example-org/example-repo
    labels:
      - ops
    paths:
      - deploy/app.yaml
    values:
      tier: ops

pullRequests:
  - provider: github
    organization: example-org
    repository: example-repo
    number: 42
    title: Update chart
    branch: renovate/chart
    targetBranch: main
    headSHA: abcdef1234567890
    author: renovate
    labels:
      - dependencies
    values:
      kind: renovate

plugins:
  - configMapRef: generator-plugin
    outputs:
      - environment: prod
        cluster:
          name: prod-a
    values:
      source: fixture
```

Additional provider-specific fixture fields are available where Argo provider
configuration needs scope data that should not alter emitted template params:
SCM repositories accept `project`, `region`, and `tags`; pull requests accept
`project` and `state`. For example, Azure DevOps uses `organization` plus
`project` for matching, AWS CodeCommit requires explicit `region` and can
evaluate `tagFilters` from `tags`, and GitLab `pullRequestState` is evaluated
from `state`.

Provider fixtures emit the same stable template parameter names that Argo CD
uses for each supported provider family:

| Generator | Stable template parameters |
| --- | --- |
| `clusters` | `name`, `nameNormalized`, `server`, `project`, metadata labels/annotations, `values` |
| `clusterDecisionResource` | `name`, `server`, decision fields, `values` |
| `scmProvider` | `organization`, `repository`, `repository_id`, `url`, `branch`, `branchNormalized`, `sha`, `short_sha`, `short_sha_7`, `labels`, `values` |
| `pullRequest` | `number`, `title`, `branch`, `branch_slug`, `target_branch`, `target_branch_slug`, `head_sha`, `head_short_sha`, `head_short_sha_7`, `author`, `labels`, `values` |
| `plugin` | fixture output fields, `generator.input.parameters`, `values` |

For non-Go-template ApplicationSets, nested maps are flattened with dot
notation, including `metadata.labels.<key>`, `metadata.annotations.<key>`, and
`values.<key>`. For Go-template ApplicationSets, nested values remain available
as maps or arrays, such as `.metadata.labels`, `.metadata.annotations`,
`.labels`, and `.values`.

Local `AppProject` manifests are also discovered. They are used for offline
diagnostics only; discovery does not contact a Kubernetes cluster or Argo CD
server.

## Rendering

Build every discovered Application:

```bash
drydock build apps --path .
```

Build exactly one discovered Application by `metadata.name`:

```bash
drydock build app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
drydock build app argocd/renovate --path .
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
`.drydock` paths and does not write generated manifests into the source
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
- `--appset-provider-fixture PATH` supplies offline YAML/JSON data for
  provider-backed ApplicationSet generators. The flag is repeatable and is
  local-file only; it never enables live provider access.
- `--skip-kind KIND` omits rendered resources with that Kubernetes kind from
  build output, manifest diffs, image extraction, and render tests. The flag is
  repeatable and matches kind only.
- `--skip-crds` omits rendered `CustomResourceDefinition` resources.
- `--skip-secrets` omits rendered `Secret` resources.

`--allow-network` is not the Helm chart-fetch flag. It only gates Git
repository-source clone/fetch for path sources. Chart and remote Kustomize
network behavior is controlled by `--offline`, `--refresh-charts`,
`--refresh-remotes`, `--chart-cache-dir`, and `--remote-cache-dir`.
`--offline` cannot be combined with `--allow-network`.

Caches must stay outside Git repository trees. New cache entries include
hidden `.drydock-cache/metadata.json` sidecars with redacted target metadata,
and older hash-only entries are listed as legacy entries when their filesystem
layout is recognized. Offline render/build/diff commands require cache hits
or local chart availability; populate caches with a prior non-offline render
using the relevant auth, cache-dir, and refresh flags.

## Cache Lifecycle

Print resolved cache roots:

```bash
drydock cache path
```

List recognized Git, chart, and remote Kustomize cache entries:

```bash
drydock cache list
drydock cache list --source chart -o json
```

Report stale entries without deleting them:

```bash
drydock cache prune --older-than 720h --dry-run
```

Delete a specific entry or all selected entries:

```bash
drydock cache delete --source git --key HASH --yes
drydock cache delete --source remote --all --dry-run
```

`cache prune` and `cache delete` require `--yes` for non-dry-run deletion.
Dry-runs never require confirmation and leave cache files in place. Cache
commands accept `--git-cache-dir`, `--chart-cache-dir`, and
`--remote-cache-dir`; `--path` and `--path-orig` are used only for safety
checks, and `--path` defaults to the current directory. Render-time network
and credential flags such as `--allow-network`, `--offline`, `--refresh-*`,
and auth flags are not cache lifecycle behavior, except that cache commands
resolve the same cache directories.

Cache lifecycle commands do not render Applications, clone/fetch Git
repositories, fetch Helm charts, fetch remote Kustomize resources, or read
credential flags. They operate only on recognized drydock cache entry roots
and reject cache roots that resolve inside the current working directory, the
selected `--path`/`--path-orig` protected roots, any Git repository tree, or
symlink-resolved equivalents. They never retry failed network or
authentication acquisitions; rerun the render/build/diff acquisition path with
the relevant credentials or refresh flags to repopulate a missing or stale
cache entry. Corrupt, mismatched, or unsupported metadata may hide descriptive
metadata, but it must not make an unrecognized filesystem child deletable.

## Go API

Use `github.com/sholdee/drydock/pkg/drydock` when embedding
the renderer directly:

```go
client := drydock.NewClient(drydock.Config{
	Path: ".",
	RepoMaps: []drydock.RepoMap{
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
`drydock.Config` to apply the same rendered-resource filters exposed by
the CLI.

Config management plugin sources are explicit. The CLI and default Go client
do not execute plugin commands; an Application source with `spec.source.plugin`
fails closed with a plugin diagnostic unless an embedding caller supplies
`drydock.Config.PluginRenderer`. Injected plugin renderer output
participates in normal render, diff, image extraction, destination namespace
defaulting, and resource filtering.

## Render Tests

Test every discovered Application without printing manifest bodies:

```bash
drydock test apps --path .
```

Test exactly one discovered Application by `metadata.name`:

```bash
drydock test app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
drydock test app argocd/renovate --path .
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
drydock test apps --path . -o json
drydock test apps --path . -o yaml
```

Structured test output contains only status records and diagnostics remain on
stderr.

## Manifest Diffs

Diff all affected Applications between two repository trees:

```bash
drydock diff apps --path ./current --path-orig ../base
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
drydock diff apps \
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
`drydock` applies the union of matching Application-local and global
settings from the baseline and current trees so a newly added ignore rule can
suppress the intended PR noise immediately.

`jqPathExpressions` are evaluated as Argo CD-style `del(<expression>)` delete
filters. Invalid expressions fail the diff so unsafe normalization does not hide
changes. `managedFieldsManagers` is an offline approximation: it suppresses
fields only when rendered desired manifests include matching
`metadata.managedFields` ownership data.

Global `resource.customizations.knownTypeFields.*` settings are also applied
when normalizing rendered resources for desired-vs-desired diffs. Global
`resource.customizations.ignoreResourceUpdates.*` settings are parsed and
reported as diagnostics, but they are not applied as desired diff ignores.
Health and action customizations, including `useOpenLibs` and Lua metadata, are
parsed and reported only. `drydock` does not execute Lua offline.

Discovered `resource.compareoptions` settings are also honored for
`ignoreResourceStatusField` and `ignoreAggregatedRoles`. By default status is
ignored for all resources. Use `ignoreResourceStatusField: none`, `off`, or
`false` when rendered status fields should remain visible in PR diffs.

Rendered-resource filters run before diff comparison. Argo CD core exclusions
and discovered `resource.exclusions`/`resource.inclusions` are applied
automatically. For example, omit CRDs and Secrets from a pull request diff with:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --skip-crds \
  --skip-secrets
```

Diff one requested Application by `metadata.name`:

```bash
drydock diff app renovate --path-orig ../base --path .
```

Use `NAMESPACE/NAME` to disambiguate:

```bash
drydock diff app argocd/renovate --path-orig ../base --path .
```

`diff app` selects the requested Application directly in each tree and does not
use changed-only Git path filtering. If the Application exists only in current,
the diff shows additions; if it exists only in baseline, the diff shows
deletions. If it is absent from both trees, the command errors.

For local inspection, keep the command successful even when a diff exists:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --exit-code=false
```

## Image Diffs

Diff conservative workload container images from rendered manifests:

```bash
drydock diff images --path ./current --path-orig ../base
```

This projection is intentionally conservative and does not report arbitrary
`image` keys from ConfigMaps or CRDs.

## Diagnostics

Run repository diagnostics without printing rendered manifests:

```bash
drydock diag --path .
```

`diag` uses the same discovery, ApplicationSet expansion, source resolution,
and render validation path as `build apps`. It prints diagnostics to stderr and
returns an error when runtime failures or error-severity diagnostics are found.
Use `--strict` to promote warnings to errors.

When local `AppProject` manifests are present, `diag`, `build`, `test`, `diff`,
and the Go API report source repository and destination server/name/namespace
validation diagnostics from those manifests. They also report source namespace
diagnostics when a project sets `spec.sourceNamespaces`. RBAC roles and
policies are parsed and reported as metadata only; Argo CD authorization is not
simulated. `permitOnlyProjectScopedClusters` is reported as deferred metadata,
and project-scoped cluster Secret enforcement is not simulated offline.
Repository credential matching diagnostics use discovered repository Secret
metadata only and never read secret credential fields.

## Local Verification And Benchmarks

Run the normal local verification suite before merging:

```bash
go test ./...
go vet ./...
golangci-lint run --allow-parallel-runners
git diff --check main..HEAD
```

Run the Phase 6 render and ApplicationSet benchmarks when changing discovery,
rendering, ApplicationSet expansion, cache event recording, or diagnostics on
hot paths:

```bash
go test ./internal/app -run '^$' -bench 'BenchmarkOrchestrator(BuildManyLocalApplications|ExpandApplicationSetList)' -benchmem -count=1
```

Benchmark numbers are trend signals, not hard pass/fail thresholds.

## Deferred Commands And Sources

These source paths are not wired in the current MVP:

- Live provider API calls for cluster, clusterDecisionResource, SCM provider,
  pull-request, and plugin ApplicationSet generators. Use explicit local
  `--appset-provider-fixture` data instead.
- CLI config management plugin execution, shellout plugin adapters, Argo CD
  repo-server sidecar plugin discovery, ambient plugin configuration, ambient
  plugin environment loading, and plugin credential injection.
- Live cluster and Argo CD API sources.
- Live destination cluster existence, sync windows, source integrity
  verification, project-scoped cluster Secrets, and full RBAC simulation.

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
