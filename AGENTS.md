# AGENTS.md - argocd-local

## What This Is

`argocd-local` is an independent Go CLI for offline Argo CD GitOps repository
analysis. The first product goal is desired-vs-desired PR diffing: render Argo
CD Applications from a current tree and a baseline tree, then show what desired
Kubernetes manifests changed.

The product contract is a self-contained Go binary that can render and diff
Argo CD repositories from checked-out files plus explicit local caches, without
requiring a Kubernetes cluster, `kubectl`, the `argocd` CLI, Helm/Kustomize
executables, or any other external renderer. Network-aware source acquisition
may exist as an explicit cache-population path, but the core render/diff engine
must remain local, deterministic, and library-backed.

It is not a live-cluster diff tool in the MVP. Do not add Kubernetes API
dependencies, live Argo CD server calls, or shellout-based render paths unless
the design spec is updated first.

## Current Design

Read the design spec before substantive changes:

- `docs/superpowers/specs/2026-05-22-argocd-local-design.md`

## Subagent Sandbox Rules

Subagents must not request sandbox escalation as part of routine
implementation, review, or verification. Run local, non-network commands that
work inside the current sandbox. If a useful command would require escalation,
network access, or approval, skip that command, report the verification gap,
and continue with the rest of the local review or implementation. Do not leave
roadmap execution blocked on approval prompts from review agents.

Every worker and reviewer prompt for roadmap work must include this exact
constraint:

> Do not request sandbox escalation. If a useful command would require
> approval, network, or escalation, skip it and report it as skipped.

The coordinating agent should treat approval-gated checks as unavailable
verification, not as blockers. If a spawned agent does request approval anyway,
do not wait for that prompt. Mark the command as skipped, redirect the agent
once with the constraint above, or close/replace that agent and continue the
phase using local evidence plus an explicit skipped-check note.

Roadmap phases must keep progressing when only optional verification is
approval-gated. Do not wait on a subagent approval prompt before starting other
independent implementation or review work. If the skipped command is required
to prove correctness, record the gap in the slice review and use another local
check or a narrower review prompt. Controller prompts for future roadmap phases
should state that approval prompts from workers or reviewers are abandoned as
skipped verification, never treated as human-blocking phase status.

## Command Surface

Current top-level commands:

- `argocd-local get apps --path .`: list discovered Applications by name.
- `argocd-local get images --path .`: render discovered Applications and list
  conservative workload container images.
- `argocd-local build apps --path .`: render all discovered Applications.
- `argocd-local build app NAME --path .`: render exactly one discovered
  Application by `metadata.name`.
- `argocd-local test apps --path .`: report PASS/FAIL/SKIPPED render status
  for all discovered Applications without printing manifests.
- `argocd-local test app NAME --path .`: report PASS/FAIL/SKIPPED render
  status for exactly one discovered Application by `metadata.name`.
- `argocd-local diff apps --path . --path-orig ../base`: render and diff all
  Applications between a baseline tree and current tree.
- `argocd-local diff app NAME --path . --path-orig ../base`: diff one
  requested Application by name between a baseline tree and current tree.
- `argocd-local diff images --path . --path-orig ../base`: render both trees
  and compare conservative workload container images.
- `argocd-local diag --path .`: run repository diagnostics without printing
  manifests.
- `argocd-local version`: print version, commit, Go version, and Argo CD module.

Named app arguments accept `NAME` or `NAMESPACE/NAME`; use the
namespace-qualified form when the same `metadata.name` exists in multiple
namespaces.

Current shared flags are `--path`, `--path-orig`, `--selector`/`-l`, `--repo-map`,
`--allow-network`, `--git-cache-dir`, `--refresh-git`, `--offline`,
`--git-username`, `--git-password`, `--git-bearer-token`,
`--git-ssh-key-file`, `--git-ssh-passphrase`, `--git-known-hosts-file`,
`--refresh-charts`, `--chart-cache-dir`, `--helm-username`,
`--helm-password`, `--helm-bearer-token`, `--registry-config`,
`--refresh-remotes`, `--remote-cache-dir`, `--remote-username`,
`--remote-password`, `--remote-bearer-token`, `--changed-only`,
`--strict-changed-only`, `--strict`, `--exit-code`, `--output`/`-o`,
`--unified`/`-u`, `--strip-attr`, `--skip-kind`, `--skip-crds`,
`--skip-secrets`, and `--limit-bytes`.

Public embedding API lives in `pkg/argocdlocal`. Keep its exported types free
of `internal/...` package types. Package-level functions should follow CLI
default network/cache behavior, while `NewClient` accepts public Git, chart,
and remote-resource acquirer interfaces for deterministic tests and embedding
without shelling out or requiring network access. Preserve partial render
results: public `Render` must return successful manifests, diagnostics, and
per-Application statuses even when one selected Application fails.

## Settings Discovery

Settings flow into `internal/config.ArgoSettings`. Providers must record
provenance and must fail closed on conflicting discovered values. Repository
Secrets may contribute non-sensitive fields (`url`, `type`, `name`, `project`,
`enableOCI`) but must not retain username, password, bearer tokens, SSH keys,
or TLS material.

## AppProject Validation

Discovery includes local `AppProject` manifests. Source repository and
destination server/name/namespace checks are offline diagnostics derived from
those local manifests only. Application source namespace checks are also local
diagnostics when `spec.sourceNamespaces` is set.

RBAC roles and policies are parsed and reported as metadata only; do not
simulate Argo CD authorization or Casbin policy evaluation offline.
`permitOnlyProjectScopedClusters` is reported as deferred metadata, and
project-scoped cluster Secret enforcement is not simulated offline.

Repository credential matching diagnostics use discovered repository Secret
metadata only. They may compare non-sensitive fields such as `url`, `type`,
`name`, `project`, and `enableOCI`, but must never read, retain, or report
secret credential fields.

## Discovery

Discovery scans YAML files for Argo CD entrypoints. Keep scanning generic; do
not hard-code `home-ops` paths. Use `--app-manifests` style narrowing when a
caller wants explicit paths. Classify candidates by full GVK, skip symlinks in
default scans, reject symlink components in explicit app manifest paths, and
keep default scans tolerant of unrelated YAML files.

## ApplicationSet Support

`internal/appset` supports local Git directories, Git files, list, matrix, and
merge generators with Go templates. Use path-style matching, keep
include/exclude semantics deterministic, and preserve Argo CD template
behavior such as `missingkey=error` and Sprig functions. Multiple supported
top-level generators are evaluated independently and concatenated in manifest
order. Unsupported generators must produce diagnostics.

List generators support both `elements` and `elementsYaml`; `elementsYaml`
entries must decode to mapping objects, including empty mappings. Supported
generators honor generator-level selectors and generator-level template
overrides. Selectors match flattened parameter keys for nested Go-template
maps.

Matrix generators support exactly two child generators. The second child is
interpolated from first-child params, including list `elementsYaml` values.
Merge generators support two or more child generators and deterministic
merge-key overlays in base generator order. Supported matrix/merge children
are list, Git directories, Git files, and nested matrix/merge combinations
where the Argo CD v3 nested JSON API permits them. Provider-backed children,
including cluster, clusterDecisionResource, SCM provider, pull-request, and
plugin generators, remain unsupported diagnostics in this phase.

Git files generator support is intentionally local and fail-closed. Matches are
sorted by normalized relative path. Do not follow symlinks, allow absolute
paths, or allow `..` escapes outside the repository root. Decode only YAML/JSON
mapping documents; arrays, scalars, invalid files, and empty documents must
produce diagnostics. Supported path params are `.path.path`,
`.path.basename`, `.path.basenameNormalized`, `.path.filename`,
`.path.filenameNormalized`, and `.path.segments`, with `pathParamPrefix`
variants matching Argo CD. Non-Go-template params use `path`,
`path.basename`, `path.basenameNormalized`, `path.filename`,
`path.filenameNormalized`, and indexed segment keys such as `path[0]`, again
with prefix variants when `pathParamPrefix` is set. Decoded file values become
template params; Go-template mode preserves nested maps and non-Go-template
mode flattens nested keys. Git files `values` use the same `values.*` and
`.values.*` behavior as Git directories. Git files `exclude: true` excludes a
file even when another pattern includes it.

## Supported Features

The MVP currently supports:

- Direct `Application` CR discovery.
- Git-directory, Git-files, list, matrix, and merge `ApplicationSet` CR
  expansion, including list `elementsYaml`, generator selectors, generator
  template overrides, matrix interpolation, deterministic merge-key overlays,
  and supported nested matrix/merge combinations.
- Single-source and multi-source planning for supported source types.
- Kustomize, directory, local Helm chart, Kustomize `helmCharts`, remote
  Kustomize HTTP(S) files and Git refs, and chart-only remote Helm source
  rendering through Go libraries.
- Deterministic `--repo-map` and gated `--allow-network` Git clone/fetch for
  path-based Git sources.
- Explicit Git HTTPS bearer/basic auth, Git SSH key-file auth, HTTP(S) Helm
  bearer/basic auth, HTTP(S) remote Kustomize resource bearer/basic auth, and
  explicit OCI Helm registry config path plumbing.
- Repeated-resource last-wins behavior inside one Application, with a
  diagnostic.
- Parent Application-aware desired manifest identity for diffs.
- Conservative container image extraction.
- Structured `get apps` and `get images` output with table, name, JSON, and
  YAML formats.
- Per-Application `test apps` and `test app` PASS/FAIL/SKIPPED status output
  with text, JSON, and YAML formats.
- Public Go API for listing, rendering, manifest diffs, image diffs, and
  injectable Git/chart/remote-resource acquisition plus injectable config
  management plugin rendering.
- Config management plugin source detection with fail-closed diagnostics in
  the CLI/default path when no plugin renderer is injected.
- Structured `diff apps` and `diff app` output with diff, JSON, and YAML
  formats, plus metadata label/annotation stripping through `--strip-attr`.
- Application-level `spec.ignoreDifferences[]` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers` for rendered manifest diffs.
- Global `resource.customizations.ignoreDifferences.*` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers` from discovered `argocd-cm`
  and Argo CD Helm values `configs.cm`.
- Global `resource.customizations.knownTypeFields.*` normalization for
  desired-vs-desired manifest diffs.
- Global `resource.customizations.ignoreResourceUpdates.*` parsing and
  diagnostics; these settings are not applied to desired-vs-desired diffs.
- Health and action customization parsing and diagnostics, including
  `useOpenLibs`/Lua metadata. Lua is not executed offline.
- Discovered `resource.compareoptions.ignoreResourceStatusField` and
  `resource.compareoptions.ignoreAggregatedRoles`.
- Argo CD core resource exclusions plus discovered global
  `resource.exclusions` and `resource.inclusions`.
- Explicit rendered-resource filters through `--skip-kind`, `--skip-crds`, and
  `--skip-secrets` for build output, diffs, image extraction, tests, and the
  public Go API.
- Argo CD settings discovery from Helm values, `argocd-cm`, and repository
  Secrets, limited to rendering/diff-affecting non-secret values.
- Discovered `AppProject` manifests with offline diagnostics for Application
  project references, source repositories, destinations, source namespaces,
  RBAC role metadata, deferred project-scoped cluster metadata, and repository
  Secret metadata matching without reading credential fields.

## Deferred Features

Do not treat these as supported without an explicit design update:

- Live-cluster diffing or live Argo CD API calls.
- Kubernetes API defaulting or admission mutation.
- Live server-side apply field ownership prediction and live Argo CD
  server-side diff behavior.
- Managed fields ignores when ownership data exists only on the live cluster.
- Health or action Lua execution.
- Live destination cluster existence checks.
- Sync window enforcement.
- Source integrity signature verification.
- Project-scoped cluster Secret enforcement.
- Full Argo CD RBAC/Casbin authorization simulation.
- CLI config management plugin execution, shellout plugin adapters, Argo CD
  repo-server sidecar plugin discovery, ambient plugin configuration, ambient
  plugin environment loading, and plugin credential injection.
- Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
  ApplicationSet generators.
- Required default shellouts to `helm`, `kustomize`, `kubectl`, or `argocd`.
- A first-class cache inspection command or structured cache event stream for
  Git, Helm, and remote Kustomize acquisition.

## Source Resolution

Repository URL maps are deterministic and preferred over network fetches.
Normalize URLs consistently, including optional `.git` suffixes, trailing
slashes, and whitespace. Path source resolution order is: explicit
`--repo-map`, existing source path under `--path`, gated `--allow-network`
Git clone/fetch, then clear failure. `--allow-network` controls only Git
repository-source fetching and must not control Helm chart acquisition.
Git repositories cache under the user cache or `--git-cache-dir`, never inside
the current or baseline Git repository tree. `--refresh-git` fetches existing
cached Git repositories before rendering. `--offline` cannot be combined with
`--allow-network`.
Chart acquisition is shared by Kustomize `helmCharts` and Argo CD chart-only
sources. Public chart fetching is allowed by default for render/diff commands;
`--offline` disables chart and remote Kustomize resource network fetches. Cache
charts under the user cache or `--chart-cache-dir`, never inside the Git
repository tree.
Chart network behavior is controlled by `--offline`, `--refresh-charts`, and
`--chart-cache-dir`. Do not reuse `--allow-network` for Helm chart fetching;
that flag is reserved for Git repository-source fetching.
Remote Kustomize resource network behavior is controlled by `--offline`,
`--refresh-remotes`, and `--remote-cache-dir`. Cache remote Kustomize resources
under the user cache or `--remote-cache-dir`, never inside the Git repository
tree.
OCI chart acquisition must use Helm registry Go libraries, not helm pull.

Authenticated source handling is explicit and non-interactive. Do not prompt
for credentials, read ambient Git credential helpers, or read ambient Helm
registry config in this slice. Git HTTPS auth supports bearer token and basic
auth; bearer token wins over username/password. Git SSH auth supports
`ssh://git@host/org/repo.git`, `git@host:org/repo.git`, and
`ssh://host/org/repo.git`; omitted SSH usernames default to `git`. SSH auth
requires `--git-ssh-key-file` and `--git-known-hosts-file`; missing key files,
missing known-hosts files, and passphrase failures must fail before network
access with non-secret diagnostics. HTTP(S) Helm auth and HTTP(S) remote
Kustomize resource auth support bearer token and basic auth; bearer token wins
over username/password. Kustomize Git remote refs reuse the explicit `--git-*`
credentials. OCI Helm auth is provided only through an explicit
`--registry-config` path. Do not consume secret data from discovered Argo CD
repository Secrets until a later design update says so. Never print password,
bearer token, SSH private key, SSH passphrase, remote resource credential, or
registry credential values.

## Application Planning

Application planning follows Argo CD precedence: `spec.sources` wins over
`spec.source`. Validate refs before rendering. Ref-only sources are valid and
produce no manifests. A source may not combine `ref` and `chart`. Destination
namespace normalization only fills namespace-scoped objects; until discovery
mapping exists, keep the built-in cluster-scoped GVK predicate current.
Within one Application, repeated rendered resource identity is last-wins and
must emit a diagnostic. Do not dedupe across Applications; cross-Application
shared-resource behavior belongs to live Argo CD semantics and is out of scope
for offline MVP.

Diff output is keyed by parent Application plus child resource identity.
Same-named resources rendered by different Applications must remain separate.
Named `diff app` compares the requested Application directly in both trees and
does not use changed-only Git path filtering. If the Application exists only in
current, show additions; if it exists only in baseline, show deletions; if it is
absent from both, error.
Manifest diff output supports unified diff, JSON, and YAML formats. Keep
diagnostics on stderr for structured diff output. Do not support `-o name` for
`diff apps` or `diff app`; that format is for list-style commands and image
projections. `--strip-attr KEY` removes matching metadata label and annotation
keys before manifest body comparison and diff generation.
`--skip-kind KIND`, `--skip-crds`, and `--skip-secrets` drop rendered resources
before build output, diff comparison, and image extraction. These filters are
explicit opt-ins; do not change defaults to hide Secrets or CRDs.
Application `spec.ignoreDifferences[]` rules are honored with Argo CD glob
matching for group/kind and exact optional name/namespace matches.
When both sides contain a matching resource, apply the union of left and right
Application-local and global `jsonPointers`, `jqPathExpressions`, and
`managedFieldsManagers` to both sides before comparison. JQ expressions run as
`del(<expression>)` and fail closed on compile/runtime errors.
`managedFieldsManagers` is an offline desired-vs-desired approximation using
rendered `metadata.managedFields`; do not claim live server-side ownership
prediction. `resource.compareoptions` supports status-field and aggregated-role
normalization. `knownTypeFields` normalization is applied to
desired-vs-desired diffs. `ignoreResourceUpdates`, health customizations,
action customizations, and `useOpenLibs`/Lua metadata are parsed and reported
as settings diagnostics only; do not execute Lua or apply
`ignoreResourceUpdates` as a desired diff ignore.
Image extraction is conservative in the MVP and may be broadened only behind an
explicit mode.
CLI diff exit codes are fixed: 0 means success/no diff, 1 means success/diff
found, 2 means runtime/config/render error. Keep command errors quiet enough for
CI and avoid Cobra usage spam on runtime failures.

The orchestrator owns end-to-end flow. Keep it thin: discovery, ApplicationSet
expansion, planning, rendering, and formatting should stay in their packages. If
orchestration grows complicated, split behavior into narrower package functions
rather than accumulating logic in one file.
Build results preserve partial manifests, diagnostics, and per-Application
statuses when one selected Application fails. CLI commands must keep stdout
machine-parseable; diagnostics and failure summaries belong on stderr unless
the command's primary output is explicitly status text.

## Renderers

Renderers implement `internal/render.Renderer`. The default implementation path
must not shell out. Directory rendering parses YAML/JSON files and flattens
Kubernetes `List` objects. Keep directory rendering contained to the resolved
repository root: reject escaping source paths and symlinked source path
components, and skip symlinked files or directories while walking.
Kustomize rendering uses Go libraries. Preserve the no-shellout path. Build
options from Argo settings must be parsed and applied explicitly; do not pass
opaque command-line strings to a shell. Until that parsing exists, nonempty
Kustomize build options must fail explicitly. Before invoking Kustomize,
prevalidate local Kustomization graph references and reject unsupported remote
refs, absolute paths, repo-root escapes, and symlinked graph entries.
Kustomize graph references may point elsewhere inside the same repository, such
as shared components/, but must not escape the repository root or traverse
symlinked graph entries. Treat Kustomize path-bearing fields fail-closed:
validate new fields before render rather than assuming Kustomize's loader
restrictions are enough. Kustomize helmCharts must be rendered through
argocd-local's chart
acquisition and Helm Go renderer into a temporary workspace. Do not enable
Kustomize's Helm shellout plugin or write generated charts into the Git tree.
Remote Kustomize HTTP(S) file refs and Git refs are fetched through
argocd-local's remote resource cache and rewritten into the temporary
Kustomize workspace. Supported remote fields include `resources`, `bases`,
`components`, `patches.path`, `patchesJson6902.path`, non-inline
`patchesStrategicMerge`, `generators`, `transformers`, `validators`,
`configurations`, `crds`, `openapi.path`, `replacements.path`, and
ConfigMap/Secret generator `files`, `envs`, and `env` entries. Remote
Kustomize HTTP(S) credentials are explicit flags only and must be redacted in
errors. Kustomize Git refs reuse explicit Git credentials but follow remote
Kustomize cache/offline/refresh semantics, not repository-source
`--allow-network` semantics.
Helm rendering must use Go libraries by default. Preserve these Argo CD
semantics in the MVP: release name defaults to Application name, destination
namespace is passed to Helm, and `valuesObject` overrides `values`.

## Repository Layout

- `cmd/argocd-local/`: binary entrypoint
- `internal/cli/`: Cobra command tree and exit-code mapping
- `internal/config/`: Argo settings discovery and merge model
- `internal/discovery/`: repository scanning for Applications, ApplicationSets, and settings
- `internal/appset/`: supported local ApplicationSet generators
- `internal/source/`: repository URL normalization, repo maps, and network opt-in
- `internal/render/`: renderer interfaces and source renderers
- `internal/app/`: Application source planning and multi-source combine behavior
- `internal/change/`: changed-file detection and Application input indexing
- `internal/diff/`: parent-aware desired-vs-desired diffs and image diffs
- `internal/format/`: CLI table, name, JSON, and YAML output helpers
- `internal/diagnostic/`: structured warnings/errors with provenance and strict-mode promotion
- `internal/manifest/`: YAML/JSON document loading, List flattening, and resource identity helpers
- `testdata/`: minimal fixtures only

## Validation

Portable integration fixtures should model `home-ops` behavior without depending
on `/Users/ethan.shold/git/home-ops`. Real `home-ops` checks belong in optional
smoke scripts that use temporary worktrees.
Optional real-repository smokes must use temporary worktrees and clean them up.
Never mutate `/Users/ethan.shold/git/home-ops` directly from tests.
`docs/home-ops-pattern-coverage.md` is the source of truth for real
`home-ops` pattern coverage. Normal tests must use portable fixtures; optional
smoke scripts may target the real checkout through temporary worktrees only.
Portable fixtures cover HTTP(S) and Git remote Kustomize resources, including
remote bases, components, authenticated HTTP resource credentials, and remote
patch files. The real `home-ops` `apps/system-upgrade` remote-resource pattern
is covered and supported.

Run the smallest check that covers your change:

```bash
go test ./...
go vet ./...
golangci-lint run
markdownlint-cli2 '**/*.md'
```

If a tool is not installed locally, say so in your final response.

## Hard Constraints

- Default workflows must not require `helm`, `kustomize`, `kubectl`, or
  `argocd` on `PATH`.
- The render/diff path must stay inside the compiled Go executable and its
  libraries. Do not require a live cluster, sidecar service, Argo CD server, or
  host-installed renderer to produce PR diffs.
- Public Helm chart fetching for Kustomize `helmCharts` and chart-only sources
  is enabled by default for render/diff. Git repository-source fetching is
  gated by `--allow-network` and must not be controlled by Helm chart flags.
- Do not print secret data. Repository Secrets may provide non-sensitive
  metadata only.
- Manifest loaders must never print Secret values. Diagnostics may include file
  paths and YAML pointers, not manifest data.
- `spec.sources` takes precedence over `spec.source`.
- Changed-only mode must not use Flux-style "most-specific owner wins"; Argo CD
  may have overlapping Applications.
- Changed-only mode keeps every Application whose inputs intersect a changed
  file. Do not implement Flux-style longest-prefix ownership. If any changed
  path is unowned, default behavior is render-all with diagnostics; strict mode
  can fail.
- Server-side diff/apply settings are diagnostics in offline mode, not
  executable behavior.

## Exit Codes

- `0`: command succeeded and, for diff-style commands, no diff was found.
- `1`: command succeeded and a diff was found when `--exit-code` is enabled.
- `2`: tool, configuration, discovery, or render error.

`--exit-code=false` makes diffs exit `0` for local inspection. Warnings do not
change exit code unless strict mode promotes them to errors. Cobra usage output
should stay suppressed for runtime failures.

## Common Mistakes

- Do not add a shellout path for default rendering when a Go library path is
  available.
- Do not enable network access implicitly for unmapped Git/repository sources;
  keep them local-only until repository fetching is explicitly wired.
- Do not use `--allow-network` as the Helm chart-fetch flag; chart fetching is
  controlled by `--offline`, `--refresh-charts`, and `--chart-cache-dir`.
- Do not put chart or remote Kustomize resource caches inside Git repository
  trees.
- Do not print Secret manifest values or repository credentials in diagnostics.
- Do not hard-code one user's repository layout or `home-ops` paths.
- Do not collapse overlapping Applications to one owner in changed-only mode.
- Do not dedupe repeated resources across Applications; only last-wins inside
  one Application is part of the offline model.
- Do not execute server-side diff/apply settings offline; report them as
  limitations.
- Do not add supported features, commands, renderers, providers, diagnostics, or
  validation commands without updating this file.

## Agent Maintenance Rule

When you add, remove, or materially change a package, command, renderer,
setting provider, diagnostic, validation command, or supported Argo CD feature,
update this file in the same change.
