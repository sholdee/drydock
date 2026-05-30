---
title: Local Diffs
---

Use local diffs to compare desired state before opening or merging a pull
request. drydock renders both sides and compares desired manifests; it does not
ask live Argo CD or Kubernetes for current state.

## Compare Two Trees

```bash
git worktree add ../baseline main
drydock diff apps --path . --path-orig ../baseline
drydock diff images --path . --path-orig ../baseline -o markdown
```

Use this when you already have a separate baseline checkout or want to inspect
uncommitted changes in the current working tree.

## Compare Git Refs

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main -o json
```

`--repo` is a local Git repository path. `--ref-orig` selects the baseline
snapshot, and `--ref` selects the current snapshot. Top-level remote `--repo`
URLs are not supported.

## Useful Diff Flags

- `--changed-only=false` renders all discovered Applications.
- `--strict-changed-only` fails if changed-file ownership is incomplete.
- `--skip-secrets` omits Secret resources from output and diffs.
- `--skip-crds` omits CRDs from output and diffs.
- `--show-ignored-fields` shows drydock default ignored metadata fields.
- `--exit-code=false` keeps the command successful when differences exist.

## Markdown For Reviews

Manifest and image diffs support markdown output:

```bash
drydock diff apps --path . --path-orig ../baseline -o markdown
drydock diff images --path . --path-orig ../baseline -o markdown
```

Image markdown is comment-facing and omits unchanged images by default:

```markdown
## drydock image diff

**Summary:** 2 added, 1 removed.

| Change | Image |
| --- | --- |
| added | `registry.example.com/api:v2` |
| added | `registry.example.com/worker:v2` |
| removed | `registry.example.com/api:v1` |
```

For the full diff behavior, output formats, ignore rules, and exit codes, see
the [CLI usage guide](/docs/usage/).
