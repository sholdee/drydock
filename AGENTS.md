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

## Source Resolution

Repository URL maps are deterministic and preferred over network fetches.
Normalize URLs consistently, including optional `.git` suffixes, trailing
slashes, and whitespace. Unmapped repositories must error unless network access
was explicitly enabled by the caller.

## Application Planning

Application planning follows Argo CD precedence: `spec.sources` wins over
`spec.source`. Validate refs before rendering. Ref-only sources are valid and
produce no manifests. A source may not combine `ref` and `chart`. Destination
namespace normalization only fills namespace-scoped objects; until discovery
mapping exists, keep the built-in cluster-scoped GVK predicate current.

## Renderers

Renderers implement `internal/render.Renderer`. The default implementation path
must not shell out. Directory rendering parses YAML/JSON files and flattens
Kubernetes `List` objects. Keep directory rendering contained to the resolved
repository root: reject escaping source paths and symlinked source path
components, and skip symlinked files or directories while walking.

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

Run the smallest check that covers your change:

```bash
go test ./...
go vet ./...
golangci-lint run
markdownlint-cli2 '**/*.md'
```

If a tool is not installed locally, say so in your final response.

## Hard Constraints

- Default workflows must not require `helm`, `kustomize`, `kubectl`, or `argocd` on `PATH`.
- Network access is opt-in through `--allow-network`; prefer `--repo-map`.
- Do not print secret data. Repository Secrets may provide non-sensitive metadata only.
- Manifest loaders must never print Secret values. Diagnostics may include file paths and YAML pointers, not manifest data.
- `spec.sources` takes precedence over `spec.source`.
- Changed-only mode must not use Flux-style "most-specific owner wins"; Argo CD may have overlapping Applications.
- Server-side diff/apply settings are diagnostics in offline mode, not executable behavior.

## Agent Maintenance Rule

When you add, remove, or materially change a package, command, renderer,
setting provider, diagnostic, validation command, or supported Argo CD feature,
update this file in the same change.
