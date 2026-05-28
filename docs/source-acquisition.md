# Source Acquisition

`drydock` renders from local files and explicit source caches. Declared Git,
HTTP Helm, OCI Helm, and remote Kustomize sources may be fetched into those
caches unless `--offline` is set. The tool does not read ambient Git
credential helpers, ambient Helm registry config, or live Argo CD repository
state.

## Resolution Model

Repository source resolution is deterministic:

1. Explicit `--repo-map URL=PATH`.
2. Existing local source path under the selected repository tree.
3. Declared Git cache/fetch behavior for unmapped external repositories.
4. Clear failure when a source cannot be resolved.

`--repo-map` wins over local source-path fallback and network fetching.
`--path` and `--path-orig` are authoritative for mapped pull-request
repositories and override declared revisions.

Ref-only sources are allowed and render no manifests. `$ref/...` Helm value
files resolve from the referenced source root, not from its `path`.

## Kustomize Sources

For local Kustomize sources, drydock applies the supported subset of Argo CD
`kustomize.buildOptions` discovered from `argocd-cm` or Argo CD Helm values:

- `--enable-helm`
- `--helm-api-versions`
- `--load-restrictor=LoadRestrictionsRootOnly`
- `--load-restrictor=LoadRestrictionsNone`

Unsupported build options fail explicitly instead of being ignored.
Version-specific `kustomize.buildOptions.<version>` and
`kustomize.path.<version>` settings produce warnings because drydock uses
embedded Go libraries instead of selecting external Kustomize binaries.
Kustomize `helmCharts` render natively through the same Helm library path used
for Argo CD chart sources; no external Kustomize CLI is required for chart
inflation.

Supported Kustomize remote refs include:

- `https://github.com/org/repo?ref=v1`
- `https://github.com/org/repo.git?ref=v1`
- `https://github.com/org/repo//path?ref=v1`
- `git::https://github.com/org/repo.git?ref=v1`
- `git::https://github.com/org/repo.git//path?ref=v1`
- `ssh://git@github.com/org/repo.git?ref=v1`
- `ssh://git@github.com/org/repo.git//path?ref=v1`
- `git@github.com:org/repo.git?ref=v1`
- `git@github.com:org/repo.git//path?ref=v1`

Remote Kustomize refs are supported in `resources`, `bases`, `components`,
`patches.path`, `patchesJson6902.path`, non-inline `patchesStrategicMerge`,
`generators`, `transformers`, `validators`, `configurations`, `crds`,
`openapi.path`, `replacements.path`, and ConfigMap/Secret generator `files`,
`envs`, and `env` entries.

HTTP(S) refs are treated as single YAML/JSON files. Directory-shaped fields,
including remote bases and components, must use Git refs that resolve to
Kustomization directories. The renderer copies acquired content into a
temporary workspace under generated `.drydock` paths and does not write
generated manifests into the source tree.

Git refs may omit `ref`; omitted or empty `ref` values resolve to `HEAD`.
Root Git refs copy the repository root as the remote Kustomization root.
Ambiguous non-file HTTP(S) URLs are rejected unless they use known Git host
shorthand, a `.git` repository path, or explicit Git syntax such as `git::`,
`ssh://`, or SCP-style `git@host:org/repo.git`.

## Network And Cache Flags

| Flag | Behavior |
| --- | --- |
| `--offline` | Disable Git, Helm chart, and remote Kustomize network fetching. |
| `--repo-map URL=PATH` | Map a source repository URL to a local checkout. |
| `--refresh-git` | Fetch cached Git repositories before rendering. |
| `--git-cache-dir PATH` | Override the default Git repository cache root. |
| `--refresh-charts` | Refresh cached immutable chart entries before rendering. |
| `--chart-cache-dir PATH` | Override the default chart cache root. |
| `--refresh-remotes` | Refresh cached remote Kustomize resources before rendering. |
| `--remote-cache-dir PATH` | Override the default remote-resource cache root. |
| `--registry-config PATH` | Supply the only Helm OCI registry credentials. |

Offline render/build/diff commands require cache hits, repo maps, local files,
or local chart availability. Populate caches with a prior non-offline render
using the relevant auth, cache-dir, and refresh flags.

Caches must stay outside Git repository trees. Cache entries include hidden
`.drydock-cache/metadata.json` sidecars with redacted target metadata. Older
hash-only entries are listed as legacy entries when their filesystem layout is
recognized.

## Credentials

Authenticated source handling is explicit and non-interactive:

- Git HTTPS auth supports bearer and basic auth; bearer wins.
- Git SSH auth requires explicit key and known-hosts files.
- HTTP(S) Helm auth supports bearer and basic auth; bearer wins.
- HTTP(S) remote Kustomize auth supports bearer and basic auth; bearer wins.
- OCI Helm auth is provided only through `--registry-config`.

Credential flags:

| Source | Flags |
| --- | --- |
| Git HTTPS bearer | `--git-bearer-token TOKEN` |
| Git HTTPS basic | `--git-username USER`, `--git-password PASS` |
| Git SSH | `--git-ssh-key-file PATH`, `--git-known-hosts-file PATH`, `--git-ssh-passphrase PASSPHRASE` |
| HTTP(S) Helm bearer | `--helm-bearer-token TOKEN` |
| HTTP(S) Helm basic | `--helm-username USER`, `--helm-password PASS` |
| HTTP(S) remote Kustomize bearer | `--remote-bearer-token TOKEN` |
| HTTP(S) remote Kustomize basic | `--remote-username USER`, `--remote-password PASS` |

Kustomize Git remote refs reuse the explicit `--git-*` credentials, but use
the remote Kustomize cache and `--offline`/`--refresh-remotes` behavior.

Supported SSH URL forms are `ssh://git@host/org/repo.git`,
`git@host:org/repo.git`, and `ssh://host/org/repo.git`. Missing usernames
default to `git`.

Passwords, bearer tokens, SSH private keys, SSH passphrases, registry
credential values, and credential-bearing URLs are never printed in diagnostics
or formatted errors.

## Cache Lifecycle Boundary

Cache lifecycle commands are local filesystem operations only. They do not:

- render Applications
- clone or fetch Git repositories
- fetch Helm charts
- fetch remote Kustomize resources
- read credential flags
- retry failed network or authentication acquisitions

`cache prune` and `cache delete` operate only on recognized drydock cache entry
roots. They reject cache roots that resolve inside the current working
directory, selected repository roots, Git repository trees, or symlink-resolved
equivalents. Non-dry-run deletion requires `--yes`; dry-runs never require
confirmation.

A shared content-addressed store with ref tables, leases, and mark-sweep
collection is intentionally deferred. It would be useful only after drydock has
multiple cache surfaces sharing immutable blobs.
