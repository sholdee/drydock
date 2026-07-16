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

Scope the changed paths considered by ownership mapping:

```bash
drydock diff apps \
  --repo . \
  --ref HEAD \
  --ref-orig main \
  --changed-only-include 'apps/**' \
  --changed-only-ignore '.github/**'
```

Include and ignore globs are repository-relative. If no include globs are set,
all changed paths are considered. Ignore globs are applied after includes, so
ignore wins. When every changed path is filtered out, drydock reports an empty
diff instead of rendering the fleet.

`diff app` selects one requested Application directly and does not use
changed-only Git path filtering.

## Changed-Only Ignore vs Discover Ignore

`--changed-only-ignore` and `--discover-ignore` share glob syntax but act at
different stages. `--changed-only-ignore` filters which changed paths are
considered for changed-only ownership; the files themselves are still
discovered and decoded. `--discover-ignore` removes matching files from
repository discovery before decoding, on every command that discovers
Applications.

The two flag families stay independent under `--strict-changed-only`: a
discover-ignored file that changes still surfaces as an unowned changed path
unless it is also `--changed-only-ignore`d. Repositories that commit
non-deployable YAML usually want the same glob in both flags:

```bash
drydock diff apps \
  --repo . \
  --ref HEAD \
  --ref-orig main \
  --strict-changed-only \
  --discover-ignore 'templates/**' \
  --changed-only-ignore 'templates/**'
```

## Mental Model

Changed-only behavior is Argo Application-aware. Shared resources from
different Applications remain separate diff identities; drydock does not
collapse overlapping Applications into one owner.

Path filters are explicit command or workflow policy. Keep filters narrow in
repositories that use plugins or unusual generation paths, because an ignored
file is no longer eligible to trigger a render.

For diff outputs and exit codes, see [Output controls](/workflows/output/) and
[Compatibility](/compatibility/).
