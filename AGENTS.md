# AGENTS.md - drydock

## Product Contract

`drydock` is an independent Go CLI for local Argo CD GitOps repository
analysis. Its core job is desired-vs-desired PR diffing: render Argo CD
Applications from a current tree and a baseline tree, then show what desired
Kubernetes manifests changed.

The default workflow must remain a self-contained Go binary using checked-out
files plus explicit local caches. It may fetch declared Git, HTTP Helm, OCI
Helm, and remote Kustomize sources into those caches unless `--offline` is set.
Do not require a Kubernetes cluster, `kubectl`, the `argocd` CLI, Helm CLI,
Kustomize CLI, an external renderer, an Argo CD server, or any live runtime
dependency for default render, diff, image, test, or diagnostic paths.

Network-aware acquisition may exist only as declared source cache population.
Live Kubernetes, live Argo CD, server-side diff, defaulting, admission,
managed fields ownership prediction, SCM/cloud/provider API calls, and
shellout renderers require an approved design update first.

## Read This First

Use these entry points before substantive work:

- `docs/agent-orientation.md`: fast routing for fresh agents.
- `docs/agent-reference.md`: task-specific agent constraints and canonical
  links.
- `docs/README.md`: documentation ownership map.
- `docs/design.md`: canonical product architecture and behavior model.
- `docs/roadmap.md`: canonical supported/deferred feature status.
- `docs/reports/2026-05-24-live-integration-design-gate.md`: required before
  proposing live runtime, server-side diff, defaulting, admission, or
  managed-fields work.

If a repo-local `CLAUDE.md` exists, read it alongside this file and resolve
conflicts conservatively.

## Subagent Sandbox Rules

Subagents must not request sandbox escalation for routine implementation,
review, or verification. Run local, non-network commands that work inside the
current sandbox. If a useful command would require escalation, network access,
or approval, skip it, report the verification gap, and continue.

Every worker and reviewer prompt for roadmap work must include this exact
constraint:

> Do not request sandbox escalation. If a useful command would require
> approval, network, or escalation, skip it and report it as skipped.

Treat approval-gated checks as skipped verification, not blockers. Do not wait
on a subagent approval prompt before starting other independent work. If a
spawned agent requests approval anyway, redirect it once with the constraint
above or close/replace it. If a skipped command is required to prove
correctness, record the gap and use another local check or a narrower review.
Controller prompts for roadmap phases should state that approval prompts from
workers or reviewers are abandoned as skipped verification, never treated as
human-blocking phase status.

## Hard Constraints

- Do not add default shellouts to `helm`, `kustomize`, `kubectl`, `argocd`, or
  config-management plugins.
- Do not add live Kubernetes or Argo CD server behavior without updating the
  live integration design gate and preserving `--offline` behavior.
- Do not hard-code `home-ops` paths, chart versions, branches, or repository
  names.
- Do not print Secret manifest values, repository credentials, tokens, SSH
  private keys, passphrases, registry credentials, or credential-bearing URLs.
- Do not read ambient Git credential helpers, ambient Helm registry config, or
  discovered repository Secret credential fields unless a future design update
  explicitly allows it.
- Do not expose `internal/...` package types through `pkg/drydock`.
- Do not use Flux ownership rules. Changed-only behavior is Argo
  Application-aware; overlapping Applications are not collapsed to one owner.
- Do not dedupe repeated resources across Applications. Last-wins behavior
  applies only inside one Application and must emit a diagnostic.
- Keep caches outside the current and baseline repository trees, protected
  roots, and symlink-resolved equivalents.
- Keep stdout machine-parseable for structured/list outputs; diagnostics and
  failure summaries belong on stderr unless status text is the primary output.

## Common Mistakes

- Reintroducing a separate network-enabling flag. `--offline` is the
  user-facing switch for disabling declared source acquisition.
- Hiding Secrets or CRDs by default. `--skip-secrets`, `--skip-crds`, and
  `--skip-kind` are explicit opt-ins.
- Executing Lua or server-side diff/apply settings offline. Parse and report
  metadata only unless the design changes.
- Adding provider generator network/API access. Provider-backed
  ApplicationSet generators are fixture-backed offline.
- Assuming old plans or reports describe current file sizes. Use `rg --files`,
  `wc -l`, and current code before repeating historical claims.

## Task Routing

Use `rg` first, then read the smallest relevant files.

| Task | Start Here |
| --- | --- |
| CLI flags, output, exit codes | `internal/cli`, `internal/requestopts` |
| Public Go API | `pkg/drydock`, then matching `internal/app` request types |
| Discovery and Argo settings | `internal/discovery`, `internal/config` |
| ApplicationSet generation | `internal/appset` |
| Application planning and orchestration | `internal/app` |
| Kustomize, Helm, directory rendering | `internal/render` |
| Git, chart, remote acquisition | `internal/source`, `internal/chart`, `internal/remote`, `internal/acquisition` |
| Manifest diffs and image extraction | `internal/diff`, `internal/manifest` |
| Cache lifecycle and cache events | `internal/cache`, `internal/cacheevent`, `internal/cli/cache.go` |
| Path containment and symlink rules | `internal/pathsafety`, then caller-specific checks |

For detailed task constraints, read the matching section in
`docs/agent-reference.md`.

## Validation

Run the smallest check that covers your change. Common checks:

```bash
go test ./...
go vet ./...
go test -race ./internal/app -run 'Parallelism|Parallel'
golangci-lint run
markdownlint-cli2 '**/*.md'
```

If a command is unavailable or approval-gated, skip it and record the gap
rather than requesting sandbox escalation from a subagent.

Portable integration fixtures should model real repository behavior without
depending on a maintainer-provided `home-ops` checkout. Optional
real-repository smokes must use temporary worktrees and clean them up. Never
mutate the real `home-ops` checkout from tests.

## Maintenance Rule

- Use `docs/README.md` for documentation ownership decisions.
- Update `AGENTS.md` only when mandatory agent rules, hard constraints,
  validation expectations, or subagent coordination rules change.
