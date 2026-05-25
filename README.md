# drydock

`drydock` is a Go CLI and embeddable Go package for local Argo CD GitOps
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

This project is early implementation work. See `docs/README.md` for the full
documentation index.

Default commands are local desired-vs-desired analysis. They may fetch declared
Git, HTTP Helm, OCI Helm, and remote Kustomize sources into explicit caches
unless `--offline` is set. They do not contact a Kubernetes cluster or Argo CD
server, do not read ambient live runtime config, and do not require `kubectl`,
`argocd`, Helm CLI, or Kustomize CLI. Kubernetes defaulting, admission
mutation, Argo CD server-side diff, and live-only managed-field ownership are
design-gated rather than silently approximated.

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

## Documentation

- `docs/README.md`: documentation ownership and routing.
- `docs/usage.md`: CLI examples, flags, outputs, cache behavior, and optional
  smoke tests.
- `docs/compatibility.md`: supported and deferred Argo CD behavior.
- `docs/roadmap.md`: current status and next-work rules.
- `docs/reports/2026-05-24-live-integration-design-gate.md`: required before
  proposing live runtime behavior.

## Current MVP Limits

- Desired-vs-desired only; no live cluster diff.
- No live Argo CD server integration or server-side diff parity.
- No Kubernetes defaulting or admission mutation approximation.
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
