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

## Force Cluster Capabilities

```bash
drydock test apps --path . --api-versions monitoring.coreos.com/v1/ServiceMonitor
drydock build apps --path . --kube-version 1.31 --api-versions monitoring.coreos.com/v1
```

Some charts gate optional resources on cluster capabilities, such as rendering a
`ServiceMonitor` only when `monitoring.coreos.com/v1` is available. drydock
already derives a default Kubernetes version from its embedded Helm
libraries, so the default tracks a real cluster version without a
flag. Use these capability flags when a render must target a specific cluster's
capabilities:

- `--kube-version` overrides the per-app `kubeVersion` with a specific
  Kubernetes version. The global override wins over any per-app value.
- `--api-versions` adds Kubernetes API versions for capability-gated rendering.
  The flag is repeatable, and the values are unioned (deduped and sorted) with
  each app's per-app `apiVersions` rather than replacing them. Accept either the
  `group/version` form (`monitoring.coreos.com/v1`) or the full
  `group/version/Kind` form (`monitoring.coreos.com/v1/ServiceMonitor`). Many
  charts check the full `group/version/Kind` form against
  `.Capabilities.APIVersions`, so pass the `group/version/Kind` form when a
  capability-gated resource still does not render.

Both flags are common render flags, available on the render-backed commands
(`build`, `test`, `diff`, and `get`, in their `app`/`apps` forms).

## Remove Diff Noise

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main --show-ignored-fields
drydock diff apps --repo . --ref HEAD --ref-orig main --strip-attr checksum/config
```

Use `--show-ignored-fields` to inspect default ignored fields, and
`--strip-attr` for extra label or annotation noise.

For symptom-based routing, see [Troubleshooting](/troubleshooting/). For full
command behavior, see the [reference hub](/reference/).
