# Agent Orientation

This page is a routing map for fresh agents. It is intentionally shorter than
`AGENTS.md`, `docs/agent-reference.md`, `docs/design.md`, and `docs/roadmap.md`;
use it to find the right context quickly, then read the authoritative docs for
the area you touch.

## Purpose

`drydock` renders and diffs Argo CD GitOps repositories locally. The default
workflow is deterministic and library-backed: checked-out files, declared
source fetches, and explicit local caches are the inputs, and no Kubernetes
cluster, Argo CD server, `kubectl`, `argocd`, Helm CLI, Kustomize CLI, or
external renderer is required. `--offline` is the cache-only/no-network mode.

Network-aware source acquisition exists only as explicit cache population. Do
not add live runtime behavior, ambient credentials, shellout renderers, or
repository-specific shortcuts without an approved design update.

## First Five Minutes

1. Read `AGENTS.md` for mandatory repository constraints and subagent rules.
2. Use `docs/README.md` for documentation ownership.
3. Use this file to route to the right package or detailed doc.
4. Read `docs/agent-reference.md` only for task-specific agent constraints.
5. Read `docs/design.md` and `docs/roadmap.md` for product design and
   supported/deferred feature context.
6. Check `git status --short --branch` before editing.
7. Use `rg` and focused file reads before loading broad tests or reports.

If your task involves Kubernetes API access, Argo CD API/server behavior,
server-side diff, defaulting, admission, managed fields, or live cluster
state, read `docs/reports/2026-05-24-live-integration-design-gate.md` before
planning. Those behaviors are design-gated and outside default workflows.

## Task Routing

Use these entry points before broad searches:

| Task | Start Here |
| --- | --- |
| CLI flags, command output, exit behavior | `internal/cli`, then `internal/requestopts` |
| Public Go API behavior | `pkg/drydock`, then matching `internal/app` request types |
| Application discovery or settings discovery | `internal/discovery`, `internal/config` |
| ApplicationSet generation | `internal/appset` |
| Render orchestration, statuses, partial results | `internal/app` |
| Kustomize, Helm, or directory rendering | `internal/render` |
| Git, Helm chart, or remote resource acquisition | `internal/source`, `internal/chart`, `internal/remote`, `internal/acquisition` |
| Desired manifest diffing or image extraction | `internal/diff`, `internal/manifest` |
| Cache metadata and cache lifecycle | `internal/cache`, `internal/cacheevent`, `internal/cli/cache.go` |
| Path containment and symlink rules | `internal/pathsafety`, then caller-specific checks |

When behavior crosses packages, prefer following the request path from CLI or
`pkg/drydock` into `internal/app`, then into the renderer, generator, or
acquisition adapter. That usually costs less context than reading packages in
alphabetical order.

## High Context Areas

Some tests remain broad by design. Verify current file sizes before repeating
historical line-count claims, then load only the relevant behavior family:

- `internal/appset/generator_test.go`: shared generator-family coverage.
- `internal/app/orchestrator_test.go`: orchestration, status, plugin,
  acquisition, and selection coverage.
- `pkg/drydock/drydock_test.go`: public API integration contracts.
- `internal/render/kustomize_test.go`: now a narrow Kustomize entrypoint; use
  neighboring split test files for specific behavior.
- `internal/render/kustomize_workspace.go`: now a smaller workspace
  orchestrator; helper behavior lives in neighboring split files.

Use `rg --files`, `wc -l`, and current code inspection before repeating file
size or refactor-history claims.

## Reports

- `docs/reports/2026-05-24-live-integration-design-gate.md`: required before
  proposing live runtime work.

## Boundaries

Before touching a package, read the matching section in
`docs/agent-reference.md`. The most common boundaries are:

- Do not hard-code `home-ops` paths, chart versions, branches, or repository
  names.
- Do not add default shellouts to `helm`, `kustomize`, `kubectl`, or `argocd`.
- Do not add live Kubernetes, Argo CD server, SCM provider, pull-request
  provider, cloud API, or plugin service calls to default workflows.
- Do not read, retain, or report secret credential fields from discovered
  repository metadata.
- Do not weaken path containment, symlink rejection, cache-root safety, or
  credential redaction to make a fixture pass.
- Do not expose `internal/...` package types through `pkg/drydock`.
