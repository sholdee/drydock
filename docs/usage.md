# Usage

`drydock` is a runtime-offline CLI for Argo CD GitOps repositories. Default
commands render and compare desired state from checked-out files plus explicit
local caches. They do not contact a Kubernetes cluster or Argo CD server, and
they do not require `kubectl`, `argocd`, Helm CLI, or Kustomize CLI.

Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources may be fetched
into drydock caches unless `--offline` is set. Use `--offline` when the command
must be cache-only.

## Application Discovery

List discovered `Application` CRs, supported generated `ApplicationSet`
Applications, and rendered child Applications from app-of-apps bootstrap
sources:

```bash
drydock get apps --path .
```

`get apps` defaults to table output and supports `-o table`, `-o name`,
`-o json`, and `-o yaml`. Use `-l`/`--selector` with Kubernetes label selector
syntax to match `Application.metadata.labels`:

```bash
drydock get apps --path . -l 'env in (prod,stage),tier!=test'
```

List rendered image references from Applications:

```bash
drydock get images --path . -o name
```

`get images` uses the same output formats as `get apps`. Diagnostics are
printed to stderr for both commands.

By default, fleet commands recursively render discovered Applications to find
additional Argo CD `Application`, `ApplicationSet`, `AppProject`, and settings
objects in their desired output. This lets repositories that commit a bootstrap
Application or Helm app-of-apps chart behave like repositories that commit the
inflated child objects. Discovery stops when it converges, with a default
maximum depth of `4`.

Use `--discovery-mode static` to disable recursive rendered fleet discovery.
Committed `ApplicationSet` objects still expand through supported offline
generators. Use `--max-discovery-depth 0` to keep normal static discovery and
explicit `--discover-kustomize` rendering while disabling recursive rendered
fleet discovery.

Static committed objects take precedence over default rendered fleet objects
with the same API version, kind, namespace, and name. Rendered duplicates emit
diagnostics instead of silently replacing committed intent.

Supported local `ApplicationSet` generators are Git directories, Git files,
list, matrix, merge, and explicit fixture-backed provider generators.
Unsupported generators emit diagnostics; non-strict commands keep supported
Applications, while `--strict` promotes the diagnostics to errors.

For generator details, fixture schemas, and stable template parameters, see
[`applicationsets.md`](applicationsets.md).

Committed and rendered `AppProject` manifests are discovered for offline
diagnostics only. Discovery does not contact a Kubernetes cluster or Argo CD
server.

Some repositories commit Argo CD bootstrap inputs as Kustomize bases and
overlays rather than committing the fully inflated Argo CD objects. Add
`--discover-kustomize PATH` to render one or more local Kustomize entrypoints
with drydock's native renderer, then discover `Application`, `ApplicationSet`,
`AppProject`, and Argo CD settings objects from the rendered output:

```bash
drydock get apps --path . --discover-kustomize argocd/overlays/prod
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

The path must be relative to `--path`, must not escape through `..`, and must
not include symlinked path components. Rendered discovery is additive: normal
repository scanning still runs, and explicitly rendered Kustomize objects take
precedence over committed duplicates with the same identity.

## Rendering

Build every discovered Application:

```bash
drydock build apps --path .
```

Build exactly one Application by `metadata.name`:

```bash
drydock build app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
drydock build app argocd/my-app --path .
```

`build app` errors when no discovered Application matches. The unqualified
`NAME` form must identify exactly one Application.

When one selected Application fails to render, embedding callers receive a
partial `BuildResult` containing successful manifests, diagnostics, and
per-Application statuses. CLI `build` commands keep stdout parseable: on render
failure, diagnostics go to stderr and invalid partial manifest streams are not
mixed into stdout.

Use `--parallelism N` on render-backed commands to render up to `N`
Applications concurrently. Multi-Application `test` and `diff` commands default
to bounded host CPU parallelism; single-Application commands default to `1`.
Parallel rendering preserves selected Application order for manifests,
statuses, diagnostics, cache events, and structured output.

Rendering supports directory sources, directory Jsonnet, Kustomize sources,
local Helm charts, Kustomize `helmCharts`, remote Kustomize HTTP(S) files and
Git refs, and Argo CD chart-only remote Helm sources. Path-based Git sources
use the local `--path` tree when the source path exists there. Use
`--repo-map URL=PATH` to force a source repository URL to a local checkout.

Repositories tagged with `argocd` or `gitops` are not always Argo CD
Application fleet repositories. `drydock test apps` reports zero applications
when no `Application` or supported `ApplicationSet` objects are discovered.

For acquisition, cache, auth, and remote source details, see
[`source-acquisition.md`](source-acquisition.md).

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
Dry-runs never require confirmation and leave cache files in place.

Cache lifecycle commands are local filesystem operations only. They do not
render Applications, clone/fetch Git repositories, fetch Helm charts, fetch
remote Kustomize resources, or read credential flags. They operate only on
recognized drydock cache entry roots and reject cache roots that resolve inside
the current working directory, selected repository roots, Git repository trees,
or symlink-resolved equivalents.

## Go API

Use `github.com/sholdee/drydock/pkg/drydock` when embedding the renderer:

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
CLI. `NewClient` accepts public Git, chart, remote-resource, and plugin
renderer interfaces so tests and embedding callers can provide deterministic
fakes without importing internal packages.

Public render results include Applications, manifests, diagnostics, and
per-Application statuses. If one selected Application fails, `Render` returns
the error and still returns partial successful manifests, stable diagnostics,
and statuses. `SkipKinds`, `SkipCRDs`, and `SkipSecrets` apply the same
rendered-resource filters exposed by the CLI.

Config management plugin sources are explicit. The CLI and default Go client do
not execute plugin commands by default. Discovered Argo CD CMP definitions that
normalize to a safe `kustomize build` command are interpreted through drydock's
native Kustomize renderer. Other plugin sources fail closed with
`plugin.unsupported` unless an embedding caller supplies
`drydock.Config.PluginRenderer` or a trusted drydock plugin policy matches the
plugin name. Exec policy requires trusted provenance and `--enable-plugins`;
native policy engines such as `avp-compat` and `native-kustomize` do not
execute plugin commands. Embedders can pass a renderer directly or use
`drydock.NewPluginRegistry(map[string]drydock.PluginRenderer{...})` to dispatch
in-process renderers by `plugin.name`. The public plugin request includes the
resolved source, `$ref` roots and source metadata, kube version, and API
versions.

See [`plugin-policy.md`](plugin-policy.md) for `--plugin-policy-path`,
`--plugin-policy-ref`, `--plugin-policy-repo`, `--disable-plugin-policy`, the
policy schema, CMP compatibility behavior, and exec security model.

## Render Tests

Test every discovered Application without printing manifest bodies:

```bash
drydock test apps --path .
```

Test exactly one Application:

```bash
drydock test app renovate --path .
drydock test app argocd/my-app --path .
```

Default text output prints one status line per selected Application:

```text
PASS renovate
FAIL argocd/broken Application argocd/broken source[0] path="..." ...
SKIPPED argocd/skipped unsupported ApplicationSet generator ...
```

When `test apps` discovers no Applications, text output prints
`No Applications discovered.` and structured JSON output prints `[]`.

Status values are `PASS` for Applications that rendered successfully, `FAIL`
for render or planning failures, and `SKIPPED` when discovery or expansion
preconditions prevented safe rendering. `test apps` and `test app` return exit
code `0` only when every selected Application passes. Any `FAIL`, `SKIPPED`,
or runtime failure returns exit code `2`.

`drydock test apps` and `drydock test app` validate configured custom Argo CD
health Lua by default. Validation is offline and runs against rendered desired
manifests, not live Argo CD Application health aggregation. Use
`--skip-lua-health` to isolate render failures or benchmark render-only
behavior:

```bash
drydock test apps --path . --skip-lua-health
```

When `test apps` writes text output to a terminal, statuses stream as each
Application completes. Status labels are colored, a single stderr progress
counter updates in place when stderr is also a terminal, and a final summary is
printed after the status lines. Redirected text output stays plain and
buffered.

Structured status output is available with `-o json` and `-o yaml`:

```bash
drydock test apps --path . -o json
drydock test apps --path . -o yaml
```

## Manifest Diffs

Diff all affected Applications between two repository trees:

```bash
drydock diff apps --path ./current --path-orig ../base
```

`diff apps` renders the baseline and current trees, then prints
desired-vs-desired manifest diffs. It uses changed-only selection by default:
if changed files can be mapped to Application inputs, only affected
Applications render; if any changed file is unowned, non-strict mode warns and
renders all Applications.

Use `--changed-only=false` to render all Applications explicitly, or
`--strict-changed-only` to fail on incomplete input ownership.

### Git Ref Diffs

Diff commands can compare local Git refs without creating a separate baseline
worktree. `--ref-orig` replaces `--path-orig` with a temporary snapshot of the
baseline ref. `--ref` replaces `--path` with a temporary snapshot of the current
ref. `--repo` selects the local Git repository used to resolve refs and
defaults to `--path`.

```bash
drydock diff apps --path . --ref-orig main
drydock diff apps --repo . --ref feature --ref-orig main
```

`--path . --ref-orig main` compares the current working tree, including tracked
uncommitted changes, against committed `main`. `--repo . --ref feature
--ref-orig main` compares committed refs only. Top-level remote `--repo` URLs
are not supported yet; clone the repository locally and pass the local path.

Manifest diffs default to unified diff output. `diff apps` and `diff app` also
support `-o markdown`, `-o json`, and `-o yaml`. Markdown output is intended for
review comments: it includes a summary plus expandable per-Application rendered
manifest patches, and embeds successful diagnostics in the document. JSON and
YAML serialize the structured `[]diff.Result` payload. Diagnostics remain on
stderr for unified, JSON, and YAML output so stdout stays parseable. `-o name`
is not supported for manifest diffs.

```bash
drydock diff apps --path ./current --path-orig ../base -o markdown
```

Unified diff output supports git-style ANSI color with
`--color=auto|always|never`. The default, `auto`, colors only when writing to a
terminal. `always` forces ANSI color for text diff output, while `never`
disables ANSI color for text diff output. Diagnostics keep their existing
stderr terminal-color behavior. Structured JSON and YAML payloads never contain
ANSI escapes.

Manifest diffs hide common Helm-rendered metadata noise by default:

- `metadata.labels.helm.sh/chart`
- `metadata.labels.chart`
- `metadata.labels.app.kubernetes.io/version`
- `spec.template.metadata.labels.helm.sh/chart`
- `spec.template.metadata.labels.chart`
- `spec.template.metadata.labels.app.kubernetes.io/version`
- `spec.template.metadata.annotations.checksum/*`

Use `--show-ignored-fields` on `diff apps` or `diff app` to include these
drydock-default ignored fields again. This flag does not disable Argo CD
`ignoreDifferences`, compare options, or explicit `--strip-attr` filters.

Use repeatable `--strip-attr KEY` to remove matching keys from
`metadata.labels` and `metadata.annotations` before comparing rendered
manifests:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --strip-attr helm.sh/chart \
  --strip-attr app.kubernetes.io/version
```

Application-level `spec.ignoreDifferences[]` rules and global
`resource.customizations.ignoreDifferences.*` settings are honored for rendered
resource diffs. Supported ignore fields are `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers`. `jqPathExpressions` are
evaluated as Argo CD-style `del(<expression>)` delete filters. When a matching
resource exists on both sides, drydock applies the union of matching
Application-local and global settings from the baseline and current trees so a
new ignore rule can suppress intended PR noise immediately.

By default status is ignored for all resources. Use
`ignoreResourceStatusField: none`, `off`, or `false` in discovered Argo CD
compare options when rendered status fields should remain visible in PR diffs.

Rendered-resource filters run before diff comparison. Argo CD core exclusions
and discovered `resource.exclusions`/`resource.inclusions` are applied
automatically. Omit CRDs and Secrets from a pull request diff with:

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
drydock diff app argocd/my-app --path-orig ../base --path .
```

`diff app` selects the requested Application directly in each tree and does not
use changed-only Git path filtering. If the Application exists only in current,
the diff shows additions; if it exists only in baseline, the diff shows
deletions.

For local inspection, keep the command successful even when a diff exists:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --exit-code=false
```

## Image Diffs

Diff rendered image references from rendered manifests:

```bash
drydock diff images --path ./current --path-orig ../base
```

This projection includes PodSpec container images plus scalar manifest fields
whose key is exactly `image`. It does not scan arbitrary string content, Secret
manifests, top-level metadata/status, or ConfigMap data payloads. Use `-o name`
to print current-only added image references, one per line. Removed-only image
changes print no names but still return the diff exit code unless
`--exit-code=false` is set. Use `-o markdown` for pull request comments, or
`-o json` / `-o yaml` for machine-readable `added`, `removed`, and `unchanged`
image lists. Diagnostics remain on stderr so stdout stays valid JSON or YAML.
Text image diffs use the same `--color=auto|always|never` behavior as manifest
diffs.

## Diagnostics

Run repository diagnostics without printing rendered manifests:

```bash
drydock diag --path .
```

`diag` uses the same discovery, ApplicationSet expansion, source resolution,
and render validation path as `build apps`. It prints diagnostics to stderr and
returns an error when runtime failures or error-severity diagnostics are found.
Use `--strict` to promote warnings to errors.

Structured diagnostic output can include a redacted settings summary:

```bash
drydock diag --path . --settings -o json
drydock diag --path . --settings -o yaml
```

The settings summary is CLI-only. It reports parsed resource-customization
metadata such as action names, `useOpenLibs`, and SHA-256 hashes for
health/action Lua. It does not print raw Lua bodies, embedded secret-looking
strings, or any live-cluster state.

When local `AppProject` manifests are present, `diag`, `build`, `test`, `diff`,
and the Go API report source repository and destination validation diagnostics
from those manifests. RBAC roles and policies are parsed and reported as
metadata only; Argo CD authorization is not simulated. Repository credential
matching diagnostics use discovered repository Secret metadata only and never
read secret credential fields. Cluster Secret diagnostics likewise use only
`name`, `server`, `namespaces`, `clusterResources`, and `project` metadata;
credential/config fields are not decoded, retained, or printed.

`argocd-cmd-params-cm` settings are parsed as runtime-boundary metadata when
they imply live repo-server, controller, or ApplicationSet controller behavior.
They may produce diagnostics and settings summaries, but they do not mutate
drydock render behavior.

## Local Verification And Benchmarks

Run the normal local verification suite before merging:

```bash
go test ./...
go vet ./...
golangci-lint run --allow-parallel-runners
git diff --check main..HEAD
```

Run render and ApplicationSet benchmarks when changing discovery, rendering,
ApplicationSet expansion, cache event recording, or diagnostics on hot paths:

```bash
go test ./internal/app -run '^$' -bench 'BenchmarkOrchestrator(BuildManyLocalApplications|ExpandApplicationSetList)' -benchmem -count=1
```

Benchmark numbers are trend signals, not hard pass/fail thresholds.

Advanced profiling flags are available in release binaries and `go run` builds
for maintainers diagnosing real repository performance:

```bash
drydock --profile cpu --profile-out ./drydock-profiles test apps --path .
drydock --profile trace --profile-out ./drydock-profiles diff apps --path . --ref-orig main
drydock --profile mem --profile-out ./drydock-profiles get images --path .
```

`--profile` accepts `cpu`, `mem`, `block`, `mutex`, or `trace`. Profile metadata
is written to stderr, and profile artifacts are written under `--profile-out`.
Normal stdout output remains unchanged for text, JSON, YAML, and diff formats.
The `mem` mode writes a heap profile at command completion; it is not a memory
timeline.

Inspect pprof profiles with:

```bash
go tool pprof ./drydock-profiles/drydock-test-apps-*.cpu.pprof
```

Inspect traces with:

```bash
go tool trace ./drydock-profiles/drydock-diff-apps-*.trace.out
```

Profiles may include local paths, command arguments, symbols, and sampled
in-memory strings. Review profile files before sharing them publicly.

Use the optional maintainer script for repeated real-repository smokes:

```bash
scripts/profile-smoke.sh ~/git/home-ops --binary ./dist/drydock --profile cpu --warm-runs 3
```

The script is not a CI gate. It accepts explicit `--ref` and `--ref-orig`
values, and otherwise tries to detect the baseline branch from `origin/HEAD`,
`main`, or `master`.

## Runtime-Boundary Commands And Sources

These source paths are outside the current default runtime contract:

- Live provider API calls for cluster, clusterDecisionResource, SCM provider,
  pull-request, and plugin ApplicationSet generators.
- Arbitrary CLI config management plugin execution outside trusted exec policy
  plus `--enable-plugins`, Argo CD repo-server sidecar plugin discovery,
  ambient plugin configuration, ambient plugin environment loading, and plugin
  credential injection.
- Live cluster and Argo CD API sources.
- Live destination cluster existence, sync windows, source integrity
  verification, project-scoped cluster Secret enforcement beyond discovered
  metadata, and full RBAC simulation.

See [`plugin-policy.md`](plugin-policy.md) for the supported trusted CMP
compatibility path.

See
[`reports/live-integration-design-gate.md`](reports/live-integration-design-gate.md)
before proposing live-runtime features.

## Optional Home-Ops Smoke

Run the optional home-ops Renovate smoke:

```bash
RENOVATE_CHART_TO=4.8.2 scripts/home-ops-renovate-smoke.sh
```

The smoke script is optional, targets maintainers with a local `home-ops`
checkout, uses temporary worktrees, and does not mutate the real checkout.
Committed tests and portable fixtures do not depend on `home-ops`.

Run the optional home-ops pattern smoke:

```bash
scripts/home-ops-pattern-smoke.sh
```

The pattern smoke applies small synthetic changes across representative
`home-ops` app patterns in temporary worktrees. It is optional, may fetch public
charts, and accepts `RENOVATE_CHART_TO` or `EXTERNAL_SECRETS_CHART_TO` when
maintainers want to choose explicit chart target versions.
