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
- `argocd-local build app NAME --path .`: command is present but not wired yet.
- `argocd-local diff apps --path . --path-orig ../base`: render and diff all
  Applications between a baseline tree and current tree.
- `argocd-local diff app NAME --path . --path-orig ../base`: command is
  present but not wired yet.
- `argocd-local diff images --path . --path-orig ../base`: render both trees
  and compare conservative workload container images.
- `argocd-local diag --path .`: command is present but not wired yet.
- `argocd-local version`: print version, commit, Go version, and Argo CD module.

Current shared flags are `--path`, `--path-orig`, `--repo-map`,
`--allow-network`, `--offline`, `--refresh-charts`, `--chart-cache-dir`,
`--changed-only`, `--strict-changed-only`, `--strict`, `--exit-code`,
`--output`/`-o`, `--unified`/`-u`, and `--limit-bytes`.
Some flags are parsed ahead of wiring: `--repo-map` and `--allow-network` do
not currently drive the E2E build/diff path, and `diff app` and `diag` are not
wired yet.

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

## ApplicationSet MVP

`internal/appset` supports one Git directories generator with Go templates. Use
path-style matching, keep include/exclude semantics deterministic, and preserve
Argo CD template behavior such as `missingkey=error` and Sprig functions.
Unsupported generators must produce diagnostics.

## Supported Features

The MVP currently supports:

- Direct `Application` CR discovery.
- Git-directory `ApplicationSet` CR expansion.
- Single-source and multi-source planning for supported source types.
- Kustomize, directory, local Helm chart, Kustomize `helmCharts`, and
  chart-only remote Helm source rendering through Go libraries.
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
- Additional ApplicationSet generators beyond Git directories.
- Required default shellouts to `helm`, `kustomize`, `kubectl`, or `argocd`.
- Authenticated/private Helm chart repositories.
- Remote Kustomize refs and Git/repository-source fetching.

## Source Resolution

Repository URL maps are deterministic and preferred over network fetches.
Normalize URLs consistently, including optional `.git` suffixes, trailing
slashes, and whitespace. Git/repository-source network fetching remains
deferred; unmapped non-chart repositories must fail unless a local repository
source is available through a wired path. `--allow-network` is reserved for
future Git/repository-source fetching and must not control Helm chart
acquisition.
Chart acquisition is shared by Kustomize `helmCharts` and Argo CD chart-only
sources. Public chart fetching is allowed by default for render/diff commands;
`--offline` disables chart network fetches. Cache charts under the user cache or
`--chart-cache-dir`, never inside the Git repository tree.
Chart network behavior is controlled by `--offline`, `--refresh-charts`, and
`--chart-cache-dir`. Do not reuse `--allow-network` for Helm chart fetching;
that flag is reserved for future Git/repository-source fetching.
OCI chart acquisition must use Helm registry Go libraries, not helm pull.
Authenticated/private chart repositories remain unsupported and must fail with
a clear message instead of prompting or reading credentials.

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
prevalidate local Kustomization graph references and reject remote refs,
absolute paths, repo-root escapes, and symlinked graph entries. Kustomize graph
references may point elsewhere inside the same repository, such as shared
components/, but must not escape the repository root or traverse symlinked
graph entries. Treat Kustomize path-bearing fields fail-closed: validate new
fields before render rather than assuming Kustomize's loader restrictions are
enough. Kustomize helmCharts must be rendered through argocd-local's chart
acquisition and Helm Go renderer into a temporary workspace. Do not enable
Kustomize's Helm shellout plugin or write generated charts into the Git tree.
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
  is enabled by default for render/diff. Git/repository-source network fetching
  remains deferred and reserved for future `--allow-network` behavior.
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
