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
fetch declared Git, Helm, OCI Helm, OCI artifact, and remote Kustomize
sources into drydock caches. `--offline` disables those source network fetches and requires local
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
   `actions/checkout`-style checkouts outside the pr-action, set it from the
   default branch the workflow already knows:

   ```yaml
   - run: >-
       git remote set-head origin
       "${{ github.event.repository.default_branch }}"
   ```

   That is local-only but needs `refs/remotes/origin/<default-branch>`, which
   a `fetch-depth: 0` checkout provides. `git remote set-head origin -a`
   queries the remote instead, so it needs credentials that a checkout with
   `persist-credentials: false` does not leave behind.

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

## Symptom: OCI Source Renders The Local Checkout Or Fails With `load helm chart .`

A first-class OCI artifact source — any `repoURL: oci://...` — has two
historical failure modes on older drydock releases, which did not classify
`oci://` URLs:

- With a `helm:` block, the render failed with an error like
  `load helm chart .`, because the local repository root was treated as the
  chart directory.
- Without a `helm:` block, the run silently succeeded but rendered the LOCAL
  checkout as plain manifests (`path: .` always exists locally), not the
  artifact content.

Upgrade drydock: current releases classify every `oci://` source before local
path resolution, resolve the revision to a digest, and render the pulled
artifact content. If you added `--repo-map` of the `oci://` URL as a
workaround, it still wins over registry acquisition — remove it when you want
the artifact fetched, keep it when a local checkout of the content should be
rendered instead.

Related shapes on current releases:

- `chart:` on an `oci://` source is ignored, as in Argo CD, and reported as
  `source.oci-chart-ignored` — see the next symptom.
- An OCI URL as a `$ref` value-file source fails clearly; Argo CD supports
  Git-only `$ref` sources.
- An `offline cache miss` error under `--offline` means the digest, tag, or
  constraint was never resolved online against this cache directory — run
  once without `--offline` to warm the image cache and tag records.

## Symptom: `source.oci-chart-ignored`, Or An `oci://` + `chart:` Source Is Not Found

An Application whose `repoURL` starts with `oci://` and also sets `chart:`:

```yaml
source:
  repoURL: oci://ghcr.io/bjw-s-labs/helm/app-template
  chart: app-template
  targetRevision: 5.2.1
```

Argo CD's repo-server dispatches every `oci://` URL as an OCI artifact source
before it considers Helm, so it pulls `repoURL` itself at `targetRevision`
and never reads `chart:`. Current drydock releases do the same and report one
advisory warning per Application source:

```text
warning source.oci-chart-ignored: Application argocd/app source[0] chart="app-template": chart "app-template" is ignored: Argo CD treats every oci:// repository URL as an OCI artifact source and pulls oci://ghcr.io/bjw-s-labs/helm/app-template at revision "5.2.1" without reading spec.source.chart, so drydock renders that artifact; to silence this warning drop chart and set path: . (Argo CD requires path or chart on every source, or ref in a multi-source list); only if you meant the chart named "app-template" under repository ghcr.io/bjw-s-labs/helm/app-template, switch to the schemeless Helm OCI shape (repoURL "ghcr.io/bjw-s-labs/helm/app-template", chart "app-template")
```

The warning is advisory: `--strict` neither escalates it nor fails on it, the
manifests are identical with or without it, and the Argo CD render parity
smoke pins the shape live. Two remedies, both Argo-valid:

- Keep the artifact: drop `chart:` and set `path: .` (Argo CD requires `path`
  or `chart` on every source, or `ref` in a multi-source list).
- Keep a chart under a repository: switch to the schemeless Helm-OCI shape,
  `repoURL: ghcr.io/bjw-s-labs/helm` plus `chart: app-template`, which uses
  the Helm registry client and the chart cache.

Older drydock releases pulled `<repoURL>/<chart>:<version>` for this shape
instead. A source that relied on that now pulls `repoURL:targetRevision`,
which fails with `resolve OCI artifact ... not found` or an
`authenticate OCI artifact ...` error (registries answer 401/403 for private
and for nonexistent repositories alike); the warning is printed alongside,
and the schemeless remedy above restores the previous pull.

Credential errors surface at the first `oci://` acquisition and name the
flag. `--registry-config "<path>" does not exist` means the path is wrong —
runs without OCI artifact sources never open the file, so this appears only
on OCI pulls. `the credential for <host> is an identity token` means the
file's entry came from a token login that the OCI artifact client cannot
present; log in to that registry with a username and password or pass
`--oci-username`/`--oci-password`. An error naming
`docker-credential-<helper>` means the file routes that host through a
credential helper that cannot run here; use a plain `auths` file or the
`--oci-*` flags.

Under `--offline`, including the pr-action's `offline` input, an Application
that moved from the chart flow to the artifact flow fails with
`offline cache miss` until one online run with `--oci-cache-dir` warms the
image cache and tag records:

```bash
drydock build apps --path . --oci-cache-dir /var/cache/drydock-oci
drydock build apps --path . --oci-cache-dir /var/cache/drydock-oci --offline
```

Its earlier chart-cache entries are orphaned; `drydock cache prune --source
chart` removes them.

## Symptom: Changed-Only Falls Back To All Apps

Multi-Application diffs use changed-only selection by default. If a changed
file cannot be safely mapped to Application inputs, non-strict mode warns and
renders all Applications. Use strict mode when ambiguous ownership should fail:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --strict-changed-only
```

If the unowned file is one an exec or container plugin reads, declare it in the
plugin Application's `argocd.argoproj.io/manifest-generate-paths` annotation
so changed-only selects that Application instead of rendering all. Declare it
too when a plugin reads a file another Application owns, through its graph or
its own annotation: changed-only then renders only that owner, and strict mode
does not catch the omission. See
[Declare plugin inputs](/workflows/local-diffs/#declare-plugin-inputs).

A changed Argo CD setting that every render or diff reads also renders all
Applications, with `diff.changed-only-settings`: the tracking method, resource
exclusions, compare options, an `ignoreDifferences` customization, or a CMP
definition, for example. A repository or cluster Secret change does not,
unless the Secret enables OCI for an OCI repository URL: it selects only the
Applications that use it (`diff.changed-only-settings-scoped`). Neither it nor
a setting no output reads excuses an unowned changed path, which still renders
all Applications and fails `--strict-changed-only`.

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
