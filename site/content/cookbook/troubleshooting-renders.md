---
title: Troubleshooting Renders
---

## Start Narrow

```bash
drydock get apps --path .
drydock test apps --path .
drydock diag --path .
```

`get apps` checks discovery. `test apps` checks render health without manifest
output. `diag` reports repository and settings diagnostics without rendering
every Application; add `--render` when the diagnostic report needs render-backed
diagnostics.

## Isolate One Application

```bash
drydock test app argocd/my-app --path .
drydock build app argocd/my-app --path .
```

Use `NAMESPACE/NAME` when names are reused across namespaces.

## Check Source Access

```bash
drydock test apps --path . --offline
drydock diag --path . --cache-events -o json
```

If offline mode fails, add repo maps or populate caches before expecting
cache-only CI runs to pass.

## Check Plugins

```bash
drydock test apps --path .
drydock plugin-policy doctor --path .
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

Run without plugin execution first, then use `plugin-policy doctor` to check
policy readiness. Add trusted plugin policy flags only when the failing source
requires exec or container plugin commands.

## Remove Diff Noise

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --show-ignored-fields
drydock diff apps --repo . --ref HEAD --ref-orig main --strip-attr checksum/config
```

Use `--show-ignored-fields` to inspect default ignored fields, and
`--strip-attr` for extra label or annotation noise.

For symptom-based routing, see [Troubleshooting](/troubleshooting/). For full
command behavior, see the [reference hub](/reference/).
