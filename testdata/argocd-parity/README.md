# Argo CD parity fixture

This fixture is a deterministic local GitOps repository for the manual Argo CD
parity smoke. The manifests under `repo/` are intentionally small and avoid
remote chart and remote Kustomize fetches.

Application and ApplicationSet specs use the canonical placeholder repository
URL:

`git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git`

The smoke harness maps that URL back to this local fixture repository with
`--repo-map` so drydock and Argo CD render the same source tree.

## Source override merge semantics

`applications/source-overrides.yaml` renders `charts/source-overrides`, which
carries a path-level `.argocd-source.yaml` and a per-Application
`.argocd-source-parity-source-overrides.yaml` (a bare-name file, because this
Application is in the controller namespace, so its instance name is its
`metadata.name`).

The chart pins two expectations at once. `pathLevel: from-argocd-source`
proves the path-level file is read: only `.argocd-source.yaml` sets that key,
and it sets it under `helm.valuesObject`, which merges key by key and so
survives the second file. `fromRepoOverride: from-default` proves override
merging is an RFC 7386 JSON merge patch that replaces arrays wholesale: the
per-Application file's `helm.parameters` list replaces the path-level list
entirely, discarding the path-level `fromRepoOverride` parameter before Helm
runs, so the chart default wins.

Both Argo CD and drydock must render this `data`:

| key | value |
| --- | --- |
| `fromRepoOverride` | `from-default` |
| `fromAppOverride` | `from-app-specific-source` |
| `both` | `from-app-specific-source` |
| `pathLevel` | `from-argocd-source` |

Keep the `helm.parameters` lists in both override files. The tenant chart
below deliberately avoids `parameters` so its keys survive merging; this chart
deliberately uses them so the replacement itself stays pinned. Converting
these files to `valuesObject` would delete that coverage silently.

## Tenant namespace fixture

`projects/parity-tenant.yaml` and `tenant-applications/` cover Applications
that live outside the Argo CD controller namespace, which is where the
instance-name rules become visible.

The smoke enables the `parity-tenant` namespace by patching
`application.namespaces` in `argocd-cmd-params-cm` and restarting the server
and controller *before* it logs in to Argo CD and applies fixtures, because
`login_argocd` starts a port-forward bound to a single `argocd-server` pod
that a later restart would kill.

`tenant-applications/parity-tenant-overrides.yaml` is a Helm Application in
the `parity-tenant` namespace whose parameters substitute `$ARGOCD_APP_NAME`
and `$ARGOCD_APP_NAMESPACE`. Its chart `charts/tenant-overrides` carries three
override files:

- `.argocd-source.yaml` — the path-level override, read for every
  Application that renders this source path.
- `.argocd-source-parity-tenant_parity-tenant-overrides.yaml` — the
  per-Application override, named by the Application *instance name*
  (`<namespace>_<name>`) because the Application is outside the controller
  namespace.
- `.argocd-source-parity-tenant-overrides.yaml` — a deliberate decoy named by
  the bare Application name. The repo-server never reads it for this
  Application; it exists so a regression that resolves the override file by
  `metadata.name` flips the comparison instead of passing silently.

The override files use `helm.valuesObject` maps rather than `helm.parameters`
lists: override merging is an RFC 7386 JSON merge patch, which replaces arrays
wholesale, so a `parameters` list in an override would erase the Application's
own `appName`/`appNamespace` parameters. Maps merge key by key, later files win
per key, and Helm applies `parameters` over `valuesObject`, so the six keys
stay independent.

Both Argo CD and drydock must render this `data`:

| key | value |
| --- | --- |
| `fromRepoOverride` | `from-argocd-source` |
| `fromAppOverride` | `from-instance-override` |
| `both` | `from-instance-override` |
| `decoy` | `from-default` |
| `appName` | `parity-tenant_parity-tenant-overrides` |
| `appNamespace` | `parity-tenant-workloads` |

The `sourceNamespaces: [parity-tenant]` entry on the `parity-tenant`
AppProject is what lets live Argo CD reconcile the Application at all; without
it the controller refuses it and `argocd app manifests` returns nothing.
drydock reports a missing `sourceNamespaces` entry only as a warning
diagnostic, so this half of the rule is enforced by live Argo CD.

`parity-tenant-overrides` is also in the tracking comparison, which runs
without ignore rules, so the instance name in the tracking annotation is
compared too.

## OCI artifact fixture

`oci-artifact/` is the content directory for the one first-class OCI
Application, `parity-oci-config`:

- repoURL `oci://argocd-parity-registry.argocd-parity.svc.cluster.local:5443/parity/config`
- exact pinned tag `v1.0.0`, `path: .`, plain manifests only

The smoke harness pushes the directory contents with `oras push` from inside
the directory, producing exactly one
`application/vnd.oci.image.layer.v1.tar+gzip` content layer with the
manifests at the extraction root, then verifies that manifest shape before
Argo CD ever sees the artifact. drydock warms its `--oci-cache-dir` with one
non-offline build of the app before the offline per-app loop runs.

The content directory deliberately lives outside `repo/` so the artifact
content is never part of the git fixture Argo CD serves. An OCI
classification regression that fell back to path-exists resolution would
render the whole fixture repository instead of the artifact and flip the
comparison hard.

### Deliberately not covered by this fixture

- The `oci://` + `chart:` divergence shape: live Argo CD v3.4.5 rejects that
  combination, and the harness has no expected-to-fail-on-Argo notion
  (capture hard-fails on empty output). That shape stays pinned by fleet and
  unit tests only.
- The helm-content-inside-first-class-artifact shape (no `chart:` field, a
  `Chart.yaml` inside the artifact): hermetically pinned by unit tests, not
  live-pinned. This plain-manifests fixture does not cover it. It is a
  distinct shape from the `oci://` + `chart:` divergence above; do not
  conflate the two.
- Semver tag constraints (a tag-list moving part), registry authentication
  (an htpasswd surface for a hermetically-pinned path), and multi-registry
  setups.

### Local runs

The registry bridge requires an `/etc/hosts` entry for
`argocd-parity-registry.argocd-parity.svc.cluster.local`. The smoke script
appends it with `sudo` and removes its own marked line on exit; a
pre-existing entry for that hostname skips `sudo` entirely. On macOS the
bridge uses local port 5443 (AirPlay owns 5000) and certificate generation
uses the LibreSSL-compatible config-file `subjectAltName` form.
