# drydock

`drydock` is a Go CLI and embeddable Go package for offline Argo CD GitOps
repository analysis.

Inspect your Argo CD fleet without getting wet.

The MVP goal is desired-vs-desired pull request diffing: compare a current
repository tree with a baseline tree and inspect the rendered Kubernetes
manifests that changed. The currently wired commands are `get apps` for
Application discovery, `get images` for rendered workload image listing,
`build apps` and `build app NAME` for local rendering, `diff apps` and
`diff app NAME` for desired-vs-desired manifest diffs, and `diff images` for
conservative workload image diffs. `test apps` and `test app NAME` report
per-Application render status without printing manifests. `diag --path` reports
repository diagnostics without printing manifests, and `diag -o json` or
`diag -o yaml` emits structured diagnostic reports. `cache path`, `cache list`,
`cache prune`, and `cache delete` inspect and maintain local source caches.

This project is early implementation work. See `docs/design.md` for the
approved MVP design and `docs/roadmap.md` for outstanding work.

## Go API

Embedding callers can use `github.com/sholdee/drydock/pkg/drydock`
to list, render, and diff Applications without shelling out:

```go
result, err := drydock.Render(ctx, drydock.Config{Path: "."})
```

`drydock.NewClient` accepts public Git, chart, and remote-resource acquirer
interfaces, plus a public config management plugin renderer hook, for tests
and embedding. Those fakes can satisfy remote source and plugin render
requests without network access or shelling out. When rendering returns an
error for one Application, the public result still includes successful
manifests, diagnostics, and per-Application statuses from the partial build.
Set `RecordCacheEvents` to include optional redacted cache acquisition events
for API callers.

## Quick Start

```bash
go run ./cmd/drydock get apps --path ./testdata/applications/e2e
go run ./cmd/drydock get apps --path ./testdata/applications/e2e -o json
go run ./cmd/drydock get images --path ./testdata/renovate-diff/current -o name
go run ./cmd/drydock build apps --path ./testdata/applications/e2e
go run ./cmd/drydock build app renovate \
  --path ./testdata/renovate-diff/current
go run ./cmd/drydock test apps --path ./testdata/applications/e2e
go run ./cmd/drydock test apps --path ./testdata/applications/e2e -o json
go run ./cmd/drydock diff apps \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  --strip-attr helm.sh/chart \
  --exit-code=false
go run ./cmd/drydock diff app argocd/renovate \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  -o json \
  --exit-code=false
go run ./cmd/drydock diag --path ./testdata/applications/e2e
go run ./cmd/drydock diag --path ./testdata/applications/e2e -o json
go run ./cmd/drydock diag --path ./testdata/applications/e2e \
  --settings \
  -o json
go run ./cmd/drydock diag --path ./testdata/applications/e2e \
  -o yaml \
  --cache-events
go run ./cmd/drydock cache path
go run ./cmd/drydock cache list -o json
go run ./cmd/drydock cache prune --older-than 720h --dry-run
```

Render and diff commands fetch public Helm charts by default for Kustomize
`helmCharts` and Argo CD chart-only sources. They also support remote
Kustomize HTTP(S) file resources and Git refs. HTTP(S) Kustomize refs are
single manifest/data files; directory-shaped refs such as bases and components
must be Git refs that resolve to Kustomization directories. Supported remote
fields include bases, components, patches, generators, transformers,
validators, `crds`, `openapi.path`, `replacements.path`, and generator
file/env refs through the remote resource cache. Use `--offline` to require
cached or local chart availability and cached remote Kustomize resources. Use
`--refresh-charts` and `--chart-cache-dir` for chart caching, and
`--refresh-remotes` and `--remote-cache-dir` for remote Kustomize resource
caching. Git path sources can be resolved with deterministic
`--repo-map URL=PATH` entries, or fetched with `--allow-network` when the source
path is not present locally. Git fetches use `--git-cache-dir` and
`--refresh-git`. Caches must stay outside Git repository trees.
`--allow-network` is not the Helm chart-fetch flag; chart fetching remains
controlled by the chart flags. Authenticated source support is explicit:
Git HTTPS accepts `--git-bearer-token` or `--git-username`/`--git-password`;
Git SSH accepts `--git-ssh-key-file`, optional `--git-ssh-passphrase`, and
requires `--git-known-hosts-file`; HTTP Helm repositories accept
`--helm-bearer-token` or `--helm-username`/`--helm-password`; OCI Helm auth is
read only from an explicit `--registry-config` path. Remote Kustomize HTTP(S)
resources accept `--remote-bearer-token` or
`--remote-username`/`--remote-password`; Kustomize Git refs reuse the explicit
`--git-*` credentials.

Diagnostics include stable codes in structured CLI and public API output.
`diag --cache-events` can include optional redacted cache acquisition events in
JSON or YAML reports; cache data and event metadata must stay outside the
GitOps repository tree.

Cache lifecycle commands are local filesystem operations only. They do not
render Applications, clone/fetch Git repositories, fetch Helm charts, fetch
remote Kustomize resources, or use credential flags. New cache entries include
hidden `.drydock-cache/metadata.json` sidecars with redacted target metadata;
older hash-only entries are listed as legacy entries when their filesystem
layout is recognized. Non-dry-run `cache prune` and `cache delete` operations
require `--yes`, and cache roots are rejected when they resolve inside the
current working directory, selected repository roots, any Git repository tree,
or symlink-resolved equivalents. Offline render/build/diff runs require
existing cache hits or local chart availability; populate caches with a prior
non-offline render using the relevant auth, cache-dir, and refresh flags.
Cache lifecycle commands inspect and delete local entries only, so they never
retry failed network or authentication acquisitions.

Manifest diffs default to unified diff output and also support `-o json` and
`-o yaml` for structured `diff apps` and `diff app` results. Use repeatable
`--strip-attr KEY` to remove matching metadata label and annotation keys before
comparison and diff generation. Diagnostics continue to print on stderr for
structured output.

ApplicationSet expansion supports local Git directories, Git files, list,
matrix, merge, and explicit fixture-backed provider generators. Supported
generators honor `elementsYaml`, generator selectors, generator template
overrides, matrix interpolation for local children, deterministic merge-key
overlays, and nested matrix/merge combinations where the Argo CD v3 API permits
them. Cluster, clusterDecisionResource, SCM provider, pull-request, and plugin
ApplicationSet generators require explicit local YAML/JSON fixtures; drydock
does not call live Kubernetes, Argo CD, SCM, pull-request, cloud, or plugin
service APIs for those generators.

Local `AppProject` manifests are discovered and used for offline diagnostics.
`drydock` reports Application source repository and destination
server/name/namespace policy mismatches from those manifests, plus source
namespace mismatches when `spec.sourceNamespaces` is set. RBAC roles and
policies are parsed and reported as metadata only; authorization is not
simulated. `permitOnlyProjectScopedClusters` is reported as deferred metadata,
and project-scoped cluster Secret enforcement is not simulated offline.
Repository credential matching diagnostics use discovered repository Secret
metadata only and never read secret credential fields.

Application sources that declare `spec.source.plugin` are detected explicitly.
The CLI and default Go client do not execute plugin commands; without an
injected renderer they fail closed with a plugin diagnostic. Embedders can
provide `drydock.Config.PluginRenderer` to render those sources
deterministically inside their own Go process, and returned manifests
participate in normal render, diff, image extraction, namespace defaulting,
and resource filtering.

Rendered-resource filters include Argo CD core exclusions and discovered
`argocd-cm` or Helm values `configs.cm` `resource.exclusions` and
`resource.inclusions`. Use repeatable `--skip-kind KIND`, `--skip-crds`, or
`--skip-secrets` for additional explicit omissions from build output, manifest
diffs, image extraction, and render tests. Embedding callers can set
`SkipKinds`, `SkipCRDs`, and `SkipSecrets` on `drydock.Config` for the
same explicit-filter behavior.

Application-level `spec.ignoreDifferences[]` `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers` rules are honored for rendered
manifest diffs. Global `resource.customizations.ignoreDifferences.*`
`jsonPointers`, `jqPathExpressions`, and `managedFieldsManagers` settings are
also honored. Managed-fields support is an offline rendered-manifest
approximation; it uses `metadata.managedFields` only when those fields are
present in the rendered manifests.

Global `resource.customizations.knownTypeFields.*` settings are applied during
desired-vs-desired diff normalization. Global
`resource.customizations.ignoreResourceUpdates.*` settings are parsed and
reported, but they are not applied to desired-vs-desired diffs because they
belong to live update/cache behavior. Health and action customizations,
including `useOpenLibs` and Lua metadata, are parsed and reported only; Lua is
not executed offline. Use `diag --settings -o json|yaml` to include a CLI-only
redacted settings summary with names, booleans, and SHA-256 hashes. Raw Lua
bodies and secret-looking strings embedded in Lua are not printed.

Discovered `resource.compareoptions` settings are honored for
`ignoreResourceStatusField` and `ignoreAggregatedRoles`. The default matches
Argo CD's `ignoreResourceStatusField: all`; set it to `none`, `off`, or
`false` to keep status diffs.

Maintainers with a local `home-ops` checkout can also run the optional
Renovate smoke script:

```bash
RENOVATE_CHART_TO=4.8.2 scripts/home-ops-renovate-smoke.sh
```

They can also run the optional pattern smoke, which applies representative
synthetic changes in temporary `home-ops` worktrees:

```bash
scripts/home-ops-pattern-smoke.sh
```

## Current MVP Limits

- Desired-vs-desired only; no live cluster diff.
- Live provider access for ApplicationSet provider-backed generators remains
  deferred. Use explicit local fixtures for cluster, clusterDecisionResource,
  SCM provider, pull-request, and plugin generators.
- No CLI config management plugin execution or shellout plugin adapters.
- No required shellouts in default workflows.
- Cache lifecycle commands operate on recognized drydock cache layouts only;
  legacy entries expose key, path, and layout, but not recovered target, name,
  version, or revision metadata.
- Live server-side diff/apply behavior is not reproduced.
- Live-only managed-field ownership is not reproduced when ownership data is
  absent from rendered manifests.
- Live integration work is design-gated; see
  `docs/reports/2026-05-24-live-integration-design-gate.md` before proposing
  live-cluster, Argo CD server, server-side diff, defaulting, admission, or
  server-side apply ownership behavior.
- Health/action Lua is not executed offline.
- Live destination cluster existence, sync windows, source integrity
  verification, project-scoped cluster Secrets, and full RBAC simulation remain
  deferred.

See `docs/usage.md` for command examples, `docs/compatibility.md` for offline
Argo CD compatibility notes, `docs/roadmap.md` for outstanding work,
`docs/ci.md` for the local CI contract, `docs/release.md` for release and
Argo CD dependency upgrade notes, and
`docs/reports/2026-05-24-live-integration-design-gate.md` for the boundary
around future live integration. See `docs/home-ops-pattern-coverage.md` for the
portable coverage matrix that models real `home-ops` Application patterns.
