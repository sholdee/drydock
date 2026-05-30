---
title: Troubleshooting
---

Start with the command that exercises the same path as the failing workflow:

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

Add `--strict` when warnings should fail the run, and use `-o json` or
`-o yaml` when you need stable machine-readable diagnostics.

## No Applications Discovered

Check that the selected `--path` points at the GitOps repository root or pass
the directory that contains Argo CD objects. If the repository stores bootstrap
inputs as Kustomize rather than committed inflated objects, add an explicit
entrypoint:

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
```

## Render Fails In CI But Works Locally

Confirm whether CI has the same source access and caches. Default runs may
fetch declared Git, Helm, OCI Helm, and remote Kustomize sources into drydock
caches. `--offline` disables those source network fetches and requires local
files, repo maps, or existing cache entries.

```bash
drydock test apps --path . --offline
drydock diag --path . --cache-events
```

## Changed-Only Falls Back To All Apps

Multi-Application diffs use changed-only selection by default. If a changed
file cannot be safely mapped to Application inputs, non-strict mode warns and
renders all Applications. Use strict mode when ambiguous ownership should fail:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --strict-changed-only
```

## Plugin Source Fails Closed

The CLI and default Go client do not execute config management plugin commands
by default. Safe Kustomize wrapper plugins may render natively. Other plugin
sources need trusted drydock plugin policy, and exec policy also needs
`--enable-plugins`.

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

See [Plugin policy](/plugin-policy/) for the operator gate.

## Diff Noise Is Hiding Or Showing Too Much

drydock hides common Helm chart/version labels and pod-template checksum
annotations by default. Use `--show-ignored-fields` to inspect them, or
`--strip-attr KEY` to remove additional label or annotation keys before
comparison.

For deeper reference, see the [CLI usage guide](/docs/usage/), [Compatibility](/compatibility/),
and [Source acquisition](/concepts/source-acquisition/).
