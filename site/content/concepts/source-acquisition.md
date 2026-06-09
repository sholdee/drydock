---
title: Source Acquisition
aliases:
  - /docs/source-acquisition/
---

drydock renders from local files and explicit source caches. It may fetch
declared Git, HTTP Helm, OCI Helm, and remote Kustomize inputs unless
`--offline` is set. It does not read ambient Git credential helpers, ambient
Helm registry config, or live Argo CD repository state.

## Resolution Order

For repository sources, drydock resolves deterministically:

1. Explicit `--repo-map URL=PATH`.
2. Existing local source path under the selected repository tree.
3. Declared Git cache or fetch behavior for unmapped external repositories.
4. Clear failure.

`--repo-map` wins over local fallback and network fetching. Use it for adjacent
local checkouts or CI jobs that prepare dependencies explicitly:

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
```

For mapped pull-request repositories, `--path` and `--path-orig` are
authoritative source roots and override declared revisions.

Ref-only sources are allowed and render no manifests. `$ref/...` Helm value
files and file parameters resolve from the referenced source root, not from its
`path`.

## Helm Sources

Chart-only HTTP(S) and OCI Helm sources may be fetched into the chart cache
unless `--offline` is set. Local Helm chart sources render from the repository
tree.

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
| `--offline` | Disable Git, Helm chart, and remote Kustomize network fetching. |
| `--repo-map URL=PATH` | Map a source repository URL to a local checkout. |
| `--refresh-git` | Fetch cached Git repositories before rendering. |
| `--git-cache-dir PATH` | Override the default Git repository cache root. |
| `--refresh-charts` | Refresh cached immutable chart entries before rendering. |
| `--chart-cache-dir PATH` | Override the default chart cache root. |
| `--refresh-remotes` | Refresh cached remote Kustomize resources before rendering. |
| `--remote-cache-dir PATH` | Override the default remote-resource cache root. |
| `--plugin-cache-dir PATH` | Runtime override for policy-managed container plugin cache mounts. |
| `--registry-config PATH` | Supply the only Helm OCI registry credentials. |

Render-time Git, chart, and remote-resource caches must stay outside the
current repository tree, compared repository trees, repo-map roots, and their
symlink-resolved equivalents. Drydock validates these roots before cache reads,
fetches, or writes so a repository cannot double as its own mutable source
cache.

Cache entries include hidden `.drydock-cache/metadata.json` sidecars with
redacted target metadata. Older hash-only entries are listed as legacy entries
when their filesystem layout is recognized.

`--plugin-cache-dir` is separate: it is a render-time override for
policy-managed container plugin cache mounts. Cache lifecycle commands still
manage only Git, chart, and remote-resource cache entry roots for now.

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
```

They do not:

- render Applications
- clone or fetch Git repositories
- fetch Helm charts
- fetch remote Kustomize resources
- read credential flags
- retry failed network or authentication acquisitions

`cache prune` and `cache delete` operate only on recognized drydock Git, chart,
and remote-resource cache entry roots for now. They do not list, prune, or
delete plugin cache mount roots selected with `--plugin-cache-dir`.

Cache lifecycle commands reject cache roots that resolve inside the current
working directory, selected repository roots, Git repository trees, or
symlink-resolved equivalents. Non-dry-run deletion requires `--yes`; dry-runs
never require confirmation.

A shared content-addressed store with ref tables, leases, and mark-sweep
collection is intentionally deferred. It would be useful only after drydock has
multiple cache surfaces sharing immutable blobs.
