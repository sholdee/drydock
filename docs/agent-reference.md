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
- `site/content/docs/plugin-policy/` for trusted plugin policy provenance,
  schema, bootstrap discovery, and command security controls.

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
- Public plugin requests expose only public value types. They include the
  selected source metadata, `$ref` roots and source metadata, kube version, and
  API versions so embedders can render deterministic in-process plugins without
  reaching into `internal/...` packages.

Config management plugin command execution fails closed by default. CLI/default
paths do not execute plugin commands unless trusted drydock plugin policy
provenance matches `engine: exec` or `engine: container` and the caller
explicitly sets `--enable-plugins`. Discovered Argo CD CMP definitions that
normalize to a safe `kustomize build` command may be interpreted through
drydock's native Kustomize renderer without shelling out. Native compat paths
include `avp-compat` (AVP placeholder redaction; default-on for AVP-shaped
sources, flag-forced via `--enable-avp-compat` for ordinary ones) and
`ksops-compat` (KSOPS generator placeholder rendering without decryption;
opt-in via `--enable-ksops-compat`, and fail-closed — a KSOPS generator
without the flag fails the source with an actionable error). Neither executes
external commands or contacts secret backends. Other native engines must remain narrow,
in-process compatibility paths with fail-closed validators. Keep detailed
policy behavior in `site/content/docs/plugin-policy/`. Public API plugin
rendering is allowed only through explicit in-process `Config.PluginRenderer`
or registry injection. Preserve `plugin.unsupported`, `plugin.failed`, and
`plugin.unspecified` diagnostics, and do not reclassify caller cancellation as
plugin timeout.

## Validation, Benchmarks, And Profiling

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

## Settings And Project Discovery

Canonical references:

- `internal/config`
- `internal/project`
- `internal/discovery`
- `docs/design.md`

Settings flow into `internal/config.ArgoSettings`. Providers must record
provenance and fail closed on conflicting discovered values.

`argocd-cmd-params-cm` may be parsed only as command-parameter metadata for
runtime-boundary diagnostics. Runtime-only repo-server, controller, and
ApplicationSet-controller settings must not mutate render behavior.

Repository Secrets may contribute non-sensitive metadata such as `url`, `type`,
`name`, `project`, and `enableOCI`. They must not retain username, password,
bearer tokens, SSH keys, TLS material, or other credential fields.

Cluster Secrets may contribute only metadata fields `name`, `server`,
`namespaces`, `clusterResources`, and `project`. Do not decode, retain, print,
or fingerprint cluster credential/config fields.

Local `AppProject` manifests may produce offline diagnostics for Application
project references, source repositories, destinations, source namespaces,
repository Secret metadata matches, cluster Secret metadata matches, RBAC role
metadata, rendered-resource policy, and deferred project-scoped cluster
metadata. Do not simulate live cluster existence, full Argo CD RBAC/Casbin
authorization, sync-window scheduling, orphaned-resource detection, source
signature verification, or destination service account sync impersonation
offline. Implementing any of those runtime AppProject semantics requires a new
explicit design gate.

## Discovery And ApplicationSet Support

Canonical references:

- `internal/discovery`
- `internal/appset`
- `docs/design.md`

Discovery scans YAML files by GVK. Keep scans generic; do not hard-code a
specific user's repository layout. Default scans skip symlinks and tolerate
unrelated YAML. Explicit app manifest paths reject symlinked components.

`--discover-ignore` globs (repository-relative, doublestar semantics shared
with `--changed-only-ignore` via `internal/change`) remove matching files from
top-level repository discovery before any decode, including explicit app
manifest paths. They must not filter ApplicationSet Git generator file
matching, Helm value files, Kustomize inputs, changed-path selection, or
rendered-tier discovery. Fatal discovery decode errors append a
`--discover-ignore` remediation hint at the discovery call sites, not inside
`internal/manifest`.

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
symlinks, and decode only YAML/JSON mapping documents. Empty-content decoding
is an Argo CD parity pin: empty, comment-only, and bare `---`/`...` files
decode to one empty param set, `[]` decodes to zero, and multi-document files
decode only the first document. Do not change it.

Template and `templatePatch` execution errors are appset-scoped warning
diagnostics (`appset.template-render-failed`), and the failing ApplicationSet
contributes zero generated Applications, matching the Argo CD controller's
`ErrorOccurred` condition scoping. Generator evaluation errors stay fatal. The
"template render failed" message substring is the stable-code dispatch key in
`internal/diagnostic`; keep the message and dispatch case in sync.

## Source Resolution, Cache, And Credentials

Canonical references:

- `internal/source`
- `internal/chart`
- `internal/remote`
- `internal/ociartifact`
- `internal/acquisition`
- `internal/cache`
- `internal/cacheevent`
- `docs/design.md`

Repository URL maps are deterministic and preferred over network fetches. Path
source resolution order is explicit repo map, `oci://` classification (a
first-class OCI artifact source — `oci://` URL, no `chart:` — acquires from
the registry and never falls to the local-path, self-repo, or Git branches;
`oci://` + `chart:` keeps the Helm-chart flow as a recorded divergence from
strict Argo CD v3.4.5, and `chart:` + `path:` on one `oci://` source is an
error), existing local source path,
self-repo resolution (on all render surfaces, a source naming a configured
remote of the local checkout at `""`/`HEAD`, a diffed ref name during diffs,
or the repository's default-branch name resolves to the local tree — the
active side tree during diffs; full-commit-SHA revisions still acquire
remotely), declared Git cache/fetch behavior, then clear failure.
Default-branch names are read from remote HEAD symrefs only — symref-or-nothing,
no `init.defaultBranch` or checked-out-HEAD fallback, and the symref target
name is read unresolved (a dangling symref still yields the branch name).
Build/list surfaces populate the self-repo refs in exactly two orchestrator
leaves (`ListApplications` and `Build`), guarded so diff sides carrying
pre-populated refs are never re-detected. Detection opens the Git repository
at the given path without walking up, so `--path <subdir>` runs get no
self-resolution (documented; use the checkout root or `--repo-map`).
Consequent behavior: dirty working-tree edits flow into self-`$repo` values on
renders, and a locally deleted `path` fails path-not-found even when the
remote tip still has it. Diff commands also rewrite `--repo-map` entries
pointing at the diffed checkout to each side's tree and warn once per URL per
provider (`source.self-repo-near-miss`) for fork-shaped URLs that match a
remote's host and repository name but a different owner. The pr-action's
`run.sh` records the pull request's base branch as `refs/remotes/origin/HEAD`
(`ensure_origin_head_symref`, tolerant and never overwriting an existing
symref) so render-test-only configs — which never run fetch-base — still get
default-branch self-resolution.

`--offline` controls Git, Helm chart, OCI artifact, and remote Kustomize
network behavior. When it is set, source resolution must use local files, repo
maps, or existing cache entries. Offline OCI artifact resolution is seam-level:
digests pass through to the digest-keyed image cache, and tags/constraints
resolve from records written on online resolves; misses carry the literal
`offline cache miss` contract string that `cacheevent.ActionForError` keys on.

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
- OCI artifact auth is provided only through the explicit `--oci-*` flags: one
  global username/password and TLS set for every OCI artifact registry in a
  run. Any TLS-implying `--oci-*` flag disables the loopback plain-HTTP
  default. Credentials in `oci://` repository URLs are rejected with a
  redacted error.

Never print password, bearer token, SSH private key, SSH passphrase, remote
resource credential, registry credential, or credential-bearing URL values.

## Persistent Render Cache

Canonical references:

- `internal/rendercache` (store, eviction, engine fingerprint)
- `internal/app/render_cache_persistent.go` (keying, eligibility, store-time
  verification)
- `internal/filedigest`, `internal/gitref`, `internal/digestpath` (the two
  digest schemes and their shared canonicalization)
- `site/content/concepts/source-acquisition.md` (operator-facing behavior)

The cache key is a model of every input the renderers read. The load-bearing
invariants:

- Any new render input (a new kustomize field, Helm option, override file, or
  classification probe) must be added to the digest enumerators, and the
  read-coverage tripwires in `internal/app/render_input_coverage_test.go`
  should gain a fixture exercising it.
- Tool classification for digest paths derives from `selectLocalRenderer`;
  never fork it. The kustomization filename list is shared via
  `render.KustomizationFileNames`.
- Refactors of digest internals must prove digest byte-identity: existing
  digest-sensitive suites pass unmodified, or the change rotates keys and says
  so in the commit body.
- Every eligibility decision fails closed: errors, globs in dirty worktrees,
  symlinks, gitlinks, nested `.git`, duplicate Application keys, and unknown
  roots degrade to re-rendering without persistence, never to serving
  unverified content.
- Stores are re-verified at store time (`renderInputsUnchanged`); committed
  keys verify the worktree against the pinned revision, filesystem keys
  re-digest with the run-scoped memo bypassed. The sabotage-validated tests in
  `internal/app/render_cache_verify_test.go` pin this wiring — never weaken
  them.
- Engine fingerprint module paths live only in `internal/rendercache`
  (`FingerprintFromBuildInfo`); dev builds without VCS stamping or ldflags
  disable persistence by design.

Changes to keying or verification follow a written implementation plan with an
independent review before code and per-change review after; new guard tests
are sabotage-validated (break the guarded property, confirm the test fails,
restore exactly).

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

Changed-only include and ignore globs apply before ownership mapping for
`diff apps` and `diff images`. Patterns are repository-relative and
slash-normalized; ignore wins over include. No include globs means all changed
paths are considered. If filtering leaves zero paths, return an empty diff
without rendering. Strict changed-only diagnostics apply only to the remaining
considered paths. Do not auto-load implicit policy files without a design
update; explicit CLI/API/action policy is intentional.

Manifest diff output supports unified diff, markdown, JSON, and YAML. Image
diff output supports unified diff, markdown, JSON, and YAML for `added`,
`removed`, and `unchanged` image lists. Image markdown output is comment-facing
and omits unchanged images by default. `diff images -o name` prints current-only
added image references, one per line. Removed-only image changes print no names
but still count as a diff for exit-code semantics unless `--exit-code=false`.
Diagnostics stay on stderr for structured/name/unified diff output. Markdown
diff output embeds successful diagnostics in the markdown document: errors are
listed openly, warnings collapse inside a `<details>` block, and repeated
diagnostic codes aggregate with counts. `--markdown-diagnostics all|errors|none`
narrows or drops the embedded list without touching summary counts or
non-markdown diagnostics surfaces. `-o name`
remains unsupported for `diff apps` and `diff app`.

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

Directory rendering parses YAML/JSON files, flattens any document with an
`items` array into its items regardless of kind (matching Argo CD's
shape-based detection) and drops `items: null` documents as no-ops, renders
Jsonnet with native Go libraries, honors Argo CD's
`+argocd:skip-file-rendering` marker, stays within the resolved repository
root, rejects escaping source paths and symlinked source path components, and
skips symlinked files or directories while walking.

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

Use the smallest verification that covers the change. Follow the subagent
sandbox rule in `AGENTS.md` for approval-gated checks.

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

The Argo CD render parity smoke is the upstream-rendering oracle for fixture
coverage. It runs manually and as a reusable workflow called by CI for selected
pull requests that modify render parity fixtures or semantic-rendering Go
modules. In
GitHub Actions, `.github/workflows/argocd-parity-smoke.yml` provisions
`kubectl` and kind with pinned setup actions and then calls
`scripts/argocd-parity-smoke.sh --existing-cluster`. Keep the CI module
detector small and keep setup input versions covered by
`.github/renovate.json5` custom managers.
