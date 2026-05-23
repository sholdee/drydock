# AGENTS.md - argocd-local

## What This Is

`argocd-local` is an independent Go CLI for offline Argo CD GitOps repository
analysis. The first product goal is desired-vs-desired PR diffing: render Argo
CD Applications from a current tree and a baseline tree, then show what desired
Kubernetes manifests changed.

It is not a live-cluster diff tool in the MVP. Do not add Kubernetes API
dependencies, live Argo CD server calls, or shellout-based render paths unless
the design spec is updated first.

## Current Design

Read the design spec before substantive changes:

- `docs/superpowers/specs/2026-05-22-argocd-local-design.md`

## Command Surface

Current top-level commands:

- `argocd-local get apps --path .`: list discovered Applications by name.
- `argocd-local build apps --path .`: render all discovered Applications.
- `argocd-local build app NAME --path .`: render exactly one discovered
  Application by `metadata.name`.
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

Current shared flags are `--path`, `--path-orig`, `--repo-map`,
`--allow-network`, `--git-cache-dir`, `--refresh-git`, `--offline`,
`--refresh-charts`, `--chart-cache-dir`, `--refresh-remotes`,
`--remote-cache-dir`, `--changed-only`, `--strict-changed-only`, `--strict`,
`--exit-code`, `--output`/`-o`, `--unified`/`-u`, and `--limit-bytes`.

## Settings Discovery

Settings flow into `internal/config.ArgoSettings`. Providers must record
provenance and must fail closed on conflicting discovered values. Repository
Secrets may contribute non-sensitive fields (`url`, `type`, `name`, `project`,
`enableOCI`) but must not retain username, password, bearer tokens, SSH keys,
or TLS material.

## Discovery

Discovery scans YAML files for Argo CD entrypoints. Keep scanning generic; do
not hard-code `home-ops` paths. Use `--app-manifests` style narrowing when a
caller wants explicit paths. Classify candidates by full GVK, skip symlinks in
default scans, reject symlink components in explicit app manifest paths, and
keep default scans tolerant of unrelated YAML files.

## ApplicationSet Support

`internal/appset` supports local Git directories, Git files, and list
generators with Go templates. Use path-style matching, keep include/exclude
semantics deterministic, and preserve Argo CD template behavior such as
`missingkey=error` and Sprig functions. Multiple supported top-level
generators are evaluated independently and concatenated in manifest order.
Unsupported generators must produce diagnostics.

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
- Git-directory, Git-files, and list `ApplicationSet` CR expansion.
- Single-source and multi-source planning for supported source types.
- Kustomize, directory, local Helm chart, Kustomize `helmCharts`, safe
  single-file HTTP(S) Kustomize `resources:`, and chart-only remote Helm source
  rendering through Go libraries.
- Deterministic `--repo-map` and gated `--allow-network` Git clone/fetch for
  path-based Git sources.
- Explicit Git HTTPS bearer/basic auth, Git SSH key-file auth, HTTP(S) Helm
  bearer/basic auth, and explicit OCI Helm registry config path plumbing.
- Repeated-resource last-wins behavior inside one Application, with a
  diagnostic.
- Parent Application-aware desired manifest identity for diffs.
- Conservative container image extraction.
- Argo CD settings discovery from Helm values, `argocd-cm`, and repository
  Secrets, limited to rendering/diff-affecting non-secret values.

## Deferred Features

Do not treat these as supported without an explicit design update:

- Live-cluster diffing or live Argo CD API calls.
- Kubernetes API defaulting or admission mutation.
- Server-side apply field ownership, managed fields ignores, and live Argo CD
  server-side diff behavior.
- Project, RBAC, and destination validation.
- Config management plugins.
- Cluster, SCM provider, pull-request, plugin, matrix, and merge
  ApplicationSet generators.
- Required default shellouts to `helm`, `kustomize`, `kubectl`, or `argocd`.
- Remote Kustomize bases, components, patches, generators, transformers,
  validators, `crds`, `openapi`, replacements, authenticated remote resources,
  and arbitrary Kustomize Git refs.

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
access with non-secret diagnostics. HTTP(S) Helm auth supports bearer token and
basic auth; bearer token wins over username/password. OCI Helm auth is provided
only through an explicit `--registry-config` path. Do not consume secret data
from discovered Argo CD repository Secrets until a later design update says so.
Never print password, bearer token, SSH private key, SSH passphrase, or
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
Image extraction is conservative in the MVP and may be broadened only behind an
explicit mode.
CLI diff exit codes are fixed: 0 means success/no diff, 1 means success/diff
found, 2 means runtime/config/render error. Keep command errors quiet enough for
CI and avoid Cobra usage spam on runtime failures.

The orchestrator owns end-to-end flow. Keep it thin: discovery, ApplicationSet
expansion, planning, rendering, and formatting should stay in their packages. If
orchestration grows complicated, split behavior into narrower package functions
rather than accumulating logic in one file.

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
Single-file HTTP(S) Kustomize `resources:` entries are fetched through
argocd-local's remote resource cache and rewritten into the temporary
Kustomize workspace. Remote Kustomize bases, components, patches, generators,
transformers, validators, `crds`, `openapi`, replacements, authenticated remote
resources, and Git-style refs remain unsupported.
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
Portable fixtures cover safe single-file HTTP(S) Kustomize resources. The real
`home-ops` `apps/system-upgrade` remote-resource pattern is covered and
supported in that narrow form.

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
