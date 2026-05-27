# Documentation Index

This page owns documentation routing. Update the canonical owner instead of
copying the same rule into multiple files.

## Ownership

| File | Owns |
| --- | --- |
| `README.md` | Project overview, quick start, and highest-value links. |
| `AGENTS.md` | Mandatory agent operating rules and hard constraints. |
| `docs/agent-reference.md` | Task-specific agent constraints and code-area notes. |
| `docs/design.md` | Product architecture and behavior model. |
| `docs/usage.md` | Operator CLI workflows and high-value command examples. |
| `docs/applicationsets.md` | ApplicationSet generator behavior, fixture schema, and template parameters. |
| `docs/source-acquisition.md` | Git, Helm, remote Kustomize, cache, and auth behavior. |
| `docs/compatibility.md` | Supported Argo CD behavior and runtime-boundary status. |
| `docs/release.md` | Release and Argo CD dependency upgrade notes. |
| `docs/logo/` | Project logo assets. |
| `docs/reports/live-integration-design-gate.md` | No-live-runtime boundary and gate for exceptions. |

## Anti-Sprawl Rules

- Do not add plan files for completed implementation work. Use git history for
  closed plans and audits.
- Keep `docs/reports` for active design gates only.
- Put durable product behavior in `docs/design.md`, `docs/usage.md`,
  `docs/applicationsets.md`, `docs/source-acquisition.md`, or
  `docs/compatibility.md`, depending on ownership above.
- Split a focused reference page only when it removes dense detail from
  `docs/usage.md` or `docs/design.md`.
- Keep `README.md`, `AGENTS.md`, and `docs/agent-reference.md` as routing
  surfaces; link to canonical docs instead of duplicating long explanations.
- Update this file when adding, deleting, or changing ownership of docs.
