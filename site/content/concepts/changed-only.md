---
title: Changed-Only Diffs
---

Multi-Application diffs use changed-only selection by default. drydock maps
changed files to Application inputs and renders only the affected Applications
when ownership is clear.

If any changed file is unowned or ambiguous, non-strict mode warns and renders
all Applications. This preserves correctness at the cost of a broader diff.

## Commands

Use the default behavior:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
```

Render everything explicitly:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --changed-only=false
```

Fail when ownership cannot be proven:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --strict-changed-only
```

`diff app` selects one requested Application directly and does not use
changed-only Git path filtering.

## Mental Model

Changed-only behavior is Argo Application-aware. Shared resources from
different Applications remain separate diff identities; drydock does not
collapse overlapping Applications into one owner.

For detailed diff semantics and exit codes, see the [CLI usage guide](/docs/usage/)
and [compatibility notes](/docs/compatibility/).
