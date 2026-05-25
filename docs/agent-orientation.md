# Agent Orientation

This page is a routing map for fresh agents. It is intentionally shorter than
`AGENTS.md`, `docs/design.md`, and `docs/roadmap.md`; use it to find the right
context quickly, then read the authoritative docs for the area you touch.

## Purpose

`drydock` renders and diffs Argo CD GitOps repositories locally. The default
workflow is offline, deterministic, and library-backed: checked-out files plus
explicit local caches are the input, and no Kubernetes cluster, Argo CD server,
`kubectl`, `argocd`, Helm CLI, Kustomize CLI, or external renderer is required.

Network-aware source acquisition exists only as explicit cache population. Do
not add live runtime behavior, ambient credentials, shellout renderers, or
repository-specific shortcuts without an approved design update.

## First Five Minutes

1. Read `AGENTS.md` for repository constraints and subagent rules.
2. Read `docs/design.md` for the product contract and package map.
3. Read `docs/roadmap.md` for completed support boundaries and deferred work.
4. Check `git status --short --branch` before editing.
5. Use `rg` and focused file reads before loading large tests or reports.

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
| Path containment and symlink rules | `internal/pathsafety`, then the caller-specific checks |

When behavior crosses packages, prefer following the request path from CLI or
`pkg/drydock` into `internal/app`, then into the renderer, generator, or
acquisition adapter. That usually costs less context than reading packages in
alphabetical order.

## High Context Files

Some files are intentionally broad test or implementation surfaces. Load them
only after narrowing to the behavior family you need:

- `internal/appset/generator_test.go`: generator-family coverage.
- `internal/render/kustomize_test.go`: Kustomize renderer parity and path
  safety coverage.
- `internal/app/orchestrator_test.go`: orchestration, partial status,
  plugin, acquisition, and parallelism coverage.
- `pkg/drydock/drydock_test.go`: public API integration contracts.
- `internal/render/kustomize_workspace.go`: Kustomize workspace preparation.
- `internal/app/orchestrator.go`: orchestration and local provider adapter
  behavior.
- `internal/appset/generator_provider_generators.go`: fixture-backed provider
  generator behavior.

For current size data and refactor priorities, read
`docs/reports/2026-05-25-refactor-orientation-roadmap.md` instead of copying
line counts into new task prompts.

## Historical Plans And Reports

`docs/plans/` and `docs/reports/` contain both active guidance and closed
history. Treat completed phase reports as evidence of why a shape exists, not
as proof that old line counts or old next steps are still current.

Current high-signal reports:

- `docs/reports/2026-05-25-refactor-orientation-roadmap.md`: active refactor
  and splitting roadmap.
- `docs/reports/2026-05-24-refactoring-audit.md`: closed R1-R5 remediation
  history.
- `docs/reports/2026-05-24-live-integration-design-gate.md`: required context
  before proposing live runtime work.

## Do Not Cross These Boundaries

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

