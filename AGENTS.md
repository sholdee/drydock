# AGENTS.md - drydock

## Product Contract

`drydock` is an independent Go CLI and library for Argo CD GitOps repository
analysis. Its job is runtime-offline desired-state analysis for Argo CD
Applications: discover, render, test, diff, inspect images, and report
diagnostics without a running Argo CD instance or Kubernetes cluster.

Default render, diff, test, image, and diag paths must remain native Go
execution in a single static binary. Do not require `kubectl`, `argocd`, Helm
CLI, Kustomize CLI, a repo-server wrapper, an external renderer, or default
config-management plugin shellouts.

In this repository, "offline" means offline from live Argo CD and Kubernetes
runtime. It does not mean source networks are always disabled. `drydock` may
fetch declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources into
explicit local caches unless `--offline` is set.

Live Kubernetes, live Argo CD, server-side diff, defaulting, admission, managed
fields ownership prediction, SCM/cloud/provider API calls, and new shellout
renderers require an approved design update first.

## Fresh Agent Workflow

1. Read this file.
2. Load only the relevant section of `docs/agent-reference.md`.
3. Use `rg` first, then read the smallest relevant files.
4. Make narrow, behavior-preserving changes unless the task asks otherwise.
5. Run the smallest meaningful validation for the change.
6. Report skipped or approval-gated checks explicitly.

## Read This First

- `docs/agent-reference.md`: task-specific constraints and canonical links.
- `docs/README.md`: documentation ownership map.
- `docs/design.md`: product architecture and behavior model.
- `docs/compatibility.md`: supported Argo CD behavior and runtime boundary.
- `docs/source-acquisition.md`: Git, Helm, remote Kustomize, cache, and auth.
- `site/content/docs/plugin-policy/`: trusted plugin policy and opt-in exec
  rendering.
- `docs/reports/live-integration-design-gate.md`: required before proposing
  live runtime, server-side diff, defaulting, admission, or managed-fields work.
- `site/`: Hugo docs site source and curated operator-facing pages.

## Subagent Sandbox Rules

Delegated workers and reviewers must not request sandbox escalation.

Include this line in worker and reviewer prompts:

> Do not request sandbox escalation. If a command needs approval, network
> access, or broader filesystem access, skip it and report the verification gap.

Treat approval-gated checks as skipped verification, not blockers. Continue
independent work while skipped checks are reported. If a delegated agent asks
for approval anyway, remind it once; if it still blocks, replace it or mark that
check skipped. Only the controller agent should request escalation, and only
when the user-facing task requires it.

## Hard Constraints

- Do not add default shellouts to `helm`, `kustomize`, `kubectl`, `argocd`, or
  config-management plugins. Exec and container plugin engines require trusted
  policy provenance plus explicit `--enable-plugins`.
- Do not add live Kubernetes or Argo CD server behavior without updating the
  live runtime boundary document and preserving `--offline` behavior.
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
- Keep caches outside current and baseline repository trees, protected roots,
  and symlink-resolved equivalents.
- Keep stdout machine-parseable for structured/list outputs. Diagnostics and
  failure summaries belong on stderr unless status text is the primary output.

## Common Mistakes

- Reintroducing `--allow-network`; `--offline` is the source-acquisition
  switch.
- Confusing runtime-offline analysis with source-network-disabled analysis.
- Treating native plugin compatibility or argocd-vault-plugin compatibility as
  permission for default shellouts.
- Hiding Secrets or CRDs by default. `--skip-secrets`, `--skip-crds`, and
  `--skip-kind` are explicit opt-ins.
- Treating custom health Lua as metadata-only. Custom health Lua is validated
  offline against rendered desired manifests during render tests; resource
  action Lua remains metadata-only/deferred.
- Adding provider generator network/API access. Provider-backed ApplicationSet
  generators are fixture-backed offline.
- Trusting old audit or report claims without checking the current code.

## Task Routing

| Task | Start Here |
| --- | --- |
| CLI flags, output, exit codes | `internal/cli`, `internal/requestopts` |
| Public Go API | `pkg/drydock`, then matching `internal/app` request types |
| Discovery and Argo settings | `internal/discovery`, `internal/config` |
| ApplicationSet generation | `site/content/docs/applicationsets.md`, then `internal/appset` |
| Application planning and orchestration | `internal/app` |
| Kustomize, Helm, directory rendering | `internal/render` |
| Git, chart, remote acquisition | `docs/source-acquisition.md`, then `internal/source`, `internal/chart`, `internal/remote`, `internal/acquisition` |
| Manifest diffs and image extraction | `internal/diff`, `internal/manifest` |
| Cache lifecycle and cache events | `internal/cache`, `internal/cacheevent`, `internal/cli/cache.go` |
| Path containment and symlink rules | `internal/pathsafety`, then caller-specific checks |
| Documentation site | `site/`, `docs/README.md`, then canonical `docs/*.md` owner |

For detailed task constraints, read the matching section in
`docs/agent-reference.md`.

## Validation

Run the smallest check that covers your change. Prefer focused package tests
over full-suite checks when they prove the edited behavior.

```bash
mise run ci
go test ./...
mise run test-race
mise run lint
mise run markdownlint
```

Portable integration fixtures should model real repository behavior without
depending on a maintainer-provided `home-ops` checkout. Optional
real-repository smokes must use temporary worktrees and clean them up. Never
mutate the real `home-ops` checkout from tests.

The Argo CD render parity smoke is optional maintainer validation and selective
CI-gated PR validation for semantic-rendering module updates. See
`docs/agent-reference.md` before changing its kind/kubectl setup, fixture
inventory, module detector, or Renovate-managed tool versions.

## Maintenance Rule

- Use `docs/README.md` for documentation ownership decisions.
- Update this file only when changing mandatory agent behavior, hard product
  constraints, validation expectations, or first-hop orientation. Put detailed
  design notes in the relevant docs instead.
