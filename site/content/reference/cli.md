---
title: CLI Reference
aliases:
  - /docs/usage/
---

Use this page for command behavior that is too detailed for the quickstart.
For task-oriented examples, start with [Getting started](/getting-started/),
[Local diffs](/workflows/local-diffs/), or [GitHub Actions](/workflows/github-actions/).

## Command Map

| Goal | Command |
| --- | --- |
| List discovered Applications | `drydock get apps --path .` |
| List rendered image references | `drydock get images --path . -o name` |
| Build manifests | `drydock build apps --path .` |
| Render-test Applications | `drydock test apps --path .` |
| Compare rendered manifests | `drydock diff apps --repo . --ref HEAD --ref-orig main` |
| Compare rendered images | `drydock diff images --repo . --ref HEAD --ref-orig main` |
| Inspect diagnostics | `drydock diag --path .` |
| Inspect cache roots and entries | `drydock cache path`, `drydock cache list -o json` |

All render-backed commands are runtime-offline: they do not contact live Argo
CD or Kubernetes. Declared Git, HTTP Helm, OCI Helm, and remote Kustomize
sources may still be fetched into explicit drydock caches unless `--offline`
is set.

## Discovery Commands

List discovered `Application` CRs, supported generated `ApplicationSet`
Applications, and rendered child Applications from app-of-apps bootstrap
sources:

```bash
drydock get apps --path .
```

`get apps` defaults to table output and supports `-o table`, `-o name`,
`-o json`, and `-o yaml`. Use `-l` or `--selector` with Kubernetes label
selector syntax to match `Application.metadata.labels`:

```bash
drydock get apps --path . -l 'env in (prod,stage),tier!=test'
```

List rendered image references from Applications:

```bash
drydock get images --path . -o name
```

`get images` uses the same output formats as `get apps`. Diagnostics are
printed to stderr for both commands.

By default, fleet commands recursively render discovered Applications to find
additional Argo CD `Application`, `ApplicationSet`, `AppProject`, and settings
objects in their desired output. Discovery stops when it converges, with a
default maximum depth of `4`.

Use `--discovery-mode static` to disable recursive rendered fleet discovery.
Committed `ApplicationSet` objects still expand through supported offline
generators. Use `--max-discovery-depth 0` to keep normal static discovery and
explicit `--discover-kustomize` rendering while disabling recursive rendered
fleet discovery.

Static committed objects take precedence over default rendered fleet objects
with the same API version, kind, namespace, and name. Rendered duplicates emit
diagnostics instead of silently replacing committed intent.

For repositories that commit Argo CD bootstrap inputs as Kustomize bases or
overlays, add one or more local Kustomize discovery entrypoints:

```bash
drydock get apps --path . --discover-kustomize argocd/overlays/prod
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

The path is relative to `--path`, must not escape through `..`, and must not
include symlinked path components. Explicitly rendered Kustomize objects take
precedence over committed duplicates with the same identity.

For generated ApplicationSet behavior, see [ApplicationSets](/docs/applicationsets/).
For repository shapes, see [Repository topologies](/concepts/topologies/).

## Build Commands

Build every discovered Application:

```bash
drydock build apps --path .
```

Build exactly one Application by `metadata.name`:

```bash
drydock build app renovate --path .
```

Use `NAMESPACE/NAME` when a name appears in multiple namespaces:

```bash
drydock build app argocd/my-app --path .
```

`build app` errors when no discovered Application matches. The unqualified
`NAME` form must identify exactly one Application.

When one selected Application fails to render, embedding callers receive
partial results containing successful manifests, diagnostics, and
per-Application statuses. CLI `build` commands keep stdout parseable:
diagnostics go to stderr, and invalid partial manifest streams are not mixed
into stdout.

Use `--parallelism N` on render-backed commands to render up to `N`
Applications concurrently. Multi-Application `test` and `diff` commands default
to bounded host CPU parallelism; single-Application commands default to `1`.
Parallel rendering preserves selected Application order for manifests,
statuses, diagnostics, cache events, and structured output.

Rendering supports directory sources, directory Jsonnet, Kustomize sources,
local Helm charts, Kustomize `helmCharts`, remote Kustomize HTTP(S) files and
Git refs, and Argo CD chart-only remote Helm sources. Path-based Git sources
use the local `--path` tree when the source path exists there. Use
`--repo-map URL=PATH` to force a source repository URL to a local checkout.

Use `--enable-avp-compat` when an Application explicitly names the
`argocd-vault-plugin` plugin and you want drydock to render it without running
AVP. drydock renders the source through its native renderers, then replaces
supported AVP placeholders with deterministic redacted values.

AVP compatibility does not execute the AVP binary, a config-management plugin
command, a shell, the Helm CLI, or the Kustomize CLI. For trusted
command-backed plugins, use [Plugin policy](/plugin-policy/) with
`engine: exec` or `engine: container` and `--enable-plugins`.

Repositories tagged with `argocd` or `gitops` are not always Argo CD
Application fleet repositories. `drydock test apps` reports zero Applications
when no `Application` or supported `ApplicationSet` objects are discovered.

For acquisition, cache, auth, and remote source details, see
[Source acquisition](/concepts/source-acquisition/).

## Render Tests

Test every discovered Application without printing manifest bodies:

```bash
drydock test apps --path .
```

Test exactly one Application:

```bash
drydock test app renovate --path .
drydock test app argocd/my-app --path .
```

Default text output prints one status line per selected Application:

```text
PASS renovate
FAIL argocd/broken Application argocd/broken source[0] path="..." ...
SKIPPED argocd/skipped unsupported ApplicationSet generator ...
```

When `test apps` discovers no Applications, text output prints
`No Applications discovered.` and structured JSON output prints `[]`.

Status values are:

| Status | Meaning |
| --- | --- |
| `PASS` | The Application rendered successfully. |
| `FAIL` | Rendering, planning, or runtime validation failed. |
| `SKIPPED` | Discovery or expansion preconditions prevented safe rendering. |

`test apps` and `test app` return exit code `0` only when every selected
Application passes. Any `FAIL`, `SKIPPED`, or runtime failure returns exit code
`2`.

`drydock test apps` and `drydock test app` validate configured custom Argo CD
health Lua by default. Validation is offline and runs against rendered desired
manifests, not live Argo CD Application health aggregation. Use
`--skip-lua-health` to isolate render failures or benchmark render-only
behavior:

```bash
drydock test apps --path . --skip-lua-health
```

When `test apps` writes text output to a terminal, statuses stream as each
Application completes. Status labels are colored, a single stderr progress
counter updates in place when stderr is also a terminal, and a final summary is
printed after the status lines. Redirected text output stays plain and
buffered.

Structured status output is available with `-o json` and `-o yaml`:

```bash
drydock test apps --path . -o json
drydock test apps --path . -o yaml
```

For stdout, stderr, and format rules, see [Output controls](/workflows/output/).

## Diagnostics

Run repository diagnostics without printing rendered manifests:

```bash
drydock diag --path .
```

By default, `diag` uses static repository discovery, ApplicationSet expansion,
and settings metadata without rendering Applications. It prints diagnostics to
stderr and returns an error when runtime failures or error-severity diagnostics
are found. Use `--strict` to promote warnings to errors.

`diag` refuses broad roots such as the filesystem root or the current user's
home directory. Run it from the GitOps repository root or pass `--path` to that
repository.

Use `--render` when the diagnostic report should include render-backed
diagnostics. `--cache-events` and `--plugin-executions` also opt into the
render-backed path so source-acquisition and plugin execution metadata reflect
Application rendering.

Structured diagnostic output can include a redacted settings summary:

```bash
drydock diag --path . --settings -o json
drydock diag --path . --settings -o yaml
```

The settings summary is CLI-only. It reports parsed resource-customization
metadata such as action names, `useOpenLibs`, and SHA-256 hashes for
health/action Lua. It does not print raw Lua bodies, embedded secret-looking
strings, or live-cluster state.

When local `AppProject` manifests are present, `build`, `test`, `diff`, and the
Go API report source repository and destination validation diagnostics from
those manifests. `diag --render` includes the same render-backed project
diagnostics, including rendered-resource allow/deny policy.

Project diagnostics default to `--project-diagnostics=actionable`. In this
mode, drydock keeps known local denials visible, including missing projects,
denied source repositories, denied destinations, denied source namespaces, and
denied rendered resources. AppProject diagnostics that cannot be conclusively
enforced offline are hidden by default.

Use `--project-diagnostics=all` for full AppProject audit output during
compatibility investigations, or `--project-diagnostics=off` to suppress
AppProject diagnostics entirely.

RBAC roles and policies are parsed and reported as metadata only when full
project diagnostics are enabled; Argo CD authorization is not simulated. Sync
windows, orphaned-resource tracking, source signature verification, and
destination service account impersonation are runtime-bound and are not
evaluated offline.

Repository credential matching diagnostics use discovered repository Secret
metadata only and never read secret credential fields. Cluster Secret
diagnostics likewise use only `name`, `server`, `namespaces`,
`clusterResources`, and `project` metadata; credential/config fields are not
decoded, retained, or printed.

For stable diagnostic codes in CI, use rendered structured diagnostics:

```bash
drydock diag --path . --render -o json
```

`argocd-cmd-params-cm` settings are parsed as runtime-boundary metadata when
they imply live repo-server, controller, or ApplicationSet controller behavior.
They may produce diagnostics and settings summaries, but they do not mutate
drydock render behavior.

## Cache Commands

Print resolved cache roots:

```bash
drydock cache path
```

List recognized Git, chart, and remote Kustomize cache entries:

```bash
drydock cache list
drydock cache list --source chart -o json
```

Report stale entries without deleting them:

```bash
drydock cache prune --older-than 720h --dry-run
```

Delete a specific entry or all selected entries:

```bash
drydock cache delete --source git --key HASH --yes
drydock cache delete --source remote --all --dry-run
```

`cache prune` and `cache delete` require `--yes` for non-dry-run deletion.
Dry-runs never require confirmation and leave cache files in place.

Cache lifecycle commands are local filesystem operations only. They do not
render Applications, clone or fetch Git repositories, fetch Helm charts, fetch
remote Kustomize resources, or read credential flags. They operate only on
recognized drydock cache entry roots and reject cache roots that resolve inside
the current working directory, selected repository roots, Git repository trees,
or symlink-resolved equivalents.

For cache boundaries, offline behavior, and credentials, see
[Source acquisition](/concepts/source-acquisition/).
