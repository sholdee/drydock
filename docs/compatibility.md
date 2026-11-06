# Argo CD Compatibility Notes

`drydock` tracks Argo CD compatibility for runtime-offline desired-state
analysis. Default commands discover, render, test, diff, inspect images, and
diagnose local Argo CD desired state without contacting live Argo CD or
Kubernetes runtime. Declared sources may be fetched into explicit caches unless
`--offline` is set.

## Render Parity Validation

Argo CD remains the semantic reference for generated desired manifests. drydock
keeps normal commands runtime-offline, then validates compatibility work with
an isolated Argo CD render parity smoke.

The smoke installs the pinned Argo CD version into kind, serves the
`testdata/argocd-parity` fixture repository to Argo CD, renders the same
Applications with drydock, and compares generated desired manifests. It also
compares the Application inventories: drydock must generate exactly the
Applications that Argo CD holds, with the same project, destination, and
sources. Maintainers run it manually. When render parity fixtures or
semantic-rendering dependencies change, CI runs it too. See `docs/release.md`
for maintainer workflow details.

It also runs a thin live AppProject Application-spec policy sanity check for
source repository, destination, and source namespace outcomes against the real
Argo CD instance. The destination cases include these:

- A deny entry that wins over an allow entry, listed before or after it.
- The built-in `in-cluster` cluster, by name and by server.
- A cluster Secret named `in-cluster` at another server.
- argocd-cm `cluster.inClusterEnabled` set to `"false"`.
- A project-scoped cluster Secret that only the live Argo CD has. Argo CD
  allows this destination. drydock cannot see the Secret, so the check
  requires a deferred diagnostic and no denial.

It also checks directory files that fail the source. Each such Application
lives in its own namespace and holds one file that fails the source. Argo CD
must report a `ComparisonError` condition, drydock must fail to render, and
both errors must name the same file. For a `.json` object without a `kind`,
the Argo CD controller error quotes the object instead of the file, so the
check matches the quoted object. Files that both sides skip, and files whose
text both sides decode first, render in the main manifest comparison.

This check is scoped to generated desired state. Live-only runtime behavior
such as Kubernetes defaulting, admission mutation, server-side diff, managed
fields, health aggregation, sync behavior, and controller state remains outside
drydock's runtime-offline boundary. It is not broad live authorization parity
and excludes RBAC/Casbin, sync windows, orphaning, signatures, sync
impersonation, and rendered-resource policy.

## Runtime Model

Supported:

- Direct `Application` CRs.
- Single-source and multi-source Applications.
- Desired-vs-desired manifest and image diffs.
- Render tests with per-Application `PASS`, `FAIL`, and `SKIPPED` status.
- Structured JSON and YAML outputs for list, test, diagnostic, and diff
  commands where supported.
- Stable diagnostic codes in CLI and public API output.
- Render concurrency with deterministic output ordering.
- Partial build results for embedding callers when some selected Applications
  fail to render.

Not reproduced offline:

- Kubernetes API defaulting.
- Admission mutation.
- Live Argo CD server-side diff.
- Live server-side apply field ownership prediction.
- Managed-fields ignores when ownership data exists only on the live cluster.
- Live Argo CD Application health aggregation.
- Live destination cluster existence checks.
- Sync window enforcement.
- Orphaned resource detection.
- Source integrity signature verification.
- Destination service account sync impersonation.
- Project-scoped cluster Secret enforcement when cluster Secret metadata is not
  present in the analyzed desired state.
- Full Argo CD RBAC/Casbin authorization simulation.

These live-state behaviors must stay explicit runtime boundaries, not silent
approximations. drydock may report deferred diagnostics when it can identify a
specific unevaluated policy decision without creating strict-mode noise for
valid runtime-only configuration.

## Applications And ApplicationSets

Supported:

- Direct Application discovery from repository manifests.
- Manifest YAML read the way kubectl and the repo-server read it
  (apimachinery's YAMLOrJSONDecoder with sigs.k8s.io/yaml, YAML 1.1), for
  discovered manifests, rendered directory, Helm, Kustomize, and plugin
  output, `elementsYaml`, and Git files params: unquoted `y`/`yes`/`on` and
  `n`/`no`/`off` are booleans, number and boolean map keys become strings,
  timestamps keep their text, a duplicate key keeps its last value, and text
  after `---` on a separator line is an error. Kustomize `helmCharts` output
  reaches kustomize as kustomize reads it (YAML 1.2). Error messages number
  documents as that splitter does, so in a file that opens with a comment
  header and `---`, the first object is `document 1`.
- Recursive rendered fleet discovery from desired output, including rendered
  `Application`, `ApplicationSet`, `AppProject`, and Argo CD settings objects.
  Static committed objects take precedence over default rendered fleet
  duplicates. drydock renders each parent with the tracking settings of the
  repository (`application.resourceTrackingMethod`,
  `application.instanceLabelKey`, and `installationID`), as the Argo CD
  controller does. So a discovered child Application carries the tracking
  metadata of its parent that Argo CD sets. Under label tracking, it has the
  instance label with the parent name and no `argocd.argoproj.io/tracking-id`
  annotation. `-l` selects on these labels. Earlier drydock releases always
  used annotation tracking for this render.
- Explicit `--discover-kustomize PATH` discovery from rendered local
  Kustomize entrypoints. Explicitly rendered Kustomize objects take precedence
  over committed duplicates because the path is the operator-selected Argo CD
  entrypoint. For the same reason, the path counts as the Argo CD install of
  this cluster: its rendered `argocd-cm`, `argocd-cmd-params-cm`, and
  `argocd-cmp-cm` in the `argocd` namespace replace the values files and
  settings ConfigMap files that its render reads, also when no Application
  installs Argo CD (see "Settings from the rendered Argo CD install"). An
  Application whose source renders the same path is not rendered again for
  its settings, because the `--discover-kustomize` render is that install.
- ApplicationSet Git directories, Git files, list, matrix, and merge
  generators.
- The Argo CD v3.6.1 OCI ApplicationSet generator (`generators[].oci`),
  top-level and nested in matrix/merge, with directories and files, `exclude`,
  `pathParamPrefix`, `values`, and the same path params as the Git generator.
  The artifact is acquired like an `oci://` Application source (`--repo-map`,
  `--offline`, the OCI cache, `--registry-config`, and `--oci-*` flags).
  Directory and file matching follow the v3.6.1 repo-server: hidden
  directories are skipped, file patterns are always doublestar (`.` means
  `**`), and files resolving outside the artifact are skipped.
  The directory walk never lists a symlink to a directory, but file patterns
  follow symlinks that stay inside the artifact. The Argo CD render parity
  smoke tests the generator against an in-cluster registry. Its fixtures use
  directories and files with include and exclude patterns, an empty file
  pattern, and hidden and symlinked directories. They read JSON and YAML
  param files, one of them a list of objects. They use `pathParamPrefix` and
  `values` in Go template and fasttemplate mode, and one of them nests the
  generator in a matrix with a list generator.
- ApplicationSet `templatePatch` rendering and strategic merge patching, with
  generated `spec.project` preserved.
- ApplicationSet list `elementsYaml`, including matrix-interpolated
  `elementsYaml`.
- Generator selectors and generator template overrides for supported
  generators. Selectors match the params flattened as Argo CD v3.6.1
  `flattenParameters` does (`applicationset/generators/generator_spec_processor.go`):
  maps and untyped lists are walked into dot-separated keys, but a `[]string`
  such as Git `path.segments` or pull-request `labels` is one key whose value
  is the Go print form, such as `[apps alpha]`. The render parity smoke checks a
  selector on `path.segments`.
- Nested matrix/merge combinations where the Argo CD v3 nested JSON API
  permits them.
- Explicit local YAML/JSON fixtures for cluster, clusterDecisionResource, SCM
  provider, pull-request, and plugin ApplicationSet generators. Cluster
  generator params omit the annotations Argo CD v3.6.1 reserves
  (`argocd.argoproj.io/refresh`, `refresh-timestamp`, `hydrate`,
  `hydrate-timestamp`, `tracking-id`, `installation-id`, and
  `kubectl.kubernetes.io/last-applied-configuration`). The Go-template
  `.metadata.annotations` key exists only when the fixture cluster has
  annotations; `.metadata.labels` is always present, as it is for Argo CD's
  secret-type-labelled cluster Secrets. The render parity smoke checks the
  hidden annotations in Go-template and fasttemplate modes against cluster
  Secrets applied to Argo CD.
- Argo CD v3.6.1 Go types for provider generator params in Go-template mode.
  Cluster `.metadata.annotations`, cluster `.metadata.labels`, and `.values`
  from every generator that has `values` are `map[string]string`. The cluster
  `flatList` `.clusters` param is a `[]map[string]any`. Pull-request `.labels`
  is a `[]string`. Sprig map functions such as `hasKey`, `keys`, `pick`,
  `omit`, `values`, and `dig` need a `map[string]any`, so they fail on these
  maps. drydock then warns `appset.template-render-failed` and generates no
  Applications for that ApplicationSet, as Argo CD does. The render parity
  smoke checks a `hasKey` call on cluster annotations.
- Argo CD v3.6.1 Go types for the SCM provider `repository_id` param. The
  fixture `repositoryID` text is converted per provider: `*int64` for GitHub,
  `int64` for GitLab, `int` for Gitea and Bitbucket Server, `uuid.UUID` for
  Azure DevOps, and `string` for Bitbucket Cloud and AWS CodeCommit
  (`applicationset/services/scm_provider/*.go`). An ID that does not parse as
  its provider type is an `appset.provider-fixture-invalid` error. An omitted ID
  is the zero value of the type. Go templates print the number or UUID;
  fasttemplate leaves `{{repository_id}}` literal for the integer and UUID
  providers, because Argo CD only replaces string params.
- Argo CD v3.6.1 Go-template functions, including `tpl`, in templates,
  `templatePatch`, generator `values`, and matrix/merge child interpolation.
- Argo CD v3.6.1 `clusterDecisionResource` `values`: the generator copies each
  value as a plain string and does not render it as a template
  (`applicationset/generators/duck_type.go`). `region: '{{region}}'` stays the
  literal `{{region}}`, and broken template syntax in a value is not an error.
  All other generators render their `values`. In Go-template mode, a decision
  field named `values` together with generator `values` fails the
  ApplicationSet (Argo CD panics in that case), so drydock warns
  `appset.template-render-failed` and generates no Applications for it.
- Argo CD v3.6.1 matrix and merge param type clashes in Go-template mode. Both
  generators merge child params with `mergo` and `WithOverride`
  (`applicationset/generators/matrix.go:95` and `merge.go:81`). If the merge
  writes a `map[string]any` entry into a `map[string]string` under the same
  key, it panics. A list element field named `values` is a `map[string]any`.
  Cluster generator `values` are a `map[string]string`. So a matrix with that
  list first and a cluster generator with `values` second panics. A merge
  generator with a
  cluster base and a list override that sets `values` panics too. The reverse
  order merges without error. Argo CD recovers the panic, sets no
  `ErrorOccurred` condition, and creates no Applications for the
  ApplicationSet. drydock warns `appset.template-render-failed`, names the
  ApplicationSet, and generates no Applications for it. The
  `clusterDecisionResource` panic above has the same outcome.
- Fail-closed fixture diagnostics for invalid provider fixtures, no provider
  matches, and unsupported provider filters.
- Warning diagnostics when a discovered ApplicationSet generates zero
  Applications; `--strict` promotes those diagnostics to errors.
- Argo CD Git and OCI files param decoding for empty content: empty,
  comment-only, and bare `---`/`...` files decode to one empty param set,
  `[]` decodes to zero param sets, and multi-document files decode only the
  first document.
- ApplicationSet template, `templatePatch`, generator `values`, and
  matrix/merge child interpolation execution errors scoped to the failing
  ApplicationSet as `appset.template-render-failed` warning diagnostics; that
  ApplicationSet contributes zero Applications and `--strict` promotes the
  warnings to errors, matching the Argo CD controller's `ErrorOccurred`
  condition behavior. Other generator evaluation errors, such as file IO and
  source acquisition, remain fatal.
- Argo CD v3.6.1 generated-Application name validation: a generated
  Application with an empty `metadata.name` (including a
  `generateName`-only `templatePatch`) and every generated Application that
  shares a duplicated name are omitted with an
  `appset.application-validation-failed` warning; the ApplicationSet's other
  Applications still generate, and `--strict` promotes the warning. The
  render parity smoke checks both cases against Argo CD, and it compares the
  whole drydock Application list with the Argo CD list.
- Argo CD v3.6.1 RollingSync handling for `spec.strategy.type: RollingSync`,
  gated on `applicationsetcontroller.enable.progressive.syncs` in
  `argocd-cmd-params-cm` (off by default; only `true` in any letter case
  turns it on). With it off, Argo CD ignores the strategy: the Applications
  generate as usual, and drydock gives one strict-exempt
  `appset.rollout-strategy-ignored` advisory. With it on, an empty step list
  warns as `appset.rollout-no-steps` and the ApplicationSet contributes no
  Applications. This is a recorded divergence. Argo CD returns before it
  creates, updates, or deletes any Application
  (`applicationset_controller.go:265-277`), so the Applications that already
  exist stay live and unchanged. drydock follows the template render failure
  rule instead and generates none. So a change that empties the step list
  shows every Application of that ApplicationSet as removed in
  `drydock diff`, and `--strict` fails on the warning. Invalid `matchExpressions` operators, Applications selected by several
  steps, invalid `maxUpdate` values, and steps that match no generated
  Application warn as `appset.invalid-rollout-config`, and the Applications
  still generate. If the step list is not empty, the Argo CD controller sets
  `spec.syncPolicy.automated.enabled: false` on each generated Application
  with an automated sync policy. drydock does the same. drydock does not
  model the sync operations that the controller then starts. `--strict`
  promotes `appset.rollout-no-steps`, but neither
  escalates nor fails on `appset.invalid-rollout-config`, because Argo CD's
  `InvalidRolloutConfig` condition never blocks Application create or update.
  The render parity smoke turns progressive syncs on and checks each finding
  class against the live ApplicationSet condition and Application list.
  In Argo CD v3.6.1, concurrent reconciles share one store for these
  findings, so an ApplicationSet can show the `InvalidRolloutConfig` message
  of another one. drydock reports each ApplicationSet's own findings. The
  smoke reconciles one ApplicationSet at a time to keep the message stable.

Not supported:

- Live Kubernetes, Argo CD, SCM provider, pull-request, cloud, or plugin
  service API calls for ApplicationSet generation.
- Unsupported generators without diagnostics.
- OCI generator `requeueAfterSeconds` and webhook refreshes; each drydock run
  resolves the generator `revision` once.

See `site/content/docs/applicationsets.md` for generator details and fixture
schema.

## Sources And Acquisition

Supported:

- Kustomize, directory, local Helm chart, and chart-only remote Helm sources.
- HTTP(S) and OCI Helm chart fetching by default for chart-only remote Helm
  sources.
- First-class Argo CD OCI artifact sources (every `repoURL: oci://...`, with
  `path:` defaulting to `.`): digest, exact tag, and semver-constraint
  revisions; extraction with Argo CD repo-server default guards (layer
  media-type allowlist, single content layer, at most 10 layers, 1G
  extracted-size cap); Helm inferred from a `Chart.yaml` at the content path
  with the source's `helm` block applied; `.argocd-source*.yaml` merged at
  the content path; digest-keyed image caching with offline tag/constraint
  records; and cache lifecycle support as source `oci`.
- Kustomize `helmCharts` rendered through the shared Go-library Helm path.
- Helm `$ref/...` external value files and file parameters from Git sources.
- HTTP(S) remote Helm value files through the remote-resource cache.
- Remote Kustomize HTTP(S) file refs and Git refs through the remote-resource
  cache.
- Default Git clone/fetch into the Git cache for unmapped path sources missing
  from the local tree.
- `--repo-map URL=PATH` path-source resolution for local external checkouts.
- Explicit Git HTTPS bearer/basic auth; Git SSH auth from an explicit key file
  or from the ssh-agent and `~/.ssh` identities.
- Explicit HTTP(S) Helm bearer/basic auth.
- Explicit HTTP(S) remote Kustomize bearer/basic auth.
- Explicit `--registry-config` (a Docker/Helm registry config file in
  `helm registry login` format) with per-registry credentials for OCI
  artifact sources and OCI Helm chart registries; for artifact sources the
  file is read and validated at the first `oci://` acquisition.
- Explicit OCI artifact registry basic auth and TLS material
  (`--oci-username`/`--oci-password`, `--oci-ca-file`,
  `--oci-client-cert-file`/`--oci-client-key-file`,
  `--oci-insecure-skip-verify`): the username/password pair is the global
  override for every OCI artifact registry in the run and takes precedence
  over `--registry-config`; the TLS flags are one global set.
- Cache event API/reporting for Git, Helm, OCI artifact, and remote Kustomize
  acquisition, with redacted targets and errors.
- Local cache lifecycle commands for recognized Git, chart, OCI artifact, and
  remote Kustomize cache layouts; render output entries are not listed,
  pruned, or deleted.

Important boundaries:

- `--offline` disables Git, Helm chart, OCI artifact, and remote Kustomize
  resource network fetching and requires cache hits, repo maps, local files,
  or local chart availability. Offline OCI artifact renders use digest
  passthrough plus the digest-keyed image cache; tags and semver constraints
  resolve from records captured on earlier online runs. An Application whose
  `oci://` source sets `chart:` was served from the chart cache by earlier
  releases and is served from the OCI cache now, so it needs one online run
  with `--oci-cache-dir` (`drydock build apps --path . --oci-cache-dir
  <dir>`) before `--offline` — or the pr-action's `offline` input — can
  render it; its old chart-cache entries orphan and `drydock cache prune
  --source chart` removes them, and `--refresh-charts` no longer applies to
  it.
- Argo CD OCI dispatch is followed: every source whose `repoURL` has the
  `oci://` scheme is an OCI artifact source, because the repo-server checks
  `IsOCI` before `IsHelm` and `spec.source.chart` never participates in the
  pull, the routing, override merging, or rendering. drydock pulls `repoURL`
  at `targetRevision`, merges `.argocd-source*.yaml` at the content path,
  and renders at `path` (default `.`), inferring Helm from a `Chart.yaml`
  there and applying the `helm` block. A source that sets `chart:` receives
  one advisory warning, `source.oci-chart-ignored`, naming both Argo-valid
  spellings; `--strict` neither escalates nor fails on it, because Argo CD
  renders the shape silently and the manifests are identical either way.
  `chart:` together with `path:` on one `oci://` source is valid — `path`
  selects within the artifact. The schemeless Helm-OCI shape
  (`repoURL: ghcr.io/org/charts` plus `chart:`, including nested chart
  names) is unchanged and keeps the Helm registry client and the chart
  cache; Kustomize `helmCharts` with `repo: oci://` and `Chart.yaml`
  `oci://` dependencies are untouched. Argo CD's API server
  (`argocd app create`) still classifies such a repository Helm-first and
  rejects the shape; drydock mirrors the controller, which renders it when
  applied declaratively.
- Recorded leniencies and gaps around that dispatch: `oci://` classification
  trims surrounding whitespace and matches the scheme case-insensitively,
  where Argo CD's `IsOCI` is an exact lowercase prefix check — a widening
  that only adds classification and never re-routes a URL Argo CD treats as
  OCI. A single-source `oci://` Application with neither `path:` nor
  `chart:` is an invalid spec upstream (`InvalidSpecError`) but renders the
  artifact root in drydock. The schemeless Helm-OCI chart flow resolves
  exact chart versions only, where the repo-server also resolves semver
  constraints against the registry tag list. The last two are recorded
  follow-ups.
- `$ref` value-file sources are Git-only, matching upstream: an OCI `$ref`
  source fails with a clear error.
- Private OCI registries are supported for OCI artifact sources through
  `--registry-config`, which supplies per-registry credentials from a
  Docker/Helm registry config file (plain `auths` entries, legacy
  `https://host/` keys, `credsStore` and `credHelpers`) for OCI artifact
  sources and OCI Helm chart registries alike, and through the explicit
  `--oci-username`/`--oci-password` pair, the global override presented to
  every OCI artifact registry in the run whenever either half is set. For
  artifact sources the file is validated lazily at the first `oci://`
  acquisition, never on runs without OCI artifact sources; hosts without an
  entry stay anonymous; an identity-token entry
  (`identitytoken`/`registrytoken`, which the OCI artifact client cannot
  present) and a credential helper that cannot run are errors naming the host
  and the remedy, never a silent anonymous pull; an auth-less file never
  reaches the OS keychain for artifact sources, unlike helm's loader for
  charts. The TLS configuration is one global set, and any TLS-implying
  `--oci-*` flag disables the loopback plain-HTTP default for the whole run.
  Credentials embedded in `oci://` repository URLs are rejected with a
  redacted error naming the flags, and artifact authentication failures
  (401/403) name both credential flags. Per-registry CLI credential maps are
  a recorded follow-up, as are CLI flags for the extraction media-type
  allowlist and size cap. The Argo CD render parity smoke covers the OCI
  source shapes against an in-cluster registry: an OCI artifact source, an
  OCI Helm chart source whose chart name is nested
  (`parity/nested/parity-nested-chart`) under a scheme-less `host:port`
  repository URL, and the `oci://` + `chart:` spelling of that same chart,
  applied declaratively and compared with drydock's `source.oci-chart-ignored`
  warning required on stderr.
- The `--oci-*` TLS flags (`--oci-ca-file`, `--oci-client-cert-file`,
  `--oci-client-key-file`, `--oci-insecure-skip-verify`) also apply to OCI
  Helm chart pulls, so a private chart registry behind a corporate CA is
  reachable. The bundle is handled differently per source kind: artifact
  registries replace the system pool (vendored Argo CD behavior), chart
  registries add the bundle to the system pool so public charts in the same
  run keep resolving. TLS material and `--registry-config` are shared by the
  two OCI flows; `--oci-username`/`--oci-password` apply to OCI artifact
  registries only, and the HTTP(S) Helm repository client is left untouched.
  `--oci-insecure-skip-verify` has no per-registry scoping: it disables
  certificate verification for every OCI artifact and OCI Helm chart registry
  in the run, including public ones, and any `--registry-config` or `--oci-*`
  credentials are then sent over an unverified connection.
- `--repo-map` takes precedence over local fallback and network fetching.
- Top-level `--repo` for Git ref diffs supports local repository paths only.
- Missing HTTP(S) and OCI Helm chart dependencies declared in `Chart.yaml` are
  acquired through drydock's native chart cache, without shelling out to
  `helm dependency build` or mutating the source checkout. Local `file://`,
  repository-alias, or otherwise unresolved dependencies must be available
  under `charts/` and fail closed when missing.
- Cached Helm charts never expire by default. Argo CD v3.6.1's repo-server
  re-pulls a cached chart archive older than its repo cache expiration (24h
  by default), so a republished chart version converges there; drydock needs
  `--refresh-charts`, or opts in with `--chart-cache-max-age DURATION` (public
  API `Config.ChartCacheMaxAge`). A positive max age refetches chart sources,
  Helm dependencies, and Kustomize `helmCharts` whose cache entry was fetched
  longer ago (fetch time, not last use; an entry with no recorded fetch time
  counts as expired, even after reuse), and persisted render outputs built from an expired or since
  refetched chart are never served. With `--offline` an expired chart is
  still used and reported by a `source.chart-cache-stale` warning, which
  `--strict` promotes.
- Recorded divergence from strict Argo CD v3.6.1 (unchanged since v3.5.x):
  drydock extracts fetched Helm chart archives itself and accepts only
  directory and regular-file entries with relative names under the chart
  root, so an archive holding a symlink, hard link, or other entry type, or
  an absolute entry name, fails to acquire. The repo-server's extractor
  (`util/io/files` `untar`) creates symlinks whose target stays inside the
  extraction directory, skips other entry types, and extracts absolute names
  relative to that directory. Charts built with `helm package` rarely contain
  such entries.
- Ambient Git credential helpers, ambient Helm registry config, and secret
  credential fields from discovered repository Secrets are not read. SSH
  identities are the exception: without `--git-ssh-key-file`, the agent and
  `~/.ssh` identities are used, with known_hosts verification. An explicit
  `--registry-config` file is read, including the `credsStore` or
  `credHelpers` entries it names; without it no Docker or Helm registry
  config and no OS keychain is consulted.
- Cache lifecycle commands are local filesystem operations only; they do not
  render, fetch, read credential flags, or retry failed acquisitions.

See `site/content/concepts/source-acquisition.md` for cache, auth, and remote
source details.

## Rendering

Supported:

- Argo CD resource tracking metadata injection after rendering and destination
  namespace normalization, without contacting a live application controller.
  The default tracking method is `annotation`, the default instance label key
  is `app.kubernetes.io/instance`, `annotation`, `label`, and
  `annotation+label` modes are supported, `installationID` is applied for
  annotation-based tracking, and CRDs are not stamped.
- Argo CD build-environment substitution (`$ARGOCD_APP_NAME`,
  `$ARGOCD_APP_NAMESPACE`, `$ARGOCD_APP_PROJECT_NAME`, `$ARGOCD_APP_SOURCE_*`)
  in Helm parameters, value-file names, and `fileParameters` paths, Kustomize
  images, labels, and `commonAnnotationsEnvsubst` annotations, and Jsonnet ext
  vars and TLAs, with
  the repo-server's values: `ARGOCD_APP_NAME` is the Application instance name
  (`<namespace>_<name>` for Applications outside the controller namespace;
  drydock assumes the Argo CD controller namespace is `argocd`) and
  `ARGOCD_APP_NAMESPACE` is `spec.destination.namespace`.
- Helm `parameters` and `fileParameters` applied the way Argo CD's
  `helm template` call applies them: every `--set`, then every `--set-string`
  (`forceString: true`), then every `--set-file`, keeping only the last entry
  per exact name within each kind. Names are used verbatim, an empty name sets
  nothing (its `fileParameters` file is still read), and an overridden
  `fileParameters` path is resolved but never read.
- Recorded divergence from strict Argo CD v3.6.1 (unchanged since v3.5.x):
  the repo-server passes the surviving entries of one kind in Go's randomized
  map iteration order, so overlapping names within one kind (such as `a` and
  `a.b`) can render differently from one Argo CD run to the next. drydock
  applies them in list order, which is one of the possible outcomes.
- Recorded divergence from strict Argo CD v3.6.1 (unchanged since v3.5.x):
  drydock does not follow a symlink at an overridden `fileParameters` path,
  so it accepts one whose target escapes the repository where the
  repo-server fails.
- `.argocd-source.yaml` and `.argocd-source-<app>.yaml` overrides, where
  `<app>` is the Application instance name like the repo-server's lookup: an
  Application outside the controller namespace reads
  `.argocd-source-<namespace>_<name>.yaml`, and a bare-name file is ignored.
- Recorded divergence from strict Argo CD v3.6.1 (unchanged since v3.5.x):
  `ARGOCD_APP_REVISION`, `ARGOCD_APP_REVISION_SHORT`, and
  `ARGOCD_APP_REVISION_SHORT_8` carry the spec `targetRevision` string; a
  repo-server reports the resolved revision (commit SHA for Git sources,
  chart version for Helm repository sources, manifest digest for OCI
  sources).
- Directory rendering honors `+argocd:skip-file-rendering`, walks hidden
  directories when `recurse` is enabled, renders Jsonnet through native Go
  libraries with extVars, TLAs, code mode, env substitution, and repo-relative
  libs, confines Jsonnet imports to the repository root as the Argo CD
  repo-server does from v3.5.4, and skips drydock cache metadata sidecars.
- Jsonnet `libs` resolve as in the Argo CD v3.6.1 repo-server
  (`ResolveFileOrDirectoryPath`). A lib is relative to the repository root,
  also when it starts with `/`. A lib that is itself a symlink is followed,
  and the root itself is allowed. A lib outside the repository fails with
  `file '<lib>' resolved to outside repository root`. A missing lib is
  allowed. Earlier drydock releases rejected an absolute lib and a
  symlinked lib directory, and trimmed spaces around a lib, which Argo CD
  keeps. drydock still rejects a lib in a `.git` directory.
- Directory file selection as the Argo CD v3.6.1 repo-server performs it: the
  built-in `.yaml`/`.yml`/`.json`/`.jsonnet` extension filter is
  case-sensitive (`deploy.YAML` is not rendered), `include` and `exclude`
  match the source-relative path with `github.com/gobwas/glob` v1.0.0
  semantics (`{**/,}crds/*.yaml` matches `crds/a.yaml` and `x/crds/a.yaml`).
- Directory file decoding as the Argo CD v3.6.1 repo-server performs it: file
  text is read as UTF-8 unless a byte order mark says UTF-16LE or UTF-16BE,
  the byte order mark is dropped, and invalid UTF-8 becomes U+FFFD, before
  the `+argocd:skip-file-rendering` check and every decoder. A file ending in
  `.json` must decode as one JSON object with a `kind` followed only by
  whitespace, so an empty file, YAML content, a JSON array, a syntax error,
  or several concatenated objects fail the source. Every other file except
  a `.jsonnet` file is a YAML/JSON stream. The stream stops at a syntax
  error or at a non-empty document that is not an object with a non-empty
  `kind` (values-like YAML, `{}`, and `kind: ""` included). A document needs
  a `kind` but not an `apiVersion`, so a document with a `kind` and no
  `apiVersion` renders without one, as in Argo CD. When the stream stops,
  the file fails the source if an object decoded before it or if its
  content holds all of `apiVersion:`, `kind:`, and `metadata:`. Otherwise the
  whole file is skipped as unrelated YAML. Earlier drydock releases failed
  the source on a document with exactly one of `apiVersion` and `kind`. The
  render parity smoke covers the byte order mark and invalid UTF-8 cases,
  the skipped files, and each failing case above.
- Jsonnet output in a directory source is decoded as the repo-server
  decodes it: as an array of objects, or else as one object. Each object
  needs a non-empty `kind`, so an object without one fails the source with
  `failed to unmarshal generated json`. A `null` array element adds nothing.
  The objects then go through the same list split and `kind` check as the
  objects of every other file. A `kind: List` from Jsonnet renders its items,
  and a List whose `items` field is `null` renders nothing. Earlier drydock
  releases rendered the List itself and skipped an object without a `kind`.
- Directory sources fail in the stage order of Argo CD v3.6.1. The
  repo-server first reads every candidate file, drops files with the
  `+argocd:skip-file-rendering` marker, and checks the size limit. Then it
  decodes every file. Then it splits each list into its items and sets the
  tracking metadata on each manifest. Last, the controller fails on a
  manifest without a `kind`. So a file that fails an earlier stage is
  reported, even when a file before it fails a later stage. For example, a
  `.json` file that does not decode is reported before a list item that is
  not an object in an earlier file, and a file that cannot be read is
  reported before a decode error in an earlier file.
- The files of one directory source must not be larger in sum than the
  repo-server limit, as in Argo CD v3.6.1. The limit is
  `reposerver.max.combined.directory.manifests.size` in
  `argocd-cmd-params-cm`, a Kubernetes quantity, and its default is `10M`
  (10000000 bytes). `0` turns the limit off. The sum counts the raw size of
  each file that passes the extension filter, `include`, and `exclude`. It
  does not count Jsonnet files or files with the skip marker. Over the limit,
  Argo CD fails with `exceeded max combined manifest file size`, and drydock
  fails with an error that names the file that went over and the limit.
  drydock reads the setting from an `argocd-cmd-params-cm` that it
  discovers in the repository or in rendered Application output. When the
  repository installs Argo CD from the argo-cd Helm chart, that includes the
  `argocd-cmd-params-cm` that the chart makes from `configs.params` (see
  "Discovery of Argo CD settings"). Otherwise drydock uses `10M`. drydock
  does not read the repo-server Deployment. So drydock does not see a limit
  that the chart sets only through `repoServer.extraArgs`
  (`--max-combined-directory-manifests-size`) or as a literal value in
  `repoServer.env`. An empty value means `10M`, and a value that is not a Kubernetes quantity,
  spaces around it included, fails the build, as it stops the repo-server at
  startup. Earlier drydock releases had no limit.
- drydock reads `applicationsetcontroller.enable.progressive.syncs` the same
  way: from a discovered or rendered `argocd-cmd-params-cm` only. It does not
  read the ApplicationSet controller Deployment. The chart's
  `configs.params` sets the key in `argocd-cmd-params-cm`, which drydock
  reads. The chart can also set the flag on the Deployment, through
  `applicationSet.extraArgs` (`--enable-progressive-syncs`). It can set it
  through `applicationSet.extraEnv` too
  (`ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS`). drydock does
  not see a value set on the Deployment, and treats progressive syncs as off.
- An `argocd-cmd-params-cm` from a rendered Application can set the flag
  late, after drydock expanded the ApplicationSets with the earlier value.
  Then drydock runs discovery a second time with the final value, but only
  when it found an ApplicationSet with a RollingSync strategy. A first run
  that failed after its first ApplicationSet expansion can also lead to a
  second run. For that, the partial result of the failed run must already
  hold the rendered install's settings. A committed app-of-apps can
  render the install Application from a Helm chart, one level deeper. If the
  first run fails at or before that level, drydock returns its error. With
  the flag committed, the same repository succeeds. The second run is the
  last. If that run ends with the other
  value again, drydock keeps its result and warns
  `settings.progressive-syncs-cycle`, which `--strict` fails on. Here is an
  example. An ApplicationSet has a RollingSync strategy with an empty steps
  list. With the flag off, it generates an Application. That Application
  renders the `argocd-cmd-params-cm` that turns the flag on. After such a
  cycle, the settings that drydock reports show the final value of the
  second run, which is not the value its generation used. The warning names
  both values.
- A directory source follows file symlinks as the Argo CD v3.6.1
  repo-server does (`getPotentiallyValidManifestFile`). The walk never
  enters a symlinked directory. The extension filter, `include`, and
  `exclude` match the name and path of the symlink itself. A symlink whose
  target is missing, or is not a regular file, such as a directory, is
  skipped. A target outside the repository fails the source with `illegal
  filepath in symlink at`, also when `exclude` matches the symlink, and a
  symlink that cannot be resolved, such as a loop, fails with `failed to
  evaluate symlink at`. Otherwise the target is read, so the skip marker and
  the decoders see the target, the target size counts toward the limit, and
  a `.jsonnet` symlink is evaluated through the symlink path. A relative
  Jsonnet import in such a file resolves next to the symlink, and an import
  can go through an in-repository symlink, as in the repo-server's confined
  importer. An import that resolves outside the repository fails with
  `resolves outside repository root`. drydock also fails a source whose
  symlink or Jsonnet import resolves into a `.git` directory, which the
  repo-server would read from its own clone.
- An Argo CD checkout is never a symlink. When a `--path` root, a diff root,
  or a `--repo-map` root is itself a symlink, drydock resolves it once at
  the start of the run and reads the tree it points to everywhere:
  discovery, rendering, the out-of-bounds check, and the cache keys. Earlier
  releases found no Applications under such a `--path` root, and rendered
  nothing for a directory source at path `.` under such a `--repo-map` root.
  A root with a symlink only above it, such as a macOS temporary directory,
  keeps its spelling. Paths in output stay relative to the root.
- The walk skips `.git` directories. Git cannot track a nested `.git`
  directory, so no Argo CD checkout holds one. The repo-server walks its own
  root `.git` directory when a source at the repository root recurses. A
  fresh clone keeps no file there that the default extension filter passes,
  but with `disableExtensionFilter` Argo CD reads that clone metadata, which
  drydock cannot reproduce.
- Out-of-bounds symlinks fail every Application that renders from the tree
  that holds one, as in Argo CD v3.6.1 (`CheckOutOfBoundsSymlinks`,
  `util/app/path/path.go`). A symlink is out of bounds when its target is
  absolute, or when any step of its target leaves the tree. The check runs
  before the source renders, on the Git repository of a source with the root
  `.git` skipped (`repository contains out-of-bounds symlinks. file:
  <path>`), on each Git repository that a Helm `$ref` value file or file
  parameter reads, on an OCI artifact (`oci image contains out-of-bounds
  symlinks. file: <path>`), and on a chart of a Helm repository (`chart
  contains out-of-bounds symlinks. file: <path>`). The path is the first
  such symlink in walk order. For a source with a path, drydock first
  reports a `$ref` of the spec that names no source, as Argo CD does. It
  then checks the source's own tree, then each `$ref` repository that the `helm.valueFiles`
  and `helm.fileParameters` of the spec read, and only then reads
  `.argocd-source.yaml`, as the repo-server does. So an override that
  replaces the value files does not skip a `$ref` check. For a chart of a
  Helm repository, drydock checks the chart first and then its `$ref`
  repositories. A Git source that only sets `ref` is not checked, as in Argo
  CD. An `oci://` source that only sets `ref` is acquired and checked,
  because the repo-server extracts it, but nothing renders from it. When the
  check is off, drydock does not acquire it. Setting
  `reposerver.allow.oob.symlinks` to `true` in `argocd-cmd-params-cm` turns
  the check off, as the repo-server flag `--allow-oob-symlinks` does.
  drydock reads that key only from an `argocd-cmd-params-cm` that it
  discovers, as for the size limit.
- In a local Git worktree, the out-of-bounds check leaves out every path that
  Git ignores and does not track, such as a Python virtual environment or a
  Nix build result. No commit, and so no Argo CD checkout, can hold such a
  path. This applies only to a tree the user names: the `--path` root, a
  diff path, or a `--repo-map` path. drydock's own snapshots of a Git ref
  and its acquired repositories hold committed content only, so they are
  checked whole, also when the temporary directory is inside some other
  worktree. The root can be the top level of the worktree or a directory
  in it that the worktree tracks.
- When the `git` command is on `PATH`, drydock asks Git for the top level,
  the subdirectory of the root, and the ignored set (`git ls-files --others
  --ignored --exclude-standard --directory`), so every ignore rule that Git
  applies counts, in every repository format that Git reads. The calls drop
  the Git variables that point at another repository, index, or worktree,
  such as `GIT_DIR` and `GIT_INDEX_FILE` in a Git hook, and each stops after
  20 seconds.
- Without `git`, or when a call fails or stops, drydock reads the index, the
  `.gitignore` files, and `info/exclude` (of the common Git directory for a
  linked worktree) itself. It never skips more than Git, and it can skip
  less:
  - It leaves out `core.excludesFile` and the default user ignore file.
  - It does not read a `.gitignore` file that is a symlink, or one in a
    nested repository, as Git does not. It does not read an `info/exclude`
    that is a symlink either, which Git reads.
  - It decides file by file and never skips a whole directory, because a
    later `!` pattern can bring back a file below an ignored directory.
  - It reads `[!...]` in a pattern as Git does, as a negated set, and it
    requires a path part after a final `/**`, as Git does, so `foo/**`
    does not match `foo` itself.
  - It checks every path at or under a submodule, because the parent index
    does not list submodule files and Argo CD checks out submodules.
- A `.git` file at the worktree root, as in a linked worktree, is skipped
  like a `.git` directory. A root that is not in a Git worktree, that the
  worktree does not track, or that Git ignores itself, is checked whole.
- The persistent render cache key and changed-only selection of a directory
  source cover the files outside its path that its symlinks reach, and the
  files that its Jsonnet files import, directly or through other imports,
  also inside a Jsonnet library. When the source has a Jsonnet file, they
  also cover each library path as written and every symlink on the way to
  it, so moving a library is a change to the source. A source without a
  Jsonnet file never resolves its libraries, so they do not count. drydock
  reads the import paths from the parsed Jsonnet files. It does not follow
  imports in `extVars` or `tlas` code, so a change to a file
  that only such code imports does not change the key or select the
  Application. The import scan has a budget of 1000 files and 10 MB per
  source. The file limit is there because a symlink to a directory above a
  file can give that file endlessly many import paths. A single Jsonnet
  file over 10 MB also runs the budget out, because the scan does not read
  it but the renderer evaluates it. When the budget runs out, or the scan
  fails, for example on a directory it cannot read, the source gets no
  persistent cache entry and renders as usual, and changed-only renders it
  on every change, with the strict-exempt warning
  `diff.changed-only-jsonnet-scan`. A changed path that no Application owns
  still renders every Application.
- Recorded divergence from strict Argo CD v3.6.1: drydock never extracts an
  OCI artifact that holds an out-of-bounds symlink, also when
  `reposerver.allow.oob.symlinks` is `true`. Helm charts and Kustomize
  graphs still reject every symlink in drydock (see the safety boundary
  below), where the repo-server follows in-tree symlinks.
- A directory file that is not a regular file, such as a named pipe or a
  socket, is skipped, as in Argo CD. Earlier drydock releases tried to read
  it, and a named pipe stopped the render.
- A manifest whose tracking metadata cannot be set, such as one whose
  `metadata.annotations` is a string, fails the source in the repo-server
  stage with `failed to set app instance tracking info on manifest`, as in
  Argo CD. drydock runs the upstream tracking code with the configured
  tracking method, so annotation tracking reads the annotations, label
  tracking reads the labels, and annotation and label tracking reads both.
  A list item takes the same check. drydock names the file and the item.
- An object counts as having a `kind` only when its `apiVersion` also parses
  as a group version, as in the Unstructured decode that Argo CD v3.6.1 uses.
  An `apiVersion` with two or more slashes, such as the typo `apps/v1/`, does
  not parse, so the object has no `kind` for Argo CD. In a YAML file such a
  document stops the stream. A `.json` object or a list item with it fails
  in the controller stage with `Object 'Kind' is missing`. drydock reports
  the same failure and names the `apiVersion`. Earlier drydock releases
  rendered such a YAML document or list item.
- An object with a `kind` whose `items` field is a string, a number, or a
  map is not a list, so it renders unchanged, as in Argo CD. This holds for
  every source type. Earlier drydock releases failed it with
  `/items is not a list`.
- A `.json` file that holds JSON `null` or an object without a `kind`, such
  as `package.json`, `tsconfig.json`, or `{}`, fails the source. The Argo CD
  v3.6.1 repo-server accepts such content when it also decodes as an OCI
  image manifest, and emits it as a manifest without a `kind`. The controller
  then fails the Application with a `ComparisonError` that says
  `Object 'Kind' is missing`. drydock reports this failure only when no file
  fails an earlier stage, as Argo CD does. Content that does not decode as an
  OCI image manifest fails the source in the repo-server. An object without
  a `kind` whose `items` field is an array is a list, and one whose `items`
  field is `null` renders nothing, as in Argo CD. Earlier drydock releases
  rendered nothing for such a file and did not fail.
- A list item without a `kind` fails the source, in a YAML, JSON, or
  Jsonnet file of a directory source and in Helm, Kustomize, or plugin
  output, whether or not the list has a `kind`
  of its own, such as `{"items":[{"name":"web"}]}` or a `kind: List` whose
  item has no `kind`. The Argo CD v3.6.1 repo-server splits every list into
  its items and emits each item, and the controller fails on an item without
  a `kind` with the same `Object 'Kind' is missing` error. drydock reports
  this failure only when no file fails an earlier stage. Earlier drydock
  releases skipped such an item when it also had no `apiVersion`.
- Helm, Kustomize, and config management plugin output is read as the
  Argo CD v3.6.1 repo-server reads it, with gitops-engine `kube.SplitYAML`.
  Argo CD first removes one line break from the end of the command output,
  and drydock does the same. The YAML reader ends the last line with a line
  break again, so this changes a value only when the last document ends with
  a `|+` block scalar and at least one blank line: `x` with a blank line
  after it gives "x\n", not "x\n\n". Kustomize `helmCharts` keep the line
  break, because kustomize reads the output of Helm itself. The
  argocd-vault-plugin compatibility paths also keep it, because AVP reads
  the Helm or Kustomize output and prints each object followed by `---`, so
  Argo CD removes only the line break after the last `---`. The second reading of
  each document by `kube.SplitYAML` has rules of its own, and drydock runs
  it too. For example, NEL (U+0085) folds a value and fails a key, and a
  key over 1022 characters after JSON escapes fails. The whole output is
  split into documents first. An empty document, a
  document with only comments, and a `null` document add nothing. Then each
  document is decoded into an object that must have a `kind`. A document
  that is not an object fails the source. A document without a `kind` fails
  the source with `Object 'Kind' is missing`, also an empty object `{}` and
  an object whose `apiVersion` does not parse, such as `apps/v1/`. A
  document that does not parse is reported before an earlier document
  without a `kind`. Then lists are split, a `null` list adds nothing, and the
  tracking metadata is set, as for a directory source. Last, a list item
  without a `kind` fails the source in the controller stage. drydock names
  the template file, the `kustomization.yaml` file, or the plugin output,
  and the document index. Earlier drydock releases skipped an empty object,
  rendered a document without a `kind`, and did not fail this output on
  tracking metadata that cannot be set. The render parity smoke
  covers a Helm template without a `kind`, a Helm `{}`, a Helm and a
  Kustomize `apps/v1/` typo, a Helm List item without a `kind`, and a
  plugin `{}`.
- Helm output is read as `helm template` (Helm v4.3.1, the version that
  Argo CD v3.6.1 bundles) writes it. Each file whose name ends in
  `NOTES.txt` is dropped. Each template document is a hook or not by the
  `helm.sh/hook` annotation of the document itself, so a `kind: List` is
  read by its own annotations and not by the annotations of its items. A
  document whose `helm.sh/hook` value names a hook event that Helm does not
  know, such as `crd-install` or `test-failure`, or is empty, is dropped,
  with a Helm log line `skipping unknown hooks` on stderr. With
  `spec.source.helm.skipTests`, a document whose hook events include `test`
  or `test-success` is dropped as a whole, a List with its items. With
  Kustomize `helmCharts` `skipHooks`, every hook document is dropped. A
  template document whose head Helm cannot read, such as annotations that
  are not strings, fails the source with `YAML parse error on <file>`, as
  `helm template` does. That error keeps the Helm text but not the manifest
  content that the YAML error can copy, such as a map key. drydock emits
  the manifests in the order of `helm template` output: the CRDs, then the
  other documents in Helm's kind order, then the hooks. A List comes after
  every kind that Helm knows. So when several documents fail, drydock
  reports the document that Argo CD reports. And when a resource repeats,
  drydock keeps the copy that Argo CD keeps, the last one in that order. For
  example, a List item wins over a ConfigMap with the same name in a later
  file, and a hook copy wins over a regular copy. `helm template` trims the
  white space at the end of the last document before the hooks, so a `|+`
  block scalar at the end of that document loses its trailing blank lines.
  drydock trims the same document. Earlier drydock releases emitted the
  manifests by file name, did not trim, dropped only files named
  `NOTES.txt`, checked the hook annotation of each list item, kept documents
  with unknown hook events, and kept a test hook List under `skipTests`.
- Directory `disableExtensionFilter`: the extension filter is skipped and
  `include`/`exclude` become the only filters, so custom extensions such as
  `*.yaml.sealed` render. A `.jsonnet` suffix is still evaluated as Jsonnet, a
  `.json` suffix still follows the single-object JSON rule, and every other
  file, such as a README or `.libsonnet` library that `include` admits, is
  read as a YAML/JSON stream under the skip-or-fail rule above. Enabling it
  with neither `include` nor `exclude` renders every file and emits the
  `directory.extension-filter-disabled` warning, where the repo-server logs a
  warning. Upstream calls this a valid configuration, so `--strict` keeps the
  warning advisory.
- Local Kustomize rendering with supported `kustomize.buildOptions`:
  `--enable-helm`, `--helm-api-versions`,
  `--load-restrictor=LoadRestrictionsRootOnly|LoadRestrictionsNone`,
  `--enable-alpha-plugins`, and `--enable-exec`. The latter two are accepted
  without error — Argo CD sets them repo-wide in `argocd-cm`, so rejecting
  the option would fail every app in such repositories, including ones with no
  generators. They do NOT enable arbitrary exec: drydock never executes
  generator commands.
  Kustomize `helmCharts` render through drydock's Go-library Helm path, so
  chart inflation does not require an external Kustomize binary or CLI
  `--enable-helm`.
- Kustomize Helm `valuesFile` and `additionalValuesFiles` entries outside the
  kustomization directory when the resolved file remains inside the repository
  root.
- Kustomize Helm remote HTTP(S) value files through the same remote-resource
  cache path used by Helm sources.
- Kustomize `ignoreMissingComponents` as in Argo CD v3.6.1: each
  `kustomize.components` entry is joined with the source path and kept only
  when that directory holds a regular `kustomization.yaml`,
  `kustomization.yml`, or `Kustomization`, looked up inside the repository
  root with in-repository symlinks followed. Missing directories, directories
  without a kustomization file, remote refs, and refs escaping the repository
  are skipped without reading outside it; an absolute ref is joined under the
  source path. A skipped in-repository directory stays a persistent render
  cache input, so adding a kustomization file there invalidates the entry.
- Kustomize `ignoreMissingComponents` adds only the first valid component, as
  in Argo CD v3.6.1. `util/kustomize/kustomize.go:345-372` leaves its
  component loop with `break component` (line 366) after the first component
  that has a kustomization file. Invalid components before it are skipped.
  Every component after it is dropped: the repo-server never reads, checks,
  or logs it. drydock renders the same. It does not read, validate, or copy a
  dropped component, so a dropped component with invalid YAML, a resource
  that escapes the repository, or an in-repository symlink does not fail the
  render.
- For the dropped components, drydock emits the
  `kustomize.ignore-missing-components-dropped` warning. Argo CD has no such
  check. To build the warning, drydock looks in the original repository for
  a regular `kustomization.yaml`, `kustomization.yml`, or `Kustomization` in
  each dropped directory. It looks only inside the repository root, it
  follows in-repository symlinks but not symlinks that leave the repository,
  and it does not look at remote refs, refs that escape the repository, or
  refs inside `.git`. The warning names each dropped directory that has a
  kustomization file once. drydock compares directories by the cleaned
  repository-relative path that Argo CD resolves with
  `filepath.Rel(repoRoot, filepath.Join(path, c))`
  (`util/kustomize/kustomize.go:357`). So `../c/b` and `../c/b/` are named
  once, and an entry that resolves to the kept component, such as `../c/a`
  repeated or written as `../c//a`, is not named, because no resources are
  missing for it. An in-repository symlink to the kept component has a
  different path, so the warning names it. Argo CD drops it too. An
  absolute ref after the kept component is joined under the source path, as
  Argo CD does, and can be named. The warning is not
  advisory: `--strict` makes it an error, because resources that the
  components list names are missing from the render.
- Without `ignoreMissingComponents`, every component is added. We believe the
  `ignoreMissingComponents` behavior above is an upstream bug:
  argoproj/argo-cd#26576 (merge `d631fcdfb`) turned an earlier `continue`
  into `break component`. drydock mirrors it until upstream fixes it, and
  then follows the fix.
- For the persistent render cache, the three kustomization file names in
  each dropped in-repository directory are optional inputs: the key records
  whether each file exists and its bytes, so creating, deleting, or changing
  one invalidates the entry. When a dropped ref names a file, that path is
  the input instead. The rest of a dropped component is not an input.
  drydock cannot record these inputs in these cases, so that Application
  renders without the persistent cache. The render itself still succeeds,
  as in Argo CD.
  - A dropped directory is reached through an in-repository symlink.
  - A dropped kustomization file is a symlink.
  - A dropped ref is a symlink that points outside the repository.
  - A dropped ref passes through a regular file, such as
    `../components/f/sub` when `f` is a file.
- Recorded divergence from Argo CD v3.6.1: drydock rejects an absolute
  component ref that has a kustomization file when it is the component that
  would be added (the first valid one), because drydock never reads absolute
  paths. An absolute ref after the first valid component is dropped, as in
  Argo CD.
- Remote Kustomize refs in `resources`, `bases`, `components`,
  `patches.path`, `patchesJson6902.path`, non-inline
  `patchesStrategicMerge`, `generators`, `transformers`, `validators`,
  `configurations`, `crds`, `openapi.path`, `replacements.path`, and generator
  file/env refs.
- Kustomize root Git remote refs such as
  `https://github.com/org/repo?ref=v1` when they resolve to a repository-root
  Kustomization directory.
- Discovered safe Kustomize build CMP definitions interpreted by drydock's
  native Kustomize renderer.
- Bounded warning diagnostics when a discovered sidecar CMP static
  `discover.fileName` or `discover.find.glob` rule matches a native-rendered
  source. drydock does not execute `discover.find.command`.
- Native AVP compatibility for explicit `argocd-vault-plugin` sources and
  discovered simple AVP CMP aliases. Placeholders are matched with AVP's own
  regexes (inline `<path:PATH#KEY>` tokens without the
  `avp.kubernetes.io/path` annotation, any `<...>` span on a line once the
  annotation key is present), the walk visits every string in the object but
  does not descend into a list nested in a list, and `kind: Secret` values
  that decode as base64 to placeholder text are substituted in the decoded
  text and re-encoded, as AVP does. `avp.kubernetes.io/ignore: "true"` skips
  the object, items of a `kind: List` take their kind and annotations from
  the List, and a YAML-null annotation value hides every annotation on the
  object, all as AVP v1.18.1 behaves.
- KSOPS kustomize generator compatibility under `--enable-ksops-compat`. When
  the mode is active, `apiVersion: viaduct.ai/v1 / kind: ksops` generator
  entries are rendered as deterministic placeholder manifests without
  decryption: SOPS-encrypted file structure and key names are preserved; every
  encrypted value is replaced by a deterministic placeholder
  (`drydock-ksops-redacted-<12hex>` marker). Secret `data:`/`binaryData:` and
  ConfigMap `binaryData:` values are valid base64 of the placeholder;
  everywhere else — including ConfigMap `data:` — the plain grep-able marker
  is emitted. The secret
  VALUES are never decrypted and no
  decryption key, KMS network call, or exec is involved.
  Builtin generator configs (`apiVersion: builtin`) are left for krusty and
  render successfully with or without the flag; the persistent render cache
  digests the files builtin configs reference. Exec transformers, exec
  validators, container KRM functions, and other non-KSOPS generators remain
  unsupported and fail closed. krusty's own errors surface for unsupported
  transformer and validator kinds.
  KSOPS manifests using `secretFrom`, `binaryFiles`, `envs`, or non-YAML sops
  targets fail closed with explicit unsupported diagnostics. A KSOPS generator
  encountered without `--enable-ksops-compat` fails the source with an
  actionable error naming the flag.
  Important: value-only sops rotations (changing only an `ENC[...]` ciphertext
  without adding, removing, or renaming keys) render identically under
  `--enable-ksops-compat`. The placeholder derives from key identity, not from
  ciphertext, so a rotation produces no diff in drydock output even though the
  live secret changes. "No diff" does not mean "no change" for rotated values.
- Trusted drydock plugin policy entries for native plugin overrides and
  explicitly enabled exec/container CMP compatibility.
- Trusted plugin policy bootstrap entrypoints for plugin-rendered Argo CD
  bootstrap objects.
- Trusted plugin policy `configManagementPlugin` seeds for static
  `discover.fileName` / `discover.find.glob` metadata and optional
  `generate.command` / `generate.args` compatibility metadata. Unnamed
  Application plugin sources may match these trusted static rules.
- Trusted exec/container plugin policies that gate Application plugin
  parameters through `parameters.allow`, including string argv substitution and
  constrained path parameters.
- The Argo CD build environment (`ARGOCD_APP_*`, `KUBE_VERSION`,
  `KUBE_API_VERSIONS`) and an always-present `ARGOCD_APP_PARAMETERS` for
  trusted exec/container plugins, in the repo-server's order. `env.allow`
  entries naming those variables are ignored with a warning.
- Application `spec.source.plugin.env` for trusted exec/container plugins when
  allowlisted by `applicationEnv.allow`, delivered as `ARGOCD_ENV_<name>` after
  build-environment substitution in the repo-server's order and position. Host
  `env.allow` entries naming `ARGOCD_ENV_*` are ignored with a warning.
- That config management plugin environment contract is pinned live: the Argo
  CD render parity smoke renders one fixture Application through a repo-server
  CMP sidecar and through a trusted `engine: exec` policy and compares the
  emitted values.
- Config management plugin source detection with fail-closed diagnostics in
  the CLI and default Go client.
- Injectable in-process plugin renderers, named plugin registry dispatch, and
  plugin timeout controls for embedding callers. Public plugin requests include
  source metadata, `$ref` roots and source metadata, kube version, and API
  versions.

Documented runtime-offline safety boundary:

- Directory rendering rejects symlinked source path components, even when
  an Argo CD repo-server could render through an in-repository symlink.
- Under Kustomize `ignoreMissingComponents`, a component whose kustomization
  file is an in-repository symlink counts as present, as in Argo CD v3.6.1,
  and then fails drydock's symlinked graph entry check where the repo-server
  renders it.

Not supported:

- Arbitrary CLI config management plugin execution outside trusted
  `engine: exec` or `engine: container` policy plus `--enable-plugins`.
- Shellout plugin adapters or plugin command execution by default.
- Argo CD repo-server sidecar plugin discovery.
- Ambient plugin configuration or environment loading.
- Untrusted CMP descriptor execution, unallowlisted Application plugin
  parameters or env (`parameters.allow` / `applicationEnv.allow`), or
  Application plugin env for policy-backed native engines.
- Plugin credential injection.

See `site/content/docs/plugin-policy/` for trusted policy provenance, supported
engines, schema, and command security controls.

## Diff, Images, And Output

Supported:

- `diff apps` desired-vs-desired manifest diffs.
- `diff app` desired-vs-desired diffs for one Application, including
  add/delete diffs when the Application exists on only one side.
- `diff apps`, `diff app`, and `diff images` against local Git refs through
  temporary local snapshots, without `git` shellouts or checkout mutation.
- Git-style ANSI color for text diff output with
  `--color=auto|always|never`; JSON and YAML payloads remain plain.
- `diff apps` and `diff app` `-o dyff` structural reports rendered in-process
  by the dyff library, and `--markdown-diff-style dyff` to embed dyff's
  GitHub-style report in markdown output.
- Changed-only Application selection with safe render-all fallback and strict
  failure mode.
- Default diff ignores for common Helm chart/version labels and pod-template
  `checksum/*` annotations.
- `--show-ignored-fields` to include drydock-default ignored fields while
  keeping Argo CD diff customizations active.
- `--strip-attr KEY` normalization for metadata label and annotation keys.
- Application-level `spec.ignoreDifferences[]` `jsonPointers`,
  `jqPathExpressions`, and `managedFieldsManagers`.
- Global `resource.customizations.ignoreDifferences.*`,
  `knownTypeFields.*`, and selected compare options.
- The `resource.customizations` map, built as Argo CD v3.6.1
  `SettingsManager.GetResourceOverrides` builds it
  (`util/settings/settings.go:1127-1300`). drydock first reads the
  `resource.customizations` block with `sigs.k8s.io/yaml`, so field names
  match in any case. Block keys stay as written, so the block key `all` is
  not `*/*`. Then each split key `resource.customizations.<type>.<suffix>`
  sets one field of one key. The types are `health`, `useOpenLibs`,
  `actions`, `ignoreDifferences`, `ignoreResourceUpdates`, and
  `knownTypeFields`. A split key replaces its field and keeps the other
  fields of the key. The split key wins over the block. An empty value also
  replaces the field. For example, the block key `/Service` sets
  `managedFieldsManagers: [a]`, and
  `resource.customizations.ignoreDifferences._Service` sets
  `managedFieldsManagers: [b]`. The result is `[b]`.
- Split key suffixes as Argo CD reads them (`convertToOverrideKey`).
  drydock splits the suffix on `_`. Two parts become `group/kind`, and an
  empty part stays empty. So `_Service` is `/Service`, `Service_` is `Service/`, and
  `_` is `/`. The last two keys match no resource. A suffix with no `_` stays
  as written, dots included. The suffix `all` becomes `*/*`.
- Argo CD fails on these `resource.customizations` values, and drydock
  reports each as an error:
  - A block that does not decode.
  - A block entry with no value.
  - A split key suffix that is empty or has more than one `_`.
  - A split key with an unknown type.
  - A split key value that does not decode.

  Argo CD panics on a block entry with no value. The
  `useOpenLibs` value must be exactly a Go boolean such as `true`. Argo CD
  does not remove spaces or a final new line, and it rejects an empty value.
  After any of these errors, Argo CD uses no overrides. drydock reports an
  error diagnostic and keeps no resource customizations from that ConfigMap.
- Argo CD checks the split key prefix `resource.customizations` with no
  final dot. So `resource.customizationsX.health.Service` is also a split
  key. Argo CD reads the split keys in Go map order, which is not defined.
  If two split keys set the same field of the same key, the order changes
  the result. Otherwise it does not. With valid ConfigMap keys, only this
  prefix rule can cause that. drydock reads the split keys in sorted order,
  keeps the last value, and reports a warning.
- Discovery of Argo CD settings. drydock finds the `argocd-cm` ConfigMap by
  its name, as Argo CD does. drydock finds an Argo CD Helm values file by
  the keys under `configs.cm`. Every `argocd-cm` key that drydock reads
  counts, and so does the chart key `resourceExclusionsAdditional`. A key
  that starts with `resource.customizations` counts, with or without a dot
  after it, because Argo CD reads both forms.
- Settings from the rendered Argo CD install. Argo CD reads its settings
  from the `argocd-cm`, `argocd-cmd-params-cm`, and `argocd-cmp-cm`
  ConfigMaps in the cluster. When the repository installs Argo CD from a
  Helm chart, Helm makes these ConfigMaps. Helm merges all values of the
  release and the chart defaults first. So in fleet discovery, drydock
  renders the Application that installs Argo CD and reads the settings from
  the rendered ConfigMaps. An install is an Application or a generated
  Application that matches one of these rules:
  - It renders a chart named `argo-cd`: a Helm source with `chart: argo-cd`,
    a Kustomize `helmCharts` entry with `name: argo-cd`, or a local chart
    whose `Chart.yaml` has the name `argo-cd` or a dependency with that
    name.
  - A local Helm values file of its render sets a value under `configs.cm`,
    `configs.params`, or `configs.cmp`.
  - Its inline Helm values set such a value: `values`, `valuesObject`,
    Kustomize `valuesInline`, or a `parameters` or `fileParameters` name
    that starts with `configs.cm.`, `configs.params.`, or `configs.cmp.`.

  drydock emulates the Argo CD that runs the Applications in the repository.
  So an install counts only when its destination is this cluster: the
  destination resolves to `https://kubernetes.default.svc`, by server or by
  name, as project validation resolves it. Only its rendered ConfigMaps in
  the Argo CD namespace (`argocd`) count, both when drydock renders it for
  settings only and when the discovery frontier renders it. drydock drops
  its settings ConfigMaps in other namespaces. An Application that installs Argo
  CD on another cluster is not an install. drydock does not render it for
  settings, and its values files stay settings sources of their own, as in
  earlier releases.

  The local Helm values files of a render are these files:
  - The `valueFiles` entries. drydock resolves them as the renderer does, and
    it reads every YAML document of a values file, as Helm does. It
    fills in `$ARGOCD_APP_NAME` and the other build environment variables,
    resolves `$ref` roots as the render does, and expands globs. A `$ref`
    with the repository and revision of the source reads the root of that
    source. So it is local only when that source renders from this
    repository, or is a chart from a chart repository. Any other `$ref` is
    local when its source is this repository. For a chart from a chart
    repository, only `$ref` entries are local.
  - The `values.yaml` of a local chart, and of each unpacked subchart under
    its `charts/` directory.
  - The `valuesFile` and `additionalValuesFiles` of each Kustomize
    `helmCharts` entry, and the `values.yaml` files of its local chart.
    drydock reads them as the renderer does: relative to the kustomization
    directory, with globs and build environment variables. It skips
    `valuesFile` when `valuesMerge` is `replace` and `valuesInline` is set,
    because the render then does not read it. When `valuesInline` is set,
    kustomize reads `valuesFile` with its own YAML parser, which reads only
    the first YAML document, and drydock reads it the same way. Helm reads
    every other values file with all its YAML documents.

  drydock merges the `.argocd-source.yaml` and `.argocd-source-<app>.yaml`
  overrides of a local source first, as Argo CD does. Then it picks the
  source type as Argo CD does: the explicit type, else Kustomize for a
  kustomization file, else Helm for a chart. So a source with an explicit
  `directory` or `plugin` type is not a Helm install. A remote value file
  has no local file. drydock finds these files before it renders, with the
  resolution helpers of the renderer. It does not record the files during
  the render. One gap is known: a `helmCharts` entry inside a component
  that `spec.source.kustomize.components` adds is not read for values files
  or for the chart name.

  The discovery frontier already renders an install whose source path can
  hold Argo CD objects, such as a local chart whose templates name
  `argocd-cm`. drydock keeps every object of that render, as before. drydock
  renders every other install for its settings only, and keeps only the
  three ConfigMaps from that render. The final render renders each install
  again as a normal Application.

  A local Helm values file of the render is then not a settings source of
  its own. The same is true for a committed settings ConfigMap file that a
  Kustomize render read, such as a resource or a patch of `argocd-cm`.
  drydock matches files, not directories. A file in a directory that the
  render read from, but that the render did not read itself, stays a
  settings source. For example, an extra values file in a chart directory
  stays a source. A file in a local chart directory that is not a template
  is not a manifest that the install applies, and drydock does not scan
  chart templates. So layered values files of one install do not conflict:
  the later file wins for each key, as in Helm. The chart defaults are in
  the settings. For example, the default `resource.exclusions` list of the
  argo-cd chart and `resourceExclusionsAdditional` after it, and the default
  `application.instanceLabelKey`. A `null` value removes a chart default, as
  in Helm. `configs.params` reaches `argocd-cmd-params-cm`.

  Every fleet command renders the install in discovery, so it needs the
  chart of the install: `build`, `test`, `diff`, `get apps`, a changed-only
  diff, and `diag` with `--render`, `--cache-events`, or
  `--plugin-executions`, also when the change does not touch the install.
  Plain `diag` uses static discovery and does not render the install. If
  drydock cannot render an install, for example because the chart is not in
  the cache with `--offline`, it reports a
  `settings.install-render-failed` warning that names the Application and
  the error, once for each diff side. The settings are then known to be
  incomplete, so `--strict` fails on this warning. Then each values file of that install
  stays a settings source of its own, as described below, unless another
  install that rendered read the same file. drydock reads only `configs.cm`
  and `configs.cmp` from such a file. A values file that no rendered install
  reads also stays a settings source of its own. drydock does not render an
  install in `--discovery-mode static` or with `--max-discovery-depth 0`.

  Two sources can put a settings ConfigMap with the same namespace and name
  in the cluster: a committed `argocd-cm` that the install does not read, or
  two installs on this cluster. drydock keeps one by the rules for any
  duplicate object. For example, a committed file wins over the render of
  an Application, and of two Application renders the first one found wins.
  It reports the other one as a duplicate. The values files of
  both renders stay out of the settings sources.
- `ignoreResourceUpdates` customizations. Argo CD uses them only to skip
  reconciles when a live resource changes. They never change desired output
  or a desired-vs-desired diff. The argo-cd chart sets eight of them by
  default. drydock parses them and reports one
  `settings.ignore-resource-updates` warning for each settings source that
  names their keys. A diff reports each distinct warning once, not once for
  each side. `--strict` does not fail on this warning.
- `configs.cm` in an Argo CD Helm values file that drydock reads on its own,
  as the argo-helm `argo-cd` chart 10.14.0 turns it into `argocd-cm`. This
  applies when no install that drydock renders reads the file. The chart
  helper `argo-cd.config.cm` is in `templates/_helpers.tpl`. drydock reads
  the values file as Helm does, so `yes` and `on` are `true`, and every
  number is a float. Then drydock does what the chart does:
  - If `create` has a false value, the chart makes no `argocd-cm`. drydock
    then reads no `argocd-cm` settings from the file.
  - The keys `create`, `annotations`, and `resourceExclusionsAdditional`
    do not go into `argocd-cm`.
  - If `resourceExclusionsAdditional` is set, its YAML text goes at the end
    of `resource.exclusions`. If `resource.exclusions` is then a value that
    is not text, the chart fails, and drydock reports an error.
  - Each value becomes text with the Helm `toString` function. For example,
    `100000000` becomes `1e+08`, and a map becomes `map[a:1]`.
  - A key whose text is empty does not go into `argocd-cm`. So
    `resource.customizations.useOpenLibs.Service: ""` sets nothing. In a
    raw `argocd-cm` ConfigMap, the same empty value is an error, as in
    Argo CD.

  For such a file, drydock does not read the default values of the chart.
  If the chart has a default for a key, Helm removes the key when its value
  is `null`. Helm writes `<nil>` for a `null` key that the chart does not
  define. drydock always removes a key with a `null` value. For the same
  reason, `resourceExclusionsAdditional` goes only after a
  `resource.exclusions` value that the values file itself sets. drydock does
  not read `configs.params` from such a file.
- Several Argo CD settings sources, one source at a time. drydock reads each
  `argocd-cm` ConfigMap, each rendered settings ConfigMap, and each Helm
  values file that it reads on its own. Then drydock merges the settings
  field by field. If two sources set the same field of the same
  `resource.customizations` key to different values, drydock reports a
  conflict. Helm instead merges all values files of one release into one
  `configs.cm` map, and a later file wins for each key. For a rendered
  install, drydock gets this result from Helm. For values files that drydock
  reads on their own, drydock does not know which release uses them, or
  their order. So for such layered values files, drydock reports a conflict
  where Helm keeps the value from the later file.
- The location of each `resource.customizations` field. Each field keeps
  the `argocd-cm` key that set it. A health Lua error points at the key that
  holds the script. A merge conflict points at the key that sets the field
  with the conflict.
- Ignore rule order as in Argo CD v3.6.1. Application rules run first, then
  the resource's annotation rule, then `resource.customizations` rules
  (`util/argo/diff/ignore.go`, `util/argo/normalizers/diff_normalizer.go`).
  Each rule removes its `jsonPointers` and then its `jqPathExpressions` before
  the next rule runs. drydock keeps every copy of a repeated pointer or
  expression, so a repeated array-index pointer removes one element per copy.
  Argo CD walks `resource.customizations` in Go map order, which is not
  defined. drydock uses sorted key order so the result is stable.
- A `jqPathExpressions` entry that fails to run on one resource, such as a
  `containers[]` expression on a ConfigMap, is skipped for that resource, and
  the other rules still apply. Argo CD logs such a failed patch and goes on
  (`util/argo/normalizers/diff_normalizer.go`). An expression that returns
  more than one value, or that runs longer than the controller's jq
  timeout, is skipped the same way. The timeout is the
  `argocd-cmd-params-cm` key `controller.ignore.normalizer.jq.timeout` of
  the current side, and 1 second when the key is empty, zero, negative, or
  not a duration.
- A jq expression that does not parse or compile fails the comparison of
  each current-side Application it belongs to, even when it matches no
  resource or the Application renders no resources. Argo CD builds every jq
  patch of the Application's rules and of all `resource.customizations`
  entries before it compares any resource, and it sets a `ComparisonError`
  condition with sync status `Unknown` (`controller/state.go`). So an
  expression in `resource.customizations` fails every Application. drydock
  reports a `diff.ignore-rule-invalid` error for each such Application. The
  `diff` commands then print the diagnostics and no diff, and fail, as for a
  render failure. Library callers of `DiffApps` still get the results of the
  other Applications with the error. Argo CD never compares a deleted
  Application, so an Application that only the baseline side has is not
  checked.
- `resource.customizations` keys as Argo CD reads them for `jsonPointers`,
  `jqPathExpressions`, and `knownTypeFields`
  (`util/argo/normalizers/util.go`): `group/kind` matches with globs, a key
  with no slash such as `Service` means that kind in the core group, and a key
  with more than one slash matches nothing. For `managedFieldsManagers`, Argo
  CD reads keys in a different way (`util/argo/diff/ignore.go`). It looks up
  the exact text `group/kind` and the key `*/*`, with no globs. A core
  resource has the lookup key `/Kind`, so a key with no slash such as
  `Service` never applies its managers. Use `/Service`, or the split key
  `resource.customizations.ignoreDifferences._Service`. drydock matches this
  behavior. `knownTypeFields` apply only when
  the key's group and kind equal the resource's group and kind, with no glob
  matching. Argo Rollouts always get Argo CD's default known type, a PodSpec at
  `spec.template.spec`, unless a key sets `knownTypeFields` for
  `argoproj.io/Rollout`. The known-types normalizer runs after the ignore
  rules, as in Argo CD (`util/argo/diff/normalize.go`).
- When the two diff sides carry different ignore settings, drydock
  normalizes both sides with the current side's settings: the Application's
  `spec.ignoreDifferences`, `resource.customizations`, compare options, and
  the annotation. Argo CD compares the new desired state under the rules of
  the live Application, which are the new rules once the Application change
  is synced. A rule that only the baseline side has does not hide a change.
  A resource that exists only on the baseline side also uses the current
  side's rules and settings of its Application, as Argo CD normalizes a
  live object that the new desired state no longer has. Its own annotation
  does not apply, because only a desired object carries the annotation
  rule. A pointer warning comes only from the current side's rules. The
  resources of an Application that only the baseline side has get no ignore
  rules, because Argo CD never compares a deleted Application. Earlier
  drydock releases used the union of both sides' rules.
- `argocd-cm` `resource.exclusions` and `resource.inclusions` (Argo CD
  v3.6.1), with Argo CD's core exclusions. drydock decodes them as Argo CD
  decodes them, with `sigs.k8s.io/yaml`. Keys match case-insensitively, so
  `Kinds:` is the same as `kinds:`. A repeated key keeps its last value, and
  unknown keys are ignored. A value Argo CD cannot decode is an error
  diagnostic. Cluster globs match the resolved destination server, as in
  Argo CD, and never the destination name. A destination name resolves
  through the cluster Secrets, and the implicit `in-cluster` name resolves to
  `https://kubernetes.default.svc`. Some destinations cannot be resolved
  offline: a name with no cluster Secret or with more than one, both or
  neither of name and server, or the in-cluster cluster that
  `cluster.inClusterEnabled: "false"` disables. For these, drydock drops a
  resource only when Argo CD would drop it for every possible server. A rule
  with no clusters, or with only `*`, still applies. When the result depends
  on the server, drydock keeps the resource. Argo CD itself fails the
  comparison when it cannot resolve the destination. drydock cannot see live
  cluster Secrets, so it treats the server as unknown.
- Resource-level `argocd.argoproj.io/ignore-differences` annotations, as in
  Argo CD v3.6.1: the value is YAML with only `jsonPointers`, the rule applies
  only to the annotated resource (including child Application CRs), and an
  unparseable value or an empty `jsonPointers` list is skipped. Annotation
  pointers stay active under `--show-ignored-fields`. Annotation pointers use
  the path rules of the JSON-patch library Argo CD applies them with: the
  first `/`-separated segment is dropped unchecked (so `#/spec/replicas`
  removes `/spec/replicas`), only `~0` and `~1` are decoded, and a negative
  array index counts from the end. As in Argo CD, where a failed JSON-patch
  remove is only logged, a pointer that cannot be applied (non-numeric or
  out-of-range array index, `-`, or a root `""` pointer) is skipped. It never
  fails the diff or hides the resource.
- Application-level and `resource.customizations` `jsonPointers` follow the
  same JSON-patch path rules, because Argo CD applies every pointer the same
  way (`util/argo/normalizers/diff_normalizer.go`). A pointer that is not a
  valid RFC 6901 pointer, such as `spec/replicas`, or the root pointer `""`,
  gives the `diff.ignore-pointer-invalid` warning, once for each Application
  and pointer. `--strict` makes it an error. Earlier drydock releases failed
  the diff on such a pointer, and removed the whole resource for `""`.
- Argo CD reads the ignore-differences annotation only from the desired
  (target) object and applies the rule to live and target. drydock compares
  desired against desired, so the current-side manifest stands in for the
  target. drydock reads the annotation from the current-side manifest and
  applies the rule to both sides. A resource that exists only on the baseline
  side has no target, so its own annotation does not normalize it.
- Repeated-resource last-wins behavior inside one Application, with a
  diagnostic, as in the Argo CD v3.6.1 controller. The controller keeps the
  last copy of each resource among the manifests of all sources, and only
  then splits off the hooks. So when the last copy is a hook, the resource
  is a hook, and Argo CD does not manage it. For example, Helm prints hooks
  last, so a ServiceAccount that a chart renders as a template and as a
  `pre-install` hook is a hook. drydock follows the same order for every
  source type: such a resource leaves the drydock build output and diffs. A
  repeated hook also gives the `render.repeated-resource` warning. An object
  with a `generateName` and no name is never a repeat. Earlier drydock
  releases kept hooks out of the repeat check.
- `get images` and `diff images` rendered image reference extraction from
  PodSpec container images and scalar fields whose key is exactly `image`.

Not supported:

- Applying `ignoreResourceUpdates` to desired-vs-desired diffs.
- Live Server-Side Diff. Since Argo CD v3.6.1 it is also used whenever the
  `ServerSideApply=true` sync option is enabled, so Argo CD's diff for those
  resources includes API server defaulting and admission webhook mutation
  that drydock's desired-vs-desired diff never shows.
- Managed-fields prediction from live state.
- Arbitrary string scanning for image references.

## Diagnostics, Projects, And Settings

Supported:

- `diag --path` repository diagnostics through static discovery, ApplicationSet
  expansion, settings metadata, and AppProject validation of the discovered
  Applications (source repositories, destinations, source namespaces).
- `diag --render` render-backed diagnostic reports.
- `diag -o json|yaml` structured diagnostic reports.
- `diag --cache-events` optional render-backed cache acquisition event
  reporting.
- `diag --plugin-executions` optional render-backed plugin execution metadata.
- `diag --settings -o json|yaml` redacted settings summaries for parsed Argo
  CD settings metadata.
- Local AppProject manifest discovery.
- Rendered AppProject discovery from app-of-apps/bootstrap desired output.
- Offline diagnostics for Application source repository, destination
  server/name/namespace, and source namespace validation from local AppProject
  manifests.
- AppProject RBAC role and policy parsing as reported metadata, without
  simulating authorization.
- Repository credential matching diagnostics based on discovered repository
  Secret metadata only, without reading secret credential fields.
- Cluster Secret metadata parsing for offline named-cluster destination
  diagnostics and project-scoped cluster checks when the desired state includes
  the relevant cluster Secret metadata. Only `name`, `server`, `namespaces`,
  `clusterResources`, and `project` are decoded. A cluster Secret whose
  `project` names an AppProject adds that project's implicit destinations, as
  Argo CD does. A server-only destination at `https://kubernetes.default.svc`,
  or a name-only `in-cluster` destination, with no cluster Secret resolves to
  Argo CD's built-in `in-cluster` cluster. When argocd-cm
  `cluster.inClusterEnabled` is `"false"`, a destination at that address is
  reported as not permitted, as Argo CD rejects it.
- Project diagnostic modes. The default `actionable` mode reports local
  AppProject denials that drydock can enforce offline and hides deferred or
  metadata-only AppProject diagnostics. Use `--project-diagnostics=all` for full
  compatibility audits, or `--project-diagnostics=off` to suppress AppProject
  diagnostics.
- Repositories that declare no AppProject. Every Application then validates
  against the permissive implicit `default` project, whatever project it
  names; an Application naming another project records that with a deferred
  `project.unresolved` diagnostic, visible only in `all` mode. The actionable
  `project.missing` warning is reserved for repositories that declare other
  AppProjects but not the named one.
- `argocd-cmd-params-cm` runtime-boundary diagnostics for settings that imply
  live repo-server, controller, or ApplicationSet controller behavior. These
  settings are parsed as metadata and do not mutate render behavior, except
  `reposerver.max.combined.directory.manifests.size` (see the directory
  size limit above) and `reposerver.allow.oob.symlinks` (see the
  out-of-bounds symlink check above). drydock also applies
  `controller.ignore.normalizer.jq.timeout` to diffs: it limits how long one
  jq ignore-rule deletion may run. drydock parses it as the Argo CD v3.6.1
  controller parses `ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT` with
  `env.ParseDurationFromEnv`: a Go duration such as `1500ms`, or a whole
  number of seconds, minutes, hours or days such as `2d`. An empty, zero,
  negative or unparseable value means the default, 1 second. A day count
  too large for the controller ends the controller. drydock uses the
  default for it. Two settings sources with different parsed values
  conflict. The key changes no render, so it is not in the render cache
  keys. It is in the changed-only settings signature, because a new
  timeout can change the diff of every Application with a jq rule.
- Custom health Lua validation in `test apps` and `test app`, executed offline
  against rendered desired manifests. It reports Lua runtime errors and
  invalid status values. A `deletionMessage` (Argo CD v3.6.1) applies only to
  terminating objects, so it never takes effect on desired manifests.
- Health and action customization parsing and diagnostics, including
  `useOpenLibs` metadata and redacted SHA-256 Lua summaries.
- A `health.lua` value that holds only white space, as in Argo CD v3.6.1.
  Argo CD runs any `health.lua` value that is not empty
  (`util/lua/lua.go:254`). A script with only white space returns nothing,
  so the resource gets no health status from Lua. A wildcard key and the
  built-in Lua script for the kind then do not apply. drydock keeps such a
  script and runs it in the same way. An empty value counts as no script.
- Resource actions, as in Argo CD v3.6.1. Argo CD keeps the `actions` text
  when it loads settings. The code that lists or runs actions decodes the
  text (`util/lua/lua.go:501`). drydock never runs actions, so it accepts
  every `actions` value and reports no error for it. This includes text
  that does not decode. For such text, the summary has only the SHA-256
  value of the text.
- Warning diagnostics for version-specific `kustomize.buildOptions.<version>`
  and `kustomize.path.<version>` settings because drydock does not select
  external Kustomize binaries.
- `argocd-cm` `resource.selectors` (Argo CD v3.6.1), decoded as Argo CD
  decodes it. An unparsable list or label selector is a
  `settings.resource-selector-invalid` error, because Argo CD fails settings
  loading on it. Selectors never drop resources: Argo CD only narrows its
  list/watch calls with them. A rendered resource or hook that survives the
  resource exclusions and inclusions, but whose labels (after tracking
  metadata) do not match the ANDed selectors for its group, kind, and
  cluster, stays in render and diff output with a
  `settings.resource-selector-mismatch` warning carrying Argo CD's
  `ExcludedResourceWarning` text: Argo CD will not watch it, so it stays
  OutOfSync. `--strict` promotes the warning. Cluster globs match the
  resolved destination server, as in Argo CD: a destination name resolves
  through the cluster Secrets, and the implicit `in-cluster` name resolves to
  `https://kubernetes.default.svc`. When a name cannot be resolved offline
  (unknown or ambiguous), or the destination is the in-cluster cluster that
  `cluster.inClusterEnabled: "false"` disables, Argo CD fails the comparison
  before it reads selectors, and no selector warning is reported.

### AppProject Field Semantics

This matrix is audited against Argo CD `v3.6.1`
(`94037c4d4e1af4bd0651d37a179fbd9e347e668c`). It records current drydock
behavior. Strict render tests promote visible project and repository warnings
after the selected project diagnostic mode is applied.

| Field | Upstream owner | drydock status | Current diagnostic | Offline input and next phase |
| --- | --- | --- | --- | --- |
| `spec.sourceRepos` | `types.go`, `IsSourcePermitted`, Argo repo metadata merge | Supported | `project` warning for denied sources; `repository` warning for mismatched repository Secret project metadata | Application sources plus redacted repository metadata; parity fixtures present, future gaps Phase 3 |
| `spec.destinations` | `app_project_types.go`, `IsDestinationPermitted`, `isDestinationMatched` | Supported with named-cluster metadata caveats | `project` warning for denied destinations; deferred `project` warning when the decision depends on an unresolved cluster's server (name-only destination) or name (server-only destination) | Application destination plus redacted cluster metadata. A name or server deny entry denies only when its namespace also matches (v3.6.1, #28854). A server-only `https://kubernetes.default.svc` or name-only `in-cluster` destination with no cluster Secret is the built-in `in-cluster` cluster. With `cluster.inClusterEnabled` `"false"`, a destination at that address, directly or through a cluster Secret name, is denied whatever the project permits (util/db `GetCluster`). Other unresolved destinations are evaluated in three-valued logic: drydock denies or permits only when every cluster the unknown name or server could be, scoped to the project or not, gives the same answer, and otherwise defers. Because an unseen project-scoped cluster adds an implicit destination for its server, only a matching deny entry denies an unresolved destination. Parity fixtures resolve clusters as Argo CD does |
| Implicit project-scoped cluster destinations | `util/argo` `GetAppProjectByName` | Supported | Same as `spec.destinations`; deny entries still win | A cluster Secret with `project: <name>` adds `{server, namespace: "*"}`, or one destination per `namespaces` value, to that AppProject, for Application destinations and rendered-resource namespaces alike |
| argocd-cm `globalProjects` | `util/argo` `GetAppVirtualProject`, `GetGlobalProjects` | Unsupported | None | Global project settings are not parsed and their policy is not merged into matching AppProjects |
| `spec.description` | `types.go` | Metadata-only | None | Decoded as typed metadata |
| `spec.sourceNamespaces` | `types.go`, `IsAppNamespacePermitted` | Supported | `project` warning for denied Application source namespace | Application namespace plus controller namespace assumption; parity fixtures present |
| `spec.permitOnlyProjectScopedClusters` | `types.go`, `IsDestinationPermitted` project-cluster check | Deferred when cluster metadata is unavailable; supported when redacted project-scoped cluster metadata is present | `project` warning for deferred metadata or known denial | Redacted cluster Secret `project` metadata; without it drydock reports a runtime-boundary deferral rather than a hard decision |
| `spec.clusterResourceWhitelist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources | Rendered objects, including sync and delete hooks, plus offline scope knowledge; unknown custom-resource scope defers with `project.resource-scope-deferred` only when possible scopes change or defer the policy outcome |
| `spec.clusterResourceBlacklist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources | Rendered objects, including sync and delete hooks, plus offline scope knowledge; unknown custom-resource scope defers with `project.resource-scope-deferred` only when possible scopes change or defer the policy outcome |
| `spec.namespaceResourceWhitelist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources; `project.resource-destination-denied` for rendered object namespace denials | Rendered objects, including sync and delete hooks, plus destination namespace normalization; discovery-only workflows do not evaluate rendered resource policy |
| `spec.namespaceResourceBlacklist` | `types.go`, `IsGroupKindNamePermitted`, `IsResourcePermitted` | Supported for render-backed workflows | `project.resource-denied` warning for denied rendered resources; `project.resource-destination-denied` for rendered object namespace denials | Rendered objects, including sync and delete hooks, plus destination namespace normalization; discovery-only workflows do not evaluate rendered resource policy |
| `spec.roles` | `types.go`, `ValidateProject`, `ProjectPoliciesString`, Argo RBAC runtime | Metadata-only | `project.rbac-metadata-only` warning when roles are present | Role presence only; no Casbin authorization, claims, scopes, or token-status simulation. Policy actions are not validated, so the `rollback` action Argo CD v3.6.1 accepts is metadata like any other |
| `spec.syncWindows` | `types.go`, `SyncWindow.Matches`, `SyncWindows.CanSync`, Argo sync runtime | Runtime-bound; documented only | None | Depends on clock, timezone, destination, and manual vs automated operation context |
| `spec.orphanedResources` | `types.go`, Argo application controller live orphan scan | Unsupported live-cluster behavior | None | Requires live namespace inventory, ownership graph, orphan exclusions, and controller state |
| `spec.signatureKeys` | `types.go`, Argo GPG verification in `controller/state.go` and repo-server verification | Unsupported runtime verification | None | Deprecated upstream in favor of `spec.sourceIntegrity`; `EffectiveSourceIntegrity` converts the keys into a Git GPG policy for every repository. Requires GPG keyring state, repo-server verification result, and resolved commit metadata |
| `spec.sourceIntegrity` | `types.go`, `EffectiveSourceIntegrity`, source integrity checks in `controller/state.go` and repo-server verification | Unsupported runtime verification | None | Requires GPG keyring state, repo-server verification result, and resolved commit metadata |
| `spec.destinationServiceAccounts` | `types.go`, `ValidateProject`, `controller/sync.go` sync impersonation runtime | Runtime-bound metadata | None | Sync impersonation is applied during controller sync; drydock does not mutate REST config or validate live service account authorization |

### AppProject Known Gap: Live Cluster Secret At The In-Cluster Address

Some repositories have no cluster Secret at `https://kubernetes.default.svc`.
In those repositories, drydock treats a destination at that server as Argo
CD's built-in `in-cluster` cluster. A name-only `in-cluster` destination
resolves the same way. drydock then checks the AppProject against the
built-in cluster. If no destination entry allows it, drydock reports a
definite `project.destination-denied` warning.

The live Argo CD can decide differently. A cluster Secret at the in-cluster
address takes the place of the built-in cluster (`util/db/cluster.go:261-266`
in Argo CD v3.6.1). Suppose that Secret exists only in the live cluster and
its `project` field names the Application's project. Argo CD then adds an
implicit destination for its server to that project
(`util/argo/argo.go:832-844`). That implicit destination can allow the
Application that drydock denies. The Secret's name can also change how name
entries match.

drydock keeps the definite denial. It does not defer this case. The live
Secret keeps the in-cluster server, so a server deny entry that matches
`https://kubernetes.default.svc` denies in Argo CD too. Name entries are
different. Argo CD matches them against the live Secret's name, and drydock
matches them against `in-cluster` (`globMatch` and `isDestinationMatched` in
`pkg/apis/application/v1alpha1/app_project_types.go`). For example, suppose
the live Secret has the name `local`. The name deny entry `!in-cluster` then
matches in Argo CD and allows the Application, but drydock denies it. In the
other direction, the name deny entry `!local` denies in Argo CD, but drydock
allows the Application. So this gap affects denials where no entry allows the
cluster, and any result that depends on a name entry. To get Argo CD's
result, put that cluster Secret in the repository, so that drydock can read
its metadata. When drydock denies a destination that it resolved to the
built-in cluster, the `project.destination-denied` message says so. The
message also names this fix.

### AppProject Current Behavior Traceability

This table maps the current drydock code paths and tests to the semantics above.
Future audit phases may replace current-behavior tests as deferred fields are
implemented or documented as runtime boundaries.

| Behavior | Current code path | Test coverage | Disposition |
| --- | --- | --- | --- |
| Local AppProject discovery | `discovery.Scan`, `appendDiscoveredProjects` | `TestScanDiscoversAppProjects`, `TestScanPreservesDocumentIdentityForTypedObjects` | No follow-up |
| Rendered AppProject discovery | `applyExplicitKustomizeDiscovery`, `discoverRenderedFleet`, bootstrap discovery | Existing explicit-rendered and fleet discovery tests | Phase 2 fixture expansion |
| Duplicate or conflicting projects | `mergeProjects`, `resolveDiscoveryConflict`, same-scan `projectIndex` last-wins | Existing rendered precedence coverage | Discovery-focused follow-up |
| Implicit default project | `effectiveProject`, `implicitDefaultProject` | `TestValidateApplicationsAllowsImplicitDefaultProject`, `TestValidateApplicationsImplicitDefaultProjectPermitsAnySourceNamespace`, `TestValidateApplicationsDefersUnresolvedProjectWithoutLocalProjects`, `TestValidateApplicationsCurrentBehaviorAllowsImplicitDefaultProjectWhenOtherLocalProjectsExist` | No follow-up |
| Missing non-default project | `ValidateApplications`, `projectIndex`, `applicationProject` | `TestValidateApplicationsCurrentBehaviorReportsMissingNonDefaultProject` | No follow-up |
| Source repository matching | `validateSources`, Argo `IsSourcePermitted` | Existing source policy tests, source parity fixtures, `TestValidateApplicationsCurrentBehaviorReportsDeniedMultiSourceRepository` | Phase 3 remediation for any future parity gaps |
| Destination matching | `validateDestination`, `destinationCluster`, `offlineDestinationPermitted`, Argo `IsDestinationPermitted` | Existing destination and project-scoped cluster tests, destination parity fixtures fed Argo CD-resolved clusters, `TestOfflineDestinationPermittedIsSoundAgainstArgoCD` | Unresolved destinations defer only when the unknown name, server, or project scoping decides the outcome |
| Cluster Secret metadata | `clusterSecretSettings`, `projectScopedClusters`, `projectScopedClusterDestinations` | Cluster parser tests, project-scoped destination tests, `TestValidateApplicationsAddsProjectScopedClusterDestinations`, metadata integration fixtures | No follow-up |
| Repository Secret project metadata | `repositorySecretSettings`, `effectiveProject`, `repositoryMetadataDiagnostics` | Repository metadata tests, project-scoped repository tests, metadata integration fixtures | No follow-up |
| Source namespace checks | `validateSourceNamespace` | Existing source namespace tests, source namespace parity fixtures, `TestValidateApplicationsCurrentBehaviorPhase3SourceNamespacesEmptyListDeniesNonControllerNamespace` | No follow-up |
| RBAC role metadata | `rbacMetadataDiagnostics` | Existing source namespace and RBAC metadata test | Metadata-only warning; authorization is not simulated |
| Resource policy fields | `ValidateRenderedResourcePolicy`, `renderOneApplication`, shared manifest scope helpers | Resource policy evaluator tests and `orchestrator_project_resource_policy_test.go` | Supported for render-backed workflows; discovery-only paths remain unevaluated |
| Runtime-bound fields | No sync window, orphan, signature, or impersonation simulation | `TestValidateApplicationsRuntimeBoundFieldsDoNotSimulateLivePolicy` | Intentionally documented runtime boundary |

Not supported:

- Raw Lua body output in structured settings summaries.
- Resource action Lua execution.
- Live Argo CD health aggregation.
- Live AppProject authorization decisions.

## Public API And Releases

Supported:

- Public Go API in `pkg/drydock` for Application listing, rendering, manifest
  diffs, image diffs, source acquisition hooks, plugin renderer hooks, and
  stable diagnostics.
- `cache path`, `cache list`, `cache prune`, and `cache delete` local cache
  lifecycle commands.
- Benchmarks for repeated local Application rendering and ApplicationSet
  expansion.
- Static binary, setup-action, and GHCR container release shape documented in
  `docs/release.md`.

The module pins Argo CD dependencies. Upgrade Argo CD deliberately and update
compatibility tests and this document in the same change.
