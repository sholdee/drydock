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

- `ARGOCD_APP_REVISION`, `_SHORT`, `_SHORT_8`: live Argo CD resolves the
  commit SHA, drydock reports the spec `targetRevision` (`HEAD`).
- `KUBE_VERSION` and `KUBE_API_VERSIONS`: the smoke passes neither
  `--kube-version` nor `--api-versions`, so drydock sends empty strings while
  live Argo CD sends the kind cluster's values. Making them comparable is a
  follow-up; `--kube-version` overrides per-app `spec` `kubeVersion`, so it
  would have to stay scoped to this Application.
- `DRYDOCK_OFFLINE`: drydock-only, set because the capture runs `--offline`.
- `PATH` and every other sidecar container variable: the sidecar prepends its
  own `os.Environ()`, which drydock's fixed exec environment never has.

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
`DOCKER_HOST` to the `unix://` endpoint `docker context inspect` reports. Run with
`KUBECONFIG` pointing at a fresh file (for example `KUBECONFIG=$(mktemp)`) so
the throwaway kind cluster never touches your kubeconfig. Apple Silicon hosts
run the `linux/arm64` image variants.

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
