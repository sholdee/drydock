---
title: Source Acquisition
aliases:
  - /docs/source-acquisition/
---

drydock renders from local files and explicit source caches. It may fetch
declared Git, HTTP Helm, OCI Helm, OCI artifact, and remote Kustomize inputs
unless `--offline` is set. It does not read ambient Git credential helpers,
ambient Helm registry config, or live Argo CD repository state.

## Resolution Order

For repository sources, drydock resolves deterministically:

1. Explicit `--repo-map URL=PATH`.
2. `oci://` source classification. An `oci://` repository URL without `chart:`
   is a first-class [OCI artifact source](#oci-artifact-sources) and is
   acquired from the registry — it never falls through to a local source path
   or Git acquisition, even when its `path:` (commonly `.`) exists in the
   local tree.
3. Existing local source path under the selected repository tree.
4. A source naming the local checkout — a configured remote of the selected
   repository — at `""`/`HEAD`, a diffed ref name (during diffs), or the
   repository's default-branch name resolves to the local tree (during diffs,
   the active side tree). Full-commit-SHA revisions still acquire remotely.
5. Declared Git cache or fetch behavior for unmapped external repositories.
6. Clear failure.

`--repo-map` wins over local fallback and network fetching. Use it for adjacent
local checkouts or CI jobs that prepare dependencies explicitly:

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
```

For mapped pull-request repositories, `--path` and `--path-orig` are
authoritative source roots and override declared revisions.

Self-references — sources naming the checkout's own repository at `HEAD` or
its default-branch name — resolve to the local tree automatically on all
surfaces, so they need no `--repo-map` entry. Keep `--repo-map` for forks,
commit-SHA pins, and runs from a subdirectory of the checkout.

Ref-only sources are allowed and render no manifests. `$ref/...` Helm value
files and file parameters resolve from the referenced source root, not from its
`path`.

### Self-Repository Sources

On every render surface — `get`, `build`, `test`, `diag`, and both sides of
`diff` — drydock detects when a source `repoURL` matches one of the configured
remotes of the local checkout. At `""`/`HEAD`, at the literal
`--ref`/`--ref-orig` names (diffs only), or at the repository's default-branch
name, such sources resolve to the local tree — the active side tree during
diffs — with no Git cache read or network fetch, including under `--offline`.
Full-commit-SHA revisions, tags, and branches not named by the diff or a
remote HEAD symref still acquire remotely.

Default-branch names come from remote HEAD symrefs only
(`refs/remotes/<remote>/HEAD` — set by `git clone`; recreate it with
`git remote set-head origin -a` after a bare `git init`/fetch checkout; the
pr-action records the pull request's base branch there whenever a base ref is
known — pull-request events or an explicit `base-ref` input). There is
no fallback guessing: without a symref, a source pinned to the default-branch
name acquires remotely, because `init.defaultBranch` or the checked-out HEAD
could wrongly treat a feature branch as the default.

Because self-repository sources read the local tree, uncommitted working-tree
edits flow into self-`$repo` value files on render surfaces, exactly as they do
for the primary source path. The converse also holds: a self-repository source
whose `path` was deleted locally but still exists on the remote tip fails with
path-not-found instead of silently rendering stale remote content — the tree
drydock is pointed at is the desired state.

Detection opens the Git repository at the selected `--path`/`--repo` exactly;
it does not walk up to a parent `.git`. Running `test apps --path <subdir>`
from inside a checkout gets no self-resolution — run from the checkout root,
or add `--repo-map`.

A `--repo-map` entry whose path is the checkout under diff
is rewritten per side for ref diffs, so a single mapped path never serves one
worktree to both sides. For path diffs of exported trees (no `.git` anywhere),
detection finds no remotes; provide per-invocation side-correct maps rather
than one static self-map. When a source merely resembles the local checkout
(same host and repository name, different owner — a fork layout), drydock
emits a `source.self-repo-near-miss` warning and keeps remote acquisition; add
`--repo-map URL=PATH` if they are the same repository. The warning is advisory
only: `--strict` neither escalates it nor fails the run on it.

## Helm Sources

Chart-only HTTP(S) and OCI Helm sources may be fetched into the chart cache
unless `--offline` is set. Local Helm chart sources render from the repository
tree.

A source combining an `oci://` repository URL with `chart:` (and no `path:`)
keeps drydock's existing Helm-chart flow. This is a deliberate, recorded
divergence from strict Argo CD v3.4.5, which dispatches `oci://` URLs to its
OCI handling before Helm handling and would reject that shape — fleets use it,
and rendering the chart matches what their clusters run. An `oci://` source
that sets both `chart:` and `path:` fails with a clear
`unsupported source shape` error instead of falling into either flow.

Drydock resolves missing HTTP(S) and OCI chart dependencies declared in
`Chart.yaml` through its native chart cache. With `--offline`, cache hits are
allowed but network fetches are disabled. The source checkout is not mutated,
and drydock does not run `helm dependency build`.

Local `file://`, repository-alias, or otherwise unresolved dependencies must
already be available under `charts/`. Missing local dependencies fail with a
clear vendored-chart requirement.

Helm `valueFiles` support local paths, `$ref/...` paths, glob expansion,
HTTP(S) remote value files, and discovered `helm.valuesFileSchemes`. Remote
value files use the remote-resource cache and `--remote-*` credentials, not the
chart cache. Explicitly empty `helm.valuesFileSchemes` disables remote
value-file URLs.

`source.helm.passCredentials` affects only HTTP chart repositories with
explicit `--helm-*` credentials. By default, drydock sends those credentials to
the repository index and to chart archive URLs on the same host. When
`passCredentials` is true, drydock also forwards them to cross-host chart
archive URLs returned by the repository index. It does not enable ambient
credential discovery.

## OCI Artifact Sources

Argo CD first-class OCI sources — `repoURL: oci://registry/repo`, a `path:`
into the artifact content, and no `chart:` — are supported on every render
surface. drydock resolves the revision to a digest, pulls the artifact from
the registry, extracts its content, and renders the selected `path` with the
normal directory, Kustomize, or Helm pipeline:

```yaml
source:
  repoURL: oci://ghcr.io/example/config-artifact
  targetRevision: 1.2.3
  path: .
```

Classification is total over `oci://` URLs: an OCI source never resolves to a
local source path, the self-repository branch, or Git acquisition, even when
`path: .` exists in the local tree. Explicit `--repo-map` of the `oci://` URL
still wins and renders the mapped local path instead of fetching.

### Revisions

`targetRevision` accepts:

- A digest (`sha256:...`), used as-is with no resolution network call.
- An exact tag, resolved to a digest via a registry HEAD request.
- A semver constraint (for example `1.x`), resolved to the highest matching
  tag from the registry tag list, then to that tag's digest.

Matching Argo CD repo-server freshness semantics, online runs always
re-resolve tags and constraints against the registry, so a re-pushed tag is
reflected on the next online render; only the image content itself is served
from the cache when its digest is already present. There is no `--refresh-oci`
flag because nothing tag-shaped can go stale between online runs.

### Extraction Guards

Artifact extraction enforces the Argo CD repo-server defaults: at most 10
layers, exactly one content layer, a layer media-type allowlist
(`application/vnd.oci.image.layer.v1.tar`,
`application/vnd.oci.image.layer.v1.tar+gzip`,
`application/vnd.cncf.helm.chart.content.v1.tar+gzip`), and a 1G
maximum extracted size. These limits are not yet configurable from the CLI.

### Cache Layout

OCI artifacts cache under `<user cache dir>/drydock/oci` (override with
`--oci-cache-dir PATH`). Each entry is keyed by repository URL and digest and
holds the pulled image tar plus metadata; `cache list` reports the entries as
source `oci`, kind `image`, and `cache prune`/`cache delete` manage them like
every other source cache (`--source oci` scopes to them). The cache root also
holds a `tags/` records directory used for offline tag resolution; it is not
an entry, so lifecycle commands list and prune around it.

### Offline Behavior

Under `--offline`:

- Digest-pinned revisions pass through without resolution and render from the
  cached image; a digest whose image was never pulled fails with an
  `offline cache miss` error.
- Tags and semver constraints resolve from records captured on earlier
  successful online runs against the same cache directory. A tag or
  constraint that no online run has resolved fails with an
  `offline cache miss` error.

The remediation for an offline cache miss is one online run of the same
command (or any render covering the same source) to populate the image cache
and tag records, then re-run with `--offline`.

### Boundaries

- `oci://` + `chart:` keeps the Helm-chart flow (see
  [Helm Sources](#helm-sources)); `chart:` + `path:` on one `oci://` source
  is an error.
- OCI sources cannot be `$ref` value-file sources. Argo CD materializes
  referenced sources from Git only, and drydock fails such refs with a clear
  error instead of accidentally supporting them.
- Changed-only diff selection treats OCI sources like chart-only Helm
  sources: they re-render when the Application manifest changes, not on
  arbitrary repository changes, and they never render discovery objects.
- Private registries requiring authentication are not yet supported for OCI
  artifact sources; `--registry-config` applies to OCI Helm chart sources
  only. Loopback registries (localhost) use plain HTTP for local development
  and testing; all other hosts use TLS.

## Kustomize Sources

For local Kustomize sources, drydock applies the supported subset of Argo CD
`kustomize.buildOptions` discovered from `argocd-cm` or Argo CD Helm values:

| Build option | Behavior |
| --- | --- |
| `--enable-helm` | Enables native Kustomize `helmCharts` rendering through drydock's Helm library path. |
| `--helm-api-versions` | Passes declared API versions to Helm chart inflation. |
| `--load-restrictor=LoadRestrictionsRootOnly` | Keeps Kustomize's root-only load restriction. |
| `--load-restrictor=LoadRestrictionsNone` | Allows Kustomize to load files outside the Kustomization root. |

Unsupported build options fail explicitly instead of being ignored.
Version-specific `kustomize.buildOptions.<version>` and
`kustomize.path.<version>` settings produce warnings because drydock uses
embedded Go libraries instead of selecting external Kustomize binaries.

Kustomize `helmCharts` render natively through the same Helm library path used
for Argo CD chart sources. No external Kustomize CLI is required for chart
inflation.

Supported Kustomize Git remote forms include:

| Form | Remote root |
| --- | --- |
| `https://github.com/org/repo?ref=v1` | Repository root at `ref`. |
| `https://github.com/org/repo.git?ref=v1` | Repository root at `ref`. |
| `https://github.com/org/repo//path?ref=v1` | `path` inside the repository at `ref`. |
| `git::https://github.com/org/repo.git?ref=v1` | Repository root at `ref` with explicit Git syntax. |
| `git::https://github.com/org/repo.git//path?ref=v1` | `path` inside the repository at `ref` with explicit Git syntax. |
| `ssh://git@github.com/org/repo.git?ref=v1` | Repository root over SSH at `ref`. |
| `ssh://git@github.com/org/repo.git//path?ref=v1` | `path` inside the repository over SSH at `ref`. |
| `git@github.com:org/repo.git?ref=v1` | Repository root over SCP-style SSH at `ref`. |
| `git@github.com:org/repo.git//path?ref=v1` | `path` inside the repository over SCP-style SSH at `ref`. |

Git refs may omit `ref`; omitted or empty `ref` values resolve to `HEAD`. Root
Git refs copy the repository root as the remote Kustomization root.

Remote Kustomize refs are supported in these fields:

| Field group | Supported fields |
| --- | --- |
| Kustomization directories | `resources`, `bases`, `components` |
| Patch files | `patches.path`, `patchesJson6902.path`, non-inline `patchesStrategicMerge` |
| Support files | `generators`, `transformers`, `validators`, `configurations`, `crds`, `openapi.path`, `replacements.path` |
| Generator inputs | ConfigMap and Secret generator `files`, `envs`, and `env` entries |

HTTP(S) refs are treated as single YAML or JSON files. Directory-shaped fields,
including remote bases and components, must use Git refs that resolve to
Kustomization directories.

Ambiguous non-file HTTP(S) URLs are rejected unless they use known Git host
shorthand, a `.git` repository path, or explicit Git syntax such as `git::`,
`ssh://`, or SCP-style `git@host:org/repo.git`.

The renderer copies acquired content into a temporary workspace under generated
`.drydock` paths. It does not write generated manifests into the source tree.

## Caches And Offline Runs

Offline render, build, and diff commands require cache hits, repo maps, local
files, or local chart availability. Populate caches with a non-offline run,
then use `--offline` for cache-only validation:

```bash
drydock test apps --path .
drydock test apps --path . --offline
```

| Flag | Behavior |
| --- | --- |
| `--offline` | Disable Git, Helm chart, OCI artifact, and remote Kustomize network fetching. |
| `--repo-map URL=PATH` | Map a source repository URL to a local checkout. |
| `--refresh-git` | Fetch cached Git repositories and bypass persisted render outputs for those inputs. |
| `--git-cache-dir PATH` | Override the default Git repository cache root. |
| `--refresh-charts` | Refresh cached immutable chart entries and bypass persisted render outputs for those inputs. |
| `--chart-cache-dir PATH` | Override the default chart cache root. |
| `--refresh-remotes` | Refresh cached remote Kustomize resources and bypass persisted render outputs for those inputs. |
| `--remote-cache-dir PATH` | Override the default remote-resource cache root. |
| `--oci-cache-dir PATH` | Override the default OCI artifact cache root. |
| `--render-cache` | Reuse persisted Application render outputs across runs (default on). |
| `--render-cache-dir PATH` | Override the default render-output cache root. |
| `--render-cache-max-size QUANTITY` | Size cap before LRU eviction (default `512Mi`). |
| `--refresh-renders` | Ignore persisted render outputs and overwrite them after rendering. |
| `--plugin-cache-dir PATH` | Runtime override for policy-managed container plugin cache mounts. |
| `--registry-config PATH` | Supply the only Helm OCI registry credentials. |

Render-time Git, chart, OCI artifact, and remote-resource caches must stay outside the
current repository tree, compared repository trees, repo-map roots, and their
symlink-resolved equivalents. Drydock validates these roots before cache reads,
fetches, or writes so a repository cannot double as its own mutable source
cache. A render-cache directory that cannot be opened fails the run at
validation time, consistent with Git and chart cache validation. Dev builds
without release version stamping never persist renders, so they do not
require access to the render-cache directory.

Cache entries include hidden `.drydock-cache/metadata.json` sidecars with
redacted target metadata. Older hash-only entries are listed as legacy entries
when their filesystem layout is recognized.

`--plugin-cache-dir` is separate: it is a render-time override for
policy-managed container plugin cache mounts. Plugin cache mount roots remain
excluded from cache lifecycle commands (policy-managed).

Cache lifecycle commands also list, prune, and delete persisted render
output entries via `--source render` and `--render-cache-dir`. During prune,
when render entries are in scope (either because `--source render` is set or
because no `--source` filter is used), `cache prune` additionally enforces the
render cache size cap and removes stale orphaned temp files. A `--source
git`, `--source chart`, `--source remote`, or `--source oci` prune scopes only
to that source and skips the render sweep entirely.

> **Warning:** The render cache size cap is a runtime flag (`--render-cache-max-size`)
> that `cache prune` cannot discover automatically. If your builds use a custom
> `--render-cache-max-size`, pass the same value to `cache prune` — otherwise
> the sweep enforces the 512Mi default and may evict entries your runtime cap
> would keep.

### Render Output Cache

Successful Application render outputs persist across processes under
`<user cache dir>/drydock/render`, keyed by the Application spec, Argo
settings signature, drydock build that produced them, and provable source
inputs. A warm run returns cached manifests without invoking render engines.

For clean local repositories, drydock hashes committed per-Application input
paths instead of the whole repository commit. A commit that changes one
Application's Helm values, Kustomize graph, directory or Jsonnet source, or
generated Application input invalidates that Application while unchanged
Applications can still hit the render cache. Generated inputs include static
Application YAML, ApplicationSet generator files, and rendered bootstrap
Application parents when discovery records those paths. Local source override
files (`.argocd-source.yaml` and `.argocd-source-<app>.yaml`) are also part
of every Application's input digest; adding, editing, or deleting an override
file invalidates that Application's cached entry.

Local dirty worktrees can also use the render output cache for Applications
whose proven render inputs are unchanged. In dirty Git-backed roots, drydock
enumerates the complete set of changed files once per run and decides the cache
strategy per Application input path set. Applications whose proven input paths
are untouched by any dirty file keep their clean-worktree cache keys — entries
persisted from clean runs are served directly across the mode transition, so
editing one Application re-renders only that Application. Applications whose
input paths overlap any dirty file are hashed from the working tree, including
modified, deleted, untracked, and ignored files under those paths. Helm sources
with glob value-file patterns always re-render and skip persistence in dirty
mode, because a new untracked file matching the glob would be invisible to the
path-set intersection. Untouched Applications that skip the working-tree hash
share clean mode's run-stability contract: the dirty-path enumeration is
computed once at the start of a run, and any mid-run edits are caught by the
existing store-time verification rather than by a second scan. A worktree that
is clean except for nested `.git` directories is classified dirty (the nested
`.git` content is treated conservatively, consistent with the store guard's
fail-closed treatment of git links).

This behavior is automatic, including in the PR action; no new action input is
required. The PR action stores persisted render outputs under
`${cache-path}/renders` when action caching is enabled.

Renders are persisted only when every input is provably pinned. Floating remote
Kustomize bases, remote URL Helm value files, plugin sources, repo-mapped
sources, and unsupported local input graphs always re-render.

Clean Git roots use committed input digests. Dirty Git roots use per-Application
committed or working-tree input digests, as described above. Symlinks,
unsupported file types, unreadable files, unbounded input graphs, and non-Git
roots render normally but skip persistence for the affected Application. Dev
builds without release version stamping disable persistence automatically.
Release binaries are always stamped; when building from source, ensure Go's VCS
stamping is active (the default in a git checkout — `go build -buildvcs=true`
forces it) or persistence silently stays off. `--cache-events` with `diag`
shows a `disabled` event with reason `dev-build` when this applies.

Use `--cache-events` when diagnosing render-cache behavior. For `build` and
`test`, cache events are written to stderr so that stdout stays
machine-parseable (manifests for `build`, status lines for `test`). For `diag`,
cache events are included in structured output alongside diagnostics. Render
cache events report `hit`, `miss`, `store`, `skipped`, and `error` actions.
`skipped` reasons include `input-graph-unsupported` (the source's input graph
is unsupported or cannot be fully enumerated), `helm-glob-inputs` (a dirty
worktree where Helm value-file globs make the input set unprovable from
committed content), `duplicate-application` (two discovered Applications share
the same namespace and name, so neither input set is authoritative),
`input-digest-error`, `ineligible-source`, and `inputs-changed`. All of these
mean drydock rendered normally without persisting the result. `inputs-changed` indicates drydock
could not prove the render inputs still match the cache key at store time —
usually because source files changed between cache-key computation and the end
of rendering (a TOCTOU guard), and always for sources whose inputs cannot be
re-verified, such as symlinks. When verification failed with an error, the
event carries the error text. One inherent limit: an edit that is reverted to
its original content before the render completes is indistinguishable from no
edit, so the verification cannot catch it.

Chart-backed renders key on the exact chart name and version. If a registry
republishes an existing version, for example a mutable OCI chart version, use
`--refresh-charts` (or `--refresh-renders`) to re-render and overwrite the
cached entries.

## Credentials

Credentials are explicit and non-interactive. Drydock does not read ambient Git
credential helpers, ambient Helm registry config, credential fields from
discovered repository Secrets, or live Argo CD repository state.

| Source | Auth form | Flags |
| --- | --- | --- |
| Git HTTPS | Bearer token | `--git-bearer-token TOKEN` |
| Git HTTPS | Basic auth | `--git-username USER`, `--git-password PASS` |
| Git SSH | SSH key | `--git-ssh-key-file PATH`, `--git-known-hosts-file PATH`, `--git-ssh-passphrase PASSPHRASE` |
| HTTP(S) Helm | Bearer token | `--helm-bearer-token TOKEN` |
| HTTP(S) Helm | Basic auth | `--helm-username USER`, `--helm-password PASS` |
| OCI Helm | Registry config | `--registry-config PATH` |
| HTTP(S) remote Kustomize | Bearer token | `--remote-bearer-token TOKEN` |
| HTTP(S) remote Kustomize | Basic auth | `--remote-username USER`, `--remote-password PASS` |

For Git HTTPS, HTTP(S) Helm, and HTTP(S) remote Kustomize, bearer auth wins
when both bearer and basic credentials are provided. Kustomize Git remote refs
reuse the explicit `--git-*` credentials, but use the remote Kustomize cache
and `--offline` or `--refresh-remotes` behavior.

Supported SSH URL forms are:

| Form | Username behavior |
| --- | --- |
| `ssh://git@host/org/repo.git` | Uses the explicit `git` username. |
| `git@host:org/repo.git` | Uses the explicit `git` username. |
| `ssh://host/org/repo.git` | Defaults the missing username to `git`. |

Passwords, bearer tokens, SSH private keys, SSH passphrases, registry
credential values, and credential-bearing URLs are never printed in diagnostics
or formatted errors.

## Cache Lifecycle Boundary

Cache lifecycle commands are local filesystem operations only:

```bash
drydock cache path
drydock cache list -o json
drydock cache prune --older-than 720h --dry-run
drydock cache prune --max-size 4Gi --yes
```

They do not:

- render Applications
- clone or fetch Git repositories
- fetch Helm charts
- fetch remote Kustomize resources
- read credential flags
- retry failed network or authentication acquisitions

`cache prune` and `cache delete` operate only on recognized drydock Git,
chart, remote-resource, OCI artifact, and render output cache entry roots.
They do not manage plugin cache mount roots selected with
`--plugin-cache-dir`.

Cache lifecycle commands reject cache roots that resolve inside the current
working directory, selected repository roots, Git repository trees, or
symlink-resolved equivalents. Non-dry-run deletion requires `--yes`; dry-runs
never require confirmation.

### Pruning by age

`--older-than DURATION` removes cache entries whose last-used timestamp
(`Metadata.UpdatedAt`, refreshed on every cache hit and fetch) predates the
given duration. Legacy entries without metadata fall back to filesystem mtime.

### Pruning by size (`--max-size`)

`--max-size QUANTITY` evicts the least-recently-used entries from the selected
sources until their total size is at or below the cap. Recency is measured by
`Metadata.UpdatedAt`, which is refreshed on every cache use — not just the
initial fetch. This means the oldest (least-recently-used) entries are evicted
first. Eviction proceeds to at or below the cap exactly, not to a hysteresis
target.

`--max-size` composes with `--source` and `--older-than`. When both flags are
set, age-based entries are removed first; the size cap is then applied to the
survivors. The cap applies to the sum of `SizeBytes` across entries matching
the selected source and kind filters — the same per-entry sizes `cache list`
reports.

> **Warning:** Evicted source cache entries hard-fail subsequent runs that use
> `--offline`, because offline mode cannot re-fetch missing entries
> (`"offline cache miss for ..."`). Do not use `--max-size` before an offline
> run unless you are confident the remaining entries cover all sources that
> offline run will need.

### Automation

The Cache Lifecycle Boundary is defined by explicit invocation with explicit
caps — not by whether a human issued the command. Automation such as the
pr-action's local-mode post-run prune step may invoke `cache prune` on the
operator's behalf; the same boundary and `--offline` warning apply.

### Deferred design

A shared content-addressed store with ref tables, leases, and mark-sweep
collection is intentionally deferred. It would be useful only after drydock has
multiple cache surfaces sharing immutable blobs.
