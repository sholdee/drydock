---
title: Documentation Index
---

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
| `docs/topologies.md` | Repository topology guidance and command patterns. |
| `site/content/docs/applicationsets.md` | ApplicationSet generator behavior, fixture schema, and template parameters. |
| `docs/source-acquisition.md` | Git, Helm, remote Kustomize, cache, and auth behavior. |
| `site/content/docs/plugin-policy/` | Trusted plugin policy provenance, editor schema, CMP compatibility, bootstrap discovery, and command security. |
| `schemas/plugin-policy.schema.json` | Editor validation schema for `.drydock/plugins.yaml`; parser remains authoritative. |
| `docs/compatibility.md` | Supported Argo CD behavior and runtime-boundary status. |
| `site/content/workflows/github-actions.md` | Setup action and full PR action usage, permissions, cache, comments, and outputs. |
| `docs/release.md` | Release and Argo CD dependency upgrade notes. |
| `docs/logo/` | Project logo assets. |
| `docs/reports/live-integration-design-gate.md` | No-live-runtime boundary and gate for exceptions. |
| `site/` | Hugo docs site source, curated operator pages, local layouts, and GitHub Pages configuration. |
| `site/content/concepts/argocd-render-parity.md` | Curated operator explanation of Argo CD render parity and drydock's validation strategy. |

## Anti-Sprawl Rules

- Do not add plan files for completed implementation work. Use git history for
  closed plans and audits.
- Keep `docs/reports` for active design gates only.
- Put durable product behavior in the canonical owner above, usually
  `docs/design.md`, `docs/usage.md`,
  `site/content/docs/applicationsets.md`,
  `docs/source-acquisition.md`, `site/content/docs/plugin-policy/`, or
  `docs/compatibility.md`.
- Split a focused reference page only when it removes dense detail from
  `docs/usage.md` or `docs/design.md`.
- Keep `README.md`, `AGENTS.md`, and `docs/agent-reference.md` as routing
  surfaces; link to canonical docs instead of duplicating long explanations.
- Keep most `site/content` pages concise and operator-facing. Detailed public
  reference pages under `site/content/docs` may own operator behavior when a
  source-only doc would be too dense for the site.
- Update this file when adding, deleting, or changing ownership of docs.
