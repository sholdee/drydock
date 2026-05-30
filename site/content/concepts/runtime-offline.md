---
title: Runtime Offline
---

Runtime-offline means drydock does not need live Argo CD or Kubernetes runtime
access to discover, render, test, diff, inspect images, or report diagnostics.
It renders desired state from repository contents, explicit mappings, and
drydock caches.

Runtime-offline does not mean every source network request is disabled.
Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources can still be
fetched into explicit caches unless `--offline` is set.

## What drydock Does Not Call

- Live Kubernetes APIs.
- Live Argo CD APIs.
- Argo CD server-side diff.
- Kubernetes defaulting or admission webhooks.
- Live managed-fields ownership data.
- Live Application health aggregation.

Those behaviors stay runtime-boundary diagnostics or documented gaps; drydock
does not silently approximate them from live state.

## When To Use `--offline`

Use `--offline` when a command must be source-offline as well as
runtime-offline:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

With `--offline`, source resolution must use local files, `--repo-map`, or
existing cache entries.

For the complete support boundary, see the canonical
[compatibility notes](/docs/compatibility/) and the
[live runtime design gate](/docs/reports/live-integration-design-gate/).
