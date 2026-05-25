# Documentation Index

This page owns documentation routing. Update the canonical owner instead of
copying the same rule into multiple files.

## Ownership

| File | Owns |
| --- | --- |
| `README.md` | Project overview, quick start, and highest-value links. |
| `AGENTS.md` | Mandatory agent operating rules and hard constraints. |
| `docs/agent-orientation.md` | Fast task routing for fresh agents. |
| `docs/agent-reference.md` | Task-specific agent constraints and code-area notes. |
| `docs/design.md` | Product architecture and behavior model. |
| `docs/usage.md` | CLI examples, flags, outputs, and user workflows. |
| `docs/compatibility.md` | Supported/deferred Argo CD compatibility status. |
| `docs/roadmap.md` | Current supported/deferred feature status and next-work rules. |
| `docs/ci.md` | Local CI and required verification contract. |
| `docs/release.md` | Release and Argo CD dependency upgrade notes. |
| `docs/home-ops-pattern-coverage.md` | Portable fixture coverage for home-ops-like patterns. |
| `docs/reports/2026-05-24-live-integration-design-gate.md` | Required gate for live runtime work. |

## Anti-Sprawl Rules

- Do not add plan files for completed implementation work. Use git history for
  closed plans and audits.
- Keep `docs/reports` for active design gates only.
- Put durable product behavior in `docs/design.md`, `docs/usage.md`,
  `docs/compatibility.md`, or `docs/roadmap.md`, depending on ownership above.
- Keep `README.md`, `AGENTS.md`, and agent docs as routing surfaces; link to
  canonical docs instead of duplicating long explanations.
- Update this file when adding, deleting, or changing ownership of docs.
