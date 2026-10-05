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

`--path . --ref-orig main` compares the current working tree, including tracked
uncommitted changes, against committed `main`. `--repo . --ref feature
--ref-orig main` compares committed refs only.

## Useful Diff Flags

- `--changed-only=false` renders all discovered Applications.
- `--strict-changed-only` fails if changed-file ownership is incomplete.
- `--changed-only-include 'apps/**'` considers only matching changed paths.
- `--changed-only-ignore '.github/**'` removes matching paths before ownership.
- `--discover-ignore 'templates/**'` excludes matching files from discovery
  before decoding.
- `--skip-secrets` omits Secret resources from output and diffs.
- `--skip-crds` omits CRDs from output and diffs.
- `--show-ignored-fields` shows drydock default ignored metadata fields.
- `--exit-code=false` keeps the command successful when differences exist.

Changed-only filters are repository-relative, slash-normalized, and
repeatable. When no include globs are set, every changed path is considered.
Ignore globs remove paths after include filtering, so ignore wins.

If filtering removes every changed path, the diff is empty and no Applications
render. `--strict-changed-only` applies only to the remaining considered paths.
Keep broad ignores out of plugin-heavy or unconventional repositories, because
ignored files cannot trigger an Application render.

Changed Argo CD settings are compared between the two trees. A setting every
render or diff reads, such as the tracking method or resource exclusions,
renders all Applications (`diff.changed-only-settings`). A repository or
cluster Secret change selects only the Applications that use it
(`diff.changed-only-settings-scoped`). Settings no output reads, such as action
Lua, select nothing. See [Changed-only diffs](/concepts/changed-only/).

`diff app` selects one requested Application directly in each tree and does not
use changed-only Git path filtering. If the Application exists only in current,
the diff shows additions; if it exists only in baseline, the diff shows
deletions.

## Declare Plugin Inputs

Changed-only selection models Application manifests, source paths, Helm value
files, and the local Kustomize graph. It cannot see which files an exec or
container plugin reads. If a plugin reads a file outside its source path, a
change to that file renders all Applications when nothing else owns it. When
another Application owns the file, through its graph or its own
`manifest-generate-paths` annotation, only that Application renders: the plugin
Application is silently left out, and `--strict-changed-only` passes. A
declared path counts as owned, so every plugin Application that reads it must
declare it.

Declare those inputs with Argo CD's
[`argocd.argoproj.io/manifest-generate-paths`](https://argo-cd.readthedocs.io/en/stable/operator-manual/high_availability/#manifest-paths-annotation)
annotation:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: reader
  annotations:
    # The source path, plus shared/ from the repository root.
    argocd.argoproj.io/manifest-generate-paths: .;/shared
spec:
  source:
    repoURL: https://github.com/example/repo
    path: workloads/reader
    plugin:
      name: helmfile
```

drydock uses Argo CD's syntax for the annotation. Entries are `;`-separated.
Relative entries resolve against each source's `path`, so `.` is the source
path itself. A leading `/` makes an entry relative to the repository root.
Globs match with Go's `path.Match`, so `/shared/*.yaml` matches
`shared/config.yaml` but not `shared/nested/config.yaml`. A changed file
selects the Application when it is a declared path, lies under one, or matches
a declared glob.

Three rules narrow Argo CD's reading. Sources without a local path, such as
chart-only or ref-only sources, use only `/` entries; OCI sources contribute
nothing. Entries that resolve to the repository root, or escape it, are
ignored, so the annotation can never suppress the render-all fallback for the
whole repository. Backslashes are read as path separators, like every other
selection path, so a glob metacharacter cannot be escaped with `\`.

## Manifest Diff Semantics

Manifest diffs hide common Helm-rendered metadata noise by default:

- `metadata.labels.helm.sh/chart`
- `metadata.labels.chart`
- `metadata.labels.app.kubernetes.io/version`
- `spec.template.metadata.labels.helm.sh/chart`
- `spec.template.metadata.labels.chart`
- `spec.template.metadata.labels.app.kubernetes.io/version`
- `spec.template.metadata.annotations.checksum/*`

Use `--show-ignored-fields` on `diff apps` or `diff app` to include these
drydock-default ignored fields again. This flag does not disable Argo CD
`ignoreDifferences`, compare options, or explicit `--strip-attr` filters.

Use repeatable `--strip-attr KEY` to remove matching keys from
`metadata.labels` and `metadata.annotations` before comparing rendered
manifests:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --strip-attr helm.sh/chart \
  --strip-attr app.kubernetes.io/version
```

Application-level `spec.ignoreDifferences[]` rules and global
`resource.customizations.ignoreDifferences.*` settings are honored for rendered
resource diffs. Supported ignore fields are `jsonPointers`,
`jqPathExpressions`, and `managedFieldsManagers`. When a matching resource
exists on both sides, drydock applies the union of matching Application-local
and global settings from the baseline and current trees.

By default status is ignored for all resources. Use
`ignoreResourceStatusField: none`, `off`, or `false` in discovered Argo CD
compare options when rendered status fields should remain visible in PR diffs.

Rendered-resource filters run before diff comparison. Argo CD core exclusions
and discovered `resource.exclusions`/`resource.inclusions` are applied
automatically. Omit CRDs and Secrets from a pull request diff with:

```bash
drydock diff apps \
  --path-orig ../base \
  --path ./current \
  --skip-crds \
  --skip-secrets
```

## Image Diff Semantics

`diff images` projects image references from rendered manifests. The projection
includes PodSpec container images plus scalar manifest fields whose key is
exactly `image`.

It does not scan arbitrary string content, Secret manifests, top-level
metadata/status, or ConfigMap data payloads. Use `-o name` to print
current-only added image references, one per line. Removed-only image changes
print no names but still return the diff exit code unless `--exit-code=false`
is set.

`diff images` uses the same changed-only defaults and path filters as
`diff apps`.

## Markdown For Pull Request Comments

Manifest and image diffs support markdown output for pull request comments:

```bash
drydock diff apps --path . --path-orig ../baseline -o markdown
drydock diff images --path . --path-orig ../baseline -o markdown
```

Manifest markdown is the primary review surface. It keeps the operator summary
near the top and puts rendered resource patches behind per-Application details:

````markdown
## drydock diff

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

For output formats and exit codes, see [Output controls](/workflows/output/).
For command behavior outside diffs, see the [CLI Reference](/reference/cli/).
