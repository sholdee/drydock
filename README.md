# argocd-local

`argocd-local` is an early Go CLI and embeddable Go package for local Argo CD
GitOps repository analysis.

The MVP goal is desired-vs-desired pull request diffing: compare a current
repository tree with a baseline tree and inspect the rendered Kubernetes
manifests that changed. The currently wired commands are `get apps` for
Application discovery, `get images` for rendered workload image listing,
`build apps` and `build app NAME` for local rendering, `diff apps` and
`diff app NAME` for desired-vs-desired manifest diffs, and `diff images` for
conservative workload image diffs. `test apps` and `test app NAME` report
per-Application render status without printing manifests. `diag --path` reports
repository diagnostics without printing manifests.

This project is early implementation work. See
`docs/superpowers/specs/2026-05-22-argocd-local-design.md` for the approved MVP
design.

## Go API

Embedding callers can use `github.com/home-operations/argocd-local/pkg/argocdlocal`
to list, render, and diff Applications without shelling out:

```go
result, err := argocdlocal.Render(ctx, argocdlocal.Config{Path: "."})
```

`argocdlocal.NewClient` accepts public Git, chart, and remote-resource acquirer
interfaces for tests and embedding. Those fakes can satisfy remote source
requests without network access. When rendering returns an error for one
Application, the public result still includes successful manifests,
diagnostics, and per-Application statuses from the partial build.

## Quick Start

```bash
go run ./cmd/argocd-local get apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local get apps --path ./testdata/applications/e2e -o json
go run ./cmd/argocd-local get images --path ./testdata/renovate-diff/current -o name
go run ./cmd/argocd-local build apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local build app renovate \
  --path ./testdata/renovate-diff/current
go run ./cmd/argocd-local test apps --path ./testdata/applications/e2e
go run ./cmd/argocd-local test apps --path ./testdata/applications/e2e -o json
go run ./cmd/argocd-local diff apps \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  --strip-attr helm.sh/chart \
  --exit-code=false
go run ./cmd/argocd-local diff app argocd/renovate \
  --path-orig ./testdata/renovate-diff/baseline \
  --path ./testdata/renovate-diff/current \
  -o json \
  --exit-code=false
go run ./cmd/argocd-local diag --path ./testdata/applications/e2e
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

Manifest diffs default to unified diff output and also support `-o json` and
`-o yaml` for structured `diff apps` and `diff app` results. Use repeatable
`--strip-attr KEY` to remove matching metadata label and annotation keys before
comparison and diff generation. Diagnostics continue to print on stderr for
structured output.

Rendered-resource filters include Argo CD core exclusions and discovered
`argocd-cm` or Helm values `configs.cm` `resource.exclusions` and
`resource.inclusions`. Use repeatable `--skip-kind KIND`, `--skip-crds`, or
`--skip-secrets` for additional explicit omissions from build output, manifest
diffs, image extraction, and render tests. Embedding callers can set
`SkipKinds`, `SkipCRDs`, and `SkipSecrets` on `argocdlocal.Config` for the
same explicit-filter behavior.

Application-level `spec.ignoreDifferences[]` `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers` rules are honored for rendered
manifest diffs. Global `resource.customizations.ignoreDifferences.*`
`jsonPointers`, `jqPathExpressions`, and `managedFieldsManagers` settings are
also honored. Managed-fields support is an offline rendered-manifest
approximation; it uses `metadata.managedFields` only when those fields are
present in the rendered manifests.

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
- ApplicationSet support is limited to top-level Git directories, Git files,
  and list generators.
- No config management plugins.
- No required shellouts in default workflows.
- No cache-inspection command or structured cache event stream yet. Remote Git,
  Helm, and Kustomize cache observability is tracked as a Phase 1B follow-up.
- Live server-side diff/apply behavior is not reproduced.
- Global `resource.customizations` support does not include
  `ignoreResourceUpdates`, health, actions, or known type fields.

See `docs/usage.md` for command examples and `docs/compatibility.md` for
offline Argo CD compatibility notes. See `docs/home-ops-pattern-coverage.md`
for the portable coverage matrix that models real `home-ops` Application
patterns.
