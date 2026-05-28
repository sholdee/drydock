# Agent Reference

This file contains task-specific drydock agent guidance. Read `AGENTS.md`
first; load this file only for the sections relevant to the work in front of
you.

## Document Ownership

Use `docs/README.md` for documentation ownership. This file should not become
a duplicate design spec; keep concise task constraints plus links to canonical
docs.

## Command And API Surface

Canonical references:

- `internal/cli` for command behavior.
- `internal/requestopts` for shared option parsing.
- `pkg/drydock` for the public embedding API.
- `docs/design.md` for architecture and behavior contracts.
- `docs/plugin-policy.md` for trusted plugin policy provenance, schema, and
  exec security controls.

Current command groups are:

- `get apps`, `get images`
- `build apps`, `build app`
- `test apps`, `test app`
- `diff apps`, `diff app`, `diff images`
- `diag`
- `cache path`, `cache list`, `cache prune`, `cache delete`
- `version`

Shared flags cover repository paths, selectors, repo maps, explicit Git/chart
and remote-resource acquisition controls, output formats, diff filters,
ApplicationSet fixtures, cache events, and render parallelism. Diff commands
also accept `--repo`, `--ref`, and `--ref-orig` for local Git ref diffs. Keep
detailed flag behavior in CLI code or generated/user-facing docs instead of
duplicating full inventories here.

Public API rules:

- `pkg/drydock` must not expose `internal/...` package types.
- Package-level functions follow CLI default network/cache behavior.
- `NewClient` accepts public Git, chart, remote-resource, and plugin renderer
  injection points for deterministic embedding.
- Preserve partial render results: callers must receive successful manifests,
  diagnostics, and per-Application statuses even when one selected Application
  fails.
- Stable diagnostic `Code` values are part of the CLI JSON/YAML and public API
  contract.

Config management plugin command execution fails closed by default. CLI/default
paths do not execute plugin commands unless trusted drydock plugin policy
provenance matches `engine: exec` and the caller explicitly sets
`--enable-plugins`. Discovered Argo CD CMP definitions that normalize to a safe
`kustomize build` command may be interpreted through drydock's native
Kustomize renderer without shelling out. Other native engines must remain
narrow, in-process compatibility paths with fail-closed validators. Keep
detailed policy behavior in `docs/plugin-policy.md`. Public API plugin
rendering is allowed only through explicit in-process `Config.PluginRenderer`
or registry injection. Preserve `plugin.unsupported`, `plugin.failed`, and
`plugin.unspecified` diagnostics, and do not reclassify caller cancellation as
plugin timeout.

## Settings And Project Discovery

Canonical references:

- `internal/config`
- `internal/project`
- `internal/discovery`
- `docs/design.md`

Settings flow into `internal/config.ArgoSettings`. Providers must record
provenance and fail closed on conflicting discovered values.

Repository Secrets may contribute non-sensitive metadata such as `url`, `type`,
`name`, `project`, and `enableOCI`. They must not retain username, password,
bearer tokens, SSH keys, TLS material, or other credential fields.

Local `AppProject` manifests may produce offline diagnostics for Application
project references, source repositories, destinations, source namespaces,
repository Secret metadata matches, RBAC role metadata, and deferred
project-scoped cluster metadata. Do not simulate live cluster existence, full
Argo CD RBAC/Casbin authorization, or project-scoped cluster Secret
enforcement offline.

## Discovery And ApplicationSet Support

Canonical references:

- `internal/discovery`
- `internal/appset`
- `docs/design.md`

Discovery scans YAML files by GVK. Keep scans generic; do not hard-code a
specific user's repository layout. Default scans skip symlinks and tolerate
unrelated YAML. Explicit app manifest paths reject symlinked components.

Supported ApplicationSet generators are local and deterministic. The supported
set includes Git directories, Git files, list, matrix, merge, and explicit
fixture-backed provider generators. Preserve Go-template behavior such as
`missingkey=error`, Sprig functions, generator selectors, generator template
overrides, supported nested matrix/merge combinations, and deterministic
merge-key overlays.

Provider-backed ApplicationSet generators are offline fixture-backed only. Do
not add Kubernetes API reads, Argo CD API reads, SCM/pull-request/cloud API
calls, plugin service calls, shellouts, or ambient credential discovery.

Git files generator behavior is intentionally local and fail-closed. Keep path
matching sorted, reject absolute paths and `..` escapes, do not follow
symlinks, and decode only YAML/JSON mapping documents.

## Source Resolution, Cache, And Credentials

Canonical references:

- `internal/source`
- `internal/chart`
- `internal/remote`
- `internal/acquisition`
- `internal/cache`
- `internal/cacheevent`
- `docs/design.md`

Repository URL maps are deterministic and preferred over network fetches. Path
source resolution order is explicit repo map, existing local source path,
declared Git cache/fetch behavior, then clear failure.

`--offline` controls Git, Helm chart, and remote Kustomize network behavior.
When it is set, source resolution must use local files, repo maps, or existing
cache entries.

Caches live under user cache roots or explicit cache directories, never inside
the current or baseline repository tree. Cache lifecycle commands are local
filesystem operations only; they must not render, fetch, clone, or read
credential flags.

Authenticated source handling is explicit and non-interactive:

- Do not prompt for credentials.
- Do not read ambient Git credential helpers.
- Do not read ambient Helm registry config.
- Git HTTPS auth supports bearer and basic auth; bearer wins.
- Git SSH auth requires explicit key and known-hosts files.
- HTTP(S) Helm and remote Kustomize auth support bearer and basic auth; bearer
  wins.
- OCI Helm auth is provided only through explicit registry config.

Never print password, bearer token, SSH private key, SSH passphrase, remote
resource credential, registry credential, or credential-bearing URL values.

## Application Planning And Diff Semantics

Canonical references:

- `internal/app`
- `internal/diff`
- `internal/manifest`
- `docs/design.md`

Application planning follows Argo CD precedence: `spec.sources` wins over
`spec.source`. Ref-only sources are valid and produce no manifests. A source
may not combine `ref` and `chart`.

Within one Application, repeated rendered resource identity is last-wins and
must emit a diagnostic. Do not dedupe across Applications; cross-Application
shared-resource behavior belongs to live Argo CD semantics and is out of
scope for offline desired-state analysis.

Diff identity is parent Application plus child resource identity. Same-named
resources from different Applications remain separate. Named app arguments may
use `NAME` or `NAMESPACE/NAME`.

Git ref diffs use temporary local snapshots before entering the normal
path-based diff pipeline. `--ref-orig` replaces `--path-orig`, `--ref`
replaces `--path`, and `--repo` defaults to `--path`. Do not add shellouts,
`git worktree add`, checkout mutation, or top-level remote `--repo` URL
support without an approved design update.

Manifest diff output supports unified diff, JSON, and YAML. Image diff output
also supports unified diff, JSON, and YAML for `added`, `removed`, and
`unchanged` image lists. Diagnostics stay on stderr for structured diff
output. `-o name` is for list-style commands such as `get apps` and
`get images`, not `diff apps`, `diff app`, or `diff images`.

Application-local and global ignore rules support `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers`. Apply the union from both
sides when both sides contain a matching resource. Managed fields support is an
offline desired-vs-desired approximation using rendered manifests; do not
claim live server-side ownership prediction.

Resource filters such as `--skip-kind`, `--skip-crds`, and `--skip-secrets`
are explicit opt-ins. Do not change defaults to hide Secrets or CRDs.

CLI diff exit codes are fixed:

- `0`: success, no diff
- `1`: success, diff found
- `2`: runtime, config, or render error

## Renderer Semantics

Canonical references:

- `internal/render`
- `internal/chart`
- `internal/pathsafety`
- `docs/design.md`

Renderers implement `internal/render.Renderer`. The default implementation
path must not shell out.

Directory rendering parses YAML/JSON files, flattens Kubernetes `List`
objects, stays within the resolved repository root, rejects escaping source
paths and symlinked source path components, and skips symlinked files or
directories while walking.

Kustomize rendering uses Go libraries. Do not enable Kustomize's Helm shellout
plugin. Validate local graph references before rendering and reject unsupported
remote refs, absolute paths, repo-root escapes, and symlinked graph entries.

Kustomize `helmCharts` and Argo CD chart-only sources use drydock chart
acquisition and Helm Go rendering. Remote Kustomize HTTP(S) file refs and Git
refs use drydock's remote resource cache and explicit credentials.

Helm rendering must use Go libraries by default. Preserve Argo CD semantics
such as release name defaulting to Application name, passing destination
namespace to Helm, and `valuesObject` overriding `values`.

## Validation And Real-Repository Smokes

Canonical references:

- `testdata/`

Portable integration fixtures should model real repository behavior without
depending on a maintainer-provided `home-ops` checkout. Real `home-ops` checks
belong in optional smoke scripts that use temporary worktrees and clean them
up.

Normal tests must use portable fixtures. Optional smokes may target the real
checkout through temporary worktrees only. Never mutate the real `home-ops`
checkout from tests.

Git ref diff implementation and unit tests should use go-git fixtures and temp
directories. Do not use `git worktree add` inside implementation or normal
tests; reserve it for optional smoke scripts that create and clean up temporary
worktrees.

Use the smallest verification that covers the change. If a useful command is
unavailable or approval-gated, skip it and report the gap rather than blocking
the work.

### Semantic Remediation Checks

Use the semantic remediation fixtures in `testdata/semantic-remediation` as
pending or active targets for Argo CD parity work. Keep normal checks portable
and offline:

```bash
go test ./internal/fixtures/semantic
go test ./internal/app ./internal/render -run 'ExplicitSource|ArgocdSource|SourceKustomize'
go test ./internal/render ./internal/app -run 'Helm.*(Value|Parameter|FileParameter|Schema|Glob)|Directory|Jsonnet'
go test ./internal/appset ./internal/app -run 'Git|List|Values|TemplatePatch|ClusterDecision'
go test ./internal/discovery ./internal/config ./internal/project ./internal/cli -run 'ClusterSecret|CmdParams|Settings|Diag'
```

Do not run optional real-repository smokes from normal tests. Use isolated
temporary worktrees and caches when a phase explicitly calls for a manual
smoke.
