---
title: Runtime Offline
---

Runtime-offline means drydock does not need live Argo CD or Kubernetes runtime
access to discover, render, test, diff, inspect images, or report diagnostics.
It renders desired state from repository contents, explicit mappings, and
drydock caches.

Runtime-offline does not mean every source network request is disabled.
Declared Git, HTTP Helm, OCI Helm, OCI artifact, and remote Kustomize sources
can still be fetched into explicit caches unless `--offline` is set.

## Runtime Boundary

Runtime-offline describes the live services drydock does not need while a
command runs. It does not call:

- Live Kubernetes APIs.
- Live Argo CD APIs.
- Argo CD server-side diff.
- Kubernetes defaulting or admission webhooks.
- Live managed-fields ownership data.
- Live Application health aggregation.
- Argo CD RBAC/Casbin authorization, sync-window scheduling, orphan-resource
  detection, source signature verification, or sync impersonation.

Those behaviors stay runtime-boundary diagnostics or documented gaps; drydock
does not silently approximate them from live state.

For how covered render output is validated against real Argo CD, see
[Argo CD Render Parity](/concepts/argocd-render-parity/).

## When To Use `--offline`

Use `--offline` when a command must be source-offline as well as
runtime-offline:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

With `--offline`, source resolution must use local files, `--repo-map`, or
existing cache entries.

## OCI Artifact Sources Offline

OCI artifact sources have revision-dependent offline behavior:

- **Digest-pinned revisions** (`sha256:...`) pass through with no resolution
  network call and render from the cached image for that digest.
- **Tags and semver constraints** resolve from records that online runs write
  into the OCI cache on every successful resolve: the tag-to-digest pairs it
  resolved and, for constraints, the registry tag list. A recorded tag or a
  constraint satisfiable from the recorded tag list renders offline; anything
  not yet recorded fails.

A revision that cannot be resolved offline — or a digest whose image was
never pulled — fails with an `offline cache miss` error. The remediation is
one online run of the same render against the same cache directory
(`--oci-cache-dir` if overridden), which populates both the image cache and
the tag records, then re-run with `--offline`.

Because online runs always re-resolve tags against the registry and overwrite
the records, offline tag resolution reflects the most recent online run —
there is no staleness window to manage and no refresh flag.

For the complete support boundary, see [Compatibility](/compatibility/) and
[Argo CD Render Parity](/concepts/argocd-render-parity/).
