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
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

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

For deeper reference, see the [CLI usage guide](/docs/usage/), [Compatibility](/compatibility/),
and [Source acquisition](/concepts/source-acquisition/).
