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
- `pr comment`
- `version`

Shared flags cover repository paths, repo maps, explicit Git/chart
and remote-resource acquisition controls, output formats, diff filters,
ApplicationSet fixtures, cache events, and render parallelism. Commands that
run on many Applications also accept the `-l`/`--selector` label selector, and
each of them filters on it; single-Application commands (`build app`,
`test app`, `diff app`) keep it hidden and deprecated, and ignore it. Diff commands
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
ApplicationSet-controller settings must not mutate render behavior. There are
two exceptions, and drydock applies each one and keeps it in the render
settings signature:

- `reposerver.max.combined.directory.manifests.size` decides whether a
  directory source fails.
- `reposerver.allow.oob.symlinks` decides whether a source fails while the
  tree it renders from holds an out-of-bounds symlink.

`controller.ignore.normalizer.jq.timeout` changes only diffs: it limits how
long one jq ignore-rule deletion may run. Keep it out of the render settings
signature and the render cache keys. It is in the changed-only settings
signature as the parsed duration.

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

Supported ApplicationSet generators are deterministic. The supported set
includes Git directories, Git files, OCI directories and files, list, matrix,
merge, and explicit fixture-backed provider generators. Preserve Go-template behavior such as
`missingkey=error`, Sprig functions, generator selectors, generator template
overrides, supported nested matrix/merge combinations, and deterministic
merge-key overlays.

Provider-backed ApplicationSet generators are offline fixture-backed only. Do
not add Kubernetes API reads, Argo CD API reads, SCM/pull-request/cloud API
calls, plugin service calls, shellouts, or ambient credential discovery.

The Argo CD v3.6 OCI generator is the one generator that acquires a source:
`internal/app` hands `appset.Options.OCI` a fetcher that reuses the `oci://`
Application source path (repo map, `--offline`, OCI cache, credentials,
snapshot session). Its matching mirrors the v3.6.1 repo-server
(`walkDirectoryTree` without hidden directories unless
`appset.Options.IncludeHiddenDirectories`, always-doublestar file
globs over `os.Root`), and its param sets carry no `SourcePath`, so
artifact paths never claim changed paths or local render-cache inputs.

The Git generator mirrors the v3.6.1 repo-server checkout
(`internal/appset/git_tree.go`). Git files patterns are `git ls-files`
pathspecs, ported in `internal/appset/git_pathspec.go` and proved against the
git CLI by `TestGitPathspecMatchesGitLsFiles`.
`appset.Options.NewGitFileGlobbing` switches to doublestar over an in-memory
view of the tree. Directory patterns go through `filterRepoPaths`, which the
OCI generator shares. `appset.Options.IncludeHiddenDirectories` adds hidden
directories. `appset.Options.Worktree` hides the paths that Git ignores and
does not track. A Git generator never reads outside the repository or in
`.git`. Empty-content decoding is an Argo CD parity pin: empty,
comment-only, and bare `---`/`...` files decode to one empty param set, `[]`
decodes to zero, and multi-document files decode only the first document.
Do not change it.

Git and OCI generator failures that Argo CD scopes to the ApplicationSet use
`generatorParamsError` and the warning `appset.generator-failed`: a param
file that does not decode, a pattern that git or doublestar refuses, a
listed path that the repo-server cannot read, and an empty project in the
ApplicationSet template. A named project is recorded on each generated
Application (`GitGeneratorProject`). drydock cannot know the AppProjects of
the cluster offline, so it never drops Applications for it: at the end of
discovery, `warnGitGeneratorProjects` warns `project.missing` when the
repository declares AppProjects but not that one. IO failures stay fatal. After the
first `generatorParamsError`, `GenerateWithOptions` evaluates no more
generators.

Inputs that drydock does not evaluate, where Argo CD generates normally
(the attr pathspec magic, a new-globbing match of a file in `.git`, a glob
past `maxGlobVisits` lookups), use `generatorUnsupportedError`: the whole
ApplicationSet is unsupported (`appset.unsupported-generator`). Gitlinks
come from `GitlinkLister` (the index of a working tree, or the empty
directories of a snapshot, which `materializeGitlinks` creates) and from
directories with their own `.git`. New globbing looks paths up lazily
(`gitGlobFS`). A param file's `SourcePaths` hold every symlink followed and
the file read.

Git generator reads resolve symlinks one component at a time
(`resolveRepoPath`): never outside the root, never through a `.git`
component, ENOTDIR through a file, ENOENT for a path that
`Options.Worktree` skips, and at most `maxRepoSymlinkHops` symlinks. The read
goes through `os.OpenRoot`. doublestar stays at the version the selected
Argo CD requires; the post-upgrade sync helper sets it, and Renovate ignores
it.

Template, `templatePatch`, generator `values`, and matrix/merge child
interpolation execution errors are appset-scoped warning diagnostics
(`appset.template-render-failed`), and the failing ApplicationSet contributes
zero generated Applications, matching the Argo CD controller's
`ErrorOccurred` condition scoping. Generator template failures are marked with
`generatorTemplateError`. Generator errors that Argo CD v3.6.1 returns from
`generators.Transform` (list decode, matrix/merge shape and merge keys, empty
or double children, selector parse, provider filter regexps) are marked with
`generatorParamsError` and give the appset-scoped `appset.generator-failed`
warning. IO and acquisition errors stay fatal. The "template render failed"
and "ApplicationSet generator failed" message substrings are the stable-code
dispatch keys in `internal/diagnostic`. Keep each message and its dispatch
case in sync.

After rendering, generated Applications go through Argo CD's name validation
(empty names first, then duplicates; every Application sharing a duplicated
name is dropped) as `appset.application-validation-failed` warnings. A
generated Application whose AppProject is missing is kept (a recorded
divergence): `project.ValidateGeneratedApplications` reports it as
`project.missing` with the ApplicationSet that generates it, from
`ApplicationSelectionInput.ApplicationSet`. Then
`spec.strategy` RollingSync handling follows
`applicationsetcontroller.enable.progressive.syncs` (`appset.Options.ProgressiveSyncs`).
Each discovery run uses one value for it and for the two Git generator keys
(`appsetGenerationSettings`), read before the first ApplicationSet
expansion. The rules below apply to all three keys, each with the
generators that it changes. A second run needs two things: the run found an ApplicationSet
with a RollingSync strategy, and its final settings give another value. Then
`discoverRepositoryWithOCI` runs discovery again with that value. A run that
failed after its first expansion counts too, with its partial result. A run
that failed earlier returns its error. Sometimes the partial result of a
failed run does not hold the install's settings yet. For example, an app-of-apps can
render the install one level deeper than the failure. Then the run also
returns its error. With the flag committed, the same repository succeeds.
There are at most two runs. If the second run
ends with the other value again, its result stays and
`settings.progressive-syncs-cycle` (not strict-exempt) is added. Off, it gives one
`appset.rollout-strategy-ignored` advisory. On, an empty steps list gives
`appset.rollout-no-steps` and no Applications, and other findings are
`appset.invalid-rollout-config` warnings that do not drop Applications. With
steps, generated Applications get `automated.enabled: false`
(`disableRollingSyncAutomatedSync`).
`appset.rollout-strategy-ignored` and `appset.invalid-rollout-config` are
strict-exempt (`strictExemptDiagnostic`); `appset.rollout-no-steps` is not. Each of these codes dispatches on a message substring kept in sync with
`internal/diagnostic.appSetCode`.

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
source resolution order is explicit repo map, `oci://` classification (every
`oci://` repository URL is a first-class OCI artifact source, as in Argo CD's
`IsOCI`-before-`IsHelm` dispatch: it acquires `repoURL` at `targetRevision`
from the registry and never falls to the local-path, self-repo, or Git
branches; `spec.source.chart` is ignored there and reported once per source
as the strict-exempt `source.oci-chart-ignored` warning, `path` defaults to
`.`, `.argocd-source*.yaml` is merged at the content path, and `chart:` +
`path:` on one `oci://` source is valid; the schemeless Helm-OCI shape keeps
the chart flow), existing local source path,
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

Authenticated source handling is non-interactive, and explicit apart from SSH
identity resolution:

- Do not prompt for credentials.
- Do not read ambient Git credential helpers.
- Do not read ambient Helm registry config.
- Git HTTPS auth supports bearer and basic auth; bearer wins.
- Git SSH auth uses an explicit key file when given; otherwise the agent,
  `~/.ssh/config` `IdentityFile`, and `~/.ssh/id_*` identities in that order.
  known_hosts verification is always on; no prompting.
- HTTP(S) Helm and remote Kustomize auth support bearer and basic auth; bearer
  wins.
- OCI Helm (schemeless chart) auth is provided only through the explicit
  `--registry-config` file.
- OCI artifact auth comes per registry host from the same explicit
  `--registry-config` file (plain `auths`, legacy `https://host/` keys,
  `credsStore`/`credHelpers`), read and validated lazily at the first
  `oci://` acquisition so runs without OCI artifact sources never open it,
  with `--oci-username`/`--oci-password` as the global override for every
  OCI artifact registry in a run. Hosts without an entry stay anonymous;
  identity-token entries and credential helpers that cannot run are errors;
  an auth-less file never reaches the OS keychain (helm's chart loader
  does). The TLS set is global, and any TLS-implying `--oci-*` flag disables
  the loopback plain-HTTP default. Credentials in `oci://` repository URLs
  are rejected with a redacted error, and artifact auth failures name both
  credential flags.
- CI helper commands (`pr comment`) read only the documented `GITHUB_*`
  variables and the token variables; nothing else in the binary reads a token
  from the environment.

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

Built-in Kubernetes kind scopes live in `internal/manifest/scope.go`, keyed by
group and kind. `TestBuiltInScopeTablesCoverStaticSchemeKinds` fails when a
Kubernetes bump adds a top-level kind to the gitops-engine static scheme that
neither table lists; classify it from its `+genclient:nonNamespaced` marker in
`k8s.io/api`.

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

Manifest diff output supports unified diff, dyff, markdown, JSON, and YAML.
`-o dyff` renders each changed resource's redacted display bodies through the
in-process dyff library (never the `dyff` binary) and is not available for
`diff images`. `--markdown-diff-style unified|dyff` (`diff apps`/`diff app`,
markdown output only) swaps the unified diff in each Application's ```diff
fence for dyff's GitHub-style report; summary counts, byte budgets, JSON/YAML,
and the raw/HTML report files stay unified. Both dyff renderers pair a removed
and an added Kustomize-hash-suffixed ConfigMap (same Application source, namespace, and name base; never Secrets,
whose per-side redaction defeats matching) via `dyffreport.Items`, and keep the
pair only when dyff reports a rename, otherwise rendering each result alone.
Pairing is presentation-only. Image
diff output supports unified diff, markdown, JSON, and YAML for `added`,
`removed`, and `unchanged` image lists. Image markdown output is comment-facing
and omits unchanged images by default. `diff images -o name` prints current-only
added image references, one per line. Removed-only image changes print no names
but still count as a diff for exit-code semantics unless `--exit-code=false`.
Diagnostics stay on stderr for structured/name/unified diff output. Markdown
diff output embeds successful diagnostics in the markdown document: errors are
listed openly above the diffs, warnings collapse inside a `<details>` block
rendered below the diffs, and repeated
diagnostic codes aggregate with counts. `--markdown-diagnostics all|errors|none`
narrows or drops the embedded list without touching summary counts or
non-markdown diagnostics surfaces. `--force-open-details` opens every
application `<details>` block. Without that flag, a single application is
open and several stay collapsed. Warning diagnostics stay collapsed. `-o name`
remains unsupported for `diff apps` and `diff app`.

Application-local and global ignore rules support `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers`. Normalize both sides with the
current side's rules and settings, as Argo CD compares under the live
Application's current rules. A resource that only the baseline side has also
uses the current side's rules of its Application. The resources of an
Application that only the baseline side has get no ignore rules. Managed fields
support is an offline desired-vs-desired approximation using rendered
manifests; do not claim live server-side ownership prediction.

Keep `github.com/itchyny/gojq` at exactly the version the selected Argo CD
requires: it parses and runs `jqPathExpressions`. Renovate ignores it, and
`scripts/sync-argocd-gitops-engine.go` sets it after an Argo CD upgrade, with
the GitOps Engine version. `TestGojqMatchesSelectedArgoCD` fails when they
differ.

Ignore rules run in Argo CD v3.6.1 order: Application rules, then the
annotation rule, then `resource.customizations` rules, each rule applying its
`jsonPointers` and then its `jqPathExpressions`. Keep every copy of a repeated
pointer, because repeated array-index pointers remove more elements. The
known-types normalizer runs after the ignore rules. For pointers, jq
expressions, and `knownTypeFields`, a customization key with no slash means
that kind in the core group. `knownTypeFields` match the exact group and kind,
and Argo Rollouts always get the default PodSpec known type. Customization
`managedFieldsManagers` use Argo CD's `HasIgnoreDifference` lookup instead:
the exact key `group/kind` (`/Kind` for core) plus `*/*`, with no globs, so a
key with no slash never matches.

The resource-level `argocd.argoproj.io/ignore-differences` annotation
(`jsonPointers` only) targets just the annotated resource. Read it only from
the current side, never from the baseline of a removed resource, and apply
its pointers leniently with evanphx/json-patch path semantics (first segment
dropped unchecked, negative indexes, literal unknown `~` escapes); pointers
that cannot be applied, including root pointers, are skipped, never errors.

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

Directory rendering selects files with the repo-server's case-sensitive
extension filter (skipped under `directory.disableExtensionFilter`, leaving
include/exclude as the only filters), parses YAML/JSON files, flattens any document with an
`items` array into its items regardless of kind (matching Argo CD's
shape-based detection) and drops `items: null` documents as no-ops, renders
Jsonnet with native Go libraries, honors Argo CD's
`+argocd:skip-file-rendering` marker, stays within the resolved repository
root, and rejects escaping source paths and symlinked source path components.
The walk never enters a symlinked directory. It follows a file symlink as the
repo-server does: the symlink's own name and path pass the filters, a target
in the repository is read, a missing or non-regular target is skipped, and a
target outside the repository fails the source. Before any source renders,
the provider checks its whole tree for out-of-bounds symlinks
(`render.CheckOutOfBoundsSymlinks`) unless `reposerver.allow.oob.symlinks` is
true. The targets of followed symlinks and the files that Jsonnet files
import from outside the source path and libraries are render inputs for the
persistent render cache and changed-only selection.

Kustomize rendering uses Go libraries. Do not enable Kustomize's Helm shellout
plugin. Validate local graph references before rendering and reject unsupported
remote refs, absolute paths, repo-root escapes, and symlinked graph entries.

Kustomize `helmCharts` and Argo CD chart-only sources use drydock chart
acquisition and Helm Go rendering. Remote Kustomize HTTP(S) file refs and Git
refs use drydock's remote resource cache and the same credentials as the
corresponding source type, including Git SSH identity resolution.

Refuse a remote ref that uses the local file transport (`file://`,
`git::file://`, `file:`, or `file::`, in any letter case), even though
kustomize in Argo CD clones it. Do not let the remote Git client read a Git
repository from the local file system.

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

The AppProject policy part of the smoke runs in phases under
`testdata/argocd-project-policy/`. Each phase has a case array in the script,
an `expected.yaml`, and an `applications.yaml`.
`go test ./scripts/argocd-project-policy-smoke` checks that the three agree.
A case that changes state every Application shares, such as a cluster Secret
named `in-cluster` or an argocd-cm key, goes in a later phase with its own
fixture repo. Add its undo step to `reset_project_policy_shared_state`, so the
cleanup trap also undoes it after a failed run on an existing cluster. The
trap skips that reset with `--keep-cluster`. Cluster Secrets that only Argo
CD sees go in `live-cluster-secrets.yaml`, outside every drydock repo. The
first phase applies the Secret `project-policy-live-scoped-cluster` from that
file. The script leaves that Secret in place on purpose and applies it again
on each run, so the reset does not remove it.
