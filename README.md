<p align="center">
  <img src="docs/logo/drydock-display.svg" alt="drydock" width="480">
</p>

# drydock

`drydock` renders, tests, and diffs Argo CD GitOps repositories locally for
pull request review.

It is built for operators who want to inspect rendered desired state before a
change reaches the cluster. The default workflow is a self-contained Go binary:
no Kubernetes cluster, no Argo CD server, no `kubectl`, no `argocd`, and no
Helm or Kustomize CLI shellouts.

Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources may be fetched
into explicit drydock caches when needed. Use `--offline` to require local
files, repo maps, local charts, or existing cache hits only.

## Install

Install from source with Go:

```bash
go install github.com/sholdee/drydock/cmd/drydock@latest
```

Workflows that install a released binary can use the versioned setup action:

```yaml
- uses: sholdee/drydock/.github/actions/setup-drydock@main
  with:
    version: v0.1.0
```

The setup action intentionally requires an explicit version. It does not accept
`latest`.

## Quick Start

Run drydock from the root of an Argo CD GitOps repository.

Test every discovered Application without printing rendered manifests:

```bash
drydock test apps --path .
```

Example text output:

```text
PASS argocd/renovate
PASS argocd/cert-manager
FAIL argocd/broken Application argocd/broken source[0] path="..." ...
```

Compare a pull request checkout against a baseline tree:

```bash
git worktree add ../baseline main
drydock diff apps --path . --path-orig ../baseline
```

Inspect image changes in a machine-readable form:

```bash
drydock diff images --path . --path-orig ../baseline -o json
```

For CI jobs that have already populated drydock's source caches, require a
cache-only run:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

## Common Workflows

| Goal | Command |
| --- | --- |
| List Applications | `drydock get apps --path .` |
| List rendered workload images | `drydock get images --path . -o name` |
| Render all Applications | `drydock build apps --path .` |
| Render one Application | `drydock build app argocd/renovate --path .` |
| Test renderability | `drydock test apps --path .` |
| Diff rendered manifests | `drydock diff apps --path . --path-orig ../baseline` |
| Diff one Application | `drydock diff app argocd/renovate --path . --path-orig ../baseline` |
| Diff workload images | `drydock diff images --path . --path-orig ../baseline -o json` |
| Inspect repository diagnostics | `drydock diag --path .` |
| Inspect redacted settings | `drydock diag --path . --settings -o json` |
| Inspect cache roots | `drydock cache path` |
| List cache entries | `drydock cache list -o json` |

`drydock <command> --help` lists command-specific flags. See
[`docs/usage.md`](docs/usage.md) for the full CLI guide.

## What It Supports

drydock discovers and renders local Argo CD desired state, including:

- `Application` resources and supported `ApplicationSet` generators.
- Single-source and multi-source Applications.
- Directory, Kustomize, local Helm chart, remote Helm chart, and remote
  Kustomize sources.
- Declared Git, HTTP Helm, OCI Helm, and remote Kustomize source acquisition
  into local caches.
- Repository maps with `--repo-map URL=PATH` for adjacent local checkouts.
- Changed-only desired-vs-desired PR diffs, with strict diagnostics available
  when a safe ownership decision cannot be made.
- Argo CD diff customizations such as `ignoreDifferences`,
  `knownTypeFields`, selected compare options, and resource filters.
- Per-Application render test status as `PASS`, `FAIL`, or `SKIPPED`, including
  structured JSON and YAML output.
- Offline validation of configured custom health Lua during render tests.
- Redacted diagnostics for settings, source repositories, AppProjects, and
  cache acquisition events.
- Cache lifecycle commands for Git, chart, and remote Kustomize caches.

See [`docs/compatibility.md`](docs/compatibility.md) for the detailed Argo CD
support matrix.

## Safety Model

drydock is desired-vs-desired analysis. It renders the desired Kubernetes
manifests from a current tree and, for diff commands, a baseline tree. It does
not ask a live cluster or Argo CD server what is currently running.

Default commands do not reproduce:

- Kubernetes API defaulting or admission mutation.
- Argo CD server-side diff.
- Live Argo CD Application health aggregation.
- Live-only managed-field ownership.
- Full Argo CD RBAC authorization.
- CLI config management plugin execution or shellout plugin adapters.

These behaviors are not silently approximated. Live-runtime work is design-gated
so the default local, cache-backed workflow stays deterministic and safe for CI.

Structured outputs keep stdout machine-parseable. Diagnostics and failure
summaries are written to stderr where appropriate, and drydock avoids printing
Secret values, repository credentials, tokens, SSH private keys, passphrases,
registry credentials, or credential-bearing URLs.

## How It Works

```text
current tree    baseline tree
     |               |
     v               v
discover Argo CD Applications and settings
     |               |
     v               v
plan sources, use repo maps, populate or read caches
     |               |
     v               v
render desired manifests with Go libraries
     |               |
     v               v
apply Argo-aware filters and diff normalization
     |               |
     v               v
test statuses, manifest diffs, image diffs, diagnostics
```

The render path imports Argo CD API types and selected reusable helpers, but
drydock owns local orchestration. See [`docs/design.md`](docs/design.md) for
the architecture and behavior model.

## Go API

Embedding callers can use `github.com/sholdee/drydock/pkg/drydock` to list,
render, and diff Applications without shelling out:

```go
result, err := drydock.Render(ctx, drydock.Config{Path: "."})
```

`drydock.NewClient` accepts public Git, chart, and remote-resource acquirer
interfaces, plus a public config management plugin renderer hook. Embedders can
use those interfaces for tests, offline fixtures, and custom source handling.
When one selected Application fails, the result still includes successful
manifests, diagnostics, and per-Application statuses from the partial build.

## Community

drydock is independently implemented, but its local-first GitOps PR-diff
workflow was inspired by
[home-operations/flate](https://github.com/home-operations/flate) and the
home-operations community.

Join the home-operations Discord at <https://discord.gg/home-operations>.

## Documentation

- [`docs/README.md`](docs/README.md): documentation ownership and routing.
- [`docs/usage.md`](docs/usage.md): CLI examples, flags, outputs, cache
  behavior, and optional smoke tests.
- [`docs/compatibility.md`](docs/compatibility.md): supported and deferred
  Argo CD behavior.
- [`docs/roadmap.md`](docs/roadmap.md): supported/deferred feature status and
  next-work rules.
- [`docs/release.md`](docs/release.md): release and Argo CD dependency upgrade
  notes.
- [`docs/reports/2026-05-24-live-integration-design-gate.md`](docs/reports/2026-05-24-live-integration-design-gate.md):
  required before proposing live runtime behavior.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
