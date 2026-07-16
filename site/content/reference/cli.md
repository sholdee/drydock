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
| Scaffold plugin policy | `drydock plugin-policy init --path .` |
| Check plugin policy readiness | `drydock plugin-policy doctor --path .` |

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

For repositories that also commit non-deployable YAML, such as unrendered
chart templates that fail strict decoding, exclude those files from discovery
with repeatable `--discover-ignore` globs:

```bash
drydock get apps --path . --discover-ignore 'templates/**'
drydock test apps --path . --discover-ignore 'charts/**/templates/**'
```

Patterns are repository-relative, slash-normalized doublestar globs with the
same syntax as `--changed-only-ignore`. Matching happens before any decoding,
and ignored files are skipped even when named by explicit app manifest paths.
The flag only affects top-level repository discovery of static manifests; it
does not filter ApplicationSet Git generator file matching, Helm value files,
Kustomize inputs, changed-path selection, or rendered fleet discovery.

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

Capability-gated charts can render optional resources only when a Kubernetes
version or API version is available. drydock derives a default Kubernetes
version from its embedded Helm libraries, so the default already
tracks a real cluster version. Use `--kube-version` to override the per-app
`kubeVersion` with a specific version; the global override wins. Use repeatable
`--api-versions` to add API versions for capability-gated rendering, unioned
(deduped and sorted) with each app's per-app `apiVersions` rather than replacing
them. Values accept the `group/version` form (`monitoring.coreos.com/v1`) or the
full `group/version/Kind` form (`monitoring.coreos.com/v1/ServiceMonitor`); many
charts check the `group/version/Kind` form against `.Capabilities.APIVersions`,
so pass that form when a gated resource still does not render. Both flags are
common render flags, available on the render-backed commands (`build`, `test`,
`diff`, and `get`, in their `app`/`apps` forms). See
[Troubleshooting renders](/cookbook/troubleshooting-renders/) for examples.

Application sources that explicitly name the `argocd-vault-plugin` plugin use
native AVP compatibility by default. The same native path is used for
discovered CMP aliases whose generate command safely normalizes to
`argocd-vault-plugin generate .`. drydock renders the source through its native
renderers, then replaces supported AVP placeholders with deterministic redacted
values. Use `--enable-avp-compat` to force the same placeholder redaction for
ordinary native-rendered sources that are not modeled as plugin sources.

AVP compatibility does not execute the AVP binary, a config-management plugin
command, a shell, the Helm CLI, or the Kustomize CLI. For trusted
command-backed plugins, use [Plugin policy](/plugin-policy/) with
`engine: exec` or `engine: container` and `--enable-plugins`.

Repositories using [KSOPS](https://github.com/viaduct-ai/kustomize-sops) can
use `--enable-ksops-compat` to render `apiVersion: viaduct.ai/v1 / kind: ksops`
kustomize generators as deterministic placeholder manifests without decryption.
Secret structure and key names are preserved; encrypted values are replaced by
a deterministic placeholder (`drydock-ksops-redacted-<12hex>`), with Secret
`data:`/`binaryData:` and ConfigMap `binaryData:` values encoded as valid
base64 (plain elsewhere). No decryption key, network call, or exec is
involved. Pair with `--skip-secrets` to suppress placeholder Secrets from diff
output.

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main --enable-ksops-compat --skip-secrets
```

Value-only sops rotations (changing only the ciphertext without renaming keys)
render identically under `--enable-ksops-compat` because the placeholder
derives from key identity, not ciphertext. "No diff" does not mean "no change"
for rotated secrets.

Repositories tagged with `argocd` or `gitops` are not always Argo CD
Application fleet repositories. `drydock test apps` reports zero Applications
when no `Application` or supported `ApplicationSet` objects are discovered.

For acquisition, cache, auth, and remote source details, see
[Source acquisition](/concepts/source-acquisition/).

## Plugin Policy Onboarding

`drydock plugin-policy init` statically inspects Applications, supported static
ApplicationSet inputs, readable Argo CD CMP settings, and sidecar image
candidates. It disables plugin policy loading for the inspection and does not
render Applications or run plugin commands.

When trusted CMP evidence is clear, `init` can scaffold native policy entries:
AVP CMP aliases become `engine: avp-compat`, and compatible `kustomize build`
CMPs become `engine: native-kustomize`. Other plugins use the fallback
scaffold engine.

For repositories whose Applications are visible only after plugin-rendered
bootstrap, start from the matching plugin policy example or existing CMP
policy, then use `plugin-policy doctor` to check readiness.

By default, `init` writes YAML to stdout:

```bash
drydock plugin-policy init --path .
```

Use `--write` for `.drydock/plugins.yaml`, or `--output` for another
repository-relative policy path. Existing files require `--overwrite`.

```bash
drydock plugin-policy init --path . --write
drydock plugin-policy init --path . --output .drydock/plugins.container.yaml
```

Generated YAML includes the `yaml-language-server` schema modeline. The
fallback scaffold engine is `container`; pass `--engine exec` explicitly for
host-process policy. An explicit `--engine` overrides native engine
suggestions. Digest-pinned sidecar images may be inferred. Tag-only image
candidates remain placeholders unless `--allow-mutable-image-tags` is passed.

`drydock plugin-policy doctor` reports onboarding readiness and gate failures
without executing plugins:

```bash
drydock plugin-policy doctor --path .
drydock plugin-policy doctor --path . --plugin-policy-ref main --enable-plugins -o json
```

Default missing `.drydock/plugins.yaml` is reported as readiness `FAIL`; an
explicit missing `--plugin-policy-path` is a command error. `-o json` provides
deterministic `status` and issue `code` fields. `--strict` exits nonzero on
readiness `FAIL` after rendering the report.

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

List recognized Git, chart, remote Kustomize, and render output cache
entries:

```bash
drydock cache list
drydock cache list --source chart -o json
```

Report stale entries without deleting them:

```bash
drydock cache prune --older-than 720h --dry-run
drydock cache prune --max-size 4Gi --dry-run
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
recognized drydock Git, chart, remote-resource, and render output cache entry
roots and reject cache roots that resolve inside the current working
directory, selected repository roots, Git repository trees, or
symlink-resolved equivalents. Render output entries are selected with
`--source render` and `--render-cache-dir`; plugin cache mount roots remain
excluded (policy-managed).

For cache boundaries, offline behavior, and credentials, see
[Source acquisition](/concepts/source-acquisition/).
