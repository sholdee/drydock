---
title: Local Diffs
---

Use local diffs to review desired-state changes before opening or merging a
pull request. drydock renders both sides and compares Argo CD desired
manifests, so operators can review resource and image changes from repository
inputs alone.

## Compare Two Trees

```bash
git worktree add ../baseline main
drydock diff apps --path . --path-orig ../baseline
drydock diff images --path . --path-orig ../baseline
```

Use this when you already have a separate baseline checkout or want to inspect
uncommitted changes in the current working tree. Add `--offline` when the
review must use only local files, repo maps, and existing drydock cache entries.
Default output is intended for terminal review and uses TTY-appropriate
color and syntax highlighting when available.

## Compare Git Refs

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main
```

`--repo` is a local Git repository path. `--ref-orig` selects the baseline
snapshot, and `--ref` selects the current snapshot. Top-level remote `--repo`
URLs are not supported.

## Useful Diff Flags

- `--changed-only=false` renders all discovered Applications.
- `--strict-changed-only` fails if changed-file ownership is incomplete.
- `--changed-only-include 'apps/**'` considers only matching changed paths.
- `--changed-only-ignore '.github/**'` removes matching paths before ownership.
- `--skip-secrets` omits Secret resources from output and diffs.
- `--skip-crds` omits CRDs from output and diffs.
- `--show-ignored-fields` shows drydock default ignored metadata fields.
- `--exit-code=false` keeps the command successful when differences exist.

Changed-only filters are repository-relative and repeatable. They are useful
when local commits include known non-GitOps files, but they should stay narrow:
ignored files cannot trigger an Application render.

## Markdown For Pull Request Comments

Manifest and image diffs support markdown output for pull request comments:

```bash
drydock diff apps --path . --path-orig ../baseline -o markdown
drydock diff images --path . --path-orig ../baseline -o markdown
```

Manifest markdown is the primary review surface. It keeps the operator summary
near the top and puts rendered resource patches behind per-Application details:

````markdown
## drydock desired state diff

**Summary:** 2 apps, 3 resources, +12/-5.

<details open>
<summary>payments-api (+9/-3, 2 resources)</summary>

```diff
--- Application: payments-api apps/Deployment: payments/payments-api
+++ Application: payments-api apps/Deployment: payments/payments-api
@@ -31,7 +31,7 @@
-        app.kubernetes.io/version: 2026.05.0
+        app.kubernetes.io/version: 2026.05.1
@@ -48,7 +48,7 @@
-          image: registry.example.com/payments-api:2026.05.0
+          image: registry.example.com/payments-api:2026.05.1
```

</details>
````

Image markdown is a companion view for scanning rendered image reference
changes. It omits unchanged images by default so the comment stays focused on
added and removed references.

For the full diff behavior, output formats, ignore rules, and exit codes, see
the [CLI usage guide](/docs/usage/).
