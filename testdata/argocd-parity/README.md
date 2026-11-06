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

- A param file that is not valid YAML or JSON. Argo CD fails the whole
  ApplicationSet on it: the generator returns the error
  (`applicationset/generators/repo_path_utils.go:183-186` and `:364-367`),
  and the controller sets `ErrorOccurred` and returns before it creates or
  updates any Application, even from other generators
  (`applicationset/controllers/applicationset_controller.go:199-216`).
  drydock reports a diagnostic and skips the file.
  That difference is documented, and a fixture for it fails the comparison.
- `reposerver.include.hidden.directories`: drydock does not read that
  repo-server setting, and the smoke keeps the default.
- `requeueAfterSeconds` and semver revisions on the generator.

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
   finding, so it can warn more than once for one ApplicationSet.

The usual inventory and manifest comparisons cover the generated
Applications. `TestArgoCDParityFixtureRollingSync` in `internal/appset` pins
the drydock side.

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
