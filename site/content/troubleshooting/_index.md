---
title: Troubleshooting
---

Start with the symptom. Run the smallest command that exercises the failing
path, then add `--strict` when warnings should fail the run or `-o json` /
`-o yaml` when another tool needs stable diagnostics.

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

## Symptom: No Applications Discovered

Check that the selected `--path` points at the GitOps repository root or pass
the directory that contains Argo CD objects. If the repository stores bootstrap
inputs as Kustomize rather than committed inflated objects, add an explicit
entrypoint:

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
```

If the repository uses ApplicationSet providers, make sure the workflow passes
the same fixture inputs used for local verification.

## Symptom: ApplicationSet Warns And Generates Zero Applications

An `appset.template-render-failed` warning means template execution failed for
that ApplicationSet, so it contributes no Applications — the same outcome the
Argo CD controller reports as an `ErrorOccurred` condition. A common cause is
a Git files generator matching an empty or comment-only param file while the
template references file params under `missingkey=error`: an empty file
decodes to one empty param set, matching Argo CD. If a matched file should
generate nothing, make its content `[]`. Use `--strict` when the warning
should fail the run.

In one-sided diffs, the failing side's zero desired Applications appear as
additions or deletions, while the live controller would abort reconciliation
without pruning.

## Symptom: Discovery Fails Decoding Non-Deployable YAML

Discovery errors that end with
`(use --discover-ignore to exclude non-deployable manifests from discovery)`
mean the scan found YAML it could not decode, such as unrendered chart
templates or scaffolding committed alongside real Argo CD objects:

```text
templates/scaffold.yaml: decode ApplicationSet: json: cannot unmarshal string
into Go struct field ... of type int64 (use --discover-ignore to exclude
non-deployable manifests from discovery)
```

If the file is not deployable Argo CD intent, exclude it from discovery with a
repository-relative glob:

```bash
drydock test apps --path . --discover-ignore 'templates/**'
```

Matching files are skipped before decoding, even when named by explicit app
manifest paths. If the failing file is a real Application manifest, fix the
manifest instead; ignoring it hides it from every discovery-based command.

## Symptom: Render Fails In CI But Works Locally

Confirm whether CI has the same source access and caches. Default runs may
fetch declared Git, Helm, OCI Helm, and remote Kustomize sources into drydock
caches. `--offline` disables those source network fetches and requires local
files, repo maps, or existing cache entries.

```bash
drydock test apps --path . --offline
drydock diag --path . --cache-events
```

`--cache-events` renders Applications so source-acquisition events match the
rendering path.

## Symptom: Self-Repo Values Render From The Remote / Values ENOENT On Renders

A source whose `repoURL` is the checkout's own repository — commonly a
ref-only `$repo` source supplying Helm value files — should resolve to the
local tree on every render surface (`get`, `build`, `test`, `diag`, and both
diff sides). If such a source instead fetches remotely, renders miss files
that exist only locally (for example, a pull request's new values file fails
with a not-found error, or edits are silently ignored).

Two remediations:

1. Upgrade drydock (older releases resolved self-repository sources only
   during diffs) and make sure `refs/remotes/origin/HEAD` exists so drydock
   can learn the default-branch name — `git clone` sets it, and the pr-action
   records the pull request's base branch there whenever a base ref is known
   (pull-request events, or an explicit `base-ref` input). For bare
   `actions/checkout`-style checkouts outside the pr-action, run:

   ```bash
   git remote set-head origin -a
   ```

   Without that symref, sources pinned to the default-branch name acquire
   remotely — drydock never guesses the default branch from
   `init.defaultBranch` or the checked-out HEAD.

2. Use `--repo-map URL=PATH` when detection cannot apply: fork-shaped URLs
   (watch for the `source.self-repo-near-miss` warning), commit-SHA pins, or
   runs from a subdirectory of the checkout (`--path <subdir>` does not walk
   up to the enclosing `.git`).

The flip side of local resolution: a self-repository source `path` that was
deleted locally fails path-not-found even though the remote tip still has it —
the local tree is the desired state.

## Symptom: Changed-Only Falls Back To All Apps

Multi-Application diffs use changed-only selection by default. If a changed
file cannot be safely mapped to Application inputs, non-strict mode warns and
renders all Applications. Use strict mode when ambiguous ownership should fail:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --strict-changed-only
```

## Symptom: Plugin Source Fails Closed

The CLI and default Go client do not execute config management plugin commands
by default. Safe Kustomize wrapper plugins and argocd-vault-plugin
compatibility may render natively. Exec or container plugin rendering needs
trusted drydock plugin policy, trusted policy provenance, and
`--enable-plugins`.

```bash
drydock plugin-policy doctor --path .
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

Use `plugin-policy doctor` to check whether drydock sees the plugin policy,
native compatibility options, trusted provenance, and command execution gates.
Then add trusted plugin flags only when the render actually needs exec or
container plugin commands.

If plugin-rendered bootstrap apps are missing, check PluginPolicy
`bootstrap.entrypoints`. Static discovery mode disables those entrypoints;
`--max-discovery-depth 0` does not.

See [Plugin policy](/plugin-policy/) for the operator gate.

## Symptom: Diff Noise Is Hiding Or Showing Too Much

drydock hides common Helm chart/version labels and pod-template checksum
annotations by default. Use `--show-ignored-fields` to inspect them, or
`--strip-attr KEY` to remove additional label or annotation keys before
comparison.

## Symptom: PR Comment Is Too Large Or Hard To Scan

Use markdown output for the review surface and keep full artifacts for deeper
inspection:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main -o markdown
drydock diff images --repo . --ref HEAD --ref-orig main -o markdown
```

For sensitive or noisy resource classes, use explicit filters such as
`--skip-secrets`, `--skip-crds`, or repeatable `--strip-attr KEY`.

For deeper reference, see the [reference hub](/reference/), [Compatibility](/compatibility/),
and [Source acquisition](/concepts/source-acquisition/).
