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

## Namespace gate fixture

`settings/argocd-cmd-params-cm.yaml` names its namespace, `argocd`, which
drydock needs to apply the lists. It sets `application.namespaces` to
`parity-tenant,parity-directory-decode,project-policy-tenant`. The smoke
sets the same value on the live ConfigMap in
`configure_application_namespaces`. Keep the two equal, because drydock
applies the list only from the ConfigMap that it reads.

`namespace-gate/` holds two objects that Argo CD never reconciles:

- `application.yaml` is `parity-namespace-disabled` in the
  `parity-namespace-disabled` namespace, which the list does not match.
- `applicationset.yaml` is `parity-appset-namespace-disabled` in
  `parity-tenant`. `applicationsetcontroller.namespaces` is not set, so the
  ApplicationSet controller watches only the `argocd` namespace, and the
  Application `parity-appset-namespace-disabled-app` never exists.

`assert_namespace_gate` makes sure that the Application has no
`reconciledAt`, that the Argo CD API server answers `permission denied` for
it, and that the ApplicationSet has no status. On the drydock side,
`get apps` must warn `discovery.namespace-not-enabled` for both objects.
The Application inventory comparison leaves the `parity-namespace-disabled`
namespace out of the Argo CD list, and it fails if drydock lists either
object or the Application that the ApplicationSet would generate.

## Application spec fixtures

Three Applications in the main comparison pin how Argo CD reads the spec:

| Application | What it pins |
| --- | --- |
| `parity-empty-project` | `spec.project: ""` is the `default` project. The chart `charts/project-name` renders `$ARGOCD_APP_PROJECT_NAME`, which must be `default`. The Argo CD controller writes `default` into the spec, and the inventory comparison requires `default` from drydock too. |
| `parity-helm-chart-file-name` | `workloads/helm-chart-file-name` holds `Chart.yaml`, `kustomization.yaml`, and `traefik-HelmChart.yaml`. The last name in name order that ends in `Chart.yaml` or is a kustomization file decides the type, so both sides render the Helm ConfigMap `parity-helm-chart-file-name-helm`, not the Kustomize one. |
| `parity-source-hydrator` | `spec.sourceHydrator` with a decoy `spec.source`. Argo CD compares only the sync source, `workloads/source-hydrator/hydrated` at the branch `parity-hydrated`. The hydrator is not enabled in the smoke cluster, so `prepare_fixture_git_image` commits that branch in `FIXTURE_GIT_WORK` as if the hydrator had written it. On the branch, the ConfigMap at the sync path is `parity-source-hydrator-hydrated-branch`. On `main`, the same path holds `parity-source-hydrator-hydrated`, and the dry and decoy paths hold their own. The drydock capture maps the fixture URL to `FIXTURE_GIT_WORK`, which has `main` checked out, so drydock must read the branch from Git. The smoke requires the branch ConfigMap in both captures. The drydock capture is a build, so it gives no `source.hydrator-sync-source` warning. |

`applicationsets/validation-empty-project.yaml`
(`parity-validation-empty-project`) generates `parity-empty-project-kept`
with the project `default` and `parity-empty-project-dropped` with an empty
project. The ApplicationSet controller looks the AppProject up by the empty
name, finds nothing, and creates only the first Application. drydock drops
the second with `appset.application-validation-failed`, and the inventory
comparison requires the same list on both sides.

`../missing-project/applicationset.yaml`
(`parity-validation-missing-project`) generates `parity-missing-project-kept`
with the project `default` and `parity-missing-project-dropped` with the
project `parity-missing-project`. No AppProject has that name. The
ApplicationSet controller does not create the second Application, and it
sets an `ErrorOccurred` condition with the message `application references
project parity-missing-project which does not exist`
(`applicationset_controller.go:626-633`). This is a recorded divergence.
drydock does not know the AppProjects of the cluster offline, so it keeps
the second Application and warns `project.missing` for it, with a note that
the ApplicationSet controller does not create it. The fixture lives in its
own directory with one AppProject, `../missing-project/project.yaml`, so
that the kept Application stays out of the main inventory comparison.
`run_missing_project_smoke` applies the directory after the main
comparisons. It waits for the condition, and it requires
`parity-missing-project-kept` and no `parity-missing-project-dropped` in
Argo CD. Then it runs `drydock get apps`, `drydock diag`, and `drydock build
apps` on the directory alone, with the fixture URL mapped to this
repository. drydock must list both Applications and warn `project.missing`
for the second one only. The build checks the AppProject before and after
rendering, and it must give exactly one warning.

`../crd-rejected/apps/parity-crd-sync-path-dot.yaml` sets
`syncSource.path: "."`. The CRD schema rejects it, so Argo CD never holds it.
`assert_crd_rejected_application` applies it with `--dry-run=server` and
requires the schema message. drydock must fail it with the same message. It
lives outside `repo/`, so the inventory comparison never sees it.

Five cases in `directory-decode-applications/` use `source: spec` in
`../directory-decode-expected.yaml`. Argo CD rejects their spec with an
`InvalidSpecError` condition before it renders anything, so they name no
file:

| Application | Why Argo CD rejects it |
| --- | --- |
| `parity-decode-spec-source-no-path` | a single source without `path` or `chart` |
| `parity-decode-spec-sources-no-ref` | a multi-source entry without `path`, `chart`, or `ref` |
| `parity-decode-spec-chart-no-revision` | a `chart` without `targetRevision` |
| `parity-decode-spec-oci-no-path` | an `oci://` source without `path` or `chart` |
| `parity-decode-spec-hydrator-no-branch` | a `spec.sourceHydrator` with an empty `syncSource.targetBranch` |

The smoke waits for the `InvalidSpecError` condition, and
`scripts/argocd-directory-decode-smoke` requires that condition type for
these cases. drydock must fail each one with the same text.
`TestParitySpecCasesMatchUpstream` in `internal/app` runs the committed
Applications offline through drydock and through the upstream
`ValidatePermissions`.

## Kustomize source option fixture

Argo CD v3.6.1 applies most `spec.source.kustomize` options with
`kustomize edit` commands on its copy of the source, before `kustomize
build` (`util/kustomize/kustomize.go:174-401`). The commands come from
kustomize v5.8.3. These fixtures pin the rules of those commands. The
sources live under `workloads/kustomize-source-options/`.

Four Applications in the main comparison render on both sides:

| Application | What it pins |
| --- | --- |
| `parity-kustomize-source-images` | `edit set image` sorts the images by name. So `a=b` runs before the `b` rename of the kustomization, and container `a` gets the new name of `b`. `nginx=...:*` keeps the `newTag` of the kustomization. A tag and a digest without `=` set both. `redis=` sets an entry with only the name, and no `redis` entry exists, so it changes nothing. `=memcached:1.6` counts as no `=`, so it replaces the `memcached` entry of the kustomization. The patch option `AllowNameChange` works, because kustomize matches field names without regard to letter case. |
| `parity-kustomize-source-labels` | The `team` key is in a `labels` entry of the kustomization, which `edit add label` does not look at, so the render does not fail. A value in double quotes loses the quotes, for labels and for annotations. |
| `parity-kustomize-source-labels-without-selector` | With `labelWithoutSelector` and `labelIncludeTemplates`, the labels go to a new `labels` entry. The `commonLabels` key and the key of the entry with `includeSelectors: true` are not looked at. |
| `parity-kustomize-source-components` | `../parts/*` adds `a` and `b`, but not `.hidden`. `a` is already in the kustomization, and the repeated `b` is added once. |

Eight more cases in `directory-decode-applications/` fail on both sides.
They use `source: render` in `../directory-decode-expected.yaml`, and each
one has its own source directory under `workloads/directory-decode/`:

| Application | Why it fails |
| --- | --- |
| `parity-decode-kustomize-source-image-bare` | the image `nginx` without a tag, a digest, or `=` |
| `parity-decode-kustomize-source-image-flag` | the image `-x`, which kustomize reads as a flag |
| `parity-decode-kustomize-source-label-exists` | the label `team` is already in `commonLabels`, with the same value |
| `parity-decode-kustomize-source-label-templates` | `labelIncludeTemplates` without `labelWithoutSelector` |
| `parity-decode-kustomize-source-annotation-exists` | the annotation `owner` is already in `commonAnnotations` |
| `parity-decode-kustomize-source-patches-null` | the kustomization has an empty `patches` field, and no edit command rewrites the file before Argo CD reads it |
| `parity-decode-kustomize-source-patch-option` | the patch option `unknownOption`, which `kustomize build` does not know |
| `parity-decode-kustomize-source-component-no-match` | a component glob that matches nothing |

`TestHelmRenderParityDecodeFixtures` in `internal/render` renders these
cases with drydock and requires `drydockMessage`. The tests in
`internal/render/kustomize_source_edit_test.go` hold the kustomize v5.8.3
test tables for these commands, and compare the flag reading with cobra.

## Config management plugin fixture

`applications/plugin-env.yaml` (`parity-plugin-env`) renders through a config
management plugin on both sides, so the plugin environment contract is pinned
against live Argo CD rather than by source reading alone.

- Argo CD side: `sidecar/plugin.yaml` is a `ConfigManagementPlugin` descriptor
  mounted from the `parity-env-cmp` ConfigMap into a sidecar patched onto
  `argocd-repo-server` by `sidecar/repo-server-patch.yaml`. The sidecar reuses
  the image the repo-server pod already pulled, so there is no extra image to
  pin or load and the staged `argocd-cmp-server` entrypoint is version
  matched. It declares no `discover` block: a named plugin then matches on
  name alone, and no other fixture Application can be captured by it. Both
  files live outside `repo/` so they never enter the git fixture Argo CD
  serves or drydock's `--path`.
- drydock side: `repo/.drydock/plugins.yaml` is a trusted `engine: exec`
  policy. The smoke passes `--enable-plugins`, `--plugin-policy-ref HEAD` and
  `--plugin-policy-repo` pointing at the ephemeral git repo the harness builds
  from the working tree, and only for this Application.

Both sides run the same committed program, `workloads/plugin-env/generate.awk`.
It is awk rather than a shell script because drydock's exec engine rejects
every interpreter as `argv[0]`, rejects relative program paths, and rejects
any absolute `argv[0]` resolving inside the repository or the copied
workspace - so a committed script cannot run under `engine: exec` by any
spelling. `awk` is a trusted executable on the controlled PATH and
`generate.awk` is a plain non-path argument resolved against the working
directory, which on both sides is the Application source path. The program is
POSIX and BEGIN-only so BSD awk and mawk agree.

Both Argo CD and drydock must render this `data`:

| key | value |
| --- | --- |
| `appName` | `parity-plugin-env` |
| `appNamespace` | `parity-plugin-env` |
| `projectName` | `default` |
| `sourceRepoUrl` | `git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git` |
| `sourcePath` | `workloads/plugin-env` |
| `sourceTargetRevision` | `HEAD` |
| `envMode` | `prod` |
| `envSuffix` | `parity-plugin-env-suffix` |
| `parameters` | `[{"name":"title","string":"hello"},{"array":["alpha","beta"],"name":"items"}]` |
| `paramTitle` | `hello` |
| `paramItems0` | `alpha` |
| `paramItems1` | `beta` |
| `bareMode` | `unset` |
| `paramCount` | `3` |

`envSuffix` pins build-environment substitution inside
`spec.source.plugin.env`; `bareMode` pins that the entry only ever arrives as
`ARGOCD_ENV_MODE`; `parameters` pins the exact `ARGOCD_APP_PARAMETERS` JSON
including its alphabetical key order within each element; `paramCount` pins
that no `PARAM_` name exists beyond the three the two parameters produce.
`parity-plugin-env` is also in the tracking comparison, which runs without
ignore rules.

### Deliberately not compared here

- `ARGOCD_APP_REVISION`, `_SHORT`, `_SHORT_8`: this capture maps the
  fixture URL to `repo/`, which is not the top of a Git repository, so
  drydock reports the spec `targetRevision` (`HEAD`) here. The
  `parity-app-revision-plugin` Application compares them (see
  [Resolved revision fixture](#resolved-revision-fixture)).
- `KUBE_VERSION` and `KUBE_API_VERSIONS`: the smoke passes neither
  `--kube-version` nor `--api-versions`, so drydock sends empty strings while
  live Argo CD sends the kind cluster's values. Making them comparable is a
  follow-up; `--kube-version` overrides per-app `spec` `kubeVersion`, so it
  would have to stay scoped to this Application.
- `DRYDOCK_OFFLINE`: drydock-only, set because the capture runs `--offline`.
- `PATH` and every other sidecar container variable: the sidecar prepends its
  own `os.Environ()`, which drydock's fixed exec environment never has.

## Resolved revision fixture

The `parity-app-revision-*` Applications pin `ARGOCD_APP_REVISION`,
`ARGOCD_APP_REVISION_SHORT` and `ARGOCD_APP_REVISION_SHORT_8`. The Argo CD
repo-server sets them to the revision it resolves for each rendered source.
`_SHORT` and `_SHORT_8` are the first 7 and 8 bytes of that value.

| Application | Source | Where the variables go | Value |
| --- | --- | --- | --- |
| `parity-app-revision-helm` | Git, `charts/app-revision` | Helm parameters (`forceString`) | commit SHA |
| `parity-app-revision-kustomize` | Git, `workloads/app-revision-kustomize` | `commonAnnotations` with `commonAnnotationsEnvsubst`, `commonLabels` | commit SHA |
| `parity-app-revision-jsonnet` | Git, `workloads/app-revision-jsonnet` | Jsonnet TLA and extVars | commit SHA |
| `parity-app-revision-plugin` | Git, `workloads/app-revision-plugin` | environment of the `parity-env` CMP | commit SHA |
| `parity-app-revision-multi-source` | Git chart, OCI artifact chart, `$values` ref | Helm parameters of each rendered source | commit SHA for the Git chart, manifest digest for the OCI chart |
| `parity-app-revision-oci` | OCI artifact chart `parity/nested/parity-nested-chart` | Helm parameter `fromValues` | manifest digest |
| `parity-app-revision-helm-repo` | schemeless OCI Helm chart, version `1.0.0` | Helm parameter `fromValues` | `1.0.0` |
| `parity-app-revision-annotated-tag` | Git at an annotated tag, in `spec.sources` | Helm parameters | commit SHA |
| `parity-app-revision-annotated-tag-single` | Git at the same annotated tag, in `spec.source` | Helm parameters | commit SHA |

For an OCI digest, `_SHORT` is `sha256:` and `_SHORT_8` is `sha256:` plus
one hex digit, on both sides.

The harness puts the annotated tag `parity-app-revision-annotated`
(`APP_REVISION_ANNOTATED_TAG`) on the fixture commit before it clones the
served bare repository. A source of a multi-source Application gets the
`git ls-remote` result as its revision, and a single-source Application
gets the checked-out commit SHA. Argo CD's `listRemote` peels annotated
tags, so both get the commit SHA, not the SHA of the tag object. The
harness checks that the tag object SHA differs from the commit SHA and that
no Argo CD capture of the two Applications holds it.

These two cases pin the Argo CD side only. drydock renders a `--repo-map`
directory at its HEAD commit whatever `targetRevision` names, and the tag
points to that commit, so the drydock capture does not resolve the tag.
Unit tests in `internal/source` and `internal/app` cover how drydock
resolves tags, branches and SHA pins for fetched sources and local trees.

The commit SHA is the commit that the fixture Git server serves. The harness
builds that repository in `FIXTURE_GIT_WORK` from the working tree and
clones the served bare repository from it, so the commit exists only for
the run. For the Applications in `APP_REVISION_GIT_APPLICATIONS`, the
drydock capture maps the fixture URL to `FIXTURE_GIT_WORK` instead of
`repo/`. drydock reports the HEAD commit of a `--repo-map` directory that is
the top of a Git repository, so both sides render the same commit. The
other captures keep `repo/`, which is a subdirectory of the drydock
repository, so drydock keeps the spec `targetRevision` there.

`parity-app-revision-plugin` reuses the `parity-env` CMP sidecar and policy.
That plugin runs `generate.awk` from the source path, so this source has its
own `generate.awk`, which prints only the three variables and
`ARGOCD_APP_SOURCE_TARGET_REVISION`. Its drydock capture carries the plugin
flags of `PLUGIN_APPLICATIONS`.

The OCI and Helm repository Applications use the chart that the smoke
already pushes for `parity-oci-helm-chart-ignored` and
`parity-oci-helm-nested`, so the OCI warm run already holds what their
offline capture needs.

Before the exact comparison, the harness makes sure that each Argo CD
capture holds the expected value: the HEAD commit of `FIXTURE_GIT_WORK`,
`sha256:`, or `1.0.0|1.0.0|1.0.0` (`APP_REVISION_EXPECTED`). Without that
check, both sides rendering an unresolved value would pass.

### Deliberately not covered by this fixture

- A multi-source Application whose `targetRevision` is an abbreviated
  commit SHA. The commit exists only for the run, so no fixture file can
  name it. Unit tests cover it.
- A `targetRevision` that is a branch other than the served default branch,
  or a semver range on Git tags: the fixture Git server serves one commit on
  `main`, and drydock does not resolve semver ranges on Git tags.

## Container plugin fixture

`applications/plugin-container.yaml` (`parity-plugin-container`) renders
through drydock's `engine: container` on one side and a CMP sidecar running
the same image on the other.

- Image: the Docker-official `alpine:3.23.6`, pinned by its multi-arch index
  digest `sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0`,
  which is identical on `public.ecr.aws/docker/library`, `mirror.gcr.io/library`
  and `docker.io/library`. The smoke pulls it through the same mirror loop as
  the registry image, re-tags it `drydock-argocd-parity-alpine:<cluster>` and
  kind-loads it.
- Argo CD side: `sidecar/plugin-container.yaml` is the `parity-container`
  descriptor, mounted from the `parity-container-cmp` ConfigMap into the
  `parity-container` sidecar of `sidecar/repo-server-patch.yaml`. The sidecar
  runs the staged `argocd-cmp-server` (static, so it runs on alpine) with its
  own private `/tmp`.
- drydock side: the `parity-container` entry in `repo/.drydock/plugins.yaml`
  names the image by digest with the default `network: none`, so the capture
  runs `docker run --network none --pull never --entrypoint awk <image> -f
  generate.awk` against a copy of the source mounted at `/work`. Like
  `parity-plugin-env` it is captured with `--enable-plugins` and the trusted
  policy ref. drydock reads policy only from the git snapshot the harness
  builds, so when a fallback mirror served the pull the harness rewrites the
  image line in that snapshot (never in this directory) to the pulled
  reference, and fails if the expected line is missing either way.

Both sides run `workloads/plugin-container/generate.awk` with busybox awk and
must render this `data`:

| key | value | proves |
| --- | --- | --- |
| `alpineRelease` | `3.23.6` | the program ran in the pinned image |
| `greeting` | `hello from the parity container workload` | the working directory is the Application source |
| `appName` | `parity-plugin-container` | build environment reached the container |
| `appNamespace` | `parity-plugin-container` | build environment reached the container |
| `envMode` | `container` | `ARGOCD_ENV_MODE` crossed the container boundary |
| `paramTitle` | `from-container` | `PARAM_TITLE` crossed the container boundary |

The program exits non-zero when either file is unreadable instead of emitting
an empty value, so both sides failing alike cannot compare equal. The full
environment contract (`ARGOCD_APP_PARAMETERS`, array parameters, the
`ARGOCD_ENV_` prefix rule) stays pinned by `parity-plugin-env`; this fixture
pins only that the container engine delivers the same inputs. It is not in
the tracking comparison.

### Deliberately not covered by this fixture

- `network: default`, cache mounts, `init` commands and post-renderers: each
  is a drydock-side option with no Argo CD counterpart to compare against.
- Mutable image tags (`allowMutableImageTag`) and image pulls at render time:
  the capture is offline, which requires a locally present digest reference.
- Container stderr: drydock omits it from errors by design, so the harness
  cannot compare it.

### Local runs

drydock's container engine looks up `docker` only on
`/usr/local/bin:/usr/bin:/bin`, and offline it ignores the shell's Docker
context: it rejects a non-empty `DOCKER_CONTEXT`, `DOCKER_CONFIG`,
`DOCKER_TLS_VERIFY` or `DOCKER_CERT_PATH` and runs with an empty client
config, so the daemon it reaches is `DOCKER_HOST` or `/var/run/docker.sock`,
and `DOCKER_HOST` must be a local `unix://` socket. Before the capture the
harness checks both with the same lookups. On Docker Desktop
`/var/run/docker.sock` exists; on colima or OrbStack without it, set
`DOCKER_HOST` to the `unix://` endpoint `docker context inspect` reports. The
smoke uses its own kubeconfig, so the throwaway kind cluster never touches
yours (see [Concurrent runs](#concurrent-runs)). Apple Silicon hosts run the
`linux/arm64` image variants.

## argocd-vault-plugin fixture

`applications/avp.yaml` (`parity-avp`) and `applications/avp-secret.yaml`
(`parity-avp-secret`) name the plugin `argocd-vault-plugin`, the exact name
drydock's built-in AVP compatibility matches. drydock needs no
`--enable-plugins` and no policy for them: it renders `workloads/avp` and
`workloads/avp-secret` as plain directories and replaces every placeholder
with `drydock-redacted-` plus the first 12 hex digits of the sha256 of the
placeholder's identity, `path:<path>#<key>`. Live Argo CD runs the real
plugin, so the fixture makes the real plugin produce those same strings.

- Argo CD side: an `argocd-vault-plugin` sidecar (descriptor
  `sidecar/plugin-avp.yaml`, mounted from the `parity-avp-cmp` ConfigMap)
  runs `argocd-vault-plugin generate ./` with `AVP_TYPE=kubernetessecret`.
  Its image is the pinned alpine plus the AVP v1.18.1 release binary,
  downloaded for the host architecture, checked against a pinned sha256,
  copied in with `docker cp` and committed - no Dockerfile build and no
  package fetch - then checked with `argocd-vault-plugin version` and
  kind-loaded.
- The backend is the `parity-avp-backend` Secret in `argocd`
  (`sidecar/avp-backend.yaml`). Its values are drydock's markers for the
  three keys, so AVP substituting them renders exactly what drydock renders.
  No secret value exists anywhere in the run.
- AVP logs in with `rest.InClusterConfig` before reading any manifest, and
  the repo-server pod sets `automountServiceAccountToken: false`, so the AVP
  container alone mounts a projected token, `ca.crt` and namespace at the
  standard path. `sidecar/avp-rbac.yaml` grants the `argocd-repo-server`
  ServiceAccount `get` on that one Secret, and the harness confirms the grant
  with `kubectl auth can-i` before patching the repo-server.

### Two oracles

`argocd app manifests` masks every Secret `data` and `stringData` value as
`++++++++`, so the masked comparison every other Application goes through
cannot see a base64 `Secret.data` substitution. The AVP fixture therefore
has two oracles:

- The masked oracle: `parity-avp` (`workloads/avp`, ConfigMaps only) is
  captured with `argocd app manifests` and compared exactly like every other
  Application.
- The kubectl-exec oracle: after the sidecar is ready and drydock has
  rendered, the harness copies each oracle Application's source directory
  (`tar | kubectl exec -i ... tar -x`) into the `argocd-vault-plugin`
  container's private `/tmp`, runs `argocd-vault-plugin generate <dir>` there
  (the same binary, `AVP_TYPE`, backend Secret and projected ServiceAccount
  token Argo CD used), and compares that unmasked stdout against drydock's
  render of the same Application with the repo comparer and the same ignore
  rules, under `avp-oracle/` and `compare-avp-oracle/` in the output
  directory. Both `parity-avp` and `parity-avp-secret` go through it.
  `parity-avp-secret` (`workloads/avp-secret`, Secrets only) is kept out of
  the masked comparison; its masked capture is recorded under
  `avp-oracle/argocd-masked-manifests/` only as proof that Argo CD rendered
  it through the sidecar.

`workloads/avp/configmaps.yaml` covers inline placeholders, two inline
placeholders embedded in one string, annotation-scoped `<key>` placeholders
(including one embedded in a string), an inline placeholder under the path
annotation, keys with spaces inside `<>`, a stray `<` before a placeholder
with and without the annotation, a present-but-empty path annotation, a list
nested in a list, and plain fields. Both sides must render this `data`:

| ConfigMap | key | value |
| --- | --- | --- |
| `parity-avp-inline` | `username` | `drydock-redacted-5401fc97182f` |
| `parity-avp-inline` | `dsn` | `postgres://drydock-redacted-5401fc97182f:drydock-redacted-4de5813a6837@db.example.invalid:5432/app` |
| `parity-avp-inline` | `plain` | `left exactly as written` |
| `parity-avp-inline` | `stray` | `<stray drydock-redacted-5401fc97182f` |
| `parity-avp-inline` | `spaced` | `drydock-redacted-6799b3ec831f` |
| `parity-avp-annotated` | `password` | `drydock-redacted-4de5813a6837` |
| `parity-avp-annotated` | `endpoint` | `drydock-redacted-6799b3ec831f` |
| `parity-avp-annotated` | `url` | `https://drydock-redacted-6799b3ec831f/v1` |
| `parity-avp-annotated` | `inline` | `drydock-redacted-5401fc97182f` |
| `parity-avp-annotated` | `spaced` | `drydock-redacted-4de5813a6837` |
| `parity-avp-annotated` | `stray` | `drydock-redacted-5401fc97182f` |
| `parity-avp-annotated` | `prefixed` | `drydock-redacted-6799b3ec831f` |
| `parity-avp-empty-path` | `inline` | `drydock-redacted-4de5813a6837` |
| `parity-avp-empty-path` | `stray` | `drydock-redacted-5401fc97182f` |
| `parity-avp-empty-path` | `plain` | `no placeholder here` |
| `parity-avp-lists` | `plain` | `lists live outside data` |

`parity-avp-lists` also carries a top-level `lists` field (not a ConfigMap
field; nothing applies the manifest and AVP's walk ignores the kind) whose
first two items, a string and a map, render `drydock-redacted-5401fc97182f`
and `key: drydock-redacted-4de5813a6837`, while the two items that are lists
themselves keep `<path:parity-avp-backend#endpoint>` verbatim on both sides.

`workloads/avp-secret/secrets.yaml` covers placeholders inside base64 `data`
values and in `stringData`, with and without the path annotation. Both sides
must render:

| Secret | field | key | value |
| --- | --- | --- | --- |
| `parity-avp-secret-inline` | `data` | `password` | `ZHJ5ZG9jay1yZWRhY3RlZC00ZGU1ODEzYTY4Mzc=` (base64 of the `password` marker) |
| `parity-avp-secret-inline` | `data` | `dsn` | base64 of `postgres://drydock-redacted-5401fc97182f:drydock-redacted-4de5813a6837@db.example.invalid:5432/app` |
| `parity-avp-secret-inline` | `data` | `markup` | `PGh0bWw+bm90IGEgcGxhY2Vob2xkZXI8L2h0bWw+` (unchanged: `<html>` is no inline token) |
| `parity-avp-secret-inline` | `data` | `plain` | `bGVmdCBleGFjdGx5IGFzIHdyaXR0ZW4=` (unchanged) |
| `parity-avp-secret-inline` | `stringData` | `username` | `drydock-redacted-5401fc97182f` |
| `parity-avp-secret-inline` | `stringData` | `endpoint` | `https://drydock-redacted-6799b3ec831f/v1` |
| `parity-avp-secret-annotated` | `data` | `endpoint` | `ZHJ5ZG9jay1yZWRhY3RlZC02Nzk5YjNlYzgzMWY=` |
| `parity-avp-secret-annotated` | `data` | `url` | `aHR0cHM6Ly9kcnlkb2NrLXJlZGFjdGVkLTY3OTliM2VjODMxZi92MQ==` |
| `parity-avp-secret-annotated` | `stringData` | `password` | `drydock-redacted-4de5813a6837` |
| `parity-avp-secret-annotated` | `stringData` | `inline` | `drydock-redacted-5401fc97182f` |

Beyond the exact comparisons, the harness counts `drydock-redacted-` markers,
literally and inside every decoded base64 token, on every side and requires
exactly `AVP_EXPECTED_MARKERS` (16) for `parity-avp` on the two masked sides
and the two oracle sides, and `AVP_SECRET_EXPECTED_MARKERS` (9: four literal
in `stringData`, five inside base64 `data`) for `parity-avp-secret` on the
two oracle sides, so "neither side replaced anything" cannot pass. Neither
app is in the tracking comparison.

### Placeholder rules

The two implementations agree only inside these rules; every value in the
workload obeys them, and new values must too.

- Spell the path exactly `parity-avp-backend` in every inline placeholder and
  in the `avp.kubernetes.io/path` annotation, with no `argocd:` namespace
  prefix: AVP reads a bare name from the `argocd` namespace, and drydock
  hashes the path text as written.
- No `|` modifiers: drydock derives its marker from the path and key alone,
  so AVP would emit the modified backend value where drydock emits the bare
  marker. No `#version` suffix: the backend Secret has no versions.
- Spaces inside `<>` are fine: both sides trim spaces (only spaces, not
  tabs) from a generic key and from an inline key, so `< password >` and
  `<path:parity-avp-backend#endpoint >` resolve the same keys as their tight
  spellings. Keys the backend lacks are AVP errors (see the next rule), so a
  key must still be `username`, `password` or `endpoint` once trimmed.
- Once the annotation key is present, with any value, both sides treat every
  `(?mU)<(.*)>` span on a line as a placeholder: the match runs from the
  first `<` to the next `>`, so a stray `<` before a placeholder is swallowed
  into the replaced span. The swallowed span must still contain an inline
  token (`<stray <path:...#key>` and `<prefix path:...#key>` resolve through
  the unanchored inline syntax), because a swallowed bare key is a missing
  value and AVP fails the whole generate, not just that value. Without the
  annotation only a literal `<path:` starts a match and a stray `<` stays.
- A present-but-empty `avp.kubernetes.io/path: ""` selects the generic span
  on both sides but fetches no data, so such an object may carry inline
  tokens only: a bare `<key>` under it is an AVP error.
- Quote every scalar. An annotation with no value (`key:` and nothing after
  it) hides every annotation on that object from AVP (its apimachinery
  v0.29.1 reads the map strictly), which drydock mirrors, but Argo CD's
  annotation tracking rejects such a manifest outright, so the fixture never
  carries one.
- No `kind: List` and no `avp.kubernetes.io/ignore`: drydock mirrors AVP's
  List-as-one-object context and the ignore skip, but a skipped placeholder is
  a "neither side replaced" value and a List item's markers count the same as
  a plain document's, so neither adds coverage here.
- Lists nested directly in lists are skipped on both sides (AVP walks one
  list level, and drydock mirrors that). `parity-avp-lists` pins that with
  two placeholders that stay verbatim; they are not markers, so they do not
  count toward `AVP_EXPECTED_MARKERS`.
- Secret values are tried as padded standard base64 first on both sides, and
  one that decodes to text with a `<...>` span is substituted in the decoded
  text and re-encoded; every other string in a Secret, `stringData`
  included, is processed as plain text. Keep base64 `data` canonical (padded,
  no trailing bits) so an unchanged value re-encodes byte-for-byte.
- Keep both directories flat (AVP's `generate <dir>` walks recursively, the
  drydock directory source does not). ConfigMaps only in `workloads/avp` and
  Secrets only in `workloads/avp-secret`: the former is the masked oracle's
  Application and the latter is excluded from it.

### Deliberately not covered by this fixture

- Modifiers and versioned placeholders: they change AVP's output, not where
  it substitutes, and have no backend here.
- Other AVP backends and authentication methods, and AVP configuration from
  `--secret-name` or `--config-path`.
- A versioned plugin name (`spec.version`), or a renamed plugin that drydock
  recognizes as AVP from a discovered ConfigManagementPlugin definition
  rather than by the exact name.

### Local runs

The harness downloads the AVP binary for the Docker daemon's architecture
(`linux_arm64` on Apple Silicon) from GitHub releases, so the run needs that
download. The
image builds from the pinned alpine already in the local image store, so it
needs no Docker Hub or package-mirror access. The kubectl-exec oracle needs
only `kubectl exec` into the sidecar: the copy is a `tar` pipe (with
`COPYFILE_DISABLE=1`, so macOS adds no `._*` entries AVP would read as YAML)
into the container's emptyDir `/tmp`.

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

- The helm-content-inside-first-class-artifact shape without a `chart:` field
  (a `Chart.yaml` inside the artifact, `path: .`): hermetically pinned by unit
  tests, not live-pinned under its own Application, and not covered by this
  plain-manifests fixture. Do not conflate it with the `oci://` + `chart:`
  spelling, which `parity-oci-helm-chart-ignored` live-pins (see the OCI Helm
  chart fixture below): Argo CD dispatches both as OCI artifacts, but only
  the latter carries a `chart:` field for drydock to warn about.
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

## OCI Helm chart fixture

`oci-chart/parity-nested-chart/` is the chart source for
`parity-oci-helm-nested`, the Application that pins a **nested** OCI chart
name:

- repoURL `argocd-parity-registry.argocd-parity.svc.cluster.local:5443` —
  scheme-less `host:port`, which is what makes Argo CD classify the source as
  Helm-OCI (`chart` set plus no scheme on `repoURL`)
- chart `parity/nested/parity-nested-chart`, targetRevision `1.0.0`
- `helm.valuesObject` overriding `fromValues`, so the rendered ConfigMap
  proves values reached the nested chart and not just that it resolved

The chart root also holds `gate-probe.yaml`, a ConfigMap outside
`templates/`. No Helm render of the chart holds it. The source type gate
phase renders the chart with Helm off, as a plain directory, and then it is
the only resource (see "Source type gate phase").

The smoke harness pushes it with `scripts/argocd-parity-chart-push`, which
packages the directory and uploads it through helm's own registry client:
`oras push` cannot produce the helm config
(`application/vnd.cncf.helm.config.v1+json`) and content
(`application/vnd.cncf.helm.chart.content.v1.tar+gzip`) media types that both
Argo CD's repo-server and drydock require, and the smoke must not depend on a
`helm` binary. The manifest shape is verified before Argo CD ever sees the
chart. drydock warms `--chart-cache-dir` — not `--oci-cache-dir`, which holds
artifacts — with one non-offline build before the offline per-app loop runs.

The chart lives outside `repo/` for the same reason the artifact content does:
a classification regression that fell back to path resolution would render the
git fixture instead of the chart and flip the comparison hard.

`applications/oci-helm-chart-ignored.yaml` (`parity-oci-helm-chart-ignored`)
reuses the same pushed chart through the `oci://` + `chart:` spelling:

- repoURL
  `oci://argocd-parity-registry.argocd-parity.svc.cluster.local:5443/parity/nested/parity-nested-chart`
  — the `oci://` scheme is what makes Argo CD's repo-server dispatch the
  source as an OCI artifact (`IsOCI` before `IsHelm`,
  `reposerver/repository/repository.go:358-364`) and pull the repoURL itself
  at `targetRevision: 1.0.0`
- `chart: parity-nested-chart`, which neither Argo CD nor drydock reads
- the same `helm.valuesObject`, so the rendered ConfigMap proves the extracted
  artifact was recognised as a chart and the Application's values applied

The Application is Argo-valid only when applied declaratively, which is how
the smoke applies it (`kubectl apply`; the controller validates permissions
only). `argocd app create` would reject it: the API server's `ValidateRepo`
classifies the repository Helm-first (`TestRepoWithKnownType`,
`util/argo/argo.go:271-281`) and never reaches the controller's OCI dispatch.
The earlier claim that live Argo CD "rejects that combination" came from that
API-create path, not from the controller. drydock renders the same artifact
and reports the advisory warning `source.oci-chart-ignored` once (exempt from
`--strict`); the capture step fails if that line is missing from drydock's
stderr. The app fills `--oci-cache-dir` through the artifact flow, not
`--chart-cache-dir`, so the warm step covers it alongside `parity-oci-config`.

## OCI ApplicationSet generator fixture

`oci-appset-artifact/` is a second OCI artifact. The smoke pushes it to
`parity/appset:v1.0.0` the same way as `oci-artifact/` and checks the same
one-layer manifest shape. Five ApplicationSets in `repo/applicationsets/`
use the Argo CD v3.6.1 `oci` generator on it. Every Application they
generate renders from the same artifact as a Kustomize source.

The artifact tree:

| path | role |
| --- | --- |
| `apps/alpha`, `apps/beta`, `apps/skipped` | app directories |
| `apps/.hidden` | hidden directory: the directory walk never lists it |
| `apps/linked` | symlink to `alpha`: the directory walk never lists it |
| `teams/platform/gamma`, `teams/platform/delta` | nested app directories |
| `params/clusters.yaml` | YAML list of two objects |
| `params/regions/east/site.json` | JSON object with a nested map |
| `params/regions/west/site.yaml` | YAML object |
| `params/regions/mirror` | symlink to `west`: file patterns follow it |
| `params/regions/legacy/site.yaml` | removed by an exclude pattern |
| `params/.hidden/site.yaml` | hidden file match: file patterns include it |
| `root.yaml` | YAML list of two objects with nested maps and lists |

The ApplicationSets:

| ApplicationSet | mode | generator shape | generated Applications |
| --- | --- | --- | --- |
| `parity-oci-directories` | Go template | `directories` with two includes and one exclude, `values` | `parity-oci-dir-{alpha,beta,delta,gamma}` |
| `parity-oci-directories-fasttemplate` | fasttemplate | `directories` with two includes and two excludes, `pathParamPrefix: src`, `values` | `parity-oci-ft-{alpha,skipped,gamma}` |
| `parity-oci-files` | Go template | `files` with two doublestar includes and one exclude, `pathParamPrefix: cfg`, `values` | `parity-oci-file-{hidden-hidden,north-params,south-params,east-east,west-mirror,west-west}` |
| `parity-oci-files-fasttemplate` | fasttemplate | `files` with an empty include pattern and three excludes, `values` | `parity-oci-ftfile-root-{one,two}` |
| `parity-oci-matrix` | Go template | `matrix` of a list generator and an `oci` generator whose `revision` comes from the list element | `parity-oci-mx-{dev,prod}-{delta,gamma}` |

`apps/*` matches `apps/.hidden` and `apps/linked` by name. Only the
repo-server directory walk keeps them out: it skips hidden directories and
never treats a symlink as a directory
(`reposerver/repository/utils.go:90-121`, called by `GetOciDirectories` at
`reposerver/repository/repository.go:3846`). If drydock lists either one,
it generates an extra `parity-oci-dir-*` Application.

File patterns behave the other way. `GetOciFiles` globs with doublestar over
an `os.Root` of the artifact (`reposerver/repository/repository.go:3760-3802`),
which matches hidden directories and follows in-artifact symlinks. That is
why `parity-oci-file-hidden-hidden` and `parity-oci-file-west-mirror` exist.
An empty pattern means `**` (`repository.go:3729-3731` and `:3760-3763`), so
`parity-oci-files-fasttemplate` must exclude everything except `root.yaml`.

Each template feeds generator params into `kustomize.namePrefix`,
`nameSuffix`, `namespace`, and `commonAnnotations`. A wrong param therefore
changes the rendered ConfigMap, not only the Application spec.

On the Argo CD side, the smoke script first waits for each of the five
ApplicationSets to report `ParametersGenerated=True`. It does this right after
it applies the fixture Applications and before it waits for the generated
Applications, so a registry or generator failure names the ApplicationSet.

On the drydock side, a later warm step builds `parity-oci-dir-alpha` online
once, which fills `--oci-cache-dir` with this artifact. Every offline drydock
run after that evaluates these ApplicationSets during discovery and reads the
artifact from the cache.

### Application inventory comparison

The manifest capture loops over the fixed `APPLICATIONS` list, so on its own
it cannot see an Application that only drydock generates. The smoke script
therefore also compares the two Application inventories:

- Argo CD side: `kubectl get applications.argoproj.io --all-namespaces -o json`,
  taken right after the inventory check and before the project-policy smoke
  adds its own Applications.
- drydock side: `drydock get apps -o json` with the same offline flags and
  caches as the manifest capture.

`scripts/argocd-parity-compare --argocd-applications ... --drydock-applications ...`
projects each Argo CD Application into the shape `drydock get apps -o json`
prints: namespace, name, project, destination, and each source's
`repoURL`, `targetRevision`, `path`, `chart`, and `ref`. Then it requires
the same set of Applications with the same projected spec on both sides. A
new field in drydock's output fails the load until the comparer projects it
too. The comparison covers every fixture Application, not only the OCI
generator ones. Its output lands in `compare-applications/`.

### Deliberately not covered by this fixture

- A param file that is not valid YAML or JSON. Argo CD and drydock both
  fail the whole ApplicationSet on it. The Git generator fixture covers that
  failure with a Git param file, because the code is the same for both
  generators (`applicationset/generators/repo_path_utils.go:363-367`).
- `reposerver.include.hidden.directories: "true"`. drydock reads the key,
  and unit tests cover it. The smoke keeps the default for the whole install,
  because the key changes every directory generator.
- `requeueAfterSeconds` and semver revisions on the generator.

## Git generator fixture

The ApplicationSets `git-*.yaml` in `repo/applicationsets/` pin the Argo CD
v3.6.1 Git generator. Their param files and directories are in
`repo/workloads/appset/git-parity/`. The smoke keeps the defaults of
`applicationsetcontroller.enable.new.git.file.globbing` and
`reposerver.include.hidden.directories`. Unit tests cover both keys set to
`true`.

| ApplicationSet | case | result on both sides |
| --- | --- | --- |
| `parity-git-files-pathspec` | `pathspec/*/config.yaml` and the directory `prefix` as file patterns | `parity-git-ps-{a,a-b,c,linked,p-one,p-two}` |
| `parity-git-directories-as-written` | `./...`, a trailing `/`, a malformed `[`, a plain path, and `dirs/*` with a hidden directory | `parity-git-written-{alpha,visible}` |
| `parity-git-bad-param` | a param file that is a list of strings | no Applications, `ErrorOccurred` |
| `parity-git-outside-pattern` | the file pattern `../outside.yaml` | no Applications, `ErrorOccurred` |
| `parity-git-symlinked-directory` | a file pattern that matches a symlink to a directory | no Applications, `ErrorOccurred` |

The default Git files generator runs `git ls-files -- <pattern>`
(`util/git/client.go:763-770`). In a git pathspec, `*` also matches `/`, so
`pathspec/*/config.yaml` matches `pathspec/a/b/config.yaml`. A pattern that
names a directory matches every file below it, so `prefix` matches
`prefix/one.yaml` and `prefix/deep/two.json`. `pathspec/link/config.yaml` is
a symlink to `targets/linked.yaml`. `git ls-files` lists the symlink, and the
repo-server reads it through to its target
(`reposerver/repository/repository.go:3363-3369`). Earlier drydock releases
matched doublestar globs and skipped every symlink, so they generated only
`a` and `c`.

Directory patterns match with `path.Match` as written
(`applicationset/generators/repo_path_utils.go:70-101`). So
`./workloads/appset/git/*` and `workloads/appset/git/*/` match nothing, and
Argo CD logs and skips `workloads/appset/git/[`. Earlier drydock releases
cleaned the patterns and failed the whole run on the malformed one. The
repo-server walk skips `dirs/.hidden`.

Each of the failing ApplicationSets also has a list generator. Argo CD
fails the whole ApplicationSet, so the list element creates no Application
either (`applicationset/controllers/applicationset_controller.go:199-216`).
The smoke waits for the `ErrorOccurred` message of each one in
`APPLICATIONSET_ERRORS`. Then `compare_applicationset_errors` requires the
drydock warning `appset.generator-failed` for the same file, with the same
text. git refuses `../outside.yaml`
with "is outside repository". The repo-server cannot read the symlink to a
directory, so it fails with "is a directory".

`git-missing-project/` holds a Git generator ApplicationSet whose template
names the AppProject `parity-missing-project`, which does not exist, and one
AppProject. The Git generator gets that AppProject before it lists anything
and fails the whole ApplicationSet (`applicationset/generators/git.go:93-111`).
drydock cannot know the AppProjects of the cluster offline. They can come
from another repository, a chart, a remote Kustomize base, or the cluster
itself. So drydock keeps the Applications and warns `project.missing`. This
is a recorded difference, and the case has its own directory, out of the
main inventory. `run_git_missing_project_smoke` applies the directory after
the main comparisons. Argo CD must report the `ErrorOccurred` condition and
create no Application. drydock, run on the directory alone, must keep the
Application of the list generator and the one that the Git generator
generates from `workloads/appset/git/alpha` in the directory. `diag` and
`build` must warn `project.missing` exactly once for each of them, with the
note that the Git generator fails the whole ApplicationSet.

### Deliberately not covered by this fixture

- Paths that Git ignores and does not track. An Argo CD checkout never holds
  them, and drydock skips them in a working tree. A smoke case would have to
  write an ignored file into the drydock checkout. Unit tests cover it.
- Untracked paths that Git does not ignore. drydock keeps them in a working
  tree, and an Argo CD checkout does not hold them. This is a recorded
  difference.
- A symlinked param file whose target is outside the repository or in
  `.git`. Argo CD reads the target in its own file system, and drydock
  reports the ApplicationSet unsupported.
- Submodule content in a Git generator. The generator of the smoke reads
  this fixture directory, which is part of the drydock repository and
  cannot hold a submodule. Unit tests cover the directories generator, new
  globbing, `git ls-files`, and
  `applicationsetcontroller.enable.git.submodule`.

## Git submodule fixture

`submodule-repos/` holds two repositories that the smoke builds and serves
from its git daemon. `submodule-child.git` comes from `child/`.
`submodule-parent.git` comes from `parent/` and holds the child as the
submodule `child`, with the relative URL `../submodule-child.git` in
`.gitmodules`. The Argo CD repo-server resolves that URL against the
`git://` URL of the parent and runs `git submodule update --init
--recursive`, because `reposerver.enable.git.submodule` is on by default
(`util/git/client.go:783-818`).

| Application | case | result on both sides |
| --- | --- | --- |
| `parity-submodule-directory` | a directory source in the submodule, `child/app` | the ConfigMap `parity-submodule-directory` |
| `parity-submodule-kustomize` | a Kustomize overlay in the parent whose base `../child/base` is in the submodule | the ConfigMap `parity-submodule-kustomize-base` |

drydock reads the local clone of the parent through `--repo-map`. Its
submodule is initialized, so it holds the content that Argo CD checks out.
`require_submodule_content` fails the smoke when either side renders a
capture without a ConfigMap from the submodule. Both Applications render
nothing from an empty submodule, so the manifest comparison alone would
pass.

### Deliberately not covered by this fixture

- drydock's own submodule fetch for a fetched Git source or a `--ref`
  snapshot. drydock runs offline here and reads the parent through
  `--repo-map`. To fetch from the in-cluster git daemon, drydock would need
  the cluster Service name in `/etc/hosts`, which needs `sudo`. Instead,
  `TestDefaultGitAcquirerFetchesSubmodulesOverGitDaemon` serves the
  submodule fixture with a local `git daemon` and fetches it over
  `git://`, then offline. Unit tests cover the offline cache miss, the
  `.git/modules` store, and the transport and URL rules.
- `reposerver.enable.git.submodule` set to `false`. The key changes every
  Application of the cluster. Unit tests cover it.
- Git LFS. A Git LFS server needs the HTTP batch API, which the git daemon
  of the smoke does not serve. Unit tests cover `enableLfs`.

## Helm block scalar fixture

`applications/helm-block-scalars.yaml` (`parity-helm-block-scalars`) renders
`charts/helm-block-scalars`, whose Deployment carries two multi-line env
values. drydock's YAML output writes each as a literal block scalar inside a
sequence item:

- `QUERY` starts with a newline: the template writes `value: |` and puts the
  `nindent` call on the next line, so the literal's first line is blank.
- `BANNER` starts with spaces: `indent 2` prefixes every line.

That first character makes the encoder write an explicit indentation
indicator. At yaml.v3's default 4-space indent the indicator inside a
sequence item disagreed with the column the content was written at:
`QUERY` made drydock's output unreadable to the comparison's decoder (and to
kubectl), and `BANNER` on its own read back without its leading spaces. The
ConfigMap carries the same two strings nested under mappings only, where the
output was always read back exactly, as a control.

Both Argo CD and drydock must render these values:

| resource | field | value |
| --- | --- | --- |
| Deployment | env `QUERY` | `"\nSELECT id, name\nFROM widgets\nWHERE enabled\n"` |
| Deployment | env `BANNER` | `"  drydock parity\n  block scalars"` |
| ConfigMap | `data.query` | the `QUERY` value |
| ConfigMap | `data.banner` | the `BANNER` value |

## Helm generated output fixture

`applications/helm-generated-decode.yaml` (`parity-helm-generated-decode`)
renders `charts/helm-generated-decode` with `skipTests: true`. It covers how
Argo CD reads the output of `helm template`. Both sides must render exactly
six ConfigMaps:

- `templates/lists.yaml` holds a List with two ConfigMaps, which renders
  `helm-list-item-a` and `helm-list-item-b`. It also holds a List whose
  `items` field is `null`, which renders nothing, and the ConfigMap
  `helm-items-map`, whose `items` field is a map and which renders as is.
  An empty document and a document with only a comment render nothing.
- `templates/hooks.yaml` holds documents that Helm drops. A List with the
  `helm.sh/hook: test` annotation is dropped with its item under
  `skipTests`, although the item has no hook annotation of its own. Three
  ConfigMaps whose `helm.sh/hook` value is `crd-install`, `test-failure`, or
  empty are dropped because Helm does not know the hook event.
- `templates/extra-NOTES.txt` is not YAML. Helm drops every file whose name
  ends in `NOTES.txt`, so it is not read.
- `templates/dup-a-list.yaml`, `templates/dup-a-hook.yaml`, and
  `templates/dup-b-plain.yaml` hold two repeated resources. The Argo CD
  controller keeps the last copy in the order of `helm template` output.
  Helm sorts a List after every kind that it knows and prints hooks last.
  So Argo CD keeps the List item copy of `helm-dup-list` and the hook copy
  of `helm-dup-hook`, although `dup-b-plain.yaml` comes later by file name.
  The hook copy also has `argocd.argoproj.io/hook: Skip`, so it is not an
  Argo CD hook and stays in the comparison. `argocd app manifests` lists
  every copy, and the comparison keeps the last one, as the controller
  does.
- `templates/zz-keep-chomp.yaml` is the last document before the hooks.
  `helm template` trims the white space at the end of it, so the `|+` value
  of `helm-keep-chomp` is `x` and a line break, without the line with a tab
  and the blank lines that follow. The comparison drops line breaks at the
  end of each value but keeps the tab, so it sees the difference.

The fixture holds no document that Argo CD keeps as a hook. `argocd app
manifests` lists hooks, but drydock keeps hooks out of its build output.

`TestHelmRendererParityRenderFixtureMatchesUpstream` in `internal/render`
renders the chart with drydock and with the upstream oracle and requires the
same objects.

## Helm file parameter symlink fixture

Argo CD v3.6.1 resolves every `helm.fileParameters` path before it runs
helm, with `ResolveValueFilePathOrUrl` (`util/io/path/resolved.go`). When
the path itself is a symlink, the resolver follows it and joins a relative
target to the directory of the path as text. helm `--set-file` then reads
the resolved path. `file-parameter-links/` holds the symlinks. It is
outside every chart, because drydock rejects a symlink in a chart. Git
stores each symlink as mode `120000`, and every symlink stays in the
repository, so the out-of-bounds symlink check passes.

| path | what it is |
| --- | --- |
| `linked.txt` | symlink to `targets/message.txt` |
| `chain.txt` | symlink to `linked.txt` |
| `linked-dir`, `to-dir` | symlinks to the directory `targets` |
| `lexical-dir` | symlink to the directory `targets/deep` |
| `targets/deep/sibling.txt` | symlink to `../which.txt` |
| `targets/deep/up.txt` | symlink to `../../../file-parameter-links/targets/message.txt` |
| `dangling.txt` | symlink to `missing-target.txt`, which does not exist |
| `which.txt`, `targets/which.txt` | `lexical` and `physical` |

`applications/helm-file-parameter-symlinks.yaml`
(`parity-helm-file-parameter-symlinks`) renders
`charts/helm-file-parameter-symlinks`, whose ConfigMap prints every value.
It has a second source, the same repository with `ref: files`. Both
Argo CD and drydock must render this `data`:

| key | file parameter path | value |
| --- | --- | --- |
| `linked` | `../../file-parameter-links/linked.txt` | `from-symlink-target` |
| `chain` | `../../file-parameter-links/chain.txt` | `from-symlink-target` |
| `linkedDir` | `../../file-parameter-links/linked-dir/inner.txt` | `from-linked-dir` |
| `absolute` | `/file-parameter-links/linked.txt`, relative to the repository root | `from-symlink-target` |
| `lexical` | `../../file-parameter-links/lexical-dir/sibling.txt` | `lexical` |
| `ref` | `$files/file-parameter-links/chain.txt` | `from-symlink-target` |
| `overridden` | the last of four entries, `../../file-parameter-links/targets/message.txt` | `from-symlink-target` |

`lexical` is `lexical`, not `physical`. The real directory of
`sibling.txt` is `targets/deep`, so the symlink points at
`targets/which.txt`. The resolver joins `../which.txt` to the path as
written, `file-parameter-links/lexical-dir`, so helm reads
`file-parameter-links/which.txt`. The three overridden `overridden` entries
are `dangling.txt`, `to-dir`, and `$files/file-parameter-links/dangling.txt`.
They resolve, so the render does not fail, and helm never reads them.

`applications/helm-file-parameter-symlinks-override.yaml`
(`parity-helm-file-parameter-symlinks-override`) renders the same chart but
sets no file parameters. They come from the chart's
`.argocd-source-parity-helm-file-parameter-symlinks-override.yaml`, which
the other Application does not read. Both sides must render this `data`:

| key | value |
| --- | --- |
| `chain` | `from-symlink-target` |
| `linkedDir` | `from-linked-dir` |
| `overridden` | `from-symlink-target`, after an overridden `dangling.txt` |

`TestHelmFileParameterSymlinksSmokeFixtureMatchesUpstream` in
`internal/render` renders both with drydock and with the upstream resolver.
"Helm file parameters that fail the source" below lists the failing cases.

## Helm value file path fixture

Argo CD v3.6.1 resolves `helm.valueFiles` with `getResolvedValueFiles`
(`reposerver/repository/repository.go`) and the same resolver as file
parameters. `value-file-links/` holds the files and symlinks. It is outside
every chart, because drydock rejects a symlink in a chart.

| path | what it is |
| --- | --- |
| `a.yaml`, `b.yaml` | `winner: a` and `winner: b`, and the keys `fromA` and `fromB` |
| `link-a.yaml` | symlink to `a.yaml` |
| `absolute.yaml` | the key `fromAbsolute` |
| `linked-dir` | symlink to the directory `real`, which holds `inner.yaml` |
| `glob/plain.yaml` | the key `globPlain` |
| `glob/linked.yaml` | symlink to `../absolute.yaml` |

`applications/helm-value-file-paths.yaml` (`parity-helm-value-file-paths`)
renders `charts/helm-value-file-paths`, whose ConfigMap prints all values
as JSON. Its value files are `a.yaml`, `b.yaml`, `a.yaml` again,
`link-a.yaml`, `/value-file-links/absolute.yaml` (relative to the
repository root), `linked-dir/inner.yaml`, and the glob `glob/*.yaml`. The
repo-server reads each resolved path once, at its first place. The second
`a.yaml` and `link-a.yaml` resolve to the path of the first `a.yaml`, so
`winner` is `b`. Both Argo CD and drydock must render these values:

| key | value |
| --- | --- |
| `winner` | `b` |
| `fromA` | `a` |
| `fromB` | `b` |
| `fromAbsolute` | `absolute` |
| `fromLinkedDir` | `linked-dir` |
| `globPlain` | `plain` |

Two more Applications render the same chart:

| Application | value files | values |
| --- | --- | --- |
| `parity-helm-value-file-ref-glob` | `$values/value-file-links/b.yaml`, the `$ref` glob `$values/value-file-links/glob/*.yaml`, and `$values/value-file-links/glob/linked.yaml` again | `winner: b`, `fromB`, `fromAbsolute`, `globPlain` |
| `parity-helm-value-file-glob-missing` | the glob `value-file-links/missing/*.yaml`, which matches nothing, `a.yaml`, and the missing `missing.yaml`, with `ignoreMissingValueFiles: true` | `winner: a`, `fromA` |

In the `$ref` case, the glob leaves out `glob/linked.yaml`, because the list
also names it, and the explicit entry keeps its place at the end. The
second source of the Application is the same repository with `ref: values`.

`TestHelmValueFileSmokeFixtureMatchesUpstream` in `internal/render`
renders the value files of all three Applications with drydock and with a
copy of `getResolvedValueFiles`.

## HTTP Helm repository fixture

`helm-repo/` is an HTTP Helm repository. The smoke serves it from a second
container in the OCI registry Pod, at
`http://argocd-parity-registry.argocd-parity.svc.cluster.local:18879`, so it
uses the same `/etc/hosts` entry, and the registry port-forward also
forwards port `18879`. `package_helm_repo` in the smoke script builds one
archive for each `HELM_REPO_CHARTS` entry from `chart-templates/`, and
copies `index.yaml` and `files/`. The index lists `parity-range` at
`1.0.0`, `1.2.0`, `1.2.3`, `1.3.0-rc.1`, and `2.0.0`, and `parity-extra` at
`1.0.0`. Each chart renders a ConfigMap `<release>-<chart>` with the chart
name and version and the value `revision`.

The drydock captures run with `--offline`. Before them,
`warm_drydock_helm_repo_cache` renders each `HELM_REPO_APPLICATIONS` entry
once online, which fills the chart cache, the recorded version lists, and
the remote cache.

Chart sources with a version range. Each one sets the Helm parameter
`revision` to `$ARGOCD_APP_REVISION`:

| Application | `targetRevision` | chart | `revision` |
| --- | --- | --- | --- |
| `parity-helm-range-wildcard` | `1.2.*` | `parity-range-1.2.3` | `1.2.3` |
| `parity-helm-range-constraint` | `>=1.0.0 <1.3.0` | `parity-range-1.2.3` | `1.2.3` |
| `parity-helm-range-short` | `1.2` | `parity-range-1.2.3` | `1.2` |
| `parity-helm-range-prerelease` | `~1.3.0-0` | `parity-range-1.3.0-rc.1` | `1.3.0-rc.1` |

`1.2` parses as a semver version, so the repo-server keeps it as written,
and helm pull takes the highest `1.2.x`. The other revisions are
constraints, and the repo-server resolves them against the index.

Local charts with dependencies on the repository:

| Application | dependencies | rendered charts |
| --- | --- | --- |
| `parity-helm-dependency-range` | `parity-range ~1.2.0`, no `Chart.lock` | `parity-range-1.2.3` |
| `parity-helm-dependency-partial` | `parity-range ~1.2.0` and `parity-extra 1.0.0`, no `Chart.lock` | `parity-range-1.2.3` and `parity-extra-1.0.0` |
| `parity-helm-dependency-vendored-version` | `parity-range 9.9.9` | the vendored `parity-range-1.0.0` |

`parity-extra` is missing in `parity-helm-dependency-partial`, so the
repo-server runs `helm dependency build`. The smoke adds the archive
`charts/parity-range-1.2.0.tgz` to that chart in `FIXTURE_GIT_WORK` before
it commits the served repository. The build fetches `parity-range` again
at `1.2.3` and removes the vendored archive. The drydock capture of this
Application maps the fixture URL to `FIXTURE_GIT_WORK`
(`HELM_REPO_GIT_WORK_APPLICATIONS`). In
`parity-helm-dependency-vendored-version`, `charts/parity-range` is version
`1.0.0`. helm template matches dependencies by name only, so nothing is
fetched, and the vendored chart renders.

`parity-oci-helm-range` renders the OCI Helm chart of the "OCI Helm chart
fixture" at `targetRevision: "1.*"`. The repo-server lists the registry
tags and pulls the highest match, `1.0.0`, and drydock does the same with
helm's registry client. The warm run records the tag list for the offline
capture.

`parity-helm-file-parameter-url` renders `charts/helm-file-parameter-url`
with the value file `files/values.yaml` and the file parameter
`fromFileParameter` from `files/file-parameter.txt`, both by URL. Both
sides must render `fromRemoteValueFile: remote` and `fromFileParameter`
with the text of the file and its final newline.

## ApplicationSet name validation fixture

Argo CD v3.6.1 checks every generated Application name before it creates
anything (`applicationset/controllers/applicationset_controller.go:608-642`).
It sets an `ErrorOccurred` condition with reason `ApplicationValidationError`,
drops each Application that failed the check, and still creates the rest
(lines 298-352). Two fixtures pin this:

- `applicationsets/validation-empty-name.yaml` renders three list elements.
  The middle one renders an empty `metadata.name`. Argo CD creates
  `parity-empty-name-alpha` and `parity-empty-name-beta` only.
- `applicationsets/validation-duplicate-name.yaml` renders
  `parity-duplicate-name-shared` twice and `parity-duplicate-name-unique` once.
  Argo CD keys the error by the shared name, so it creates neither shared
  Application. It creates `parity-duplicate-name-unique` only.

drydock drops the same Applications and warns
`appset.application-validation-failed` for each failure.

The dropped Applications have no manifests to compare, so the smoke checks
the Application lists instead. It already compares the Argo CD list with the
`APPLICATIONS` array in the smoke script. It also runs `drydock get apps` over
the fixture repository and compares that list with the same array. Without
the second check, an Application that drydock keeps by mistake would go
unnoticed, because the smoke renders only the Applications it names.

## ApplicationSet generator fixtures

Argo CD v3.6.1 runs each generator through `generators.Transform`
(`applicationset/generators/generator_spec_processor.go`). These fixtures pin
how it reads selectors and generator fields, and which generator errors fail
the whole ApplicationSet.

`applicationsets/matrix-child-selector.yaml` has two matrix generators. In
the first one, the second child selects `env: "{{.env}}"`. Transform filters
the child's params with the selector as written, before it fills in the
params of the first child (lines 34 and 89). The literal text matches no
param, so no `parity-matrix-selector-templated-*` Application exists. In the
second matrix, the second child is a merge. The merge runs its own children
from the filled-in spec, so the selector inside it becomes `stage: dev`.
Only `parity-matrix-selector-inner-dev` exists.

`applicationsets/generator-two-fields.yaml` sets `list` and `matrix` in one
`spec.generators` entry. Transform runs every generator field that the entry
sets (`GetRelevantGenerators`), so both `parity-two-fields-list` and
`parity-two-fields-matrix` exist.

Each `applicationsets/generator-error-*.yaml` file has a list generator that
works and one generator that fails:

| File | Failure | Argo CD source |
| --- | --- | --- |
| `generator-error-elements-yaml.yaml` | `elementsYaml` holds a scalar entry | `list.go:80-86` |
| `generator-error-empty-child.yaml` | A matrix child written as `{}` | `matrix.go:129-131` |
| `generator-error-nested-too-deep.yaml` | A matrix inside a matrix inside a matrix. The middle matrix decodes its children as terminal generators, which have no `matrix` field, so the innermost one names no generator. | `matrix.go:129-131` |
| `generator-error-selector.yaml` | A selector with the operator `Sometimes` | `generator_spec_processor.go:34-37` |
| `generator-error-merge-keys.yaml` | A merge whose first child repeats a merge key value | `merge.go:106-134` |
| `generator-error-matrix-count.yaml` | A matrix with one child | `matrix.go:45-51` |
| `generator-error-cluster-selector.yaml` | A cluster generator selector whose value is not a valid label value | `cluster.go:194-198` |

Argo CD sets an `ErrorOccurred` condition and creates no Applications for
each of these ApplicationSets, also none from the list generator that works.
The condition message is the generator error with the wraps that the matrix
and merge generators add, for example `failed to get params for second
generator in the matrix generator: child generator generated no parameters`.
drydock carries the same text.
drydock warns `appset.generator-failed` and generates none. The smoke lists
each one in `APPLICATIONSET_ERRORS` with the text that the condition must
contain. `compare_applicationset_errors` then requires a drydock warning
with that code for the file, and the warning must contain the same text.

## Cluster generator fixture

`applicationsets/cluster-annotations.yaml` (Go-template mode) and
`applicationsets/cluster-annotations-fasttemplate.yaml` (default mode) use the
cluster generator. Their input is two cluster Secrets in
`cluster-secrets/clusters.yaml`. The smoke applies them to the `argocd`
namespace before the ApplicationSets.

The Secrets carry the annotations that Argo CD v3.6.1 keeps out of cluster
generator parameters (`applicationset/generators/cluster.go:132-140`):
`argocd.argoproj.io/refresh`, `refresh-timestamp`, `hydrate`,
`hydrate-timestamp`, `tracking-id`, `installation-id`, and
`kubectl.kubernetes.io/last-applied-configuration`. The smoke applies the
Secrets with client-side `kubectl apply`, which writes the last one. The
smoke fails if that annotation is missing. The alpha Secret also has two
annotations that Argo CD does expose: `parity.drydock.dev/team` and
`argocd.argoproj.io/manifest-generate-paths`. The second one shows that only
the listed keys are hidden, not every `argocd.argoproj.io/` key.

Both ApplicationSets render `charts/appset-cluster`, a chart that writes its
`data` values into a ConfigMap. The Go-template ApplicationSet stores
`toJson .metadata.annotations`, so the comparison sees the exact set of
exposed keys. The beta Secret has only reserved annotations, so it renders
`{}`: the key exists because Argo CD checks the unfiltered map
(`cluster.go:171-173`). The fasttemplate ApplicationSet names single keys.
Argo CD leaves a placeholder it cannot resolve as literal text
(`applicationset/utils/utils.go:434-441`), so each reserved key renders as
`{{metadata.annotations.<key>}}`.

Each generator uses a label selector. With a selector, Argo CD does not add
the local `in-cluster` entry (`cluster.go:60`). The drydock provider fixture
does not model that entry.

The Secret servers do not exist. Every generated Application deploys to
`in-cluster`, so Argo CD never connects to them.

drydock reads cluster generator input only from a provider fixture, never
from Secrets in the repository. `appset-provider-fixture.yaml` describes the
same two clusters, and every drydock capture passes it with
`--appset-provider-fixture`. The fixture lists the
`argocd.argoproj.io/secret-type` label, because Argo CD only reads Secrets
with that label. It also lists a stand-in last-applied annotation for each
cluster. `TestArgoCDParityProviderFixtureMatchesClusterSecrets` in
`internal/appset` fails if the fixture and the Secrets drift apart.

## Cluster generator param type fixture

`applicationsets/cluster-sprig-map-function.yaml` uses the same cluster
Secrets and selector as the cluster generator fixture. Its Go template calls
`hasKey .metadata.annotations "parity.drydock.dev/team"`.

Argo CD v3.6.1 passes `.metadata.annotations` as a `map[string]string`
(`applicationset/generators/cluster.go:172`). Nothing converts it before the
template runs: the generator spec processor passes the params on as they are
(`applicationset/generators/generator_spec_processor.go:68-90`), and Render
executes the template on them (`applicationset/utils/utils.go:419`). Sprig
`hasKey` takes a `map[string]interface{}`, so every render fails with a type
error. Argo CD sets an `ErrorOccurred` condition and creates no Applications
(`applicationset/controllers/applicationset_controller.go:199-215`).

Both clusters have the annotations key. The beta Secret has only reserved
annotations, so its map is empty but present. The type is the only reason the
render fails.

drydock passes the same Go type. It warns `appset.template-render-failed` and
generates no Applications. No `parity-cluster-haskey-*` name is in the
smoke's `APPLICATIONS` array, so the Argo CD and drydock inventory checks
both require zero Applications from this ApplicationSet.
`TestArgoCDParityFixtureClusterSprigMapFunctionGeneratesNothing` in
`internal/appset` pins the drydock side.

A missing Application could also mean that Argo CD has not reconciled the
ApplicationSet yet. So the smoke lists this ApplicationSet in
`APPLICATIONSET_ERRORS`. Before the inventory checks, it waits for an
`ErrorOccurred` condition with status `True` and requires the message to
contain `got map[string]string`. That text comes from the Go template type
error for the `hasKey` argument. The smoke fails if the condition does not
appear within the timeout.

The smoke has no fixture for a matrix or merge param type clash. For example,
a matrix of a list element with a `values` field and a cluster generator with
`values` makes Argo CD v3.6.1 panic in the param merge
(`applicationset/generators/matrix.go:95`). Reconcile recovers the panic and
returns before it sets any condition
(`applicationset/controllers/applicationset_controller.go:123-133`). So there
is no condition that proves Argo CD reconciled the ApplicationSet, and a
missing Application would prove nothing. Unit tests in `internal/appset` and
`internal/cli` cover the drydock side.

## Generator selector list param fixture

`applicationsets/selector-list-params.yaml` has two Git directory generators
over `workloads/appset/git/*`, each with a selector on the `path.segments`
param.

Argo CD v3.6.1 builds `path.segments` as a `[]string`
(`applicationset/generators/repo_path_utils.go`). Before it matches a
selector, it flattens the params with `github.com/jeremywohl/flatten`
(`applicationset/generators/generator_spec_processor.go:126-163`). That
library only walks `map[string]interface{}` and `[]interface{}`, so the
`[]string` is one leaf, and `fmt.Sprintf("%v")` turns it into
`[workloads appset git alpha]`. There is no `path.segments.N` key.

- The first generator selects `path.segments: "[workloads appset git alpha]"`,
  so only `parity-segments-alpha` exists. It is in the smoke's
  `APPLICATIONS` array.
- The second generator selects on `path.segments.3` with `Exists`, so it
  matches nothing. No `parity-segments-index-*` name is in `APPLICATIONS`, so
  the Argo CD and drydock inventory checks both require that these
  Applications are absent.

drydock flattens params the same way.
`TestArgoCDParityFixtureSelectorListParams` in `internal/appset` pins the
drydock side.

## RollingSync fixture

The `applicationsets/rolling-sync-*.yaml` ApplicationSets use
`spec.strategy.type: RollingSync`. The ApplicationSet controller of Argo CD
v3.6.1 has a progressive syncs flag
(`applicationsetcontroller.enable.progressive.syncs` in
`argocd-cmd-params-cm`). Without that flag, the controller does not check
RollingSync steps. The flag is off by default
(`cmd/argocd-applicationset-controller/commands/applicationset_controller.go:327`).
The smoke turns it on during the Argo CD install. drydock reads the same key
from `settings/argocd-cmd-params-cm.yaml` in the fixture repository, so keep
the two equal.

The smoke also sets `applicationsetcontroller.concurrent.reconciliations.max`
to `1`. Argo CD v3.6.1 keeps the RollingSync findings in one field of a
`progressivesync.Manager` that all reconciles share
(`applicationset/progressivesync/progressive_sync.go:82` and `:119-125`).
With the default of 10 concurrent reconciles, one ApplicationSet can show the
`InvalidRolloutConfig` message of another one. For example, a CI run showed
the `maxUpdate` message on `parity-rolling-sync-duplicate`. drydock does not
model this race. It changes only the condition message, not the Applications.

With the flag on, Argo CD sets one of these conditions on the ApplicationSet:

| ApplicationSet | Condition | Message |
| --- | --- | --- |
| `parity-rolling-sync-valid` | `InvalidRolloutConfig` `False`, reason `ApplicationSetValidRolloutConfig` | `Rolling Sync Configured correctly` |
| `parity-rolling-sync-operator` | `InvalidRolloutConfig` `True`, reason `ApplicationSetInvalidRolloutConfig` | `Step 1 has invalid matchExpression operators: Exists. ...` |
| `parity-rolling-sync-duplicate` | same | `Application 'parity-rolling-sync-duplicate-dev' is selected by multiple steps: (1-2)` |
| `parity-rolling-sync-empty-step` | same | `Steps [2] have no applications matching their criteria` |
| `parity-rolling-sync-max-update` | same | `Step 1 has an invalid maxUpdate value: 1` |
| `parity-rolling-sync-no-steps` | `ErrorOccurred` `True`, reason `ApplicationSetRolloutError` | `No steps defined for rollout` |

`InvalidRolloutConfig` comes from `getInvalidRolloutConfig`
(`applicationset/progressivesync/progressive_sync.go:953-971`). It never
stops Argo CD from creating or updating Applications, so every Application
of the first five ApplicationSets is in the smoke's `APPLICATIONS` array.
When there are several findings, the condition shows only the one with the
highest priority (`validation_issues.go:114-128`). The operator fixture has
two findings (the operator and an empty step 1), so it pins that order.
Argo CD checks `maxUpdate` only for Applications that already exist, so that
condition turns `True` on a reconcile after the first one.

An empty steps list is different. Argo CD sets `ErrorOccurred` and returns
before it creates, updates, or deletes any Application
(`applicationset/controllers/applicationset_controller.go:263-277`). drydock
also generates no Applications for it. `parity-rolling-sync-no-steps` has a
list element, and no `parity-rolling-sync-no-steps-*` name is in
`APPLICATIONS`, so the inventory checks prove that both sides drop it.

The smoke does not run Argo CD with the flag off, because that needs a second
ApplicationSet controller restart and a second set of waits. Unit tests cover
the flag off: drydock then generates every Application and gives one
`appset.rollout-strategy-ignored` advisory.

The smoke checks the conditions in two steps:

1. Before the inventory checks, it waits until each ApplicationSet in
   `ROLLING_SYNC_CONDITIONS` has the exact status, reason, and message above.
2. After `drydock get apps`, it reads the warnings from that command's
   stderr. Argo CD and drydock must flag the same ApplicationSets. drydock
   flags an ApplicationSet with `appset.invalid-rollout-config` or
   `appset.rollout-no-steps`. For each flagged ApplicationSet, one drydock
   warning must carry the Argo CD message. drydock warns once for each
   finding class, such as invalid operators or empty steps, so it can warn
   more than once for one ApplicationSet.

The usual inventory and manifest comparisons cover the generated
Applications. `TestArgoCDParityFixtureRollingSync` in `internal/appset` pins
the drydock side.

With steps, the Argo CD controller also sets
`spec.syncPolicy.automated.enabled: false` on each generated Application with
an automated sync policy. The inventory comparison checks only the name,
namespace, project, destination, and sources, not `syncPolicy`, so the smoke
does not pin this. `TestGenerateDisablesAutomatedSyncForRollingSync` in
`internal/appset` does.

The controller also starts real syncs of these Applications. Their templates
set `CreateNamespace=true`, so the syncs do not fail on a missing namespace.
Neither side's rendered manifests show the created Namespace.

## Concurrent runs

Each smoke run writes its own kubeconfig to `<out>/kubeconfig` and removes it
on exit (unless `--keep-cluster` is set). So two runs on one host do not
switch each other's kubectl context. Two runs still cannot overlap: the
registry bridge and the Argo CD server port-forward use the fixed local ports
5443 and 18080. Give each run its own `--cluster-name` and `--out`. When the
other run no longer uses either port, start the next run.

## Directory decode fixture

These fixtures cover how Argo CD v3.6.1 reads each file in a directory
source, and how it reads Helm, Kustomize, and plugin output (see "Generated
output that fails the source"). Argo CD reads a directory in three stages:

1. The repo-server reads every file into one list of objects
   (`findManifests` and `getObjsFromYAMLOrJSON` in
   `reposerver/repository/repository.go`).
2. `GenerateManifests` splits each list into its items and sets the tracking
   metadata on each manifest.
3. The controller reads each manifest back and fails on one without a
   `kind`.

Some files fail the whole source. Other files are skipped without a message.
The fixtures live under `workloads/directory-decode/`.

### Files that both sides skip or decode

`applications/directory-decode-skips.yaml` (`parity-directory-decode-skips`)
is a normal Application in the main comparison. Its directory holds one plain
ConfigMap and these files:

| file | expected result |
| --- | --- |
| `values.yaml` | skipped: a values file has no `kind` |
| `syntax-error.yaml` | skipped: a YAML syntax error with no manifest markers |
| `empty-mapping.yaml` | skipped: `{}` has no `kind` |
| `empty.yaml` | skipped: an empty YAML file holds no documents |
| `sequence.yaml` | skipped: a YAML list is not an object |
| `values-then-kind.yaml` | skipped: the first document has no `kind`, and the file text has no `metadata:` |
| `utf16le-no-bom.yaml` | skipped: UTF-16 text without a byte order mark is read as UTF-8 and does not parse |
| `utf16le-bom.yaml` | rendered: the UTF-16LE byte order mark selects the decoding |
| `utf16be-bom.json` | rendered: the same for UTF-16BE, under the single-object JSON rule |
| `utf8-bom.json` | rendered: the UTF-8 byte order mark is dropped before the JSON decoder runs |
| `invalid-utf8.yaml` | rendered: the invalid byte becomes U+FFFD |
| `apiversion-only.yaml` | skipped: the first document has an `apiVersion` but no `kind`, and the file text has no `kind:` |
| `empty-kind.yaml` | skipped: `kind: ""` stops the stream like a missing `kind`, and the file text has no `metadata:` |
| `apiversion-typo.yaml` | skipped: the `apiVersion` `apps/v1/` does not parse as a group version, so the document has no `kind` for Argo CD, and the file text has no `metadata:` |
| `kind-only.yaml` | rendered: a document needs a `kind` but not an `apiVersion`, so the ConfigMap renders without an `apiVersion` |
| `list-no-apiversion.yaml` | rendered: a `kind: List` without an `apiVersion` is still split into its item |
| `items-scalar.yaml` | rendered: a ConfigMap whose `items` field is a string is not a list, so it renders unchanged |
| `items-map.yaml` | rendered: the same for an `items` field that is a map |

The comparison proves both sides render the same nine ConfigMaps. The skipped
files never produce an object.

`applications/directory-decode-jsonnet.yaml`
(`parity-directory-decode-jsonnet`) is a second normal Application in the
main comparison. Argo CD adds Jsonnet output to the same list of objects as
every other file, so the same list split applies. Its directory holds these
files:

| file | expected result |
| --- | --- |
| `list.jsonnet` | a `kind: List` renders its two ConfigMaps, not the List |
| `array.jsonnet` | an array adds each element: `null` adds nothing, a ConfigMap renders, a List renders its item, and a List whose `items` field is `null` renders nothing |
| `kind-only.jsonnet` | an object needs a `kind` but not an `apiVersion`, so the ConfigMap renders without an `apiVersion` |

The comparison proves both sides render the same five ConfigMaps.
`TestDirectoryRendererParityRenderFixturesMatchUpstream` in `internal/render`
renders both directories with drydock and with the upstream oracle and
requires the same objects.

Several of these files are not valid text on purpose. The UTF-16 files hold
NUL bytes, so Git treats them as binary. Do not let an editor or a formatter
rewrite them.

### File symlinks

`applications/directory-symlinks.yaml` (`parity-directory-symlinks`) is a
normal Application in the main comparison. It renders
`workloads/directory-symlinks/app` with `recurse: true` and `exclude:
excluded.yaml`. Each symlink in `app/` points into `targets/`, outside the
source path. Git stores each symlink as mode `120000`. The smoke copies the
fixture with `cp -R`, which keeps symlinks, and the Argo CD checkout makes
them again.

| file in `app/` | expected result |
| --- | --- |
| `plain.yaml` | rendered: a regular file |
| `linked.yaml` | rendered: the target is a regular file in the repository |
| `chain.yaml` | rendered: the target is a second symlink, which ends at a regular file |
| `excluded.yaml` | skipped: `exclude` matches the name of the symlink, not of its target |
| `filtered.txt` | skipped: the extension filter reads the name of the symlink |
| `dangling.yaml` | skipped: the target is missing |
| `dir.yaml` | skipped: the target is a directory, not a regular file |
| `linked-dir` | skipped: the walk never enters a symlinked directory, also with `recurse` |
| `marker.yaml` | skipped: the target holds `+argocd:skip-file-rendering` |
| `linked.jsonnet` | rendered: evaluated through the symlink, and its import of `lib.libsonnet` resolves next to the symlink, so the ConfigMap is `parity-symlinks-jsonnet-link-dir` |
| `imports.jsonnet` | rendered: it imports `shared.libsonnet`, a symlink into `targets/jsonnet` |

The comparison proves both sides render the same five ConfigMaps.
`TestDirectorySymlinksSmokeFixtureMatchesUpstream` in `internal/render`
renders the directory with drydock and with copies of the repo-server code.

### Files that fail the source

`directory-decode-applications/` holds one Application per failing case. They
live in the `parity-directory-decode` namespace, so the main inventory and
manifest comparisons never see them. The smoke adds that namespace to
`application.namespaces`, and `projects/parity-directory-decode.yaml` allows
it through `sourceNamespaces`.

Each source directory holds a valid ConfigMap and one file that fails the
source. Each `order-*` directory holds two failing files, which fail at
different stages. `../directory-decode-expected.yaml` lists the cases. For
each case it names the file that Argo CD reports, the text that the Argo CD
`ComparisonError` condition must contain, and the text that drydock's stderr
must contain. Both texts must name the file, so a failure caused by a
different file does not pass.

The cases without a `kind` are different. The repo-server accepts the three
`.json` objects without a `kind` as OCI image manifests and emits each one.
One of them has a `kind` field, but its `apiVersion` does not parse, so the
Unstructured decode finds no `kind`.
It splits every list into its items and emits each item, so the three list
items without a `kind` are emitted too. The controller then fails with
`Object 'Kind' is missing in '...'`, which quotes the manifest but not the
file. These cases set `argocdQuotesObject`, and their Argo CD text quotes the
start of the manifest instead of the file name. Go sorts the manifest keys,
and the manifest holds the tracking annotation.

The tracking cases are different too. The repo-server fails with
`failed to set app instance tracking info on manifest`, and the message names
the object but not the file. These cases set `argocdNamesObject` to the name
of the object in the failing file, and the Argo CD text must contain it.

`TestDirectoryRendererParityDecodeFixtures` in `internal/render` runs every
case offline. drydock must fail with the expected text. The upstream oracle
must fail with the expected Argo CD text. The oracle runs the upstream
tracking and controller code itself, so the quoted and named texts must
match exactly. Its repo-server decode code is a copy whose wording differs in
case, so the other texts are compared without case.

| Application | failing file | why it fails |
| --- | --- | --- |
| `parity-decode-json-invalid` | `invalid.json` | JSON syntax error |
| `parity-decode-json-multiple` | `multiple.json` | two objects in one `.json` file |
| `parity-decode-json-array` | `array.json` | a JSON array is not one object |
| `parity-decode-json-empty` | `empty.json` | an empty `.json` file |
| `parity-decode-json-yaml` | `yaml-content.json` | YAML text in a `.json` file |
| `parity-decode-yaml-partial` | `partial.yaml` | a YAML syntax error after a decoded object |
| `parity-decode-yaml-kindless` | `kindless.yaml` | a document without `kind` after a decoded object |
| `parity-decode-yaml-markers` | `markers.yaml` | the first document has no `kind`, and the file text holds `apiVersion:`, `kind:` and `metadata:` |
| `parity-decode-json-package` | `package.json` | an npm `package.json` object has no `kind`, so the controller fails |
| `parity-decode-json-object` | `empty-object.json` | `{}` has no `kind`, so the controller fails |
| `parity-decode-json-list-item` | `kindless-list.json` | an item of a list without a `kind` has no `kind`, so the controller fails |
| `parity-decode-yaml-list-item` | `list.yaml` | an item of a YAML `kind: List` has no `kind`, so the controller fails |
| `parity-decode-jsonnet-kindless` | `kindless.jsonnet` | the Jsonnet output has no `kind`, so the repo-server cannot decode it |
| `parity-decode-jsonnet-list-item` | `list.jsonnet` | an item of a Jsonnet `kind: List` has no `kind`, so the controller fails |
| `parity-decode-yaml-tracking` | `tracking.yaml` | a List item whose `annotations` field is a string, so the repo-server cannot set the tracking annotation |
| `parity-decode-order-decode` | `b-invalid.json` | `b-invalid.json` does not decode. It wins over `a-scalar-item.json`, whose list item is not an object, because the repo-server reads every file before it splits lists |
| `parity-decode-json-apiversion` | `apiversion.json` | the `apiVersion` `apps/v1/` does not parse as a group version, so the object has no `kind` and the controller fails |
| `parity-decode-yaml-apiversion` | `apiversion.yaml` | the same `apiVersion` in the first YAML document stops the stream, and the file text holds every manifest marker |
| `parity-decode-order-tracking` | `b-tracking.yaml` | `b-tracking.yaml` fails the tracking annotation. It wins over `a-kindless.json`, which has no `kind`, because the controller checks kinds last |

### Out-of-bounds symlink

`parity-decode-oob-symlink` renders `app` from a second Git repository,
`oob-symlink.git`. The smoke builds it from `../oob-symlink-repo/` and adds
`other/escape.yaml`, a symlink to `/etc/hosts`, outside the source path. So
drydock's own repository never holds that symlink. The Argo CD repo-server
checks the whole checkout before it reads the source path, and fails every
Application from it with `repository contains out-of-bounds symlinks. file:
other/escape.yaml`. drydock must fail with the same text. The case cannot
live in the main fixture repository, because it would fail every Application
there. The expected file marks it with `repository: oob-symlink`, and
drydock reads the repository through `--repo-map`.
`argocd-cmd-params-cm` does not set `reposerver.allow.oob.symlinks`, so the
smoke covers only the default. Setting the key needs a restart of the
repo-server, and it changes every Application in the cluster.

### Generated output that fails the source

Six more cases in the same namespace set `source` in the expected file. Their
source is a Helm chart, a Kustomize base, or a config management plugin, not a
plain directory. Argo CD reads the output of `helm template`, `kustomize
build`, and the plugin with gitops-engine `kube.SplitYAML`. That function
decodes each document into an Unstructured object, so a document without a
`kind` fails in the repo-server. The message quotes the document as JSON with
sorted keys, without tracking metadata, so these cases set
`argocdQuotesObject`. A List item without a `kind` passes the repo-server and
fails in the controller, as for a directory source.

For a Helm or Kustomize case, `file` is the template or the kustomization
file that drydock names. For the plugin case, `file` is the plugin program,
`generate.awk`. drydock names the plugin output, `generate-output`, so the
smoke tool requires that name in the drydock text instead. The plugin case
uses the `parity-env` sidecar, and its drydock capture gets the same plugin
flags as the plugin Applications of the main comparison.

`TestGeneratedParityDecodeFixtures` in `internal/render` runs these cases
offline. It renders the Helm cases with a copy of how `helm template` prints
the output, the Kustomize case with the same kustomize library, and the
plugin case with `awk`, and then runs the upstream code on the output. The
Argo CD texts must match exactly.

| Application | source | failing file | why it fails |
| --- | --- | --- | --- |
| `parity-decode-helm-kindless` | helm | `templates/kindless.yaml` | a template document has no `kind`, so the repo-server cannot decode it |
| `parity-decode-helm-empty-object` | helm | `templates/empty-object.yaml` | the template document `{}` has no `kind` |
| `parity-decode-helm-apiversion` | helm | `templates/deployment.yaml` | the `apiVersion` `apps/v1/` does not parse, so the document has no `kind` |
| `parity-decode-helm-list-item` | helm | `templates/list.yaml` | an item of a Helm `kind: List` has no `kind`, so the controller fails |
| `parity-decode-kustomize-apiversion` | kustomize | `kustomization.yaml` | kustomize keeps the `apiVersion` `apps/v1/`, so the second document of the build output has no `kind` |
| `parity-decode-plugin-empty-object` | plugin | `generate.awk` | the plugin prints `{}` after a ConfigMap, so the second document has no `kind` |

`run_directory_decode_smoke` in `scripts/argocd-parity-smoke.sh` waits for a
`ComparisonError` on each Application, then saves the Application JSON. It
then runs `drydock build app` for each Application and saves the exit code
and stderr. `scripts/argocd-directory-decode-smoke` compares the two sides.
The results go to `directory-decode/` in the smoke output directory.

### Helm file parameters that fail the source

Four more cases set `source: helm-file-parameters`. Each is a Helm chart
whose `helm.fileParameters` use `file-parameter-links/` (see "Helm file
parameter symlink fixture"). `file` is the path that the messages name.

| Application | why it fails |
| --- | --- |
| `parity-decode-helm-fileparam-lexical-escape` | `lexical-dir/up.txt` goes three directories up from `lexical-dir` as text, which leaves the repository root: `file '<path>' resolved to outside repository root` |
| `parity-decode-helm-fileparam-overridden-escape` | the same path in an entry that a later entry overrides. The repo-server resolves it anyway |
| `parity-decode-helm-fileparam-dangling` | `dangling.txt` resolves to `missing-target.txt`, and helm cannot read it |
| `parity-decode-helm-fileparam-directory` | `to-dir` resolves to the directory `targets`, and helm cannot read it |

The repo-server names a read failure under `<path to cached source>`, and
drydock names it under the repository root, so the expected texts start at
`file-parameter-links/`. A symlink loop and a symlink to the repository
root fail with `internal error: failed to resolve path`, which names no
file, so only the unit tests cover them. `TestGeneratedParityDecodeFixtures`
in `internal/render` runs these cases with drydock and with the upstream
resolver.

### Helm and Kustomize helmCharts renders that fail

Thirteen more cases fail inside `helm template` or kustomize, not on the
decode of the output. Their messages are Helm and kustomize texts, so
`TestGeneratedParityDecodeFixtures` has no offline upstream oracle for
them. `TestHelmRenderParityDecodeFixtures` in `internal/render` renders
them with drydock and requires `drydockMessage`. The smoke checks the Argo
CD side. The maintainer who added them also ran each one with the helm
v4.3.1 and kustomize v5.8.3 binaries that Argo CD v3.6.1 ships.

Two cases set `source: kustomize` and `fileMissing: true`. The failing
file is missing on purpose, and the smoke tool test requires that the
fixture does not hold it. Argo CD names the file by its full path, so
`argocdMessage` starts at the part that the two sides share.

| Application | failing file | why it fails |
| --- | --- | --- |
| `parity-decode-kustomize-helm-no-values` | `charts/novalues/values.yaml` | the entry sets no `valuesFile`, so kustomize loads the `values.yaml` of the chart, and the chart has none |
| `parity-decode-kustomize-helm-values-glob` | `values-*.yaml` | kustomize loads `valuesFile` as a file name, not as a glob, and no file has that name. `values-a.yaml` exists, so a glob would match |

The other cases set `source: render` and name no file. Argo CD must report
a `ComparisonError` that contains `argocdMessage`.

| Application | why it fails |
| --- | --- |
| `parity-decode-kustomize-helm-release-name-dash` | kustomize rejects the `releaseName` `-bad`, which helm would read as a flag |
| `parity-decode-kustomize-helm-values-outside-root` | `additionalValuesFiles` names `../kustomize-helm-values-outside-root.yaml`, outside the kustomization directory, and the smoke `argocd-cm` keeps the default `LoadRestrictionsRootOnly`. Argo CD names absolute paths, so `argocdMessage` is the start of the text |
| `parity-decode-kustomize-helm-values-directory` | `valuesFile` names the directory `vals`, which the kustomize file loader cannot load |
| `parity-decode-kustomize-helm-values-directory-outside-root` | `additionalValuesFiles` names the directory `../kustomize-helm-values-outside-dir`. The restriction checks for a directory before it checks the root, so the text is the directory text |
| `parity-decode-kustomize-helm-charthome-outside-root` | `helmGlobals.chartHome` is `../kustomize-helm-shared-charts`, and the entry sets no `valuesFile`, so kustomize loads the chart `values.yaml` outside the kustomization directory and fails |
| `parity-decode-helm-release-name-long` | `helm.releaseName` has 54 characters, and helm allows 53 |
| `parity-decode-helm-release-name-template` | Argo CD passes the release name as `--name-template`, and `{{ .Missing.Field }` does not parse as a Go template |
| `parity-decode-helm-version` | Argo CD v3.6.1 accepts `helm.version` empty, `v3`, or `v4`, not `v2` |
| `parity-decode-helm-library` | the chart has `type: library`, and helm template renders only application charts |
| `parity-decode-helm-kube-version` | the chart needs `kubeVersion: '>=99.0.0-0'`, and the Application sets `helm.kubeVersion: 1.30.0`, so both sides check the same version |
| `parity-decode-helm-chart-v3` | helm v4.3.1 loads a chart of `apiVersion: v3`, but helm template renders only `v1` and `v2` charts |

## Helm release defaults fixture

Two Applications in the main comparison pin the release name and namespace
that helm uses when the spec leaves them out.

`applications/helm-default-namespace.yaml` (`parity-helm-default-namespace`)
renders `charts/default-namespace` without a destination namespace and
without `helm.namespace`. Argo CD then passes no `--namespace`, and helm
uses `default`. The ConfigMap names its own namespace, so the controller
can compare it.

`applications/kustomize-helm-defaults.yaml`
(`parity-kustomize-helm-defaults`) renders
`workloads/kustomize-helm-defaults`, whose one `helmCharts` entry has no
`releaseName` and no namespace, and no kustomization sets a namespace. The
Application sets `kustomize.kubeVersion` and `kustomize.apiVersions`, which
Argo CD passes to kustomize as `--helm-kube-version` and
`--helm-api-versions`. The entry sets its own `apiVersions`, which replace
the flag values.

Both Argo CD and drydock must render this `data`:

| ConfigMap | key | value |
| --- | --- | --- |
| `parity-helm-default-namespace` | `releaseName` | `parity-helm-default-namespace` |
| `parity-helm-default-namespace` | `releaseNamespace` | `default` |
| `parity-kustomize-helm-defaults-defaults` | `releaseName` | `release-name` |
| `parity-kustomize-helm-defaults-defaults` | `releaseNamespace` | `default` |
| `parity-kustomize-helm-defaults-defaults` | `kubeVersion` | `v1.31.0` |
| `parity-kustomize-helm-defaults-defaults` | `hasChartAPI` | `true` |
| `parity-kustomize-helm-defaults-defaults` | `hasFlagAPI` | `false` |

Keep the `helmCharts` entry the only one in that kustomization. kustomize
v5.8.3 gives each entry the flag values without a copy, so the
`apiVersions` of one entry overwrite the flag values that the other entries
of the build see. The
[shared API versions fixture](#kustomize-helmcharts-shared-api-versions-fixture)
pins that defect. `parity-kustomize-helm-capabilities` pins an entry without
`apiVersions`, which keeps the flag values.

## Source type gate phase

`source-type-gates/repo/` is a second fixture repo for the last phase of the
smoke. The smoke copies it into the Git repository that it serves, as it
copies the project-policy repos, so its sources live under
`source-type-gates/workloads/`. drydock renders the phase with `--path` and
`--repo-map` set to this repo, so it reads
`source-type-gates/argocd-cm.yaml`. The main comparison never sees it.

`run_source_type_gates_smoke` records the live values of the three keys
and then sets `helm.enable: "false"`, `kustomize.enable: "True"`, and
`jsonnet.enable: "no"` on the live `argocd-cm`, the same keys as the
`argocd-cm.yaml` of the repo. Argo CD keeps a tool on only for the exact
value `true`, so all three tools are off. Then the smoke applies
`source-type-gates/applications.yaml`, captures each Application on both
sides, and compares them in `compare-source-type-gates/`. At the end it
deletes the Applications and restores the recorded values. The phase runs
last because the keys change how Argo CD renders every Application.

The fixture install sets none of the three keys. So each run removes them
right after it logs in to Argo CD, before it applies any Application. A
run that failed under `--keep-cluster` can leave them set, and a later
`--existing-cluster` run would then render every Application with the tools
off.

`argocd app manifests` asks the API server, which reads the keys from its
settings manager and sends them to the repo-server with the manifest
request. The settings manager reads `argocd-cm` through an informer, so the
first request can come before it sees the patched keys.
`capture_source_type_gates_manifest` captures each Application until the
capture is the plain directory render, with a hard refresh between tries,
and fails after 60 tries. `save_source_type_gate_keys` reads each key with
a template that also works when `argocd-cm` has no `data`.

| Application | source | rendered as |
| --- | --- | --- |
| `parity-gates-helm-discovered` | `parity-HelmChart.yaml`, a ConfigMap whose file name ends in `Chart.yaml` | a plain directory, not a Helm chart |
| `parity-gates-helm-explicit` | a `helm` block and no `Chart.yaml` | a plain directory. With Helm on, the source fails |
| `parity-gates-helm-chart-dir` | a local chart: `Chart.yaml`, `values.yaml`, `templates/configmap.yaml`, and `configmap.yaml` at the chart root | a plain directory: the root ConfigMap only. `Chart.yaml` and `values.yaml` have no `kind`, and the render does not walk into `templates/` |
| `parity-gates-helm-chart-only` | `chart: parity/nested/parity-nested-chart` from the OCI Helm registry of the smoke | the extracted chart as a plain directory: the `gate-probe.yaml` ConfigMap at the chart root (`oci-chart/parity-nested-chart/gate-probe.yaml`), which no Helm render of the chart holds |
| `parity-gates-kustomize-discovered` | a `Kustomization` file with `namePrefix: kustomized-` | a plain directory. The file has no extension, so the directory render skips it |
| `parity-gates-kustomize-explicit` | a `kustomize` block with `namePrefix: kustomized-` | a plain directory without the prefix |
| `parity-gates-jsonnet` | `configmap.yaml` and `skipped.jsonnet` | only `configmap.yaml`. With Jsonnet on, the Jsonnet file adds `parity-gates-jsonnet-evaluated` |

Each source renders exactly one ConfigMap named like its Application.
`source_type_gates_capture_ok` makes sure that each Argo CD capture holds
that ConfigMap and nothing that Helm, Kustomize, or Jsonnet adds, so both
sides rendering with the tools on cannot pass. The drydock capture of the
chart-only Application reads the chart from the chart cache that the OCI
warm run filled.
`TestSourceTypeGatesSmokeFixture` in `internal/app` renders the phase
offline with and without the `argocd-cm.yaml`.

### Helm inputs that fail the source

Four more render cases cover Helm value files, chart versions, and
dependency builds. Their messages name no file. They fail in the
repo-server around helm: in `helm dependency build`, in the value file
resolver, or in the chart version resolution.
`TestHelmRenderParityDecodeFixtures` renders the first three with drydock.
It skips the chart repository source, which the tests in `internal/chart`
cover.

| Application | why it fails |
| --- | --- |
| `parity-decode-helm-dependency-stale-lock` | `parity-range` is missing, and the digest of `Chart.lock` does not match `Chart.yaml`, so `helm dependency build` fails: `the lock file (Chart.lock) is out of sync` |
| `parity-decode-helm-dependency-http-lock` | the same with a matching `Chart.lock` that names the `http://` repository. The repo-server adds only `https` and `oci` repositories before the build: `no repository definition for <url>` |
| `parity-decode-helm-value-file-bare-ref` | the value file `$values` resolves to the root of the ref source: `internal error: failed to resolve path` |
| `parity-decode-helm-range-no-match` | the chart source `targetRevision` `>=9.0.0` matches no version of the five in the index: `invalid revision: version matching constraint not found in 5 tags` |

The project `parity-directory-decode` allows the HTTP Helm repository as a
source repository for the last case. drydock resolves its range offline
from the version list that the warm run of the range Applications
recorded.

## Live diff fixture

This fixture compares drydock's diff normalization with the sync status of
a live Argo CD. Normalization is the step that removes ignored fields from
both sides before a diff. The fixture covers these sources of ignore rules:

- Application `spec.ignoreDifferences`: `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers`.
- argocd-cm `resource.customizations`: a split key for a core kind without a
  slash (`ServiceAccount`), the split key `all`, and the block form with a
  `jq` select.
- argocd-cm `resource.compareoptions`: `ignoreAggregatedRoles`.
- The resource annotation `argocd.argoproj.io/ignore-differences`.

The fixture lives in `live-diff/`. The smoke serves it as a third Git
repository, `live-diff.git`, with two tagged commits:

- `live-diff-a` holds `live-diff/repo`.
- `live-diff-b` copies the files of `live-diff/change` over it.

`live-diff/expected.yaml` lists every resource of the Applications in
`live-diff/repo/applications`, with the sync status that Argo CD must report
at `live-diff-b`. A `Synced` resource changes only fields that a rule
removes. An `OutOfSync` resource changes the same fields and one more field
that no rule removes. So each rule must remove its fields and nothing more.

`run_live_diff_smoke` in `scripts/argocd-parity-smoke.sh` runs these steps:

1. It applies the keys of `live-diff/repo/settings/argocd-cm.yaml` to the
   live argocd-cm with server-side apply and its own field manager.
   drydock reads the same file from the repository.
2. It applies the Applications, which name `targetRevision: live-diff-a`,
   and syncs them. Each Application must then be `Synced` and `Healthy`. If
   the live objects did not match Git at the first commit, a later status
   would not prove anything. The smoke does not sync an Application with
   `comparisonError` in the expected file.
3. It applies the Application manifests of `live-diff-b`, as a parent
   Application would sync a changed Application. Then it moves each
   Application to `live-diff-b` with a patch and a hard refresh, and waits
   until Argo CD compared that commit.
4. It saves each Application as JSON and runs `drydock diff apps --ref
   live-diff-b --ref-orig live-diff-a`. The repository map points the
   repository URL at the local repository, so each side of the diff renders
   its own tag. This run leaves out the `comparisonError` Applications with
   `--discover-ignore`, because their failure fails the whole command. Then
   the smoke runs `drydock diff app` for each of them on its own. It also
   saves `drydock build app` of each other Application at `live-diff-b`.
5. `scripts/argocd-live-diff-smoke` compares both sides with the expected
   file. drydock must report a change for a resource exactly when Argo CD
   reports it `OutOfSync`. A resource that one side reports and the expected
   file does not list fails the check. drydock must render every expected
   resource, so a resource that drydock never rendered cannot pass as
   unchanged. The stderr of the drydock diff must contain each
   `drydockWarnings` text, and no other `diff.ignore-*` diagnostic. For a
   `comparisonError` Application, Argo CD must report a `ComparisonError`
   condition with sync status `Unknown`, and its own drydock diff must fail
   with the expected message.
6. It deletes the live diff Applications and removes the argocd-cm keys.
   On an existing cluster, the exit trap does the same when the phase
   fails, unless `--keep-cluster` is set.

The results go to `live-diff/` in the smoke output directory.

### Diff modes

Every Application that Argo CD compares, except one, uses server-side diff
with the `ServerSideApply=true` sync option, as home-ops does. In Argo CD
v3.6.1 that sync option also turns on server-side diff. In this mode, Argo CD applies the target with a dry run
and normalizes the result and the live object. The resources are created by
the same sync, so the API server defaults are the same on both sides.

`parity-live-diff-application-legacy` renders the same resources and rules
as `parity-live-diff-application` with the client-side three-way diff, in
its own namespace. The two Applications prove that the result does not
depend on the diff mode.

### Cases

| Application | resource | status | what it checks |
| --- | --- | --- | --- |
| `parity-live-diff-application` and `-legacy` | `ConfigMap/pointer-ignored` | `Synced` | a `jsonPointers` rule for every ConfigMap removes `data.ignored`, after the pointer `data/kept`, which is not valid and gives the `diff.ignore-pointer-invalid` warning |
| | `ConfigMap/pointer-other` | `OutOfSync` | the same rule keeps `data.kept`: `data/kept` is the missing JSON-patch path `/kept` |
| | `Deployment/jq-ignored` | `Synced` | a `jqPathExpressions` select on the container name removes the image of the `app` container |
| | `Deployment/jq-other` | `OutOfSync` | the same select keeps the image of the `sidecar` container |
| | `ConfigMap/jq-error` | `Synced` | a `jq` expression that fails to run on a ConfigMap is skipped, and the `jsonPointers` rule still applies |
| | `ConfigMap/managed-fields` | `OutOfSync` | a `managedFieldsManagers` rule whose manager owns no field hides nothing |
| `parity-live-diff-customizations` | `ServiceAccount/split-key-ignored` | `Synced` | the split key `ignoreDifferences.ServiceAccount` removes an annotation |
| | `ServiceAccount/split-key-other` | `OutOfSync` | the same key keeps `automountServiceAccountToken` |
| | `Role/block-jq-ignored` | `Synced` | the block key `rbac.authorization.k8s.io/Role` removes the verbs of the `configmaps` rule |
| | `Role/block-jq-other` | `OutOfSync` | the same key keeps the verbs of the `secrets` rule |
| | `ConfigMap/all-key-ignored` | `Synced` | the split key `ignoreDifferences.all` removes a label |
| | `ConfigMap/all-key-other` | `OutOfSync` | the same key keeps `data.kept` |
| | `ClusterRole/parity-live-diff-aggregated-ignored` | `Synced` | `ignoreAggregatedRoles` removes the rules of an aggregated ClusterRole |
| | `ClusterRole/parity-live-diff-aggregated-other` | `OutOfSync` | the same option keeps `aggregationRule` |
| `parity-live-diff-annotation` | `ConfigMap/annotation-pointer-ignored` | `Synced` | the annotation removes `data.ignored` |
| | `ConfigMap/annotation-pointer-other` | `OutOfSync` | the annotation keeps `data.kept` |
| | `ConfigMap/annotation-target-side` | `Synced` | Argo CD reads the annotation from the target commit only: `live-diff-b` adds `/data/ignored` to an annotation that ignores itself |
| | `ConfigMap/annotation-lenient-pointers` | `Synced` | a pointer to a missing path is skipped, and `#/data/ignored` removes `data.ignored` because the first segment is not checked |
| `parity-live-diff-rules-change` | `ConfigMap/rule-added` | `Synced` | `live-diff-b` adds an Application rule that removes the changed field, and Argo CD uses the new rules |
| | `ConfigMap/rule-removed` | `OutOfSync` | `live-diff-b` drops the rule for the changed field, so the old rule does not hide the change |
| `parity-live-diff-invalid-jq` | `ConfigMap/invalid-jq` | `Unknown` | a `jqPathExpressions` entry that does not parse fails the comparison, although no resource matches the rule, and drydock fails with `diff.ignore-rule-invalid` |

### Deliberately not covered by this fixture

- A `managedFieldsManagers` rule that hides a field. Argo CD hides the
  fields that the named manager owns in the live object. drydock has no live
  object, so it cannot know them. The fixture covers only a manager that
  owns nothing.
- `ignoreResourceStatusField`. The API server and the controllers write
  `status` in the live object, so a `status` in Git does not reach it.
- Fields that the API server defaults, and changes that mutating webhooks
  make. drydock compares Git with Git and never sees them.
- A `resource.customizations` jq expression that does not parse. Argo CD
  builds those patches for every Application, so it would fail every
  Application in the cluster. Unit tests in `internal/app` cover it.

## Controller-level server-side diff phase

This phase checks the `argocd-cmd-params-cm` key
`controller.diff.server.side`. When the key is `true`, the Argo CD v3.6.1
application controller compares every Application with server-side diff.
An Application with the `ServerSideDiff=false` compare option still uses
the client-side diff (`shouldUseServerSideDiff` in `controller/state.go`).

The fixture lives in `live-diff-controller-ssd/`. The smoke serves it as the
Git repository `live-diff-controller-ssd.git`, with the tags
`controller-ssd-a` and `controller-ssd-b`, built as the live diff
repository is built. `live-diff-controller-ssd/expected.yaml` has the format
of `live-diff/expected.yaml`, and `scripts/argocd-live-diff-smoke` checks
it.

Both Applications render the ConfigMaps `removed` and `other` into their own
namespaces. `controller-ssd-b` changes both ConfigMaps and adds an
Application rule with the jq expression `.` for `removed`. The rule removes
the whole object on the live side and on the desired side. Server-side diff
then compares `null` with `null` and hides the change. The client-side diff
cannot build a patch from `null`, so the comparison of the Application
fails. `other` changes a field that no rule removes.

| Application | diff mode | expected result |
| --- | --- | --- |
| `parity-controller-ssd` | no diff option, so the switch turns on server-side diff | `removed` is `Synced`, `other` is `OutOfSync`, and drydock shows only `other` |
| `parity-controller-ssd-opt-out` | `ServerSideDiff=false` wins over the switch | a `ComparisonError` condition, and `drydock diff app` fails with `diff.comparison-failed` |

`run_controller_ssd_smoke` in `scripts/argocd-parity-smoke.sh` runs these
steps:

1. It sets `controller.diff.server.side: "true"` in the live
   `argocd-cmd-params-cm` and restarts the application controller. The
   controller reads the key only at startup, through
   `ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF`. drydock reads the key
   from `settings/argocd-cmd-params-cm.yaml` in the repository.
2. It applies and syncs both Applications at `controller-ssd-a`. It syncs
   the `comparisonError` Application too, because the client-side diff
   fails only when the live object exists.
3. It applies the Applications of `controller-ssd-b`, moves them to that
   tag with a hard refresh, and waits until Argo CD compared it.
4. It runs the drydock diff and the drydock renders, and compares the
   results as the live diff phase does.
5. It deletes the Applications, removes the key, and restarts the
   application controller again. On an existing cluster, the exit trap does
   the same when the phase fails, unless `--keep-cluster` is set. The
   install step also removes the key and deletes the Applications of this
   phase and of the live diff phase, in case an earlier run with
   `--keep-cluster` left them.

The results go to `live-diff-controller-ssd/` in the smoke output
directory. Earlier drydock releases read the key as metadata only. They
compared `parity-controller-ssd` with the client-side diff, so the drydock
diff of this phase failed with `diff.comparison-failed`.

## Kustomize helmCharts shared API versions fixture

`applications/kustomize-helm-shared-api-versions.yaml`
(`parity-kustomize-helm-shared-api-versions`) renders
`workloads/kustomize-helm-shared-api-versions` in the main comparison, with
the `--enable-helm` of the smoke `argocd-cm`. The Application sets two
`kustomize.apiVersions`, and Argo CD passes each as one
`--helm-api-versions` flag. The kustomization has two `helmCharts` entries
of one chart. The first entry, `own`, sets
`apiVersions: [entry.parity.example.com/v1]`. The second entry, `flags`,
sets none.

kustomize v5.8.3 gives each entry the slice of flag values itself and
decodes the `apiVersions` of the entry into it. So `own` writes its API
version over the first flag value, and `flags` sees
`entry.parity.example.com/v1` and `flag.parity.example.com/two`. Both
sides must render this `data`:

| ConfigMap | `hasEntryAPI` | `hasFlagOneAPI` | `hasFlagTwoAPI` |
| --- | --- | --- | --- |
| `parity-kustomize-helm-shared-api-versions-own` | `true` | `false` | `false` |
| `parity-kustomize-helm-shared-api-versions-flags` | `true` | `false` | `true` |

## Kustomize build options phases

`kustomize-build-options/` holds four more fixture repos, one for each
phase that runs after the source type gate phase. Each phase repo has
`kbuild-<phase>/argocd-cm.yaml` and `kbuild-<phase>/applications.yaml`. The
smoke copies the repos into the Git repository that it serves, as it copies
the source type gate repo.

For each phase, `run_kustomize_build_options_phase` merges the
`argocd-cm.yaml` of the phase into the live `argocd-cm` with `kubectl patch
--type merge`, so `kustomize.buildOptions` changes and the other keys stay.
Then it applies the Applications and reads `<phase>/expected.psv`. Each line
of that file is one Application:

1. A `render` line names the text that the Argo CD capture must hold and
   the text that it must not hold. An empty text is not checked. The API server reads `argocd-cm` through
   an informer, so the smoke captures with a hard refresh until the capture
   shows the value of the phase. Then the smoke compares the manifests of
   the two sides in `compare-kustomize-build-options-<phase>/`.
2. A `fail` line names the text of the Argo CD `ComparisonError` and the
   text of the drydock error. The smoke waits for that condition, with a
   hard refresh, and requires `drydock build app` to fail with that text.

drydock renders each phase with `--path` and `--repo-map` set to the phase
repo, so it reads the `argocd-cm.yaml` of the phase. At the end, the smoke
deletes the Applications and sets `kustomize.buildOptions` back to
`--enable-helm`, the value of the fixture install. Each run also sets that
value right after it logs in, in case a failed `--keep-cluster` run left
another value. `TestKustomizeBuildOptionsSmokeFixture` in `internal/app`
renders the four phases offline.

| Phase | `kustomize.buildOptions` | What it pins |
| --- | --- | --- |
| `alpha` | `--enable-alpha-plugins --helm-kube-version 1.29.0 --helm-api-versions parity.example.com/v1/FromBuildOptions --enable-managedby-label --reorder none --load-restrictor= --helm-command=/usr/local/bin/helm -o /dev/stdout` and the flags that change nothing | Without the text `--enable-helm`, Argo CD adds no kube or API versions, so `helmCharts` get `v1.29.0` and not the `kustomize.kubeVersion` or `kustomize.apiVersions` of the Application. Alpha plugins turn Helm on. The shared API versions defect without Argo CD flags. The managed-by label `kustomize-v5.8.3` on every resource. A helm program path and `-o /dev/stdout` change nothing. Without `--enable-exec`, kustomize skips a KSOPS exec function, so only the ConfigMap of its source renders, and skips an exec function transformer, so the source renders unchanged. A `configMapGenerator` with `behavior: merge` into a ConfigMap that only `helmCharts` make fails with `does not exist; cannot merge or replace`. A skipped exec function generator still adds its configuration until the end of the build, so a replacement reads `spec.value` from it |
| `no-helm` | `--enable-helm=false --load-restrictor LoadRestrictionsRootOnly` | A plain source renders. `helmCharts` fail with `must specify --enable-helm`. A `kustomize.version: v9.9.9` that `argocd-cm` does not register fails with `kustomize version v9.9.9 is not registered`. A KSOPS generator fails with `external plugins disabled; unable to load external plugin 'ksops'` |
| `unknown-flag` | `--enable-helm --no-such-flag` | Every Kustomize source fails with `unknown flag: --no-such-flag` |
| `output-file` | `--enable-helm -o out.yaml` | kustomize writes the build to a file, so both sides render no manifests and do not fail |

The `alpha` phase cannot show the order of the output: the comparison sorts
resources by identity. `TestKustomizeReorderNoneKeepsResourceOrder` and
`TestKustomizeDefaultOrderIsLegacy` in `internal/render` pin the order.
