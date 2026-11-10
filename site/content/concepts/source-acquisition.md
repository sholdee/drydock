---
title: Source Acquisition
aliases:
  - /docs/source-acquisition/
---

drydock renders from local files and explicit source caches. It may fetch
declared Git, HTTP Helm, OCI Helm, OCI artifact, and remote Kustomize inputs
unless `--offline` is set. It does not read ambient Git credential helpers,
ambient Helm registry config, Docker config, or live Argo CD repository state.
Git SSH identities are the one exception: when no `--git-ssh-key-file` is
given, drydock resolves one the way OpenSSH does and always verifies host keys.
See [SSH Identity Resolution](#ssh-identity-resolution).

## Resolution Order

For repository sources, drydock resolves deterministically:

1. Explicit `--repo-map URL=PATH`.
2. `oci://` source classification. Every `oci://` repository URL is a
   first-class [OCI artifact source](#oci-artifact-sources) — with or without
   `chart:`, which is ignored there — and is acquired from the registry. It
   never falls through to a local source path or Git acquisition, even when
   its `path:` (default `.`) exists in the local tree.
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

### Git Submodules

A submodule is a Git repository that another repository pins at one
commit. The Argo CD repo-server checks out submodule content by default
(`reposerver.enable.git.submodule`). drydock reads that content from these
places:

- A fetched Git source checks out its submodules into the Git cache. Each
  submodule URL has one cache entry that holds every commit drydock
  fetched for it, so `--offline` reads it.
- A `--ref` or `--ref-orig` snapshot reads each submodule commit from the
  repository's `.git/modules` store first. That needs no network. Then it
  reads the Git cache, and then, unless `--offline` is set, it fetches the
  submodule into the Git cache.
- A remote Kustomize Git base checks out its submodules into the remote
  cache, unless the ref says `?submodules=false`.
- A self-repository source in a working tree reads the submodule as the
  working tree holds it. If the submodule is not initialized, drydock warns.

With `--offline`, a submodule commit that drydock has no copy of is an
offline cache miss. A fetched Git source then fails. A `--ref` snapshot
keeps the submodule empty, warns once, and fails only the sources whose
path holds the submodule or is in it. drydock removes a user and password
from submodule URLs before it fetches, so no cache entry stores a token. drydock never
writes to your repository or its `.git` directory. Relative submodule URLs
resolve against the repository URL. drydock sends HTTP credentials only to
a submodule on the same host and port as the repository, never from
`https` to `http`. The submodule host must also match a `--git-auth-host`
entry (see [Git HTTPS credential hosts](#git-https-credential-hosts)). It refuses `file` URLs and local paths, as the
repo-server's git does.

### Git LFS

A Git LFS pointer file is a small text file that stands for a large file in
a Git LFS store. When a repository Secret sets `enableLfs`, the Argo CD
repo-server downloads the content of the pointer files with git-lfs.
drydock has its own Git LFS client, which uses the Git LFS batch API over
`http` and `https`. It fills the pointer files of fetched Git sources and
of `--ref` and `--ref-orig` snapshots. It reads each object from these
places, in this order:

1. The local repository's `.git/lfs/objects` store, for a snapshot. drydock
   never writes it.
2. The Git LFS store of the Git cache, `<git cache>/lfs`. Repositories
   share it, because an object has one path for its SHA-256.
3. Unless `--offline` is set, the Git LFS server of the repository. This is
   `lfs.url` or `remote.origin.lfsurl` from the `.lfsconfig` file of the
   commit, or else the repository URL with `.git/info/lfs`.

drydock makes sure that the size and the SHA-256 of each object match the
pointer before it uses or stores the object. It downloads at most 100 MiB
for one object and 512 MiB for one tree, within 15 minutes.

The `--git-*` HTTP credentials go to the Git LFS server only when a
`--git-auth-host` entry names its scheme, host, and port (see
[Git HTTPS credential hosts](#git-https-credential-hosts)). The server
gives a download URL for each object, often on another host. That request
gets only the headers that the server gives for it, never the `--git-*`
credentials. drydock refuses a plain `http` download from an `https`
server, and it never connects to a link-local address. For an SSH
repository URL, drydock runs `git-lfs-authenticate` over SSH with the Git
SSH key and known_hosts, as git-lfs does.

When the Git LFS server does not give an object, the source fails, as the
manifest request of the repo-server fails. When drydock itself cannot fill
a pointer, for example because the object is over a limit, the pointer
stays with a `render.lfs-unsupported` warning. With `--offline`, a pointer
whose object is not in a store stays with a `render.lfs-pointer` warning.
drydock also gives that warning for a working-tree file that still holds
the pointer of `HEAD`. drydock never writes your working tree. Run
`git lfs pull` to render the content there. Submodules keep their pointer
files, as in Argo CD. The `drydock cache` commands list, prune, and delete
the objects of the Git LFS store. See
[Compatibility](/compatibility/#git-submodules-and-git-lfs).

## Helm Sources

Chart-only HTTP(S) and OCI Helm sources may be fetched into the chart cache
unless `--offline` is set. Local Helm chart sources render from the repository
tree.

An OCI Helm chart source is the schemeless shape Argo CD classifies as
Helm-OCI: `repoURL: ghcr.io/org/charts` (host and optional path, no scheme)
plus `chart:`, including nested chart names. It is pulled through the Helm
registry client into the chart cache.

A chart source `targetRevision` can be a version range, such as `1.2.*` or
`>=1.0.0 <2.0.0`, as in Argo CD. drydock reads the repository index or the
registry tags on every online run, takes the highest version that matches,
and caches the chart under that version. Each online run records the version
list, so `--offline` resolves the range from the record. A render that
resolved a range is not stored in the persistent render cache. A `repoURL` that starts with `oci://` is never a Helm chart
source: Argo CD's repo-server dispatches on the scheme before it looks at
`chart:`, and drydock follows it — see
[OCI Artifact Sources](#oci-artifact-sources).

Drydock resolves missing HTTP(S) and OCI chart dependencies declared in
`Chart.yaml` through its native chart cache. With `--offline`, cache hits are
allowed but network fetches are disabled. The source checkout is not mutated,
and drydock does not run `helm dependency build`. It does the same steps as
that command in Argo CD: a dependency is missing only when `charts/` has no
chart of its name, a `Chart.lock` must match `Chart.yaml`, and the build
fetches every remote dependency at its locked or resolved version, also the
vendored ones.

Local `file://`, repository-alias, or otherwise unresolved dependencies must
already be available under `charts/`. Missing local dependencies fail with a
clear vendored-chart requirement.

Helm `valueFiles` support local paths, `$ref/...` paths, glob expansion,
HTTP(S) remote value files, and discovered `helm.valuesFileSchemes`. Local
paths resolve as in the Argo CD repo-server: a path that starts with `/` is
relative to the repository root, symlinks in the repository are followed, and
a file that the list names twice is read once, at its first place. drydock
never reads a value file outside the repository or in `.git`. Remote value
files and `fileParameters` URLs use the remote-resource cache and the
`--remote-*` credentials for the hosts that `--remote-auth-host` names, not the
chart cache. Explicitly empty `helm.valuesFileSchemes`
disables remote value-file URLs.

`source.helm.passCredentials` affects only HTTP chart repositories with
explicit `--helm-*` credentials. By default, drydock sends those credentials to
the repository index and to chart archive URLs on the same host. When
`passCredentials` is true, drydock also forwards them to cross-host chart
archive URLs returned by the repository index. It does not enable ambient
credential discovery.

## OCI Artifact Sources

Every source whose `repoURL` carries the `oci://` scheme is an Argo CD
first-class OCI artifact source, supported on every render surface. The
repo-server dispatches on that scheme before it considers Helm (`IsOCI`
before `IsHelm`), and drydock follows the same rule: it resolves
`targetRevision` to a digest, pulls `repoURL` from the registry, extracts the
content, merges any `.argocd-source*.yaml` found at the content path, and
renders `path` (default `.`) with the normal directory, Kustomize, or Helm
pipeline — Helm is inferred from a `Chart.yaml` there and the source's `helm`
block is applied:

```yaml
source:
  repoURL: oci://ghcr.io/example/config-artifact
  targetRevision: 1.2.3
  path: .
```

A Helm chart pushed to a registry is an OCI artifact too, so the same shape
renders it: the chart's own OCI repository is the `repoURL`, the chart
version is the `targetRevision`, and `path: .` is the chart root:

```yaml
source:
  repoURL: oci://ghcr.io/bjw-s-labs/helm/app-template
  targetRevision: 5.2.1
  path: .
  helm:
    valuesObject:
      controllers: {}
```

`spec.source.chart` never participates in an `oci://` source — not in the
pull, the routing, override merging, or rendering, with or without `path:`,
`helm:`, `plugin:`, or `ref:`. A source that sets it is rendered exactly as
above, and drydock reports one advisory warning per Application source,
`source.oci-chart-ignored`, naming both Argo-valid spellings: drop `chart:`
and set `path: .` to keep the artifact (Argo CD requires `path` or `chart` on
every source, or `ref` in a multi-source list), or switch to the schemeless
Helm-OCI shape (`repoURL: ghcr.io/bjw-s-labs/helm` plus
`chart: app-template`) if the chart under that repository was meant. The
warning is advisory only: `--strict` neither escalates it nor fails the run
on it, because Argo CD renders the shape silently and the manifests are the
same either way. `chart:` together with `path:` is valid; `path` selects
within the artifact. Argo CD's API server (`argocd app create`) still
classifies such a repository Helm-first and rejects the shape; drydock
mirrors the controller, which renders it when applied declaratively, and the
Argo CD render parity smoke pins that live.

Classification is total over `oci://` URLs: an OCI source never resolves to a
local source path, the self-repository branch, or Git acquisition, even when
`path: .` exists in the local tree. Explicit `--repo-map` of the `oci://` URL
still wins and renders the mapped local path instead of fetching; the
`chart:` warning is still reported. Two recorded leniencies: the scheme check
trims surrounding whitespace and matches case-insensitively, where Argo CD's
`IsOCI` is an exact lowercase prefix check, so it only adds classification
and never re-routes a URL Argo CD treats as OCI; and a single-source `oci://`
Application with neither `path:` nor `chart:`, an invalid spec upstream,
renders the artifact root in drydock.

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

### Authentication And TLS

Private OCI artifact registries authenticate with an explicit credential
family: `--registry-config` supplies per-registry credentials for OCI
artifact sources and OCI Helm chart registries alike, the `--oci-*`
username/password pair is the global override, and the `--oci-*` TLS flags
are one global set:

| Flag | Behavior |
| --- | --- |
| `--registry-config PATH` | Docker/Helm registry config file (`helm registry login` format) with per-registry credentials for OCI artifact sources and OCI Helm chart registries. For artifact sources it is read and validated at the first `oci://` acquisition. |
| `--oci-username USER` | Username for authenticated OCI artifact registries (every registry in the run; overrides `--registry-config`). |
| `--oci-password PASS` | Password for authenticated OCI artifact registries (every registry in the run; overrides `--registry-config`). |
| `--oci-ca-file PATH` | PEM CA bundle for OCI artifact registry and OCI Helm chart registry TLS (artifact registries: replaces the system pool; Helm chart registries: added to the system pool). |
| `--oci-client-cert-file PATH` | Client certificate file for mutual-TLS OCI artifact registries and OCI Helm chart registries (requires `--oci-client-key-file`). |
| `--oci-client-key-file PATH` | Client key file for mutual-TLS OCI artifact registries and OCI Helm chart registries (requires `--oci-client-cert-file`). |
| `--oci-insecure-skip-verify` | Skip TLS certificate verification for OCI artifact registries and OCI Helm chart registries (insecure; credentials are exposed to whoever answers). |

`--registry-config` is consulted per registry host through the same
credential store that `docker login` and `helm registry login` write: plain
`auths` entries, legacy `https://host/` keys, and `credsStore`/`credHelpers`
helpers. The lookup happens at the first `oci://` acquisition of a run, which
is also when the file's existence is checked, so a run that pulls nothing from
an OCI registry never opens it and CI can pass the flag unconditionally. A host
without an entry stays anonymous. Two shapes are errors rather than silent
anonymous pulls: an identity-token entry (`identitytoken`/`registrytoken`,
which the OCI artifact client cannot present — it sends username and
password only), and a credential helper that cannot run (the error names the
`docker-credential-<helper>` binary; use a plain `auths` file or the
`--oci-*` flags). An auth-less file never reaches the OS keychain for
artifact sources, unlike helm's chart loader. When either `--oci-username` or
`--oci-password` is set, that pair is presented to every OCI artifact
registry the run touches and the file is not opened, matching the Git and
Helm credential flags. Per-registry CLI credential maps are a recorded
follow-up.

TLS behavior follows one deterministic rule. Loopback registries
(localhost) default to plain HTTP for local development and testing; all
other hosts always use TLS. Setting any TLS-implying flag —
`--oci-ca-file`, `--oci-client-cert-file`, `--oci-client-key-file`, or
`--oci-insecure-skip-verify` — disables the loopback plain-HTTP default.
Because the family is global, one TLS-implying flag flips every loopback
registry in the run to TLS; runs without those flags are unaffected. No
flag can downgrade a non-loopback registry to plain HTTP.

TLS caveats:

- `--oci-ca-file` **replaces** the system trust pool for all OCI artifact
  registries in the run; it does not append to it. A run mixing a
  private-CA registry and a public-CA registry needs a bundle containing
  both CAs (or per-registry maps, a recorded follow-up).
- The four TLS flags also configure **OCI Helm chart** pulls, so a chart
  registry behind a corporate CA is reachable. There the bundle is
  **added** to the system pool rather than replacing it, so a run that
  pins a private artifact registry keeps pulling public charts. TLS
  material and `--registry-config` are shared by both OCI flows;
  `--oci-username`/`--oci-password` apply to OCI artifact registries only,
  and the HTTP(S) Helm repository client is left on the system pool.
- `--oci-insecure-skip-verify` has no per-registry scoping — it disables
  certificate verification for every OCI artifact *and* OCI Helm chart
  registry in the run, including public ones, and any
  `--registry-config` or `--oci-*` credentials are then sent over an
  unverified connection.
- The CA file must exist and parse as PEM, and
  `--oci-client-cert-file`/`--oci-client-key-file` must be set together
  as a valid pair. Validation fails fast at request construction with an
  error naming the flag.

Credentials embedded in the repository URL are rejected early with a
redacted error:

```text
OCI artifact oci://registry.example.com/org/app: "@" and "%" are not allowed in an OCI repository URL; put digests in targetRevision and credentials in --oci-username/--oci-password
```

Credential values never enter cache keys, cache metadata, cache events,
diagnostics, or formatted errors, whether they came from the flags or from
the `--registry-config` file. A credentialed run produces the same cache
entries as an anonymous run, and registry authentication failures (401 or
403) redact the presented authorization material and name the flags:

```text
authenticate OCI artifact oci://registry.example.com/org/app revision "1.2.3": ... response status code 401: unauthorized: authentication required; received authorization Basic [redacted] decoded [redacted]:[redacted]; pass --registry-config or --oci-username/--oci-password (registries answer 401/403 for private and for nonexistent repositories alike)
```

Credentials affect only online acquisition. `--offline` runs never
contact the registry, so the `--oci-*` credential flags have no effect on
offline acquisition — with one caveat: TLS validation (missing or
non-PEM `--oci-ca-file`, incomplete or mismatched client cert/key pair)
runs at request construction on every build and diff, including offline
runs and runs with no OCI sources at all, and fails fast. Wrappers that
pass `--oci-*` flags unconditionally should pass valid values or none.
`--registry-config` is different: it is validated only at the first
`oci://` acquisition, so a wrong path fails only runs that pull an OCI
artifact.

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
and tag records, then re-run with `--offline`:

```bash
drydock build apps --path . --oci-cache-dir /var/cache/drydock-oci
drydock build apps --path . --oci-cache-dir /var/cache/drydock-oci --offline
```

An Application whose `oci://` source sets `chart:` was served from the chart
cache by earlier releases and is served from the OCI cache now, so it needs
that warm run — or a non-offline pr-action run — before `--offline` can
render it. Its old chart-cache entries are orphaned; `drydock cache prune
--source chart` removes them, and `--refresh-charts` no longer applies to it.

### Boundaries

- `chart:` on an `oci://` source is ignored and reported once as the
  strict-exempt `source.oci-chart-ignored` warning; `chart:` + `path:` on
  one `oci://` source is valid. The schemeless Helm-OCI shape is the chart
  flow (see [Helm Sources](#helm-sources)).
- OCI sources cannot be `$ref` value-file sources. Argo CD materializes
  referenced sources from Git only, and drydock fails such refs with a clear
  error instead of accidentally supporting them.
- Changed-only diff selection treats OCI sources like chart-only Helm
  sources: they re-render when the Application manifest changes, not on
  arbitrary repository changes, and they never render discovery objects.
  Absolute (`/`-prefixed) `argocd.argoproj.io/manifest-generate-paths`
  entries they declare are honoured, as for chart-only sources.
- Private registries requiring authentication use `--registry-config` (per
  registry host) or the explicit `--oci-username`/`--oci-password` override
  (see [Authentication And TLS](#authentication-and-tls)). Loopback
  registries (localhost) use plain HTTP for local development and testing
  unless a TLS-implying `--oci-*` flag is set; all other hosts use TLS.

## Kustomize Sources

For local Kustomize sources, drydock reads the Argo CD
`kustomize.buildOptions` that it finds in `argocd-cm` or in Argo CD Helm
values, as the kustomize v5.8.3 build command reads them:

| Build option | Behavior |
| --- | --- |
| `--enable-helm` | Turns on `helmCharts`, which drydock renders with its Helm library. Argo CD then adds the kube and API versions of the Application. |
| `--enable-alpha-plugins` | Turns on `helmCharts` too. Argo CD adds no kube or API versions. drydock runs no plugin program. |
| `--helm-kube-version`, `--helm-api-versions` | Give the kube version and the API versions of `helmCharts`. |
| `--load-restrictor` | `LoadRestrictionsRootOnly`, an empty value, or `LoadRestrictionsNone`. |
| `--reorder`, `--enable-managedby-label` | Change the order of the output and add the managed-by label, as in kustomize. |
| `--helm-debug`, `--network`, `--mount`, `-e`, and the other function flags | Change nothing that drydock renders. |
| `--enable-exec` | Without it, kustomize skips exec functions in `generators:` and `transformers:`, and drydock does the same: a skipped generator adds only its configuration, which kustomize drops at the end of the build. With it, an exec function fails, because drydock does not run it. KSOPS is emulated. |
| `--output` | `/dev/stdout` changes nothing. Another path gives no manifests and a warning, as Argo CD reads none. |
| `--helm-command` | A program named `helm` changes nothing. drydock cannot check that the path exists. Another program fails `helmCharts`, because drydock does not run it. |
| `--help` | Fails, because kustomize prints its help instead of manifests. |

A flag that kustomize does not know fails with the kustomize error. With a
found `argocd-cm`, `helmCharts` fail without `--enable-helm` or
`--enable-alpha-plugins`, as in Argo CD. Without one, drydock assumes
`--enable-helm`. A source whose `spec.source.kustomize.version` names no
`kustomize.version.<name>` or `kustomize.path.<name>` key fails, as in
Argo CD. drydock renders every version with its embedded kustomize, and
both key forms produce a warning. With a found `argocd-cm`, KSOPS
generators need `--enable-alpha-plugins`, and a KSOPS exec function also
needs `--enable-exec`, as in kustomize. Argo CD v3.6.1 never applies
`kustomize.buildOptions.<name>`, and drydock warns about it.

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

HTTP(S) refs are treated as single YAML or JSON files. A `resources` entry
accepts either an HTTP(S) file or a Git ref that resolves to a Kustomization
directory. Kustomize folds the deprecated `bases` field into `resources`, so
`bases` entries accept the same refs. `components` entries must use Git refs
that resolve to Kustomization directories.

Ambiguous non-file HTTP(S) URLs are rejected unless they use known Git host
shorthand, a `.git` repository path, or explicit Git syntax such as `git::`,
`ssh://`, or SCP-style `git@host:org/repo.git`.

drydock refuses a remote ref that uses the local file transport, such as
`file:///srv/repo.git//base` or `git::file:///srv/repo.git//base`. kustomize
in Argo CD clones such a ref from the file system of the repo-server. drydock
runs on your workstation or CI runner, so it does not read Git repositories
from that file system. The error says that the ref "uses the local file
transport".

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
| `--chart-cache-max-age DURATION` | Refetch cached charts fetched longer ago than `DURATION` (for example `24h`) and never serve persisted render outputs built from them. Default `0` never expires cached charts. |
| `--chart-cache-dir PATH` | Override the default chart cache root. |
| `--refresh-remotes` | Refresh cached remote Kustomize resources and bypass persisted render outputs for those inputs. |
| `--remote-cache-dir PATH` | Override the default remote-resource cache root. |
| `--oci-cache-dir PATH` | Override the default OCI artifact cache root. |
| `--render-cache` | Reuse persisted Application render outputs across runs (default on). |
| `--render-cache-dir PATH` | Override the default render-output cache root. |
| `--render-cache-max-size QUANTITY` | Size cap before LRU eviction (default `512Mi`). |
| `--refresh-renders` | Ignore persisted render outputs and overwrite them after rendering. |
| `--plugin-cache-dir PATH` | Runtime override for policy-managed container plugin cache mounts. |
| `--registry-config PATH` | Docker/Helm registry config file with per-registry credentials for OCI Helm chart registries and OCI artifact sources; `--oci-username`/`--oci-password` override it for artifact sources. |

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
files (`.argocd-source.yaml` and `.argocd-source-<app>.yaml`, `<app>` being
the Application instance name) are also part of every Application's input
digest; adding, editing, or deleting an override file invalidates that
Application's cached entry. A local source whose spec names
`ARGOCD_APP_REVISION`, `ARGOCD_APP_REVISION_SHORT`, or
`ARGOCD_APP_REVISION_SHORT_8` renders the commit SHA into its output, so its
key also holds the commit and each new commit renders it again. The keys of
other Applications do not hold the commit.

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

A Kustomize input graph includes the files that builtin plugin configs listed
under `transformers`, `generators`, and `validators` read: patch `path` and
`paths`, `replacements[].path`, generator `files` and `envs`, and
`ValueAddTransformer` `targetFilePath`, resolved against the listing
kustomization directory. A plugin config drydock cannot enumerate, such as a
remote config entry, a non-builtin plugin, an unknown kind, or
`HelmChartInflationGenerator`, makes the source skip persistence with
`input-graph-unsupported`.

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

### Chart Cache Max Age

Cached Helm charts never expire by default. Argo CD v3.6.1's repo-server
re-pulls a cached chart archive once it is older than the repo cache
expiration (24h by default), so a republished chart version reaches Argo CD
within that window. To converge the same way, pass `--chart-cache-max-age`
with the same duration (`Config.ChartCacheMaxAge` in the Go API):

```bash
drydock test apps --path . --chart-cache-max-age 24h
```

The age is measured from when drydock fetched the chart into the cache, not
from its last use. An entry with no recorded fetch time, such as a legacy
entry without cache metadata, counts as expired; reusing it never records
one. A positive max age applies to chart sources, Helm `Chart.yaml`
dependencies, and Kustomize `helmCharts` alike:

- An expired chart is refetched once per run. Persisted render outputs built
  from it miss with reason `chart-expired`, re-render from the refetched
  chart, and overwrite their entries.
- A persisted render output is served only while every chart it was built
  from is still the cache entry it read. If the chart was refetched since, for
  example by `--refresh-charts` or by another Application's expiry, the
  lookup misses with reason `chart-unverified`, as do outputs stored without
  chart records.
- Under `--offline`, an expired chart is still used, and each render that
  reads it reports a `source.chart-cache-stale` warning naming the chart, its
  fetch time, and the max age. `--strict` promotes the warning to an error.
  Such renders are never persisted; their store is skipped with reason
  `chart-stale`.

`0`, the default, keeps today's behavior: cached charts are served until
`--refresh-charts`, and negative durations are rejected.

## Credentials

Credentials are non-interactive: drydock never prompts. It does not read
ambient Git credential helpers, ambient Helm registry config, Docker config,
the OS keychain, credential fields from discovered repository Secrets, or live
Argo CD repository state; an explicit `--registry-config` file is read,
including the credential helpers it names. Credentials are explicit, with one
OpenSSH-style exception — Git SSH identities, described in
[SSH Identity Resolution](#ssh-identity-resolution).

| Source | Auth form | Flags |
| --- | --- | --- |
| Git HTTPS | Bearer token | `--git-bearer-token TOKEN` |
| Git HTTPS | Basic auth | `--git-username USER`, `--git-password PASS` |
| Git HTTPS | Hosts that receive the credentials (required) | `--git-auth-host HOST[:PORT]` (repeatable) |
| Git SSH | SSH key or ambient identity | `--git-ssh-key-file PATH` (optional; see [SSH Identity Resolution](#ssh-identity-resolution)), `--git-known-hosts-file PATH` (optional), `--git-ssh-passphrase PASSPHRASE` |
| HTTP(S) Helm | Bearer token | `--helm-bearer-token TOKEN` |
| HTTP(S) Helm | Basic auth | `--helm-username USER`, `--helm-password PASS` |
| OCI Helm | Registry config | `--registry-config PATH` |
| OCI Helm | TLS material | `--oci-ca-file PATH`, `--oci-client-cert-file PATH`, `--oci-client-key-file PATH`, `--oci-insecure-skip-verify` |
| OCI artifact | Registry config (per registry host) | `--registry-config PATH` |
| OCI artifact | Basic auth (global override) | `--oci-username USER`, `--oci-password PASS` |
| OCI artifact | TLS material | `--oci-ca-file PATH`, `--oci-client-cert-file PATH`, `--oci-client-key-file PATH`, `--oci-insecure-skip-verify` |
| HTTP(S) remote files | Bearer token | `--remote-bearer-token TOKEN` |
| HTTP(S) remote files | Basic auth | `--remote-username USER`, `--remote-password PASS` |
| HTTP(S) remote files | Hosts that receive the credentials (required) | `--remote-auth-host HOST[:PORT]` (repeatable) |

HTTP(S) remote files are remote Kustomize HTTP(S) resources, Helm value file
URLs, and Helm `fileParameters` URLs. For Git HTTPS, HTTP(S) Helm, and HTTP(S)
remote files, bearer auth wins when both bearer and basic credentials are
provided.

drydock sends the `--remote-*` credentials only to the hosts that you name
with `--remote-auth-host`. Give the flag once for each host. Write an entry as
`host` or `host:port` for HTTPS. To allow plain HTTP for a host, write the
entry as `http://host` or `http://host:port`. With an `http://` entry, every
HTTP proxy and every system on the network path can read the credentials. A request receives the
credentials only when its scheme, host, and port match an entry. The host
match ignores case, and an entry without a port has the default port of its
scheme. A subdomain or a parent domain does not match. Other hosts get the
request without credentials.

If you set `--remote-username`, `--remote-password`, or `--remote-bearer-token`
without `--remote-auth-host`, drydock stops before it fetches anything. The
error names `--remote-auth-host`. On a redirect, drydock removes the
credentials when the new location has another scheme, host, or port than the
first request, and it does not add them again later in that redirect chain.

```bash
drydock test apps --path . \
  --remote-bearer-token "$REMOTE_TOKEN" \
  --remote-auth-host files.example.com
```

Argo CD v3.6.1 has no flags of this kind. It fetches remote Kustomize HTTP(S)
resources, Helm value file URLs, and `fileParameters` URLs without
credentials.

Kustomize Git remote refs reuse the `--git-*` credentials, including the SSH
identity resolution below, but use the remote Kustomize cache and `--offline`
or `--refresh-remotes` behavior.

### Git HTTPS credential hosts

drydock sends `--git-username`, `--git-password`, and `--git-bearer-token`
only to the Git hosts that you name with `--git-auth-host`. The entries have
the same form as `--remote-auth-host` entries. Give the flag once for each
host. Write an entry as `host` or `host:port` for HTTPS. To allow plain HTTP
for a host, write the entry as `http://host` or `http://host:port`. With an
`http://` entry, every HTTP proxy and every system on the network path can
read the credentials.

A Git URL receives the credentials only when its scheme, host, and port match
an entry. The host match ignores case, and an entry without a port has the
default port of its scheme. A subdomain or a parent domain does not match.
drydock fetches a Git URL on any other host without credentials. The rule
applies to every Git fetch over HTTP(S):

- Application and ApplicationSet template sources that drydock fetches into
  the Git cache, including the revision lookup and later fetches with
  `--refresh-git`.
- Remote Kustomize Git bases.
- Submodules of these repositories and of `--ref` snapshots. A submodule also
  must be on the host and port of its repository.
- Git LFS batch requests of these repositories and of `--ref` snapshots, also
  to a server that `.lfsconfig` names. The object downloads never get the
  credentials, and neither does the Git LFS server of an SSH repository.

If you set an HTTP Git credential without `--git-auth-host`, drydock stops
before it fetches anything. The error names `--git-auth-host`.

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main \
  --git-bearer-token "$GITHUB_TOKEN" \
  --git-auth-host github.com
```

The Git client sends the credentials with the first request, before the
server asks for them. Without the host list, a pull request can add an
Application or a remote base on a host that it controls and collect the
token from the CI run. When the server of a listed host redirects the first
request to another scheme, host, or port, the Git client removes the
credentials. It sends every later request of that fetch to the new location
without them. One exception comes from the Git client: a redirect from
`http://host` to `https://host` on the default ports keeps the credentials,
because the host stays the same and the connection becomes encrypted.

A user name and password in the URL itself, such as
`https://ci-user:TOKEN@git.example.com/org/repo.git`, go only to the host of
that URL. They take the place of the `--git-*` credentials when that host has
no `--git-auth-host` entry. drydock removes them from the URL before it
stores the URL in the cache.

drydock refuses a Git URL with `?`, `#`, or `\` between `://` and the first
`/`, such as `https://git.example.com?@other.example.net/org/repo.git`.
Parsers read another host from such a URL. A user name or password in the URL
is part of that host part, so write a `?` or `#` in it percent-encoded, as
`%3F` or `%23`. drydock also checks each Git cache
entry before it uses it. If the origin of the entry names another repository
than the request, drydock clones the request URL into the entry again. With
`--offline`, the request fails as a cache miss. Every fetch goes to the URL of
the request, never to a URL that the cache entry stores.

SSH sources do not use `--git-auth-host`. An SSH key never leaves the
machine: the client proves that it holds the key, and the server learns only
the public key. Host key verification against known_hosts also makes sure
that the server is the host in the URL.

In Argo CD v3.6.1, each repository gets its own credentials from its
repository Secret, or from the repository credential template with the
longest matching URL prefix. A repository that matches neither is fetched
without credentials. drydock has one set of `--git-*` credentials, so
`--git-auth-host` takes the place of these repository URLs. List the hosts of
the repositories that your Argo CD instance has credentials for. See
[Compatibility](/compatibility/) for the differences.

Supported SSH URL forms are:

| Form | Username behavior |
| --- | --- |
| `ssh://git@host/org/repo.git` | Uses the explicit `git` username. |
| `git@host:org/repo.git` | Uses the explicit `git` username. |
| `ssh://host/org/repo.git` | Defaults the missing username to `git`. |

Passwords, bearer tokens, SSH private keys, SSH passphrases, registry
credential values, and credential-bearing URLs are never printed in diagnostics
or formatted errors.

## SSH Identity Resolution

`--git-ssh-key-file` is optional. drydock resolves a Git SSH identity in the
order OpenSSH uses:

1. `--git-ssh-key-file` (with `--git-ssh-passphrase` for an encrypted key).
   `--git-ssh-passphrase` applies only to that key; on its own it does nothing.
   When `--git-ssh-key-file` is set, nothing else is tried — there is no
   fallback behind an explicit key.
2. The ssh-agent. The socket comes from an `IdentityAgent` directive in
   `~/.ssh/config` when one applies to the host, otherwise from
   `$SSH_AUTH_SOCK`. `IdentityAgent none` disables the agent entirely.
3. `IdentityFile` entries in `~/.ssh/config` for the host.
4. The default identities `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa`, and
   `~/.ssh/id_rsa`.

Agent keys and file identities are offered together, agent keys first. A file
identity whose public key the agent already offers is dropped. File identities
are only loaded up to six total including the agent's keys — the OpenSSH
`MaxAuthTries` default — so a large `~/.ssh` cannot exhaust the server's auth
attempts before a usable key is reached. An agent that holds more than six keys
offers all of them; that is the agent's choice, not drydock's.

Host keys are always verified. The known_hosts list is `--git-known-hosts-file`
when set, otherwise `$SSH_KNOWN_HOSTS` (a path list), otherwise
`~/.ssh/known_hosts` and `/etc/ssh/ssh_known_hosts`. Files in the
`$SSH_KNOWN_HOSTS` list and the defaults that do not exist are skipped; a
missing `--git-known-hosts-file` is an error. A file that exists but cannot be
read or parsed is an error, whether it came from `--git-known-hosts-file` or
from the defaults. When no
known_hosts file is found at all, the run fails with the `ssh-keyscan` command
that fixes it. There is no flag that skips host key verification.

drydock never prompts, so a passphrase-protected ambient key cannot be
unlocked. Such a key is skipped, and its basename is named in the error when no
other identity works. Pass it explicitly with `--git-ssh-key-file` and
`--git-ssh-passphrase`.

Only `Hostname`, `Port`, `IdentityAgent`, and `IdentityFile` are read from
`~/.ssh/config` and `/etc/ssh/ssh_config`; other directives, including
`ProxyCommand`, are ignored. `Match` blocks are not supported: a file whose
stanza for the host contains one contributes no directives for that host, and
the reason is reported in the error when no identity is found. The default
identities are still tried.

### SSH Identity Exposure

An Application's `repoURL` is repository content. On a runner that has an agent
socket or a usable `~/.ssh` identity, a pull request that adds an Application
with an `ssh://` `repoURL` can make drydock attempt an authenticated SSH
handshake against a host the pull request chose.

Host keys are verified during key exchange, before any identity is offered, so
a host that is not in the run's known_hosts files gets a dropped connection and
no public key. The residual surface is the hosts already listed in those
known_hosts files; against those, public-key authentication reveals only the
public key, never the private key or the passphrase.

Passing `--git-ssh-key-file` disables the ambient lookup entirely, so only that
one key is offered, but it does not restrict which hosts a run may dial. To
bound that, give the run a `--git-known-hosts-file` that lists only the hosts it
is allowed to reach, or start the agent only for the steps that need it.

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

The chart cache also holds the recorded version list of each chart whose
version range drydock resolved. An offline run resolves the range from that
list. Every resolve, online or offline, tries to record when it last used the
list, and an age prune of the chart cache removes the lists that no run used
within the duration, the same as chart entries. Recording a use is best
effort: drydock skips it when the cache is read-only, when another process
holds the lock on the lists for more than 5 seconds, or when the write fails. `cache list` does not show the lists,
and `cache prune` reports how many it removed. A list keeps its fetch time
apart from its last use, and `--chart-cache-max-age` judges that fetch time.

A submodule store clones into a temporary `<entry>.clone-*` directory and
moves it into place when the clone is complete. A drydock process that is
killed during a clone leaves that directory behind. `cache prune` removes
such a directory in the Git and remote caches when nothing in it changed
for one hour, so it never removes a clone that another process is still
writing. `cache delete` of a Git entry removes the stale clones of that
entry the same way. Both commands report how many they removed.

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
