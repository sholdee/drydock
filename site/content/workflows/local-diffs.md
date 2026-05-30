---
title: Local Diffs
---

Use local diffs to compare desired state before opening or merging a pull
request, or any time you want to inspect repository changes before Argo CD sees
them. drydock renders both sides and compares desired manifests; it does not ask
live Argo CD or Kubernetes for current state.

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

Manifest markdown is the primary review surface:

````markdown
## drydock desired state diff

**Summary:** 1 app, 2 resources, +4/-2.

<details open>
<summary>renovate (+4/-2, 2 resources)</summary>

```diff
--- Application: renovate apps/Deployment: renovate/renovate-operator
+++ Application: renovate apps/Deployment: renovate/renovate-operator
@@ -47,7 +47,7 @@
-          image: ghcr.io/example/renovate:1.0.0
+          image: ghcr.io/example/renovate:1.1.0
```

</details>
````

Image markdown is a companion view for scanning rendered image reference
changes, and omits unchanged images by default.

For the full diff behavior, output formats, ignore rules, and exit codes, see
the [CLI usage guide](/docs/usage/).
