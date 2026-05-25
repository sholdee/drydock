# drydock Design

Date: 2026-05-22

## Purpose

`drydock` is an independent Go CLI for offline Argo CD GitOps repository
analysis. Its first product goal is desired-vs-desired pull request diffing:
given a current GitOps tree and a baseline tree, render the Argo CD
Applications that would be reconciled and show what desired Kubernetes
manifests changed.

The tool is intentionally not a live-cluster diff in the first release. It
does not contact the Kubernetes API server, does not run Argo CD controllers,
and does not claim to reproduce server-side apply prediction, admission
mutation, or managed-fields ownership.

Default render, diff, image, and diagnostic workflows are offline
desired-vs-desired analysis. Live-cluster diffing, Argo CD server-side diff
parity, Kubernetes defaulting, admission mutation, and live-only
managed-fields ownership prediction are not approximated silently. Any future
implementation for those behaviors must first update the live integration
design gate and keep the default offline path independent of a Kubernetes
cluster, Argo CD server, `kubectl`, `argocd`, Helm/Kustomize command-line
tools, and external render services.

## Repository

The canonical module and repository identity is:

```text
github.com/sholdee/drydock
```

Local checkout paths are operator-specific and may lag the repository rename
until the external rename checklist is completed.

The repository is licensed Apache-2.0. Releases include a root `LICENSE` and
third-party notices for redistributed source, copied fixtures, binary/container
distributions, and imported Apache-2.0 components as needed.

## Scope

MVP supports:

- Direct `Application` CRs.
- `ApplicationSet` Git directories, Git files, list, matrix, and merge
  generators with Go templates.
- Single-source and multi-source Applications.
- Git path sources rendered as Kustomize or directory manifests.
- Helm chart sources from HTTP and OCI repositories.
- Helm `$ref/...` external value files from Git sources.
- Repository URL to local path mappings.
- Explicit network opt-in for unmapped external repositories.
- Explicit Git HTTPS bearer/basic auth, Git SSH key-file auth, HTTP(S) Helm
  bearer/basic auth, and explicit OCI Helm registry config path plumbing.
- Config management plugin source detection with fail-closed diagnostics in
  the CLI/default path, plus injectable Go API plugin renderers, named
  in-process plugin registry dispatch, and plugin renderer timeout controls
  for embedders that provide deterministic local plugin rendering.
- Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
  ApplicationSet generators through explicit local fixtures.
- PR diff, build, get, and image diff commands.
- Changed-only rendering with safe fallback.

Deferred:

- Live-cluster diff.
- Argo CD API/server integration, Argo CD server-side diff parity,
  Kubernetes defaulting, admission mutation, and live-only managed-fields
  ownership prediction.
- CLI config management plugin execution, shellout plugin adapters, Argo CD
  repo-server sidecar plugin discovery, ambient plugin configuration, ambient
  plugin environment loading, and plugin credential injection.
- Full Argo CD project/RBAC/destination validation.
- Health evaluation.

## Architecture

The default architecture is a native local engine with Argo CD types and
decoupled helpers. The tool imports Argo CD API types and selected reusable
normalization/diff helpers, but owns local orchestration and rendering.

Primary packages:

- `cmd/drydock`: Cobra CLI and exit-code handling.
- `internal/config`: canonical `ArgoSettings` model loaded from CLI flags,
  `argocd-cm`, Argo CD Helm values, repository secrets, and defaults.
- `internal/discovery`: scans repository trees for `Application`,
  `ApplicationSet`, Argo CD settings, and repository metadata.
- `internal/appset`: local ApplicationSet Git directories, Git files, list,
  matrix, and merge generators with Go-template support.
- `internal/source`: repository URL normalization, local repo maps, network
  opt-in, source checkout, and cache management.
- `internal/render`: renderer interface plus Helm, Kustomize, and directory
  renderers.
- `internal/app`: Application normalization, single/multi-source planning,
  `$ref` resolution, repeated-resource warnings, and per-Application rendered
  output.
- `internal/change`: changed-file detection and Application input indexing.
- `internal/diff`: parent-aware desired-vs-desired diffs and image diffs.
- `internal/diagnostic`: warnings, provenance, unsupported-feature reporting,
  and strict-mode escalation.

The repo-server wrapper approach is not the default architecture. Argo CD
repo-server internals can be used as compatibility references or test oracles,
but the production path should not depend on repo-server service/cache/gRPC
plumbing.

The shellout approach is also not the default architecture. The product goal is
a single binary with no required `helm`, `kustomize`, `kubectl`, or `argocd`
executables on `PATH`. A future optional compatibility backend may be added
behind the renderer interface if needed.

Config management plugin execution follows the same boundary. The default CLI
and package-level API do not execute plugin commands or discover repo-server
sidecars. Embedders may inject deterministic in-process plugin renderers through
`pkg/drydock`; those renderers receive explicit Application source metadata and
return manifests and diagnostics in-process. The named registry helper dispatches
only by explicit `plugin.name`. Timeouts are caller-configured through the public
API and are reported as plugin failures without converting caller cancellation
into plugin timeout diagnostics.

## Argo CD Versioning

The Go module pins Argo CD dependencies deliberately. Initial dependency
selection followed the Argo CD checkout used during project bootstrap, and
future changes should happen through explicit upgrade PRs.

`drydock version` prints:

- `drydock` version.
- Embedded Argo CD API/diff dependency versions.
- Go version.

Argo CD dependency upgrades are explicit PRs with compatibility test updates.
The project does not float Argo CD dependencies to `latest`.

## Settings Model

All runtime settings are normalized into a canonical `ArgoSettings` structure.
Providers feed this model, and every discovered value carries provenance.

Example model:

```go
type ArgoSettings struct {
    KustomizeBuildOptions []string
    HelmRepositories map[string]RepositorySettings
    ResourceCustomizations map[string]ResourceCustomization
    ResourceExclusions []ResourceExclusion
    TrackingMethod string
    InstanceLabelKey string
}
```

Provider precedence:

1. Explicit CLI flags, including `--argocd-cm`, `--argocd-values`,
   `--repo-secret`, and `--kustomize-build-option`.
2. Rendered Argo CD ConfigMap candidates, especially `argocd-cm`.
3. Argo CD Helm chart values candidates, including `configs.cm`.
4. Repository secrets labeled `argocd.argoproj.io/secret-type: repository`,
   using only non-sensitive fields such as `url`, `type`, `enableOCI`, `name`,
   and `project`.
5. Built-in Argo CD defaults.

Discovery is generic and not specific to any one repository. Common paths may
be scanned, but conflicting discovered settings fail closed and require an
explicit CLI selection. Missing settings use defaults with a warning.

Example diagnostic:

```text
kustomize.buildOptions:
  value: --enable-helm --helm-api-versions grafana.integreatly.org/v1beta1/GrafanaDashboard
  source: apps/argocd/manifests/values.yaml:configs.cm.kustomize.buildOptions
```

Health customizations, RBAC, and resource exclusions are recorded when found,
but only rendering/diff-affecting settings are enforced in the MVP.
Health/action Lua is parsed as metadata only and is never executed in the
offline render/diff path. `diag --settings -o json|yaml` exposes a CLI-only
redacted summary for operators, including names, booleans, and SHA-256 hashes
of trimmed Lua bodies. Raw Lua bodies and secret-looking strings embedded in
Lua are not part of the structured summary.

## Application Discovery

The default command behavior scans the provided tree for Argo CD entrypoints:

```bash
drydock get apps --path ./repo
drydock build app --path ./repo monitoring
drydock diff apps --path ./repo --path-orig ../repo-base
```

Optional narrowing flags include:

- `--app-manifests <path>`
- `--namespace <namespace>`
- `--project <project>`
- `--selector <label-selector>`
- `--app <name>`

The scanner identifies direct `Application` CRs and supported
`ApplicationSet` CRs. Generated Applications and direct Applications share the
same downstream planning, rendering, and diff path.

## ApplicationSet Support

The supported local ApplicationSet subset includes the Git directories
generator, Git files generator, and list generator.

Supported behavior:

- `spec.goTemplate: true`
- `spec.goTemplateOptions`, including `missingkey=error`
- Sprig-compatible template functions used by Argo CD, including
  `regexReplaceAll`
- directory include/exclude filtering
- git-files include/exclude filtering
- `.path.path`
- `.path.basename`
- `.path.basenameNormalized`
- `.path.filename` for git-files
- `.path.filenameNormalized` for git-files
- `.path.segments`
- generated `Application.metadata.namespace` set to the ApplicationSet
  namespace
- Argo CD's default generated Application finalizer where applicable
- multiple supported top-level generators evaluated independently and
  concatenated in manifest order

Unsupported generators or unsupported ApplicationSet fields produce diagnostics.
In non-strict mode, unsupported ApplicationSets are skipped with warnings. In
strict mode, they are errors.

Git files generator matches are sorted by normalized relative path. Traversal is
contained to the repository root: symlinks, absolute paths, and `..` escapes are
not followed. YAML/JSON mapping documents become template params. Go-template
mode preserves nested maps such as `.cluster.name`; non-Go-template mode
flattens nested keys such as `cluster.name` and converts scalar values to
strings. Arrays, scalars, invalid YAML/JSON, and empty files produce
diagnostics.

Git files `values` use the same `values.*` and `.values.*` behavior as Git
directories. Git files `exclude: true` excludes a file even when another
pattern includes it.

`pathParamPrefix` applies to all path-related params. For example,
`pathParamPrefix: myRepo` produces `.myRepo.path.path` in Go templates and
`myRepo.path` in non-Go-template mode.

In PR diff mode, mapped repository URLs use the local `--path` or `--path-orig`
trees for directory discovery and rendering, even when the ApplicationSet
declares `revision: master`.

## Source Planning

Applications are planned using Argo CD source precedence:

- If `spec.sources` is non-empty, it wins and `spec.source` is ignored.
- Otherwise, `spec.source` is used.

Multi-source support is included in the MVP for supported source types.

Supported multi-source semantics:

- Each source renders independently in source array order.
- Ref-only sources are allowed and render no manifests.
- `ref: values` creates a `$values` root for Helm value files.
- `$ref/...` value file paths resolve from the referenced source root, not
  from its `path`.
- Duplicate refs are errors.
- Invalid ref keys are errors.
- Sources with `ref` and `chart` are rejected for external value usage.
- If multiple sources emit the same resource identity within one Application,
  the later source wins and a repeated-resource warning is emitted.

Repository resolution:

- `--repo-map <url>=<path>` maps normalized repository URLs to local trees.
- `--path` and `--path-orig` are authoritative for mapped PR repositories and
  override declared revisions.
- Unmapped external repositories require another repo map or `--allow-network`.
- Network access is off by default.
- Git HTTPS auth is explicit via bearer token or username/password, with bearer
  token taking precedence.
- Git SSH auth is explicit via key file, passphrase when required, and
  known-hosts file. SSH auth fails closed before network access if the key file
  or known-hosts file is missing.
- Supported SSH URL forms are `ssh://git@host/org/repo.git`,
  `git@host:org/repo.git`, and `ssh://host/org/repo.git`. Missing usernames
  default to `git`.
- HTTP(S) Helm auth is explicit via bearer token or username/password, with
  bearer token taking precedence.
- OCI Helm auth is explicit via a `--registry-config` path.
- The tool never prompts for credentials, never reads ambient Git credential
  helpers, and never reads ambient Helm registry config in this slice.
- Passwords, bearer tokens, SSH private keys, SSH passphrases, and registry
  credential values are never printed in diagnostics or formatted errors.

## Rendering

All renderers implement:

```go
type Renderer interface {
    Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, Diagnostics, error)
}
```

MVP renderers:

- Helm renderer: supports HTTP and OCI chart sources, target revision,
  release name, destination namespace, `valuesObject`, `values`, value files,
  file parameters, `ignoreMissingValueFiles`, and `$ref/...` external value
  files.
- Kustomize renderer: supports local Kustomize builds with Argo settings build
  options, especially `--enable-helm` and Helm API versions. App-level
  Kustomize overrides are supported where Go libraries make this practical.
- Directory renderer: parses plain YAML/JSON manifests, flattens `List`
  resources, skips irrelevant files, and errors on invalid manifest content.

The default render path does not shell out. Renderers should use Go libraries
and isolated temp/cache directories.

## Diff Semantics

The primary diff is desired-vs-desired. It answers: "What rendered desired
manifests changed between the base tree and current tree?"

Offline-safe behavior:

- Render both sides using the same planning and settings model.
- Normalize obvious generated noise.
- Apply Argo CD `ignoreDifferences` where possible without live managed fields.
- Apply JSON pointer and jq path ignores through reusable Argo CD normalizers
  where decoupled.
- Treat `ServerSideDiff=true` and `ServerSideApply=true` as diagnostics, not
  executable behavior.

The tool does not claim to reproduce:

- Kubernetes API defaulting.
- Server-side apply field ownership.
- Admission webhooks.
- Managed-fields-manager ignores.
- Live Argo CD server-side diff.

## Changed-Only Mode

Changed-only mode is on by default for PR diffs.

Algorithm:

1. Compute changed paths between `--path-orig` and `--path`.
2. Build an Application input index from discovered direct and generated
   Applications.
3. Match changed files to every Application whose inputs intersect the change.
4. Include Application manifest files, source paths, `$ref` value-file inputs,
   and `argocd.argoproj.io/manifest-generate-paths` where supported.
5. Render only affected Applications when every changed path is owned.
6. If any changed path is unowned, render all Applications and report the
   unowned paths.
7. `--strict-changed-only` fails on unowned paths instead.
8. `--changed-only=false` renders all Applications.

Unlike Flux-oriented ownership, Argo CD changed-only mode does not choose a
single most-specific owner. Overlapping Applications are valid, so all affected
Applications are kept.

## Output

Default unified diff headers include parent Application, source, and child
resource identity:

```diff
--- Application: argocd/monitoring Source: 0 apps/monitoring Deployment: monitoring/foo
+++ Application: argocd/monitoring Source: 0 apps/monitoring Deployment: monitoring/foo
@@ ...
```

Structured output includes:

- parent Application kind, namespace, and name
- source index, source name, and source path/chart
- resource group, kind, namespace, and name
- change type
- warnings
- diff body

Supported formats:

- `diff`
- `json`
- `yaml`
- `name` where useful, especially image output

Image diff defaults to Argo-style workload image extraction. A future
`--images all-strings` mode can provide broader heuristic extraction for CRDs
and CI pull-test workflows.

## Diagnostics

Diagnostics are explicit and provenance-based.

Required diagnostics:

- settings provenance
- conflicting settings candidates
- unsupported ApplicationSet fields/generators
- unsupported source types
- server-side diff/apply offline limitations
- repeated resources per Application
- unowned changed files and render-all fallback
- strict-mode escalations

Secrets are never printed. Repository secrets only contribute non-sensitive
metadata.

## Exit Codes

Diff-style commands use:

- `0`: command succeeded and no diff was found.
- `1`: command succeeded and a diff was found.
- `2`: tool, configuration, discovery, or render error.

`--exit-code=false` makes diffs exit `0` for local inspection.

Warnings do not change exit code unless strict mode promotes them to errors.

## CLI Shape

Initial command surface:

```bash
drydock get apps --path .
drydock build app <name> --path .
drydock build apps --path .
drydock diff app <name> --path . --path-orig ../base
drydock diff apps --path . --path-orig ../base
drydock diff images --path . --path-orig ../base -o json
drydock diag --path .
drydock version
```

Important flags:

- `--path`
- `--path-orig`
- `--repo-map <url>=<path>`
- `--allow-network`
- `--changed-only`
- `--strict-changed-only`
- `--strict`
- `--exit-code`
- `--unified`
- `--limit-bytes`
- `-o diff|json|yaml|name`
- `--argocd-cm`
- `--argocd-values`
- `--repo-secret`
- `--kustomize-build-option`
- `--app-manifests`
- `--namespace`
- `--project`
- `--selector`

## Linting And Formatting

The repository includes linting from the start.

`.golangci.yml` keeps the strict Go lint profile selected during project
bootstrap:

- golangci-lint config version `2`
- 5 minute timeout
- strict linters including `bodyclose`, `copyloopvar`, `durationcheck`,
  `errcheck`, `errorlint`, `exhaustive`, `gocritic`, `gocyclo`, `govet`,
  `ineffassign`, `intrange`, `misspell`, `nakedret`, `nilerr`, `nilnil`,
  `nolintlint`, `prealloc`, `staticcheck`, `unconvert`, `unused`,
  `wastedassign`, and `whitespace`
- `gocyclo` threshold 15
- `nolintlint` requires explanations and specific linters
- `errcheck` checks type assertions
- standard error-handling exclusions
- `gofmt` formatter with simplify enabled

`.markdownlint-cli2.yaml` keeps the Markdown lint profile selected during
project bootstrap:

- disable line length
- allow inline HTML
- allow files without first-line headings

## Agent Guidance

`AGENTS.md` owns mandatory agent operating rules and hard constraints.
`docs/README.md` owns documentation routing. Update those files when a design
change alters agent rules, package routing, validation expectations, or
documentation ownership.

## Validation

Baseline validation commands:

```bash
go test ./...
go vet ./...
golangci-lint run
markdownlint-cli2 '**/*.md'
```

Compatibility and regression tests cover:

- ApplicationSet Git directories rendering.
- ApplicationSet Go-template behavior and missing-key errors.
- Multi-source precedence.
- Ref validation.
- Helm `$ref` external value files.
- Helm `valuesObject` precedence.
- OCI Helm repository metadata.
- Kustomize build options from settings discovery.
- Repeated-resource last-wins warnings.
- Changed-only mapping.
- Unowned changed-file fallback and strict failure.
- Exit-code behavior.

Home-ops-oriented tests use minimal fixtures adapted for the behavior under
test, not a wholesale copy of the operational repository.

## Open Risks

- Pure-Go Kustomize with Helm chart support may expose compatibility gaps
  against Argo CD's shellout behavior.
- Pure-Go Helm OCI handling must match Argo CD chart-source semantics closely
  enough for common repos.
- Argo CD internal packages may change across versions, so imports must stay
  conservative and pinned.
- Offline normalization cannot know all cluster-scoped CRDs without discovery
  data, so unknown GVK namespace handling must be documented and configurable
  over time.
- Network-off default requires clear diagnostics for missing external sources.

## Acceptance Criteria

- New repository is Apache-2.0 licensed.
- No required shellouts for default render/diff workflows.
- Direct Applications and supported ApplicationSets are discovered from repo
  trees.
- Multi-source Applications work for supported source types.
- Local repo maps override declared revisions in PR diff mode.
- Argo CD settings discovery is generic, provenance-based, and conflict-aware.
- Changed-only mode is safe by default and strict when requested.
- Diff output is parent-aware.
- Exit codes are CI-friendly.
- Linting configs mirror the referenced Go and Markdown lint settings.
- AGENTS.md is created early and maintained alongside implementation changes.
