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
text both sides decode first, render in the main manifest comparison. Helm
and Kustomize `helmCharts` renders that Helm or kustomize fail with a message
that names no file, such as an invalid release name or a library chart, are
checked the same way by their message.

A last phase turns Helm, Kustomize, and Jsonnet off in the live `argocd-cm`
and compares Applications whose sources then render as plain directories.

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
  used annotation tracking for this render. The fleet frontier skips a
  local source whose directory the repository scan already read, because
  its render adds no object. It still renders such a directory when it
  holds a `Chart.yaml`, or a file that the source type detection reads as a
  Helm chart, such as `traefik-HelmChart.yaml`, because chart templates can
  declare objects that the scan cannot see.
- Explicit `--discover-kustomize PATH` discovery from rendered local
  Kustomize entrypoints. Explicitly rendered Kustomize objects take precedence
  over committed duplicates because the path is the operator-selected Argo CD
  entrypoint. For the same reason, the path counts as the Argo CD install of
  this cluster: its rendered `argocd-cm`, `argocd-cmd-params-cm`, and
  `argocd-cmp-cm` replace the values files and
  settings ConfigMap files that its render reads, also when no Application
  installs Argo CD (see "Settings from the rendered Argo CD install"). An
  Application whose source renders the same path is not rendered again for
  its settings, because the `--discover-kustomize` render is that install.
- The source checks of the Argo CD v3.6.1 controller
  (`ValidatePermissions`, `util/argo/argo.go:644-737`). A single source needs
  `repoURL` and `path` or `chart`. A multi-source entry needs `repoURL` and
  one of `path`, `chart`, and `ref`. A `chart` needs `targetRevision`. An
  `oci://` source follows the same rules. If a check fails, Argo CD sets an
  `InvalidSpecError` condition and skips the comparison, so the Application
  renders nothing. drydock fails the render with the same text. Earlier
  drydock releases rendered nothing for such an Application and passed it.
  After a failed check, Argo CD also skips the AppProject checks that
  follow. In a multi-source Application, it still checks the repositories
  of the sources before the failing one. drydock reports the same project
  warnings in the same order.
- `spec.sourceHydrator`. Argo CD compares such an Application against its
  sync source only: `syncSource.path` at `syncSource.targetBranch`, in
  `syncSource.repoURL` or else in the dry source repository
  (`ApplicationSpec.GetSources`). It ignores `spec.source` and
  `spec.sources`. drydock reads the sync source at `syncSource.targetBranch`.
  It never uses the local tree only because the sync path exists there,
  because the dry and the sync source often share a path:

  - The repository under analysis renders from the tree of this side only
    when the branch passes the self-repo revision match: `HEAD`, a `--ref`
    or `--ref-orig` name, or the default branch that the remote `HEAD`
    symref names. This rule is stricter than the rule for other sources of
    that repository, which also render from the tree when their path exists
    there. A `targetBranch` equal to the default branch renders the working
    tree, even when another branch is checked out. Each side of a diff reads
    its own tree, so a change to the hydrated path shows in the diff. drydock
    finds the repository by its remote, or by a `--repo-map` to the tree
    under analysis. A `--ref` diff rewrites such a map to each side's
    snapshot. A self map behaves as if there were no map: a branch that the
    match does not track comes from the Git cache or a fetch.
  - A `--repo-map` to another Git repository renders as it is when it has
    that branch checked out. When another branch is checked out there,
    drydock renders a snapshot of the branch, or of `origin/<branch>`. A
    `--repo-map` to a directory that is no Git repository fails, because
    drydock cannot read the branch from it.
  - Changed-only selection owns the sync path only when the render reads
    the tree under analysis, in the first case.
  - Any other sync source comes from the Git cache or a fetch, and
    `--offline` fails on a cache miss.

  drydock cannot run the hydrator offline. So a change to the dry source
  shows only after the hydrator commits it to the sync branch. Each such
  Application gets the `source.hydrator-sync-source` warning, which
  `--strict` does not fail on, because the render is the one that Argo CD
  compares. The hydrator checks of the controller apply:
  `drySource.repoURL`, `syncSource.targetBranch`, and
  `hydrateTo.targetBranch` when `hydrateTo` is set. The CRD schema of
  `syncSource.path` applies too: the API server rejects an empty path, `.`,
  and `/`, so Argo CD never holds such an Application, and drydock fails it
  with the API server message. The AppProject must permit the dry source
  repository, and the sync source repository when it is another one. The
  repository metadata checks cover both repositories. `get apps` prints the
  sync source. drydock does not read Argo CD settings values files through
  a hydrator Application, because its sync branch is usually not the local
  tree. Earlier drydock releases ignored the hydrator and rendered
  `spec.source`, or nothing.
- An empty `spec.project` is the `default` project, as in Argo CD
  (`ApplicationSpec.GetProject`). drydock sets `ARGOCD_APP_PROJECT_NAME` to
  `default`, validates against the `default` AppProject, and prints
  `default` in `get apps` and in the Go API. The Argo CD controller writes
  `default` back into the Application. Earlier drydock releases used the
  empty string in `ARGOCD_APP_PROJECT_NAME` and in the output. The
  ApplicationSet controller does not default an empty project. It looks the
  AppProject up by the empty name, finds nothing, and does not create that
  Application (`applicationset_controller.go:627-631`). drydock drops it
  with the `appset.application-validation-failed` warning.
- `argocd-cmd-params-cm` `application.namespaces` and
  `applicationsetcontroller.namespaces`. The Argo CD v3.6.1 controllers
  reconcile an Application or an ApplicationSet only in the Argo CD
  namespace or in a namespace that the list matches
  (`isAppNamespaceAllowed`, `controller/appcontroller.go:2852-2854`, and
  `ignoreNotAllowedNamespaces`,
  `applicationset/controllers/applicationset_controller.go:676-680`). Each
  entry is a glob, or a regular expression when slashes wrap it
  (`glob.MatchStringInList` with `glob.REGEXP`). Argo CD sets no status on
  an object outside these namespaces, and its API server refuses it. drydock
  skips such an object with the `discovery.namespace-not-enabled` warning.
  It also skips the Applications that a skipped ApplicationSet would
  generate, and the Applications and ApplicationSets that a skipped
  Application would render. It follows only these links, never the file or
  the document that an object came from. `--strict` does not fail on the
  warning. drydock applies the lists only when it reads an
  `argocd-cmd-params-cm` and knows the Argo CD controller namespace: the
  `--argocd-namespace` flag names it, or an `argocd-cmd-params-cm` names
  the namespace that drydock uses (see the next item). Otherwise drydock
  cannot tell how the cluster sets the lists or where the controllers run,
  and it keeps every object, as earlier releases did. A committed ConfigMap
  often names no namespace because a kustomization sets it. An object
  without a namespace is in the Argo CD namespace. Project validation uses
  the same controller namespace for `sourceNamespaces`. When nothing names
  one, project validation uses the namespace of the AppProject, as earlier
  releases did, while instance names and the namespace gate use `argocd`.
  drydock also reads the lists from the controller workloads of the
  install, so it sees a list that the Argo CD Helm chart sets through
  `controller.extraArgs`, `applicationSet.extraArgs`, `controller.env`, or
  `applicationSet.extraEnv` (see "Settings from the Argo CD workloads").
- The Argo CD controller namespace. Argo CD learns this namespace from its
  own pod, not from the repository. The controller uses it for the
  instance name of an Application (`controller/state.go:402` and `:797`).
  The instance name is the bare name for an Application in the controller
  namespace, and `<namespace>_<name>` for any other Application. It is the
  value of `ARGOCD_APP_NAME`, the `<app>` in `.argocd-source-<app>.yaml`,
  and the application part of resource tracking. drydock uses the same
  namespace for these, for the namespace gate, for AppProject validation,
  for the `.argocd-source-<app>.yaml` lookup of `--changed-only` selection,
  and for the Application of a plugin policy bootstrap entrypoint. Helm
  release names stay the bare Application name. drydock picks the
  namespace with this rule:
  1. If `--argocd-namespace` is set, drydock uses its value.
  2. Else, if every counting `argocd-cmd-params-cm` names the same
     `metadata.namespace`, drydock uses that namespace.
  3. Else, drydock uses `argocd`.

  An `argocd-cmd-params-cm` counts when it has a namespace and reaches
  this cluster. A rendered object has the namespace that the render gives
  it. A committed file has each namespace where it lands. Each Application
  on this cluster that renders it puts it in the outermost kustomization
  namespace on the way, else in its own `metadata.namespace`, else in the
  destination namespace of the Application. When no Application renders
  the file, it has its own `metadata.namespace`. A
  rendered object counts unless only Applications on other clusters render
  it. A committed file counts unless only Applications on other clusters
  render it, and a committed file that no Application renders counts. The
  rule is a set test, so the order of files does not change the result.

  Each `argocd-cmd-params-cm` must also pass one test: the Argo CD that
  it configures must reconcile at least one Application on this cluster
  that puts it there. drydock applies the test to the
  `argocd-cmd-params-cm` of every render on this cluster, a Helm install or
  a plain Kustomize or directory source, and to every committed one that
  Applications on this cluster render. The Application must be in the
  namespace where the ConfigMap lands, or in a namespace that its
  `application.namespaces` enables. A committed ConfigMap lands in the
  outermost kustomization namespace on the way, else its own namespace,
  else the destination namespace of the Application. An Application
  without a namespace lives in the destination namespace of the
  Application that renders it (`controller/state.go:588-590`), its parent
  or the Applications that render its committed file. A kustomization
  namespace on the way to that file wins over the destination namespace. A
  committed `argocd-cmd-params-cm` that no Application renders passes.

  If the test fails for every Application that puts it there, the
  ConfigMap belongs to another Argo CD, such as a tenant one. It names no
  namespace. The `argocd-cm`, `argocd-cmd-params-cm`, and `argocd-cmp-cm`
  ConfigMaps that the same Applications render do not count as settings.
  Without `--argocd-namespace`, the repository and cluster Secrets of that
  install still count. The namespace warning names the namespace of the
  other install.

  Fleet discovery can find the install that names the namespace late, for
  example one level below an app-of-apps. The Applications of the earlier
  levels, and the plugin policy bootstrap entrypoints, then rendered with
  another namespace. If any render of the run used another namespace than
  the one that the settings give at its end, drydock runs discovery once
  more from the start with that namespace. The second run renders the
  bootstrap entrypoints with that namespace too. drydock never runs
  discovery a third time. If the second run ends with yet another
  namespace, drydock keeps its result and the namespace of the second run,
  also for the final render, and warns with
  `settings.controller-namespace-unsettled`, which `--strict` does not
  fail on. The second run starts with the ApplicationSet generation values
  of the first run. With `--argocd-namespace`, or when every render used
  the final namespace, there is no second run. A repository whose Argo CD
  runs in `argocd` never needs one. A top-level Helm install in another
  namespace that drydock renders only for its settings does not need one
  either. A top-level install that discovery renders with the other
  Applications, such as a local chart whose templates name the settings
  ConfigMaps, can need one, because the Applications next to it rendered
  before the install named the namespace.

  If the counting ConfigMaps name more than one namespace, drydock warns
  once. The warning lists the namespaces and names `--argocd-namespace`.
  If drydock uses one of the listed namespaces, the warning is
  `settings.controller-namespace-ambiguous`, and `--strict` does not fail
  on it, because a hub Argo CD next to tenant installs is a normal layout.
  If none of the counting ConfigMaps names the namespace that drydock uses,
  the warning is `settings.controller-namespace-unconfirmed`, and
  `--strict` fails on it. This happens when `--argocd-namespace` names a
  namespace that no ConfigMap names, or when the ConfigMaps name several
  namespaces and none of them is `argocd`. When other installs exist (see
  above), drydock warns with `settings.controller-namespace-ambiguous` and
  names them, also when they are the only ConfigMaps that name a
  namespace. Without either, drydock does not warn.

  Argo CD reads its `argocd-cm`, `argocd-cmd-params-cm`, and
  `argocd-cmp-cm` ConfigMaps, its repository and cluster Secrets, and its
  AppProjects only in its own namespace (`util/settings`, `util/db`, and
  `GetAppProject` at v3.6.1). drydock skips these objects in another
  namespace only when `--argocd-namespace` is set. A rendered object is in
  the namespace that the render gives it, and a committed object in the
  namespace where it lands, as for an `argocd-cmd-params-cm` above. drydock
  keeps a committed object when one of the namespaces where it lands is the
  flag namespace. drydock never skips an object that has no namespace
  after it lands. A file that does not parse in a skipped namespace gives
  only the skip warning, because Argo CD does not read it. drydock reports each skipped
  object with the `discovery.outside-controller-namespace` warning, and
  `--strict` does not fail on it. A skipped AppProject is still declared in
  the repository, so an Application that names it gets `project.missing`,
  as Argo CD does not find it (`util/argo/argo.go:819-822`). Without the
  flag, drydock skips none of these objects. One exception stays from
  earlier releases: a settings ConfigMap that an Argo CD install on this
  cluster renders in another namespace than the controller namespace does
  not count, because that install runs another Argo CD. A ConfigMap of
  the install without a namespace lands in the destination namespace of
  the install.

  One gap remains. If Argo CD runs in another namespace than `argocd`, and
  the repository holds no `argocd-cmd-params-cm` that names it, pass
  `--argocd-namespace`. Without it, drydock uses `argocd`, and an
  Application in the real controller namespace gets the
  `<namespace>_<name>` instance name.
- ApplicationSet Git directories, Git files, list, matrix, and merge
  generators. The Git generator follows the Argo CD v3.6.1 repo-server
  checkout. Git files patterns are `git ls-files` pathspecs, so `*` matches
  `/` and a directory name matches every file below it.
  `applicationsetcontroller.enable.new.git.file.globbing` switches them to
  doublestar globs. Git directory patterns match with `path.Match` as
  written, and a malformed one is skipped. Hidden directories are listed only
  with `reposerver.include.hidden.directories`. These fail the whole
  ApplicationSet with `appset.generator-failed`: a param file that does not
  decode, a pattern that git refuses or that drydock does not evaluate, a
  listed symlink to a directory, to a missing target, or through a file,
  a listed gitlink, a doublestar pattern that matches `.git` or a directory
  in it, and an empty AppProject name in the ApplicationSet template. The
  `attr` pathspec magic, a doublestar pattern that matches a file in `.git`,
  and a doublestar pattern that needs too many lookups make the
  ApplicationSet unsupported instead (`appset.unsupported-generator`).
  drydock models gitlinks as directories of the checkout, from the index
  or the commit. When the repository has a `.gitmodules` file, the
  generator sees submodule content, as Argo CD does by default
  (`applicationsetcontroller.enable.git.submodule`). `git ls-files` still
  lists a gitlink as one entry. A named AppProject that does not exist in the cluster also fails
  the whole ApplicationSet in Argo CD. This is a recorded difference: drydock
  cannot know the AppProjects of the cluster offline, because they can come
  from another repository, a chart, a remote Kustomize base, or the cluster
  itself. So drydock keeps the Applications. When the repository declares
  AppProjects but not that one, drydock warns `project.missing`.
  In a working tree, drydock skips the paths that Git ignores and does not
  track, and empty directories that are not gitlinks. Without a
  `.gitmodules` file, or with
  `applicationsetcontroller.enable.git.submodule` set to `false`, it also
  skips what is in a gitlink, because Argo CD checks out no submodule
  content then. A working tree whose submodule is not initialized holds an
  empty directory, so drydock warns. It keeps untracked paths that Git
  does not ignore. An Argo CD checkout does not hold them, so this is a
  recorded difference. drydock never reads a symlink target outside the
  repository or in the top-level `.git` directory. With default globbing,
  Argo CD reads such a target of a listed param file in its own file
  system, or fails when it does not exist there, so drydock reports the
  ApplicationSet unsupported. With new globbing, Argo CD drops a match
  whose real path is outside the checkout, and fails only when it cannot
  resolve a match. drydock drops such a match too, and reports the
  ApplicationSet unsupported when a wildcard still has to match below a
  symlink out of the checkout. A name must match the checkout exactly, also
  on a file system that ignores case. On macOS, drydock compares names
  composed (NFC), as Git commits them. The render
  parity smoke covers these cases. See
  [ApplicationSets](/docs/applicationsets/#git-generators).
- The Argo CD v3.6.1 OCI ApplicationSet generator (`generators[].oci`),
  top-level and nested in matrix/merge, with directories and files, `exclude`,
  `pathParamPrefix`, `values`, and the same path params as the Git generator.
  The artifact is acquired like an `oci://` Application source (`--repo-map`,
  `--offline`, the OCI cache, `--registry-config`, and `--oci-*` flags).
  Directory and file matching follow the v3.6.1 repo-server: hidden
  directories are skipped unless `reposerver.include.hidden.directories` is
  `true`, file patterns are always doublestar (`.` means `**`), and files
  resolving outside the artifact are skipped. A param file that does not
  decode fails the whole ApplicationSet.
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
- Argo CD v3.6.1 selectors on matrix children. Argo CD filters the params of
  a matrix child with the selector of that child as written. It does not
  fill in the params of the first child
  (`generator_spec_processor.go:34` and `:89`). So a child selector such as
  `env: '{{.env}}'` matches nothing. The selectors of generators inside a
  merge child do get the params, because that child runs them from the
  filled-in spec. The render parity smoke checks both cases.
- Nested matrix/merge combinations where the Argo CD v3 nested JSON API
  permits them.
- One `spec.generators` entry that sets more than one generator, such as
  `list` and `matrix` together. Argo CD v3.6.1 runs each one in the field
  order of `ApplicationSetGenerator` and keeps all their Applications
  (`GetRelevantGenerators`). Earlier drydock releases ran only the first
  one. The render parity smoke checks this case.
- Explicit local YAML/JSON fixtures for cluster, clusterDecisionResource, SCM
  provider, pull-request, and plugin ApplicationSet generators. Cluster
  generator params omit the annotations Argo CD v3.6.1 reserves
  (`argocd.argoproj.io/refresh`, `refresh-timestamp`, `hydrate`,
  `hydrate-timestamp`, `tracking-id`, `installation-id`, and
  `kubectl.kubernetes.io/last-applied-configuration`). The pull-request
  `branch_slug` and `target_branch_slug` params use the slug rules of the
  Argo CD v3.6.1 pull request generator: `github.com/gosimple/slug` with a
  50-character limit that cuts after the last whole word, and `_` replaced
  by `-`. Accented letters become plain letters, so `fix/café-crème` gives
  `fix-cafe-creme`. Earlier drydock releases cut at exactly 50 characters
  and replaced accented letters with `-`. Argo CD keeps the smart-truncation
  option of the slug library in a variable that the `slugify` template
  function also sets. drydock uses the library default. So a controller
  whose templates call `slugify` with smart truncation off can give a
  different slug. The Go-template
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
  matches, and unsupported provider filters. A provider filter that Argo CD
  itself rejects fails the ApplicationSet instead (see the generator error
  rule below): a pull request or SCM provider filter regexp that does not
  compile, a cluster generator selector that `metav1.LabelSelectorAsSelector`
  rejects, a `clusterDecisionResource` generator that sets both or neither of
  `name` and `labelSelector`, a `clusterDecisionResource` `labelSelector`
  that is empty or does not parse, and a `clusterDecisionResource` generator
  for which the fixture has decision resources with its `configMapRef` but
  none that it names or selects. When the fixture has no decision resource
  with that `configMapRef`, drydock reports `appset.provider-no-match` as
  before. Argo CD runs these checks before it reads the cluster or the SCM
  provider, so drydock runs them also when no provider fixture is given.
  The warning text assumes that the cluster lookups before these checks
  work: for a pull request or SCM provider generator, Argo CD first selects
  the provider, and for a `clusterDecisionResource` generator it first reads
  the `configMapRef` ConfigMap. If such a lookup fails, the Argo CD message
  names that failure instead, and the ApplicationSet fails all the same.
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
- Argo CD v3.6.1 generator errors that are not template errors. Argo CD
  reports them as an `ErrorOccurred` condition with reason
  `ApplicationParamsGenerationError` and generates no Applications for the
  whole ApplicationSet, also from its other generators. drydock warns
  `appset.generator-failed` with the Argo CD message and generates no
  Applications for that ApplicationSet. `--strict` promotes the warning to
  an error. The errors are these:
  - A list element that does not decode, a non-string value without
    `goTemplate`, or an `elementsYaml` that is not a YAML list of mappings
    (`applicationset/generators/list.go:44-86`). Earlier drydock releases
    warned and kept the other Applications for a bad `elementsYaml`.
  - A matrix that does not have exactly two children, or a merge with fewer
    than two children, no merge keys, or a repeated merge key value.
  - A matrix or merge child that names no generator, such as `{}`, or a
    matrix nested one level too deep. Argo CD fails with `child generator
    generated no parameters` (`matrix.go:129-131`, `merge.go:169-171`).
    Earlier drydock releases called this an unsupported generator and kept
    the Applications of the other generators.
  - A matrix or merge child that names more than one generator. Argo CD
    runs each of them first, so the error of one that fails comes before
    this one.
  - A generator selector that does not parse
    (`generator_spec_processor.go:34-37`).
  - A fasttemplate matrix whose two children set one key to two values.

  When several generators of one ApplicationSet fail, Argo CD keeps only the
  first error for its condition, so drydock stops at the first one and
  warns once. The warning carries the exact Argo CD text, with the wraps
  that the matrix and merge generators add, such as `failed to get params
  for second generator in the matrix generator: child generator generated
  no parameters`. drydock checks a matrix or merge child in the Argo CD order:
  first its selector, then its generator count. A merge runs every child, so
  an error in a later child fails the ApplicationSet also when an earlier
  child needs provider data that drydock does not have. The merge key checks
  (no merge keys, or a repeated merge key value in a child that drydock can
  run) also come before drydock reports such a merge as unsupported. A matrix runs its
  second child only for the param sets of its first child. When the first
  child gives none, the second child does not run, and nothing fails.
  Earlier drydock releases stopped the whole run for most of these errors.
  The render parity smoke checks each kind against Argo CD.
- Argo CD v3.6.1 generated-Application name validation: a generated
  Application with an empty `metadata.name` (including a
  `generateName`-only `templatePatch`) and every generated Application that
  shares a duplicated name are omitted with an
  `appset.application-validation-failed` warning; the ApplicationSet's other
  Applications still generate, and `--strict` promotes the warning. The
  render parity smoke checks both cases against Argo CD, and it compares the
  whole drydock Application list with the Argo CD list. The ApplicationSet
  controller then looks up the AppProject of each generated Application. An
  empty `spec.project` names no AppProject, so drydock omits that
  Application with the same warning, as Argo CD does. When the named
  AppProject does not exist, Argo CD does not create the generated
  Application either, and it keeps the other Applications of the
  ApplicationSet (`applicationset_controller.go:626-633`). This is a
  recorded divergence: drydock keeps that Application and renders it. The
  reason is that drydock does not know the AppProjects of the cluster
  offline. An AppProject can exist only in the cluster, or come from a
  render that discovery does not run, such as a remote chart or a Kustomize
  `namePrefix`. A wrong drop would hide the Application from `build` and
  `diff` without a trace. drydock reports the Application with the usual
  `project.missing` warning when the repository declares other AppProjects
  but not the named one. For a generated Application, the warning names the
  ApplicationSet and says that the ApplicationSet controller does not create
  or update the Application unless that AppProject exists in the cluster.
  The warning keeps its class, and `--project-diagnostics` controls it as
  before. The render parity smoke checks this divergence on a fixture
  directory of its own. drydock also skips the destination-cluster check of
  the ApplicationSet controller for generated Applications, because the
  clusters can exist only in the cluster. AppProject validation reports that
  later, as for any Application.
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
- A remote Kustomize Git ref at a branch name resolves from the
  remote-tracking ref of its clone first, so `--refresh-remote-resources`
  renders the remote's current commit also for the default branch, whose
  local branch in the clone never moves. A refresh prunes deleted branches.
- Remote Kustomize HTTP(S) file refs and Git refs through the remote-resource
  cache.
- Default Git clone/fetch into the Git cache for unmapped path sources missing
  from the local tree.
- `--repo-map URL=PATH` path-source resolution for local external checkouts.
- Explicit Git HTTPS bearer/basic auth, sent only to the hosts that
  `--git-auth-host` names. Git SSH auth from an explicit key file or from the
  ssh-agent and `~/.ssh` identities.
- Explicit HTTP(S) Helm bearer/basic auth.
- Explicit HTTP(S) remote file bearer/basic auth (`--remote-*`) for remote
  Kustomize resources, Helm value file URLs, and Helm `fileParameters` URLs,
  sent only to the hosts that `--remote-auth-host` names.
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
  `chart:` is an invalid spec upstream (`InvalidSpecError`), and drydock
  fails it the same way. Earlier drydock releases rendered the artifact
  root.
- A chart source `targetRevision` resolves as in the Argo CD v3.6.1
  repo-server (`newHelmClientResolveRevision`,
  `reposerver/repository/repository.go:3029-3066`), on HTTP(S) and
  schemeless OCI Helm repositories. A revision that parses as semver, also
  `1.2` or `v1.2.3`, stays as written. On an HTTP repository, helm then
  takes the index entry with that version, or else the highest one that
  matches it as a constraint, so `1.2` fetches the highest `1.2.x`. On an
  OCI repository, helm pulls the tag as written. Any other
  revision, such as `1.2.*`, `>=1.0.0 <2.0.0`, or `*`, is a constraint:
  drydock takes the highest index version or registry tag that matches it
  (`versions.MaxVersion`), with prereleases only for a constraint that
  names one, and fetches that version. The chart cache entry, the cache
  event revision, and `ARGOCD_APP_REVISION` hold the resolved version. A
  constraint that matches nothing fails with the repo-server's `invalid
  revision: version matching constraint not found in <n> tags`.
- drydock treats every revision that is not an exact full version
  (`1.2.3`, `v1.2.3`, `1.2.3-rc.1`) as a range: a constraint, and on an
  HTTP repository also a partial version such as `1.2` or `1`. It
  resolves such a revision on every online run, so it picks up a new
  release, and it caches the chart under the version that helm takes.
  `ARGOCD_APP_REVISION` stays as written for a revision that parses as
  semver, such as `1.2`, as in the repo-server. Each online resolve
  records the version list of the chart, with its fetch time, in the chart
  cache, and an offline run resolves the range from that record. Without a
  record, the offline run fails with `offline cache miss`. A render that
  resolved a range is never stored in the persistent render cache,
  because the next run can resolve another version.
- With `--chart-cache-max-age`, an offline resolve from a version list
  older than the max age, or of unknown age, is still used, as an expired
  chart is. It gives the `source.chart-cache-stale` warning, which names
  the version list and its fetch time, and the render is not stored. Every
  resolve, online or offline, tries to record its use of the list, and an
  age prune (`drydock cache prune --older-than`) of the chart cache removes
  the version lists that no run used within the duration. Recording a use
  is best effort. drydock skips it when the cache is read-only, when
  another process holds the lock on the lists for more than 5 seconds, or
  when the write fails, and the render goes on. A skipped use can let a
  later age prune remove a list that an offline run still needs.
- Recorded divergence from strict Argo CD v3.6.1: on a schemeless OCI
  repository, drydock lists the tags with helm's registry client, which
  keeps only strict semver tags. The repo-server lists every tag, so a
  constraint can also match a tag such as `v1.2.0` there.
- Kustomize `helmCharts` versions are not resolved as ranges. They must
  name an index version as written. A chart cache entry that a range or a
  partial version filled is named by the full version it resolved to, so a
  Kustomize `helmCharts` version such as `1.2` never reads it.
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
- A dependency is missing only when `charts/` holds no chart of its name, as
  helm template checks it (`action.CheckDependencies`, helm v4.3.1
  `pkg/action/install.go:839-869`). A vendored chart of another version
  than `Chart.yaml` or `Chart.lock` names still counts, so nothing is
  fetched and the vendored chart renders, as in Argo CD. Earlier drydock
  releases fetched the named version instead.
- When a dependency is missing, the repo-server runs `helm dependency build`
  once, and drydock does the same steps in a copy of the chart:
  - With a `Chart.lock`, its digest must match `Chart.yaml`, or the render
    fails with `the lock file (Chart.lock) is out of sync with the
    dependencies file (Chart.yaml). Please update the dependencies with
    'helm dependency update'`. Every locked repository must be one that
    helm knows. The repo-server adds only the `https` and `oci`
    repositories of `Chart.yaml` before the build, so an `http://`
    repository fails with `no repository definition for <url>. Please add
    the missing repos via 'helm repo add'`. Every locked dependency is
    fetched at its locked version. helm takes the first index entry with
    an archive URL whose version equals the locked one as semver
    (`findVersionedEntry`), so a partial version such as `1.2` in a
    hand-edited `Chart.lock` takes `1.2.0`, not the highest `1.2.x`.
  - Without a `Chart.lock`, each `Chart.yaml` version resolves against its
    repository, as `helm dependency update` resolves it: the highest index
    version with an archive URL, or the highest strict semver registry
    tag, that matches the version as a constraint. On an OCI repository, a
    version that parses as semver, such as `v1.2.3` or `1.2`, is pulled as
    a tag as written. An `http://` repository works here. With
    `--offline`, a range resolves from the version list that an earlier
    online run recorded.
  - A dependency without a repository must be a directory
    `charts/<name>` whose version matches the dependency version. Without
    a `Chart.lock`, helm first checks every dependency in order: a version
    that does not parse fails with `dependency "<name>" has an invalid
    version/constraint format`, and a missing path fails with `directory
    charts/<name> not found`, also when `charts/` holds the chart as an
    archive. Only then does a directory of another version fail with
    `dependency <name> at version <version> does not satisfy the constraint
    <constraint>`.
  - The build fetches every remote dependency, also one that is vendored,
    and removes each chart archive under `charts/` whose chart it did not
    fetch, also one that `Chart.yaml` does not name. It decides by the
    chart name inside the archive, not by the file name. A dependency
    without a repository stays.
- Behavior change: when one dependency is missing, drydock now fetches
  every remote dependency of the chart, also the vendored ones, as
  `helm dependency build` does in Argo CD. With `--offline`, the chart
  cache must then hold all of them. A chart with vendored remote
  dependencies and one missing dependency that rendered offline before can
  fail with `offline cache miss` until one online run fills the cache.
- Recorded divergences from strict Argo CD v3.6.1 in that build: drydock
  does not package a `file://` dependency or fetch a repository-alias
  dependency, so those must be vendored, and their vendored copy stays. It
  skips the lock digest check when a dependency names a repository alias,
  because helm hashes the URL of the repository that Argo CD added.
  When `charts/` holds a vendored directory of a chart that the build
  fetches, helm keeps both and picks one of them in Go map order. drydock
  replaces the directory with the fetched chart.
- Recorded divergence from strict Argo CD v3.6.1, in error text only: helm
  checks the dependencies in their order and fetches the remote ones
  between the checks of the local ones. drydock checks every dependency
  without a repository first, and fetches the remote dependencies after
  that. When a chart has both a failing local dependency and a failing
  remote dependency, drydock can report the local error where helm reports
  the remote one. Both sides fail the render.
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
- The `--remote-*` credentials are drydock-only behavior. Argo CD v3.6.1 has
  no such credentials. kustomize fetches a remote HTTP(S) resource with a
  plain `http.Client` `Get` and no credentials
  (`api/internal/loader/fileloader.go:320-345`). The repo-server passes Helm
  value file URLs and file parameters to `helm template` as `--values` and
  `--set-file` (`util/helm/cmd.go:450-457`), and helm v4.3.1 reads them with
  its HTTP getter without credentials (`pkg/cli/values/options.go:50,97,119-138`).
  For a remote Kustomize Git base, the `kustomize build` environment carries
  the Git credentials of the Application repository to the git program
  (`util/kustomize/kustomize.go:134-162`). drydock sends the `--remote-*`
  credentials only to a request whose scheme, host, and port match a
  `--remote-auth-host` entry. An entry without a scheme is HTTPS only, and
  `http://host[:port]` allows plain HTTP. A run that sets a `--remote-*`
  credential without `--remote-auth-host` fails before it fetches anything.
  On a redirect, drydock removes the credentials when a request of the chain
  has another scheme, host, or port than the first request. net/http alone
  keeps them on a redirect to another port, to a subdomain, and from HTTPS to
  HTTP. Credential values never reach cache keys, cache metadata, cache
  events, or errors.
- Recorded divergence from Argo CD v3.6.1: drydock refuses a Kustomize
  remote ref that names the file transport, such as
  `file:///srv/repo.git//base` or `git::file:///srv/repo.git//base`, in any
  letter case. The `file:` and `file::` forms fail the same way. The error
  says that the ref "uses the local file transport". kustomize v5.8.3, which
  Argo CD v3.6.1 runs in the repo-server, accepts a `file://` ref
  (`api/internal/git/repospec.go:269,322`) and clones it with
  `git fetch --depth=1` from the file system of the repo-server
  (`api/internal/git/cloner.go:16-35`). drydock runs on a workstation or a
  CI runner. Without the refusal, a repository could make drydock copy any
  Git repository on that machine into a render. The Git client for remote
  bases also refuses every URL that it would read from the local file
  system. The kind parity smoke pins this divergence with the
  `parity-decode-kustomize-file-remote-base` case.
- Recorded difference from Argo CD v3.6.1: drydock sends the `--git-*` HTTP
  credentials (`--git-username`, `--git-password`, `--git-bearer-token`) only
  to a Git URL whose scheme, host, and port match a `--git-auth-host` entry.
  An entry without a scheme is HTTPS only, and `http://host[:port]` allows
  plain HTTP. A run that sets an HTTP Git credential without
  `--git-auth-host` fails before it fetches anything. The rule covers
  Application sources, the revision lookup and refresh fetches of the Git
  cache, remote Kustomize Git bases, and submodules. go-git sends the
  credentials with the first request, before the server asks for them, so
  without the rule a repository can collect them with a URL on its own
  host. go-git v5.19.3 removes the credentials when a redirect leaves the
  scheme, host, and port of the first request, except for a redirect from
  `http` on port 80 to `https` on port 443 of the same host
  (`credentialsMayFollow` and `stripCredentials`,
  `plumbing/transport/http/common.go`). SSH keys need no entry: the server
  learns only the public key, and known_hosts verification makes sure of the
  host.
  - Application sources: Argo CD v3.6.1 gives each repository the
    credentials of the repository Secret whose URL is the same repository
    (`getRepositorySecret`, `util/db/repository_secrets.go:568-603`), else of
    the repository credential template with the longest URL prefix after
    `NormalizeGitURL` (`getRepositoryCredentialIndex`,
    `util/db/repository_secrets.go:619-634`, and `enrichCredsToRepo`,
    `util/db/repository.go:435-450`). A repository that matches neither is
    fetched without credentials (`getRepository`,
    `util/db/repository.go:151-165`). drydock has one set of HTTP Git
    credentials, so `--git-auth-host` takes the place of these URLs. Name
    the hosts of the repositories that your Argo CD instance has credentials
    for. drydock matches a host, not a URL path prefix. A redirect within the
    same host keeps the credentials in go-git, so drydock cannot enforce a
    path rule. The Argo CD prefix match is a plain string prefix, so a
    template for `https://github.com` also matches
    `https://github.com.example.net/x`. drydock does not copy that.
  - Remote Kustomize Git bases: the Argo CD repo-server runs
    `kustomize build` with the credentials of the Application repository in
    its environment (`util/kustomize/kustomize.go:134-162`). For HTTPS, the
    environment points git at the askpass server
    (`HTTPSCreds.Environ`, `util/git/creds.go:269-338`), which answers every
    request that has the nonce and does not look at the host
    (`GetCredentials`, `util/askpass/server.go:53-62`). So Argo CD gives the
    repository credentials to a remote base on any host. drydock sends the
    `--git-*` credentials to a remote base only on a `--git-auth-host` host.
    A remote base on another host that needs credentials fails to fetch in
    drydock, where Argo CD can fetch it.
  - Submodules keep the same-host rule below in addition.
  - A user name and password in a repository URL go only to the host of that
    URL. They take the place of the `--git-*` credentials when that host has
    no `--git-auth-host` entry.
  - drydock refuses a Git URL with `?`, `#`, or `\` between `://` and the
    first `/`, such as `https://a.example?@b.example/org/repo.git`. net/url
    reads the host `a.example` from it, and code that cuts at `/` reads
    `b.example`. The Git cache key and the clone URL use the net/url host.
    A `?` or `#` in the user name or password of a URL is part of the host
    part too, so it must be percent-encoded (`%3F`, `%23`).
    A cache entry whose origin names another repository than the request
    is cloned again, or is a cache miss with `--offline`, and every fetch
    goes to the URL of the request. This applies to Application sources,
    remote Kustomize Git bases, and submodule stores. Cache keys of other
    URLs do not change.
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

### Git Submodules and Git LFS

The Argo CD v3.6.1 repo-server runs `git submodule sync --recursive` and
`git submodule update --init --recursive` after each checkout when the
checkout has a `.gitmodules` file and `ARGOCD_GIT_MODULES_ENABLED` is on
(`util/git/client.go:783-818`). A gitlink is a submodule entry of a Git
tree. drydock matches the update:

- A tree without a `.gitmodules` file gets no submodule content. Each
  gitlink stays an empty directory.
- Each gitlink needs a module in `.gitmodules` with its path and a URL.
  Without one, the source fails, as `git submodule update` fails with "No
  url found for submodule path". Below the top level, git does not check
  for a `.gitmodules` file, so a nested gitlink without one fails too.
- When two modules name one path, the last one wins, as in git.
- A module with `update = none` stays an empty directory. git accepts
  exactly `checkout`, `merge`, `rebase`, and `none` as the update value.
  Any other value, such as `None` or a `!command`, fails.
- A relative URL (`../child.git`) resolves against the repository URL, as
  git resolves it against `origin`. A nested submodule resolves against
  the URL of its parent submodule.
- Only the `http`, `https`, `git`, and `ssh` transports work. The
  repo-server's git refuses `file` URLs, local paths, and remote helpers
  (`<scheme>::<address>`) for submodules (`protocol.file.allow=user`), and
  drydock refuses them too. drydock also refuses `git+ssh` and `ssh+git`,
  which its Git client does not support.
- drydock reads each URL with a model of git's rules and with its own Git
  client, and refuses it unless both find the same transport, user, host,
  port, and path. For example, git reads `x y:/path` as ssh to the host
  `x y`, and `host:2222:path` as ssh with the path `2222:path`. git
  matches `http`, `https`, `git`, and `ssh` in lower case only, so drydock
  refuses `HTTPS://`, and it reads `192.0.2.1::repo` as a remote helper.
  drydock also refuses forms that the two can read differently: a control
  character, more than one `@`, a percent sign, `#`, `?`, `\`, or a
  bracket in the host part, an empty or bad port, a host, user, or path
  that starts with `-`, and an empty path. In an `ssh` or `git` URL it
  refuses a bracket anywhere, because git reads `@[host]` after the host
  part as the host. The model is drydock's own, tested against git, and it
  refuses rather than guesses. Right before each clone or fetch, drydock
  checks the transport again.
- drydock checks each submodule URL with its user and password, as git
  reads it, and only then removes them. A user and password are allowed
  in an `http` or `https` URL only. An `ssh` URL may name a user but no
  password, and a `git` URL has no user. A percent sign is refused only in
  the host part, so an encoded password works, and drydock uses its
  decoded form. drydock never shows the user, the password, or a path in
  an error.
- drydock removes the user and password before it clones or fetches, so no
  cache entry stores a token. This matters for a relative URL of a
  repository cloned with a token, such as
  `https://gitlab-ci-token:TOKEN@host/...` in GitLab CI. drydock uses the
  removed user and password as credentials only within the credential
  scope of the repository. A nested relative URL resolves against its
  parent's URL with the user and password, as git resolves it, and gets
  the same treatment. A fetched Git source loses its own user and password
  from the stored URL the same way. drydock refuses a remote Kustomize Git
  ref with a user and password in an `http` URL, or a password in an
  `ssh` URL, before it fetches anything. The same removal runs for remote
  Kustomize bases as defense in depth.
- A cache entry that an older drydock wrote can hold a token in its
  `.git/config`. drydock removes it the next time it uses the entry. It
  rewrites the raw values of the file, every URL with a user or password
  in it, also `pushurl`, submodule URLs, and `url.<base>.insteadOf` rules,
  and replaces the file through a rename. To
  remove it at once, run `drydock cache delete --source git --all --yes`,
  and the same for `--source remote`.
- drydock refuses an `http` or `https` submodule URL with a query (`?`) or
  a fragment (`#`). A query can carry a token, such as
  `?private_token=...`, which the cache entry would store, and URLs that
  differ only in the query would share one entry. git fetches such a URL,
  so this is a recorded difference.
- A percent-encoded `http` or `https` path, such as
  `https://dev.azure.com/org/My%20Project/_git/repo`, works. drydock's Git
  client decodes the path and escapes it again before it sends it, so
  drydock refuses a path whose decoded form holds `?`, `#`, or `%`: the
  client would send another path than git.
- A pinned commit that no branch or tag holds is fetched by its SHA, as
  `git submodule update` does. This needs the network and a server that
  serves such a commit.
- drydock follows nested submodules to 8 levels and 256 submodules. git
  has no such limit. A deeper tree fails.

drydock reads submodule content from these places:

- A fetched Git source checks out its submodules into drydock's Git cache.
  Each submodule URL has one cache entry, a bare clone that holds every
  commit drydock fetched for it, so `--offline` reads it and many gitlinks
  into one repository share one clone. drydock locks the entry while it
  uses it, also for the two sides of a `--ref` diff, and clones into a
  temporary directory that moves into place only when the clone is
  complete. A store that holds a commit but lost an object of its tree is
  cloned again. A commit that the remote does not have, such as an
  unpushed commit that a pull request pins, fails without touching the
  store. Before a clone, drydock removes the temporary directories of that
  entry that nothing changed for one hour, which a killed process left
  behind. With `--offline`, a submodule
  commit that is not in the cache fails as an offline cache miss.
- A `--ref` or `--ref-orig` snapshot gets submodule content after drydock
  reads `argocd-cmd-params-cm` from it, and only when
  `reposerver.enable.git.submodule` is on. It reads each submodule commit
  from the repository's own `.git/modules` store first. That needs no
  network. Then it reads drydock's Git cache, and then, unless `--offline`
  is set, it fetches the submodule into that cache. drydock never writes
  to the repository or its `.git` directory.
- When drydock cannot find a submodule commit for a snapshot, for example
  offline, the submodule stays an empty directory, and drydock warns
  `source.submodule-missing` once for the run. A source whose path is in
  that submodule, or holds it, such as a recursive directory source of a
  parent directory, fails with an error that names the submodule and the
  offline cache miss. A source that fails for another reason gets
  the missing submodules in its error, because its inputs can be in one
  of them. The render cache keys the snapshot on the missing submodules.
- A remote Kustomize Git base checks out its submodules into the remote
  cache, as kustomize does, unless the ref says `?submodules=false`. A
  `git://` submodule URL fails there, because drydock's remote cache does
  not fetch `git://` URLs. kustomize's git fetches them, so this is a
  recorded difference.
- A working tree is rendered as it is. A submodule at another commit, or
  with local edits, is a working-tree preview like any other local edit.
  A submodule that is not initialized holds an empty directory, so drydock
  warns `render.submodule-not-initialized` for a source in it. Run
  `git submodule update --init --recursive`, or diff a Git ref with
  `--ref`.

The repo-server runs `git submodule update` with the credentials of the
repository. drydock sends a username, password, or bearer token only to a
submodule on the same host and port as the repository, and never from
`https` to `http`. SSH keys work for every host. This is a recorded
difference: a `.gitmodules` file cannot send drydock's HTTP credentials to
another host or in clear text. The submodule host must also match a
`--git-auth-host` entry.

The Git generator follows `applicationsetcontroller.enable.git.submodule`,
but a snapshot holds submodule content only when
`reposerver.enable.git.submodule` is on. When only the generator key is
on, the generator of a snapshot sees empty submodules and warns. When a
discovery run changes `reposerver.enable.git.submodule`, for example
through an `argocd-cmd-params-cm` that `--discover-kustomize` renders,
drydock fills or empties the snapshot again before it renders, so a
render never sees content that the final value turns off.

The repo-server also does these things, which drydock does not model:

- It keeps one checkout per repository between requests. With
  submodules off and no clean, an old submodule directory can stay.
- It runs `git submodule update` with `GIT_LFS_SKIP_SMUDGE=1` and runs
  `git lfs checkout` in the top repository only. drydock leaves Git LFS
  pointer files in submodules as pointers too.

A Git LFS pointer file is a small text file that names the content of a
large file by its SHA-256. When a repository Secret sets `enableLfs`, the
repo-server runs `git lfs fetch --all` and `git lfs checkout`, so the
checkout holds the content (`util/git/client.go:710-719` and `799-811`).
Without it, every git command runs with `GIT_LFS_SKIP_SMUDGE=1`, so the
checkout holds the pointer files. drydock reads `enableLfs` from a
repository Secret like `enableOCI`. drydock does not run git-lfs and does
not download Git LFS objects:

- A `--ref` snapshot gets the content of each pointer file from the
  repository's `.git/lfs/objects` store. drydock makes sure that the size
  and the SHA-256 of the object match the pointer. The render cache keys
  the snapshot on the objects that are missing, so a render runs again
  after `git lfs fetch`.
- A pointer file that drydock cannot fill stays a pointer, and drydock
  warns `render.lfs-pointer`. This happens for every pointer file of a
  fetched Git source, for a snapshot whose object is not in `.git/lfs`,
  and for a working-tree file that still holds the pointer of `HEAD`. For a
  fetched Git source this is a recorded difference: the repo-server
  downloads the objects, and drydock has no Git LFS network client.
- The render cache keys a working-tree file on its bytes on disk when they
  differ from the committed blob. `git status` reports a file that git-lfs
  smudged as unchanged, but drydock compares the bytes itself, so a cache
  entry for the pointer is never served for the content, or the reverse.
- drydock never writes a working tree. A working tree with Git LFS content
  from `git lfs pull` renders that content also without `enableLfs`, where
  the repo-server renders the pointer. This is a working-tree preview
  difference.

The render parity smoke renders a directory in a submodule and a Kustomize
overlay whose base is in a submodule. Its git daemon cannot serve Git LFS,
so unit tests cover the Git LFS cases.

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
  in Helm parameters, value-file names, and `fileParameters` paths (see the
  `$ref` rules below for a path that starts with `$`), Kustomize
  images, labels, and `commonAnnotationsEnvsubst` annotations, and Jsonnet ext
  vars and TLAs, with
  the repo-server's values: `ARGOCD_APP_NAME` is the Application instance name
  (`<namespace>_<name>` for Applications outside the Argo CD controller
  namespace, see "The Argo CD controller namespace") and
  `ARGOCD_APP_NAMESPACE` is `spec.destination.namespace`.
- Source type detection as in the Argo CD v3.6.1 repo-server
  (`discovery.Discover`, `util/app/discovery/discovery.go:50-70`) for a
  source without an explicit type. Argo CD reads the file names directly in
  the source directory in name order. Any name that ends in `Chart.yaml`
  marks a Helm chart, for example `traefik-HelmChart.yaml`. A kustomization
  file name marks Kustomize. The last match wins. So `Chart.yaml` before
  `kustomization.yaml` gives Kustomize, and `kustomization.yaml` before
  `traefik-HelmChart.yaml` gives Helm. A directory never matches, and a
  symlink counts by its own name. Without a match, the source is a
  directory. Helm then fails on a directory that has no `Chart.yaml`, as in
  Argo CD. Earlier drydock releases checked for a kustomization file first
  and then for `Chart.yaml` only.
- The `argocd-cm` keys `helm.enable`, `kustomize.enable`, and
  `jsonnet.enable` as in Argo CD v3.6.1 (`GetEnabledSourceTypes`,
  `util/settings/settings.go:621-625` and `1030-1047`). A missing or empty
  key keeps the tool on. Any other value than the exact string `true` turns
  it off, so `"True"` and `"no"` turn it off. When Helm or Kustomize is off,
  a source of that type renders as a plain directory, also when the spec
  names the type with a `helm` or `kustomize` block, and the settings of that
  block have no effect (`GetAppSourceType`,
  `reposerver/repository/repository.go:1955-1975`). A block that does not
  parse, such as `helm.values: "- not a map"` or a `kubeVersion` that is not
  a version, then does not fail the source, because Argo CD never reads it.
  A chart from a chart repository follows the same rule: Argo CD extracts
  the chart and picks the type in the chart directory (lines 451-470), so
  with Helm off it renders that directory as a plain directory. When Jsonnet
  is off, a directory source skips its `.jsonnet` files (`findManifests`,
  line 2019), and its Jsonnet libraries and imports are no render cache or
  changed-only inputs. Config management plugins are never turned off, and
  the native argocd-vault-plugin compatibility path ignores the keys too,
  because the plugin sidecar runs its own tools. drydock reads the keys from
  `argocd-cm`, from the `configs.cm` values of the argo-cd Helm chart, and
  from a rendered Argo CD install. The keys are part of the render settings
  signature, so a change to them renders every Application again and
  changes the persistent render cache keys. The defaults keep the signature
  of settings without the keys. Earlier drydock releases did not read the
  keys.
- The Helm release name and namespace as Argo CD v3.6.1 passes them to
  `helm template` (`util/helm/cmd.go:428-440`). The release name is
  `helm.releaseName`, or else the Application name. Argo CD passes it as
  `--name-template`, so Helm reads it as a Go template with the sprig
  functions, and a template that does not parse fails the source. The
  namespace is `helm.namespace`, or else the destination namespace. When
  both are empty, Argo CD passes no `--namespace`, and Helm picks the
  namespace from the repo-server environment (`EnvSettings.Namespace`,
  `pkg/cli/environment.go:276-286` at Helm v4.3.1, and client-go
  `DirectClientConfig.Namespace` and `DeferredLoadingClientConfig.Namespace`).
  The rule, in order:
  1. `HELM_NAMESPACE` in the repo-server container, when it is not empty.
     It wins also over `KUBECONFIG`, and drydock then reads nothing else.
  2. A `KUBECONFIG` that is not empty names a kubeconfig file, which drydock
     cannot read. drydock warns and goes on as if it were unset. An empty
     `KUBECONFIG` is unset. The repo-server has no kubeconfig of its own.
  3. Without a kubeconfig, a `HELM_KUBECONTEXT` that is not empty names a
     context that does not exist, so client-go fails and Helm uses
     `default` (`ConfirmUsable`, `client_config.go:441-446`). This also
     applies when drydock goes on without a `KUBECONFIG` that it cannot
     read.
  4. When a service account token is mounted, `POD_NAMESPACE`, else the
     namespace file next to the token, which is the pod namespace. The
     kubelet mounts both when the pod spec `automountServiceAccountToken` is
     `true`, or unset while the ServiceAccount does not set it to `false`. A
     volume that the container mounts at the token path counts only when it
     holds a file named `token`, because client-go uses the in-cluster
     config only with the token (`inClusterClientConfig.Possible`,
     `client_config.go:666-671`). A Secret volume without `items` holds
     both files, as a service account token Secret does. With `items`, and
     for the `secret`, `configMap`, and `downwardAPI` sources of a projected
     volume, a file counts only when an item has its path, and a
     `serviceAccountToken` source gives the token at its path. Without the
     namespace file, the namespace is `default`
     (`inClusterClientConfig.Namespace`, `client_config.go:646-661`).
  5. Else `default`.

  The upstream `install.yaml` sets `automountServiceAccountToken: false`,
  so the namespace is `default`. The argo-cd Helm chart sets
  `repoServer.automountServiceAccountToken: true` by default, so the
  namespace is the Argo CD namespace.

  drydock reads the repo-server Deployment and its ServiceAccount from these
  installs:
  - The render of an Application on this cluster that installs Argo CD. An
    Application whose destination is another cluster installs another Argo
    CD, and its repo-server does not count. A repo-server without a
    namespace gets the destination namespace.
  - A committed file, with the rules for a committed `argocd-cm`: only a
    file that also holds Argo CD objects or settings, such as the upstream
    `install.yaml`, and not a file that a rendered install read. An
    Application renders the file when one of its sources renders this
    repository (not an `oci://` artifact and not a chart from a chart
    repository) and reads the file: a Kustomize source whose kustomization
    graph reads it, also a base outside the source path, or a directory
    source with the file directly in its path, or below it with
    `directory.recurse`, when its `include` and `exclude` globs let the
    file through. A Helm chart renders its templates, and a plugin is not
    known, so neither counts. For a Kustomize source, kustomize applies the
    `namespace` of every kustomization on the way from the source to the
    file, over the object's own, and the outermost one wins. Argo CD sets
    the `kustomize.namespace` of the Application on the source
    kustomization, so it wins over all of them, but only while the source
    renders with Kustomize, not with `kustomize.enable` off. Without a
    kustomization namespace, the object's own namespace counts, then the
    destination namespace of such an Application. A file that no
    Application renders gets the `namespace` of the kustomization in its
    own directory that lists it; drydock does not search the repository for
    kustomizations further up. A file that only Applications on other
    clusters render does not count. When drydock cannot tell the namespace of a Deployment
    or a ServiceAccount, it assumes the Argo CD namespace, and it warns for
    the Deployment.

  A Deployment without containers is a patch and does not count. A
  repo-server counts when it comes from the same install as a discovered
  `argocd-cm`, or when it runs in the Argo CD namespace. When several
  count, drydock uses the first in this order: from the same install as an
  `argocd-cm`, then rendered before committed, then by path and document
  index. A repo-server that gives another namespace adds a warning. An
  `HELM_NAMESPACE`, `HELM_KUBECONTEXT`, `POD_NAMESPACE`, or `KUBECONFIG`
  from a ConfigMap, a Secret, another `valueFrom`, `envFrom`, or a `$(VAR)`
  reference also adds a warning that names the repo-server and the
  namespace that drydock uses, unless a literal `HELM_NAMESPACE` decides
  the namespace. drydock keeps only the kind of such a source, never its
  value. Of each pod volume it keeps only the name and whether the volume
  holds a `token` file and a `namespace` file. Of a ServiceAccount it keeps
  only the name, namespace, labels, and `automountServiceAccountToken`. Without a
  repo-server that counts, drydock uses `default`. The namespace is part of
  the render settings signature, so a change renders every Application
  again. Earlier drydock releases used the release name as written and an
  empty `.Release.Namespace`.
- The checks of `helm template` (Helm v4.3.1) that come before the
  templates render, in Helm's order, with Helm's error text: the release
  name must have at most 53 characters and match the Helm name pattern
  (`release name check failed: release name "...": invalid release name,
  ...`). A long Application name without `helm.releaseName` fails too. A
  chart must be an application chart (`library charts are not
  installable`), and its `apiVersion` must be `v1` or `v2` (`invalid chart
  apiVersion`). The chart `kubeVersion` constraint must match the kube
  version (`chart requires kubeVersion: ... which is incompatible with
  Kubernetes ...`). Argo CD checks the version of the destination cluster,
  which drydock does not know. So drydock checks the constraint only
  against an explicit version: a per-app `helm.kubeVersion` or
  `kustomize.kubeVersion`, a `helmCharts` `kubeVersion`, or
  `--kube-version`. Without one, it skips the check, and it does not fail
  a chart that the cluster would accept. Argo CD also fails a `helm.version`
  other than empty, `v3`, or `v4` (`helm version 'v2' is not supported`).
  Earlier drydock releases rendered these sources.
- A per-app `helm.kubeVersion` or `kustomize.kubeVersion` wins over
  `--kube-version`. Argo CD uses the per-app value and falls back to the
  cluster version (`ApplicationSource.GetKubeVersionOrDefault`,
  `pkg/apis/application/v1alpha1/types.go:385-398`), and `--kube-version`
  stands for the cluster version. A `--kube-version` that does not parse
  fails every run, also when every Application sets its own `kubeVersion`.
  Earlier drydock releases let `--kube-version` win.
- Per-app `helm.apiVersions` or `kustomize.apiVersions` replace the
  `--api-versions` values, which stand for the API versions of the cluster
  (`ApplicationSource.GetAPIVersionsOrDefault`,
  `pkg/apis/application/v1alpha1/types.go:400-412`). The per-app values
  keep their order, because the order of `--helm-api-versions` matters to
  `helmCharts`. Helm splits each API version at commas, as CSV, for a Helm
  source and for `helmCharts`. Earlier drydock releases added the two lists
  together and did not split the values.
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
- Helm `valueFiles` and `fileParameters` paths resolve as in the Argo CD
  v3.6.1 repo-server (`getResolvedValueFiles` and
  `ResolveValueFilePathOrUrl`, `reposerver/repository/repository.go` and
  `util/io/path/resolved.go`), for every entry, also an overridden file
  parameter, and also for entries that come from an `.argocd-source.yaml`
  file. A relative path is relative to the source path, and a path that
  starts with `/` is relative to the repository root.
  A path is a `$ref` path when its first segment, before env substitution,
  names a ref source, as in `getReferencedSourceName`. Its rest is
  env-substituted and is relative to the root of the ref source, so a bare
  `$values` or `$values/` resolves to that root and fails. When the path
  itself is a symlink, the resolver follows it, up to ten levels, and joins
  a relative target to the directory of the path as text. So a symlink to a
  file in the repository works, and so does a path through a symlinked
  directory. A dangling symlink resolves to its missing target, which fails
  only when helm reads it. The result must be in the repository as text,
  and must not be the repository root:
  - For a file parameter, `error resolving helm value file path: file
    '<path>' resolved to outside repository root`, or for a `$ref` path
    `error resolving set-file path: error resolving value file path: file
    '/<path>' resolved to outside repository root`.
  - For a value file, `error resolving helm value files: error resolving
    value file path: file '<path>' resolved to outside repository root`,
    with one more `error resolving value file path:` for a `$ref` path.
  - `internal error: failed to resolve path. Check logs for more details`
    for a symlink loop or a path that resolves to the repository root.
    drydock adds the cause after this text.
- The repo-server reads each resolved value file once, at its first place
  in the list, so `[a, b, a]` reads `a` and then `b`, and `b` wins. Two
  entries that resolve to the same path count as one, also when one of them
  is a symlink to the other. A glob expands with doublestar in name order
  and leaves out the files that the list names explicitly, which keep their
  explicit place. Each glob match must stay in the repository after its
  symlinks resolve, or the render fails with `glob pattern "<pattern>":
  glob match "<match>" resolved to outside repository root`. drydock names
  the match relative to the root, where the repo-server names its checkout
  path. A glob that matches nothing fails with `values file glob
  "<pattern>" matched no files`, and is skipped with
  `ignoreMissingValueFiles`. A value file URL with an allowed scheme is
  fetched through the remote-resource cache. A URL with another scheme
  fails with `error resolving value file path: the URL scheme '<scheme>'
  is not allowed`.
- The value files of a Kustomize `helmCharts` entry follow kustomize, not
  this resolver.
- Because the resolver joins a symlink target to the path as written, a
  relative target under a symlinked directory resolves from that directory,
  not from the real directory of the symlink. A target with `..` can then
  name a different file, or leave the repository as text and fail, where
  the out-of-bounds symlink check passes. drydock does the same. The parity
  smoke pins both cases.
- A Helm value file or file parameter path that starts with a `$` segment
  that names no ref source follows the repo-server's
  `resolveReferencedSources`. For an Application with `sources`, it fails
  with `source referenced "<segment>", which is not one of the available
  sources (<refs>)`, or with `source referenced "<segment>", but no source
  has a 'ref' field defined`. drydock lists the refs sorted, where the
  repo-server uses Go map order. For an Application with one `source`, the
  repo-server skips that check, so the path is a local path that is
  env-substituted: `$$values/a.yaml` is the file `$values/a.yaml`, and
  `$ARGOCD_APP_NAME/values.yaml` names a directory after the Application.
  drydock checks an Application with
  `sources` against the merged source after `.argocd-source*.yaml`, while
  the repo-server checks the spec, so a `$` segment that only an override
  file sets fails in drydock but is a local path in Argo CD.
- helm `--set-file` parses `name=<path>` and drops a backslash before any
  character, so it can read a file other than the resolved path, such as
  `ab.txt` for `a\b.txt`. drydock reads the same file, and its cache key
  and changed-only selection track that file.
- drydock reads a value file or a file parameter only when the real path
  of the file that helm reads is in the repository and outside `.git`. A
  path through a symlinked directory that leaves the repository fails with
  `makes helm read a file outside the repository, which drydock never
  reads`. The repo-server reads such a file, but its out-of-bounds symlink
  check fails the repository first unless `reposerver.allow.oob.symlinks` is
  `true`. A value file path that resolves into `.git` fails with `enters a
  .git directory`. An overridden file parameter is never read, so an
  overridden path in `.git` does not fail.
- The repo-server resolves the value file paths, then every file parameter
  path, and only then runs helm, which reads the value files and the file
  parameters before it loads the chart. drydock keeps this order. For
  example, a file parameter that does not resolve wins over a value file
  that is missing or does not parse, a missing value file wins over a file
  parameter that helm cannot read, and a value file that does not parse
  wins over a chart that does not load.
- Recorded divergence from strict Argo CD v3.6.1: helm `--set-file` parses
  `name=<path>`, so a file parameter name that holds `=` or `,` can make
  helm read another path, relative to its working directory. drydock fails
  with `helm --set-file reads "<path>", which is not the resolved path`.
- Recorded divergence from strict Argo CD v3.6.1: when the path as written
  is outside the repository, drydock fails at once with the repo-server's
  `resolved to outside repository root` error and does not read its link
  text. The repo-server reads it and accepts a symlink there that points
  back into its checkout. For the later links of a chain, drydock reads the
  link text as the repo-server does, also outside the repository, but the
  result must still be in the repository.
- A `fileParameters` URL with an allowed scheme is fetched as helm
  `--set-file` fetches it in Argo CD, through the remote-resource cache, so
  `--offline` reads only a cached copy. A file parameter or value file URL
  can have any file name, as in helm. A URL with a query or a fragment
  still fails in drydock. An overridden URL is never fetched.
  A scheme that `helm.valuesFileSchemes` allows but that is not `http` or
  `https` fails in drydock, as for a value file URL.
- Recorded divergence from strict Argo CD v3.6.1: helm `--set-file` drops a
  backslash in a URL as in a path. drydock then fails with `helm --set-file
  reads "<url>", which is not the resolved path` instead of fetching the
  other URL.
- `.argocd-source.yaml` and `.argocd-source-<app>.yaml` overrides, where
  `<app>` is the Application instance name like the repo-server's lookup: an
  Application outside the controller namespace reads
  `.argocd-source-<namespace>_<name>.yaml`, and a bare-name file is ignored.
- `ARGOCD_APP_REVISION`, `ARGOCD_APP_REVISION_SHORT`, and
  `ARGOCD_APP_REVISION_SHORT_8` carry the revision that the Argo CD v3.6.1
  repo-server resolves for each rendered source. `_SHORT` and `_SHORT_8` are
  the first 7 and 8 bytes of that value, whatever it is. The variables reach
  every place the build environment reaches: Helm parameters, value file
  paths and file parameter paths, Kustomize images, `commonLabels` and
  `commonAnnotations` (with `commonAnnotationsEnvsubst`), Jsonnet extVars and
  TLAs, and the environment and `plugin.env` substitution of config
  management plugins. In a multi-source Application each rendered source
  gets the revision of its own repository, and a `$ref` source adds nothing.
  drydock resolves the value offline as follows:
  - A Git source fetched into the Git cache gets the commit SHA that drydock
    checked out. A `targetRevision` that is also the name of a branch or
    tag checks out that ref, also a short hex name such as `c` or `2024`,
    because `ls-remote` matches ref names before it takes a hex revision as
    an abbreviated SHA (`util/git/client.go:991-1022`). A hex name shorter
    than 7 digits that names no ref does not resolve, as in `ls-remote`. An
    abbreviated SHA must match a single object, of any type, and that
    object must be a commit or an annotated tag, else the source fails as
    in `git checkout`, which reports "short object ID ... is ambiguous"
    when a commit and a tree or blob share the abbreviation. drydock looks
    for a branch first under the remote-tracking refs of its clone, which
    every fetch and `--refresh-git` update, and then under the local branch
    that the clone created. A `--refresh-git` fetch prunes the branches
    that the remote deleted, so such a branch stops resolving, as in
    `ls-remote`. It uses the remote whose URL is the `repoURL` of the
    source, compared in lower case (also a `.GIT` suffix) as Argo CD
    compares repository URLs, and every remote in name order when no
    remote has that URL.
  - A source that renders from the `--path` tree (a path that exists there,
    or a repository that drydock maps to the tree) gets the HEAD commit of
    that tree, also when the worktree has uncommitted changes. drydock
    renders the tree that is on disk whatever branch `targetRevision` names,
    so it reports the commit of that tree. Each side of a `--ref` or
    `--ref-orig` diff gets the commit of its snapshot.
  - A `--repo-map` source gets the HEAD commit of the mapped directory.
  - A `--path` or `--repo-map` source whose `targetRevision` pins a commit by
    SHA gets what Argo CD reports for that pin, although drydock still
    renders the local tree. A full SHA gives the SHA in lower case for a
    single-source Application (`git rev-parse HEAD` after the checkout) and
    the SHA as written for a multi-source Application. A hex revision that
    is also a branch or tag name gives the commit of that ref. Any other
    abbreviated SHA gives, for a single-source Application, the commit that
    it names in the local repository (an abbreviated annotated tag object
    gives the commit the tag points to, as `git checkout` does), or the
    HEAD commit when no object in the local repository matches. For a
    multi-source Application it stays as written. An abbreviation that
    matches more than one object of any type, or only a tree or blob, fails
    the source, as `git checkout` fails for it in the repo-server. In a
    local tree drydock looks up the local branch first, which is what you
    have checked out, then the remote-tracking refs.
  - The targetRevision comes from the spec. An `.argocd-source` override
    cannot change it: the repo-server keeps the spec `targetRevision` when it
    merges the override (`repository.go:1946-1949`), and so does drydock,
    in the render and in the settings discovery of an Argo CD install.
  - A Git source of a multi-source Application gets what `git ls-remote`
    resolution gives, as Argo CD does. The repo-server passes the
    `ls-remote` result as the revision there and does not read the
    checked-out commit (`repository.go:516-518`, `util/git/client.go:961-1027`).
    So an abbreviated SHA stays as written, also one of an annotated tag
    object, and a full SHA also stays as written, in its letter case. HEAD, a branch, a lightweight tag
    and an annotated tag give the commit SHA, because Argo CD's
    `listRemote` peels annotated tags (`util/git/workaround.go:86-96`). A
    single-source Application gets the checked-out commit SHA in all of
    these cases (`git rev-parse HEAD`, `repository.go:519-525`). Any
    `spec.sources` list counts as multi-source, also with one entry.
    drydock reads branch and tag names from the Git cache clone, the
    repository of a `--ref` snapshot, the `--path` tree, or the
    `--repo-map` directory, and branches from the remote whose URL is the
    `repoURL` of the source.
  - An OCI artifact source gets the manifest digest (`sha256:...`), so
    `_SHORT` is `sha256:` and `_SHORT_8` is `sha256:` plus one hex digit,
    as in Argo CD.
  - A chart from a Helm repository gets the chart version: the
    `targetRevision` as written when it parses as semver, and the resolved
    version for a version range.
- Recorded divergences from strict Argo CD v3.6.1 for these variables:
  - When the `--path` tree or a `--repo-map` directory is not the top of a
    Git repository (for example a subdirectory of a repository), or HEAD
    names no commit, drydock cannot tell the commit and keeps the spec
    `targetRevision`.
  - A source that renders from the `--path` tree reports the local commit.
    Argo CD reports the commit that `targetRevision` names in the remote
    repository, which is a different commit until the change is pushed and,
    after a merge, is the merge commit.
  - A source that renders from the `--path` tree or a `--repo-map`
    directory does not need its `targetRevision` to exist in the remote.
    For a tag or a branch that exists only locally and was never pushed,
    drydock reports the local commit, where Argo CD cannot resolve the
    revision and fails the Application.
  - Known nondeterminism in Argo CD: for a name that is both a branch and
    a tag, `ls-remote` takes the first match in a ref list without a fixed
    order, so Argo CD can resolve either. drydock always takes the branch.
  - A local tree, a `--repo-map` directory and the repository of a `--ref`
    snapshot can hold objects that the repo-server never sees: local-only
    commits, and dangling objects that a fetch or `git gc` would not keep.
    For such a tree, drydock can resolve an abbreviated SHA pin to a local
    commit, or find it ambiguous, where the repo-server finds no such
    commit or a single one.
- The persistent render cache keys a local source on its input files and not
  on the commit, so a commit that leaves the inputs unchanged keeps the
  cached entry. A source whose spec names one of the `ARGOCD_APP_REVISION`
  variables, also through a merged `.argocd-source` override, also keys on
  the value that the variable gets when the key does not hold it already:
  - a local source always keys on it, so with the HEAD commit each new
    commit renders it again, and with a SHA pin it keeps its key;
  - a fetched Git source keys on its commit, and also on the value when the
    value differs from that commit: an abbreviated SHA, or a full SHA in
    upper case, of a multi-source Application;
  - a `--ref` snapshot source keys on the snapshot commit, and also on the
    value when the value differs from that commit, such as a SHA pin of
    another commit.

  The keys of all other sources do not change.
- A diff between two commits shows a change in the objects of a source that
  reads one of these variables even when no input changed, as Argo CD does
  after the new commit syncs. `--changed-only` selects every Application
  whose spec reads one of the variables, in its spec or through a merged
  `.argocd-source` override on either side, and every Application with a
  config management plugin source that the run renders with a command,
  whose environment always holds the variables, whenever the two sides
  render different commits. A plugin source renders with a command only
  through a trusted exec or container policy with `--enable-plugins` whose
  checks before the command pass (source path, parameters, and
  `applicationEnv.allow`; the render and the selection share them), or an
  injected plugin renderer. A plugin found by discovery is matched in the
  same root the render uses, such as a `--repo-map` directory. Native
  policy engines, the built-in
  argocd-vault-plugin compatibility and the native Kustomize CMP render in
  process without the variables, and a plugin source that drydock cannot
  render stays out of the selection, so it does not fail the diff with
  `plugin.unsupported`. It also selects the Applications they render, and
  warns `diff.changed-only-app-revision` with their number. `--strict` does not fail on that warning. When no relevant path
  changed at all, the changed-only diff is still skipped before any
  Application is listed, so these Applications are not rendered then.
- Directory rendering honors `+argocd:skip-file-rendering`, walks hidden
  directories when `recurse` is enabled, renders Jsonnet through native Go
  libraries with extVars, TLAs, code mode, env substitution, and repo-relative
  libs, confines Jsonnet imports to the repository root as the Argo CD
  repo-server does from v3.5.4, and skips drydock cache metadata sidecars.
- Jsonnet extVar and TLA values get the env substitution once more for each
  Jsonnet file that is evaluated, as in the Argo CD v3.6.1 repo-server.
  `makeJsonnetVM` writes each substituted value back into the request, so
  the k-th Jsonnet file that `findManifests` evaluates, in walk order, gets
  each value substituted k times (reposerver/repository/repository.go:
  2005-2051 and 2281-2302). With an extVar `$$ARGOCD_APP_NAME`, the first
  file gets `$ARGOCD_APP_NAME` and the second gets the Application name. A
  file that the walk, the extension filter, `include`, `exclude`, the skip
  marker, or `jsonnet.enable` leaves out is not evaluated, so it does not
  count. The values in the spec do not change.
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
  also reads the repo-server Deployment of the install, so it sees a limit
  that the chart sets through `repoServer.extraArgs`
  (`--max-combined-directory-manifests-size`) or `repoServer.env` (see
  "Settings from the Argo CD workloads"). An empty value in
  `argocd-cmd-params-cm` or in the environment means `10M`, and a value that is not a Kubernetes quantity,
  spaces around it included, fails the build, as it stops the repo-server at
  startup. Earlier drydock releases had no limit.
- drydock reads `applicationsetcontroller.enable.progressive.syncs` the same
  way: from a discovered or rendered `argocd-cmd-params-cm`, and from the
  ApplicationSet controller Deployment of the install. The chart's
  `configs.params` sets the key in `argocd-cmd-params-cm`. The chart can
  also set the flag on the Deployment, through `applicationSet.extraArgs`
  (`--enable-progressive-syncs`), or through `applicationSet.extraEnv`
  (`ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS`). See
  "Settings from the Argo CD workloads".
- drydock reads `applicationsetcontroller.enable.new.git.file.globbing` and
  `reposerver.include.hidden.directories` the same way, from a discovered or
  rendered `argocd-cmd-params-cm` and the workloads, as Argo CD parses them: `true` or
  `false` in any case, and `false` for any other value. The first chooses
  how the Git files generator matches its patterns. The second adds hidden
  directories to the Git and OCI directory generators. Earlier drydock
  releases read both keys as metadata only.
- drydock reads `reposerver.enable.git.submodule` and
  `applicationsetcontroller.enable.git.submodule` the same way. Argo CD
  passes both to `ARGOCD_GIT_MODULES_ENABLED`, which is `true` unless the
  value is `false` in any case. The first decides whether a rendered Git
  source holds submodule content. The second decides whether the Git
  generator sees it. Earlier drydock releases read the first key as
  metadata only and never fetched submodule content. See
  [Git Submodules and Git LFS](#git-submodules-and-git-lfs).
- Settings from the Argo CD workloads. Argo CD v3.6.1 reads each
  `argocd-cmd-params-cm` key only from an environment variable of the
  program that uses it. The install maps the key to the variable with a
  `configMapKeyRef`. A command-line flag, where one exists, wins over the
  variable, because the program reads the variable only as the default of
  the flag (`util/env/env.go:155-195`). drydock reads these keys this way:

  | Key | Program | Variable | Flag |
  | --- | --- | --- | --- |
  | `reposerver.max.combined.directory.manifests.size` | repo-server | `ARGOCD_REPO_SERVER_MAX_COMBINED_DIRECTORY_MANIFESTS_SIZE` | `--max-combined-directory-manifests-size` |
  | `reposerver.allow.oob.symlinks` | repo-server | `ARGOCD_REPO_SERVER_ALLOW_OUT_OF_BOUNDS_SYMLINKS` | `--allow-oob-symlinks` |
  | `reposerver.include.hidden.directories` | repo-server | `ARGOCD_REPO_SERVER_INCLUDE_HIDDEN_DIRECTORIES` | `--include-hidden-directories` |
  | `reposerver.enable.git.submodule` | repo-server | `ARGOCD_GIT_MODULES_ENABLED` | none |
  | `application.namespaces` | application controller | `ARGOCD_APPLICATION_NAMESPACES` | `--application-namespaces` |
  | `controller.ignore.normalizer.jq.timeout` | application controller | `ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT` | `--ignore-normalizer-jq-execution-timeout-seconds` |
  | `applicationsetcontroller.namespaces` | ApplicationSet controller | `ARGOCD_APPLICATIONSET_CONTROLLER_NAMESPACES` | `--applicationset-namespaces` |
  | `applicationsetcontroller.enable.progressive.syncs` | ApplicationSet controller | `ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_PROGRESSIVE_SYNCS` | `--enable-progressive-syncs` |
  | `applicationsetcontroller.enable.new.git.file.globbing` | ApplicationSet controller | `ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_NEW_GIT_FILE_GLOBBING` | `--enable-new-git-file-globbing` |
  | `applicationsetcontroller.enable.git.submodule` | ApplicationSet controller | `ARGOCD_GIT_MODULES_ENABLED` | none |

  The flags and variables are in
  `cmd/argocd-repo-server/commands/argocd_repo_server.go:50`, `:264`,
  `:266`, and `:274`,
  `cmd/argocd-application-controller/commands/argocd_application_controller.go:286`
  and `:300`, and
  `cmd/argocd-applicationset-controller/commands/applicationset_controller.go:55`,
  `:316`, `:327`, and `:329`. drydock reads the workload of each program:
  the repo-server Deployment, the application controller StatefulSet or
  Deployment, and the ApplicationSet controller Deployment. It finds them
  by the upstream names, such as `argocd-repo-server`, or by the
  `app.kubernetes.io/component` and `app.kubernetes.io/part-of: argocd`
  labels of the argo-cd chart. A workload counts by the rules of the
  repo-server for the Helm namespace: it comes from the same install as an
  `argocd-cm`, or it runs in the Argo CD controller namespace. It must also
  have a container whose command line runs the program, and drydock reads
  only that container. So a CMP sidecar or a Kustomize patch without the
  program does not count. A committed file that a kustomization in its
  directory or in a directory above it lists under `patches`,
  `patchesStrategicMerge`, or `patchesJson6902` does not count either,
  because kustomize merges it into a workload and it holds only part of the
  env list. For such files drydock keeps the `argocd-cmd-params-cm` values
  and does not warn. A render of
  an install keeps these workloads, also when drydock renders it for its
  settings only. Without a workload, drydock reads the keys from
  `argocd-cmd-params-cm`, as earlier releases did.

  drydock reads the program container of the workload as the kubelet and
  the program do:
  - The command line is `command` and then `args`. Its first item must run
    the program, such as `/usr/local/bin/argocd-repo-server`. drydock parses
    the flags as pflag does. A boolean flag is true without a value, takes
    a value only after `=`, and reads it with `strconv.ParseBool`. Any other
    flag takes the text after `=` or the next argument. `--` ends the
    flags. A namespace flag adds the fields of each value, read as one CSV
    line. The last value of any other flag wins. drydock never keeps the
    command line itself, only the values of these flags.
  - The kubelet reads `envFrom` first and then `env` in order, and a later
    entry with the same name wins. A `configMapKeyRef` with
    `optional: true` whose key is missing sets nothing
    (`makeEnvironmentVariables`, `pkg/kubelet/kubelet_pods.go:795-987` at
    Kubernetes v1.37.0). So the order of the chart template decides which
    value wins. In the argo-cd chart 10.14.0, `repoServer.env` and
    `applicationSet.extraEnv` come before the references to
    `argocd-cmd-params-cm`. So a literal value there wins only when
    `argocd-cmd-params-cm` does not have the key. In the application
    controller StatefulSet, `controller.env` comes after the references, so
    it wins. The chart puts `extraArgs` after its own arguments.
  - A `configMapKeyRef` to `argocd-cmd-params-cm` reads the key from the
    `argocd-cmd-params-cm` that drydock uses. A `configMapKeyRef` to
    another ConfigMap reads the key from that ConfigMap when the same
    render holds it.
  - An empty value is the default of the program, as for
    `argocd-cmd-params-cm`.
  - An `envFrom` entry sets each key of its ConfigMap or Secret with its
    `prefix` in front. So an entry can set a setting variable only when
    the variable name starts with the prefix. An entry without a prefix
    can set any variable.

  drydock warns, and reads the variable as if the entry were not there,
  when it cannot know a value offline: a `secretKeyRef` (drydock never
  reads a Secret value), an `envFrom` entry that can set the variable, a value with a `$(VAR)` reference, a
  `configMapKeyRef` to a ConfigMap that drydock does not find, or a
  reference that is not optional to a key that is missing. It also warns
  when the command line does not run the program, when a flag value
  stops the program at startup, such as `--enable-progressive-syncs=yes`,
  and when a flag is not a flag of the program or prints the help, which
  stops it too. drydock knows every flag of the three programs in Argo CD
  v3.6.1. An argument with a `$(VAR)` reference in the place of a flag
  also warns, because the kubelet fills it in and it can become a flag.
  When `argocd-cmd-params-cm` sets a key but the workload does not pass it
  to the program, Argo CD ignores the key. drydock then uses the default
  and warns. When two workloads count and give a key different values,
  drydock uses the first, in the order of the repo-server rules, and warns.
  It does not compare a committed workload with a rendered one that comes
  first, because the committed file is often a Kustomize patch of it that
  holds only part of the env list. drydock reads only the application
  controller for `application.namespaces`, not the API server. The
  namespace gate uses the value of the controller. But drydock decides
  which `argocd-cmd-params-cm` counts for the controller namespace, and
  whether an install reconciles its own Application, before it knows the
  workloads. These checks read the `application.namespaces` of each
  `argocd-cmd-params-cm`. When the controller sets another value with a
  flag or an env entry, drydock warns that the two can disagree.
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
- The two Git generator keys use the same second run. For the globbing key,
  the run needs an ApplicationSet with a Git files generator. For the hidden
  directories key, it needs an ApplicationSet with a Git or OCI directory
  generator. Their cycle warning is `settings.appset-generation-cycle`, which
  `--strict` also fails on.
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
- The persistent render cache key and changed-only selection of a Helm
  source cover each symlink that a `valueFiles` or `fileParameters` path
  passes through and the file that helm reads. A value file path that
  starts with `/` is relative to the repository root there too. Changed-only selection reads every source with
  its `.argocd-source*.yaml` files merged in, as the render and the cache
  key do, so it owns the value files, file parameters, and Kustomize inputs
  that only an override file names. For an overridden entry whose path passes through a
  symlink, these paths are optional inputs, because they decide whether the
  path resolves.
- The persistent render cache key and changed-only selection of a directory
  source cover the files outside its path that its symlinks reach, and the
  files that its Jsonnet files import, directly or through other imports,
  also inside a Jsonnet library. When the source has a Jsonnet file, they
  also cover each library path as written and every symlink on the way to
  it, so moving a library is a change to the source. A source without a
  Jsonnet file never resolves its libraries, so they do not count. drydock
  reads the import paths from the parsed Jsonnet files. It also follows the
  imports in `extVars` and `tlas` values with `code: true`, after the same
  env substitution as the render, such as `$ARGOCD_APP_NAME`, in every
  pass: the render substitutes the values once more for each Jsonnet file,
  so the scans take one pass for each `.jsonnet` file under the source
  path. Argo CD evaluates such code with no importing file. Its importer
  then tries the import path as a relative path against the repo-server's
  working directory first (`/home/argocd` in the Argo CD image). Its
  inbound check joins a relative path to the repository root
  (`files.Inbound`, util/io/files/util.go:83-109), so a plain file found
  there passes the check and is read, although it is outside the
  repository. A path with `..` that leaves the root fails. After that the
  importer tries the Jsonnet paths: the libraries, last first, and then
  the source path (reposerver/repository/repository.go:2339-2397). drydock
  does not model the working directory, and tries the Jsonnet paths only.
  A normal repo-server has no file there that an import names. A value with
  `code: false` is a plain string, and code that does not parse adds
  nothing. Changed-only selection does not know the revision of the
  render, so when an import path in such code names an
  `ARGOCD_APP_REVISION` variable in any pass, or the code does not parse
  with a probe revision, it renders the source on every change, with the
  warning below. The import scan has a budget of 1000 files and 10 MB per
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
  `reposerver.allow.oob.symlinks` is `true`. Helm charts, Kustomize
  `helmCharts` value files, and Kustomize graphs still reject every symlink
  in drydock (see the safety boundary below), where the repo-server follows
  in-tree symlinks. Helm `valueFiles` and `fileParameters` follow symlinks
  as in Argo CD (see Rendering).
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
- `kustomize.buildOptions` as Argo CD v3.6.1 and kustomize v5.8.3 read it.
  Argo CD splits the value at white space, with no quoting, and passes the
  words after `kustomize build <path>`
  (`util/kustomize/kustomize.go:432-449`). drydock parses them with the flag
  set of the kustomize v5.8.3 build command, so every pflag form works:
  `--flag=value`, `--flag value`, `--flag=false`, and `-e value`. A flag that
  kustomize does not know fails with the kustomize text, for example
  `kustomize build: unknown flag: --no-such-flag`. A second path fails with
  `specify one path to kustomization.yaml`. An illegal `--load-restrictor` or
  `--reorder` value fails with the kustomize text. Earlier drydock releases
  accepted only a few flags and failed on the rest. The flags act as
  follows:
  - `--enable-helm` and `--enable-alpha-plugins` turn Helm on for
    `helmCharts` (`HonorKustomizeFlags`). drydock still runs no plugin
    program, also with `--enable-exec`.
  - `--load-restrictor` with an empty value means
    `LoadRestrictionsRootOnly`, as in kustomize.
  - `--reorder none` keeps the resource order, and `--reorder legacy` or no
    `--reorder` sorts in the legacy order, as in the kustomize binary. Earlier
    drydock releases kept the resource order by default.
  - `--enable-managedby-label` adds `app.kubernetes.io/managed-by:
    kustomize-v5.8.3`. The kustomization field `buildMetadata:
    [managedByLabel]` gives the same value. Earlier drydock releases wrote the
    drydock version there.
  - `--helm-kube-version` and `--helm-api-versions` give the flag values of
    `helmCharts`. The last `--helm-kube-version` wins, and the
    `--helm-api-versions` values add up.
  - `--helm-command` with a program named `helm`, such as the default `helm`
    or `/usr/local/bin/helm`, changes nothing. drydock cannot check that the
    path exists in the repo-server, where kustomize fails a path that does
    not exist. An empty value fails `helmCharts` with `must specify
    --helm-command`, as in kustomize.
  - `--output` or `-o` with `/dev/stdout`, `/dev/fd/1`, or `/proc/self/fd/1`
    changes nothing, because kustomize writes the build to its standard
    output. Extra slashes or `.` elements before the file change nothing
    either. An element after the file fails, as in kustomize, because the
    file is not a directory: `/dev/stdout/` fails with `open /dev/stdout/:
    is a directory`, and `/dev/stdout/.` or `/dev/stdout/..` fails with
    `not a directory`. With any other path, kustomize writes the
    build there, and Argo CD reads no manifests and does not fail. drydock
    then builds the source, so that a build error still fails it, renders no
    manifests, and reports the `kustomize.build-output-discarded` warning.
    drydock does not check that the path or its directory exists in the
    repo-server.
  - `--helm-debug`, `--stack-trace`, `--network`, `--network-name`,
    `--mount`, `-e`/`--env`, and `--as-current-user` change nothing that
    drydock renders. kustomize reads the last five only for KRM functions,
    which drydock never runs.
- Argo CD adds flags after `kustomize.buildOptions`: when the value contains
  the text `--enable-helm` anywhere, also as `--enable-helm=false`, it adds
  `--helm-kube-version` with the kube version of the Application and one
  `--helm-api-versions` per API version of the Application
  (`isHelmEnabled`). So with `--enable-helm`, the kube version of the
  Application wins over one in `kustomize.buildOptions`. With only
  `--enable-alpha-plugins`, Argo CD adds nothing, and `helmCharts` get the
  Helm default kube and API versions unless `kustomize.buildOptions` sets
  them. Earlier drydock releases always used the kube and API versions of the
  Application.
- With a found `argocd-cm`, `helmCharts` render only when
  `kustomize.buildOptions` turns Helm on. Otherwise they fail with the
  kustomize text `must specify --enable-helm`, as in Argo CD. Without an
  `argocd-cm`, drydock cannot tell the flags, so it assumes `--enable-helm`.
  The renders that read the Argo CD settings of an install also assume
  `--enable-helm`, because the cluster already holds the settings that the
  install creates. The final render of the install uses the found settings.
  Earlier drydock releases rendered `helmCharts` without `--enable-helm`.
- A config management plugin whose generate command is `kustomize build`
  renders natively with exactly the flags of that command. Argo CD adds no
  flags to a plugin command, so the kube and API versions of the
  Application do not reach its `helmCharts`.
- Plugins in `generators:` and `transformers:` follow the plugin flags of
  kustomize v5.8.3 when drydock finds an `argocd-cm`. A plugin that is not
  builtin, KSOPS included, needs `--enable-alpha-plugins`, or the source
  fails with `external plugins disabled; unable to load external plugin
  '<kind>'`, after `loading generator plugins: failed to load generator: `
  for a generator. Then kustomize reads the function annotation, such as
  `config.kubernetes.io/function`, and a malformed one fails with
  `loader: failed to get FunctionSpec: ...`. An exec function runs only
  with `--enable-exec`. With `--enable-alpha-plugins` alone, kustomize
  skips every exec function, and it skips a function annotation that names
  neither a container nor an exec program with any flags. A skipped
  transformer changes no resource. A skipped generator still adds its own
  configuration, marked `config.kubernetes.io/local-config`, and kustomize
  drops it only at the end of the top-level build (`fnplugin.go:166-183`,
  `kusttarget.go:162`). Until then, the configuration takes part in the
  build: two copies of it fail, and `replacements` can read it. drydock
  adds the same configuration as a resource, which krusty also keeps until
  the end, and reports the `kustomize.exec-function-skipped` warning. A
  kustomization whose entries kustomize all skips still renders, because
  drydock keeps an empty stand-in entry. drydock runs no plugin
  program, so a plugin that kustomize would run fails closed with a message
  that names the plugin form: a container function, which kustomize runs in
  a container also when it names an exec program, an exec function with
  both flags, or a plugin program without a function annotation. The
  exception is a KSOPS generator, which drydock emulates under
  `--enable-ksops-compat` when kustomize would run it: as an exec function
  with both flags, or as a plugin program with `--enable-alpha-plugins`.
  KSOPS returns only the resources that it generates, not its
  configuration (`krm` in `ksops.go` of viaduct-ai/kustomize-sops), so the
  emulation adds no configuration. Without an `argocd-cm`, drydock emulates
  every KSOPS generator and keeps builtins, as before. Earlier drydock
  releases ignored the two flags.
- `generators:` and `transformers:` entries load as kustomize loads them.
  The resource factory of kustomize parses the documents, so a `List`
  expands into its items. Under
  `LoadRestrictionsRootOnly`, a file outside the kustomization directory
  fails with `accumulation err='accumulating resources from '<entry>':
  security; file '<path>' is not in or below '<dir>''`, after `loading
  generator plugins: ` for a generator. drydock names the paths from the
  repository root. Errors and the `config.kubernetes.io/origin` and
  `alpha.config.kubernetes.io/transformations` annotations name the entry,
  not the file that drydock writes for it in its workspace.
- A generator with `behavior: merge` or `behavior: replace` fails, as in
  kustomize, when it names a resource that only a later generator of the
  same kustomization makes, and drydock added that output as a resource:
  the output of `helmCharts`, of a KSOPS generator, or the configuration of
  a skipped generator. kustomize runs `configMapGenerator`,
  `secretGenerator`, `helmCharts`, and then the documents of `generators:`
  in list order, so a builtin `SecretGenerator` with `behavior: merge` in
  `generators:` before a KSOPS entry fails too. The resource does not exist
  yet: `merging from generator ... id ... does not exist; cannot merge or
  replace` (`kusttarget.go:267-296`, `reswrangler.go:558-566`). When the
  resources of the kustomization already hold that ID, the merge works,
  and the later output then fails with `exists; can not use behavior:
  'unspecified', behavior must be merge or replace`. Earlier drydock
  releases merged into the output.
- Recorded divergences for `kustomize.buildOptions`:
  - `--help` and `-h` fail closed, because kustomize then prints its help
    instead of manifests.
  - A `--helm-command` that names a program other than `helm` fails
    `helmCharts` closed, because drydock renders charts with its Helm
    library and cannot run that program.
  - kustomize runs the generators of a kustomization in this order:
    `configMapGenerator`, `secretGenerator`, `helmCharts`, then
    `generators:` (`kusttarget_configplugin.go:36-40` at kustomize/api
    v0.21.3). drydock renders `helmCharts` and KSOPS generators before krusty
    runs, as resources of the kustomization, so their output comes before
    the output of the two builtin generators. A merge into that output fails
    as in kustomize (see above). What stays different is the order of the
    output when the sort is off: with `--reorder none`, or with
    `sortOptions: {order: fifo}` in the kustomization. In the default legacy
    order, the output is the same. To match the order, drydock would need a
    hook between the generators of krusty, which its API does not give.
  - kustomize builds a `generators:` or `transformers:` entry that names a
    kustomization directory, and its resources are the plugin
    configurations. drydock does not build such a directory itself, because
    that would read files before the graph checks. A `generators:`
    directory entry fails with `is a kustomization directory; drydock does
    not expand directory entries of generators`. A `transformers:`
    directory entry stays for krusty, which walks it with the graph checks
    and applies builtin transformers in it as kustomize does. With a found
    `argocd-cm` and `--enable-alpha-plugins`, a plugin function in such a
    directory fails with `drydock does not expand directory entries that
    hold plugin functions`, where kustomize would skip or run it. A
    directory entry at or above its kustomization fails in both tools, with
    `cycle detected` in kustomize and with the graph check text in drydock.
  - The configuration of a skipped generator that sets
    `kustomize.config.k8s.io/needs-hash` or `kustomize.config.k8s.io/behavior`
    fails closed. kustomize applies these generator options to the
    configuration that it adds, and drydock adds it as a resource without
    them.
  - With `buildMetadata: [transformerAnnotations]`, kustomize also lists a
    skipped transformer in `alpha.config.kubernetes.io/transformations`,
    with its `configuredBy`. drydock does not, because it drops the
    configuration. The
    `config.kubernetes.io/origin` annotation of output that drydock adds as
    resources (`helmCharts`, KSOPS) has the form of a resource, not of
    generator output.
  - Some errors of such builds have another text in drydock, while both
    tools fail. drydock adds the moved output with the resources, so a
    collision with a resource fails with krusty's `may not add resource
    with an already registered id`, where kustomize says `exists; can not
    use behavior: 'unspecified'`. In the same way, drydock fails such a
    merge with `does not exist; cannot merge or replace` also when the
    resources of the kustomization hold the ID. kustomize then merges and
    fails on the later output with `exists; can not use behavior`. To tell
    the two cases apart, drydock would have to build the resources before
    the build, outside the checks of the main build, so it does not.
- Kustomize `helmCharts` release names and namespaces as kustomize v5.8.3
  passes them to `helm template`. Without `releaseName`, kustomize passes
  `--generate-name`, and `helm template` renders the release
  `release-name`. Without a namespace in the entry, the kustomization, or a
  parent kustomization, kustomize passes no `--namespace`, and Helm picks
  the namespace from the repo-server environment, as for a Helm source
  above, because kustomize runs Helm with that environment
  (`HelmChartInflationGenerator.go:182`). kustomize configures every
  `helmCharts` entry of a kustomization before it renders any, so these
  checks of every entry come first, in this order: a missing chart `name`,
  a `releaseName` or chart `name` that starts with `-`, a local
  `additionalValuesFiles` entry that does not exist (`could not load
  additionalValuesFile`), and a `valuesMerge` other than `merge`,
  `override`, or `replace`. The Helm release name checks above apply too.
  Earlier drydock releases used the chart name and an empty namespace, and
  checked each entry only when it rendered it.
- Kustomize `helmCharts` `kubeVersion` and `apiVersions` as kustomize reads
  them (`HelmChartInflationGenerator.go:58-70` at kustomize/api v0.21.3).
  The `apiVersions` of an entry replace the `--helm-api-versions` values
  from `kustomize.buildOptions` and from `spec.source.kustomize.apiVersions`.
  An entry without `apiVersions` keeps those values. Earlier drydock
  releases added the two lists together.
- The kustomize v5.8.3 defect of the shared `--helm-api-versions` values, as
  Argo CD v3.6.1 has it. kustomize gives each `helmCharts` entry the slice of
  flag values itself, without a copy, and decodes the `apiVersions` of the
  entry into it. So the `apiVersions` of one entry overwrite the first flag
  values, and every entry that holds the same slice, before or after it, sees
  them. This crosses bases, overlays, and components of one build, in the
  order that kustomize configures them. drydock copies the defect with the
  same operations. Helm then splits each value at commas, as CSV. Earlier
  drydock releases did not copy the defect.
- A Kustomize `helmCharts` entry without `valuesFile` fails when its chart
  has no `values.yaml`, as kustomize fails it, unless the entry has
  `valuesInline` with `valuesMerge: replace`. kustomize reads that file
  itself before it runs Helm. Earlier drydock releases rendered the chart.
- Kustomize `helmCharts` `valuesFile` and `additionalValuesFiles` are file
  names, not globs, as in kustomize. A path such as `values-*.yaml` reads
  only a file with that exact name and fails when there is none. With
  `valuesInline` and `valuesMerge: replace`, kustomize never reads
  `valuesFile`, so a missing one does not fail. The scan for Argo CD
  settings reads the same files. Earlier drydock releases expanded the
  glob.
- Kustomize Helm `valuesFile` and `additionalValuesFiles` entries load as
  the kustomize file loader loads them (`fileloader.go:320-332` and
  `loadrestrictions.go:15-30` at kustomize/api v0.21.3). Under the default
  `LoadRestrictionsRootOnly`, a file outside the kustomization directory
  fails with `security; file '...' is not in or below '...'`. The
  `values.yaml` of the chart, which kustomize loads when the entry sets no
  `valuesFile`, counts too, so a `helmGlobals.chartHome` outside the
  kustomization directory fails, unless the entry has `valuesInline` with
  `valuesMerge: replace`. A directory fails with `'...' must resolve to a
  file` before the root is checked. With
  `kustomize.buildOptions` `--load-restrictor LoadRestrictionsNone`, a file
  outside it loads when it stays inside the repository root. drydock names
  the paths from the repository root, where kustomize names absolute paths.
  Earlier drydock releases loaded files outside the kustomization directory
  under `LoadRestrictionsRootOnly` too.
- Recorded divergence: kustomize resolves the symlinks of a `helmCharts`
  values file path before it checks the root (`fsondisk.go:51-81`), so a
  symlink that resolves inside the kustomization directory loads. drydock
  fails any symlink component in such a path. The Helm value file loader,
  the prepared Kustomize workspace, and the render cache input digests all
  reject symlinks there, and changing them together is a larger change
  than this parity fix.
- kustomize accumulates the resources of a kustomization, with the builds
  of its bases, before it runs its generators (`kusttarget.go:202-235`).
  So a missing local resource is reported before an error of the
  `helmCharts` of the same kustomization. drydock reports it with the start
  of the kustomize text, `accumulating resources: accumulation
  err='accumulating resources from '<resource>'`.
- Kustomize Helm `valuesFile` and `additionalValuesFiles` paths are read as
  written, as kustomize reads them. A build environment variable such as
  `$ARGOCD_APP_REVISION_SHORT` in such a path stays literal, because Argo CD
  substitutes the build environment into a Kustomize source only for
  `images`, `commonLabels`, and `commonAnnotations`. Earlier drydock releases
  substituted it.
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
- `spec.source.kustomize` options apply as the `kustomize edit` commands
  that Argo CD v3.6.1 runs before `kustomize build`
  (`util/kustomize/kustomize.go:174-401`), with the rules of kustomize
  v5.8.3. They apply in the order of Argo CD: `namePrefix`, `nameSuffix`,
  `images`, `replicas`, `commonLabels`, `commonAnnotations`, `namespace`,
  `patches`, then `components`. So the first option that fails gives the
  error. drydock writes the error text of kustomize after the name of the
  command, such as `kustomize edit set image: invalid format of image`.
  Earlier drydock releases merged the options with their own rules.
- Kustomize source `images` as `kustomize edit set image`. An entry without
  `=` names the image and sets its tag, its digest, or both, as in
  `nginx:1.2@sha256:...`. An empty name before `=` counts as no `=`, so
  `=nginx:1.2` sets the tag of `nginx`. An entry with only a name, such as
  `nginx`, fails. `nginx=` is accepted and sets an entry with only the
  name: it replaces an `nginx` entry of the kustomization, and otherwise
  changes nothing. A `*` new name, tag, or digest
  keeps the value of the same image in the kustomization. The last entry
  for a name wins. Then every image is sorted by name, one per name, so a
  rename can feed a later rename: `a=b` with a kustomization image `b`
  renamed to `c` gives `c`. Spaces stay in the value.
- Kustomize source `replicas` as `kustomize edit set replicas`. A name that
  holds `=` fails. The replicas are sorted by name, one per name.
- Kustomize source `commonLabels` and `commonAnnotations` as `kustomize
  edit add label` and `kustomize edit add annotation`. A key that is
  already in the map that the command writes fails, also with the same
  value, unless `forceCommonLabels` or `forceCommonAnnotations` is set.
  Without `labelWithoutSelector`, that map is `commonLabels`, and the
  `labels` entries are not looked at. With it, the labels go to the first
  `labels` entry without `includeSelectors` and with the same
  `includeTemplates` setting, or to a new entry, and only that entry is
  looked at. When that entry has no `pairs`, kustomize panics, and drydock
  fails with a message that says so. `labelIncludeTemplates` without
  `labelWithoutSelector` fails.
  Each entry is passed as `<key>:<value>`, so the key ends at its first
  `:`, and a value in double quotes loses the quotes. The build environment
  reaches the label values, and reaches the annotation values only with
  `commonAnnotationsEnvsubst`.
- Kustomize source `patches` as Argo CD adds them. When no edit command
  wrote the kustomization file before them, Argo CD reads the `patches`
  field of the file itself, and an empty field fails (`expected 'patches'
  field in kustomization.yaml to be []any, but got <nil>`). An edit command
  that only prints its help does not write the file. kustomize reads the
  patches as JSON with unknown fields disallowed, so drydock decodes them
  the same way. A patch option other than `allowNameChange` and
  `allowKindChange` fails (`invalid Kustomization: json: unknown field`),
  and the field names match without regard to letter case, so
  `AllowNameChange` works. The unknown field error comes after the
  component errors, because kustomize reads the patches only in the next
  command.
- Kustomize source `components` as `kustomize edit add component`. Each
  entry is a glob from the source directory, as `filepath.Glob` reads it.
  A match whose name starts with `.` is dropped, unless the entry's last
  element starts with `.` too. A match that is the kustomization file, or a
  component that the kustomization already lists, is not added again. An
  entry that matches nothing is kept when it is a remote ref or a
  directory, and fails with `has no match` otherwise, with the text of
  kustomize. An empty entry fails with `new root cannot be empty`. An
  absolute entry fails with `new root '...' cannot be absolute`, because
  drydock never reads absolute paths. Under `ignoreMissingComponents`, the
  first valid component goes through the same command. Argo CD checks the
  entry as written, so a folder named `[a]` with a kustomization file is
  kept, and the glob then adds the folder `a`. The kustomization files of
  the kept folder are render cache inputs, and changed-only selection owns
  them. drydock lists directories for the globs only inside the
  repository. A directory outside it, or a path whose text enters `.git`,
  adds no matches. The globs resolve each path element in order, as the OS
  does. A `..` after a symlink goes up from the symlink's target, and a
  `..` after a missing element or a file finds no match. An entry without
  a match is then loaded as a folder after kustomize cleans its text, so
  `file.yaml/../a` names the folder `a`, as in kustomize. One difference
  from the OS: an absolute symlink target must start with the repository
  root, as written or with its symlinks resolved. Any other absolute target
  counts as outside the repository. A symlink that leads out of the
  repository or into `.git` fails with drydock's symlink error before
  drydock lists or reads anything there.
  Any other symlink is followed. When it leads to no match, such as a
  folder without the wanted entry or a file, it adds nothing, as in Argo
  CD. When a match passes through it, the render fails with the symlink
  error, because drydock rejects component paths that pass through a
  symlink. A component path is checked before drydock reads any file
  under it. So drydock never lists or reads `.git` through a symlink in
  a checkout that does not change while drydock runs. Argo CD follows such
  a symlink. Before drydock lists a folder, it makes sure that the folder
  it opened is the one it resolved, which catches a swap of the last path
  element for a symlink. It does not catch a swap of a middle element. A
  process that rewrites the checkout while drydock runs is outside
  drydock's protection, but drydock still never reads outside the
  repository.
- Argo CD passes most option values to these commands without `--` in
  front. So kustomize reads an image, a replica name, a label or
  annotation key, or a component that starts with `-` as a flag. An unknown
  flag fails, `--` ends the flags, and `-h` or `--help` makes the command
  change nothing. A known flag takes effect: the label key `-ftest.x` sets
  `--force`, because pflag reads `-f` and drops the rest, which starts with
  `test.`. drydock reads them the same way. `namePrefix`, `nameSuffix`, and
  `namespace` are passed after `--`.
- The persistent render cache key holds the path of each input, and each
  component that a glob matches is an input. So a new, removed, or renamed
  match changes the key, and an entry that does not match changes neither
  the render nor the key. The directory that the glob lists is not an
  input, so a symlink elsewhere in it does not turn the cache off.
- For changed-only selection, a component glob also owns the directory
  that it lists, as a path prefix: the leading path elements without a
  glob character. So a change that removes a match there selects the
  source. A glob that lists the repository root owns nothing more than its
  matches, as for a Helm value file glob. Each path element just before a
  `..`, such as `mid` in `../../mid/../components/*`, is owned too: the
  glob finds no match when that element is missing or a file, so a change
  that adds or deletes it selects the source.
- A Kustomize source whose `kustomize.version` names no
  `kustomize.version.<version>` or `kustomize.path.<version>` key in a
  found `argocd-cm` fails with `error getting kustomize binary path:
  kustomize version <version> is not registered`, as in Argo CD v3.6.1
  (`GetKustomizeBinaryPath`, `util/settings/settings.go:300-321`). Without
  an `argocd-cm`, drydock accepts every version. drydock renders a
  registered version with its embedded kustomize v5.8.3 and warns that it
  does not run the registered binary. A registered version does not select
  `kustomize.buildOptions.<version>`, in Argo CD either (see the
  `kustomize.buildOptions.<name>` entry under settings).
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
- A Helm `valueFiles` or `fileParameters` path never reads a file outside
  the repository or in `.git`, also when `reposerver.allow.oob.symlinks` is
  `true` and the repo-server would read it through a symlinked directory.
- drydock reads an `.argocd-source.yaml` or `.argocd-source-<app>.yaml`
  file only when it is a regular file whose real path is in the repository
  and outside `.git`, for the render, for changed-only selection, and for
  the scan for Argo CD settings. A file that is a symlink out of the
  repository or into `.git`, a named pipe, or a device fails the render,
  and selection and the settings scan then use the spec source. The
  repo-server reads such a file. A missing file, a dangling symlink, and a
  directory are skipped, as in Argo CD, also a symlink to a directory
  wherever it points.

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
  name, as project validation resolves it. Its rendered
  `argocd-cmd-params-cm` names a namespace for the controller namespace
  rule (see "The Argo CD controller namespace"). Only its rendered
  ConfigMaps in the controller namespace count as settings, both when
  drydock renders it for settings only and when the discovery frontier
  renders it. A ConfigMap without a namespace lands in the destination
  namespace. An install that does not reconcile the Application that
  installs it is another Argo CD, and none of its settings ConfigMaps
  count. An Application that installs Argo
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
    directory, with globs. kustomize reads these paths as written, so a
    `$ARGOCD_APP_NAME` or other build environment variable in them stays
    literal. Argo CD substitutes the build environment only into the
    `images`, `commonLabels`, and `commonAnnotations` of a Kustomize source
    (`util/kustomize/kustomize.go:189`, `236`, and `254-257`). Earlier
    drydock releases substituted it into these paths. It skips
    `valuesFile` when `valuesMerge` is `replace` and `valuesInline` is set,
    because the render then does not read it. When `valuesInline` is set,
    kustomize reads `valuesFile` with its own YAML parser, which reads only
    the first YAML document, and drydock reads it the same way. Helm reads
    every other values file with all its YAML documents.

  drydock merges the `.argocd-source.yaml` and `.argocd-source-<app>.yaml`
  overrides of a local source first, as Argo CD does. Then it picks the
  source type as Argo CD does: the explicit type, else the type that the
  file names in the source directory give (see the source type detection
  above). So a source with an explicit
  `directory` or `plugin` type is not a Helm install. A remote value file
  has no local file. drydock finds these files before it renders, with the
  resolution helpers of the renderer. It does not record the files during
  the render. A `helmCharts` entry inside a component that
  `spec.source.kustomize.components` adds counts too, for the values files
  and for the chart name. With `ignoreMissingComponents`, only the first
  valid component counts, as only it renders. Earlier drydock releases did
  not read such a component, so they did not render that install for its
  settings and gave no warning.

  The settings that the installs render can change which Applications are
  installs. drydock renders the installs for their settings in rounds. Each
  round starts from the settings without the earlier settings-only renders
  and renders the installs that count with the settings of that round. So
  an install that counted in an earlier round, but not with the settings
  that settle, leaves no settings behind. For example, the discovery
  frontier can find a cluster Secret that moves the destination of the
  install to another cluster. Earlier drydock releases kept the settings of
  such an install and gave no warning. The rounds can also come back to
  the settings of an earlier round, so the settings never settle. For
  example, the install renders an `argocd-cm` that turns off the in-cluster
  destination of the install itself. Then drydock keeps the settings of
  every install that counted in a round, because the cluster holds them
  once the install ran. It reports a `settings.installs-unsettled` warning
  for each install that counted in some rounds and not in others.
  `--strict` fails on this warning.

  The discovery frontier already renders an install whose source path can
  hold Argo CD objects, such as a local chart whose templates name
  `argocd-cm`. drydock keeps every object of that render, as before. drydock
  renders every other install for its settings only, and keeps only the
  three ConfigMaps and the Argo CD workloads (see "Settings from the Argo
  CD workloads") from that render. The final render renders each install
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
- drydock parses and runs jq expressions with the gojq version that Argo CD
  v3.6.1 requires (v0.12.19). A newer gojq accepts some expressions that
  Argo CD rejects, such as `.data.ignored?//empty`.
- drydock reads the diff mode of each Application as Argo CD v3.6.1 does
  (`shouldUseServerSideDiff` in `controller/state.go`). The
  `ServerSideDiff=false` compare option turns server-side diff off, and it
  wins over everything else. Otherwise, server-side diff is on when the
  `argocd-cmd-params-cm` key `controller.diff.server.side` is on, when the
  Application has the `ServerSideDiff=true` compare option, or when it has
  the `ServerSideApply=true` sync option. The application controller reads
  the key through `ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF` with
  `env.ParseBoolFromEnv`. So `true` in any case turns it on, and every other
  value, such as `1`, `yes`, ` true`, or an empty value, leaves it off.
  drydock uses the value of the current side, as for the other diff
  settings. The application controller reads the key only when it starts.
  The argo-cd Helm chart restarts it after a change through its checksum
  annotation, but with a plain `install.yaml` the live mode stays the old one
  until the controller restarts, while drydock already uses the new value.
  drydock reads the key from a committed or rendered
  `argocd-cmd-params-cm`, also from the one that the argo-cd Helm chart
  makes from `configs.params`. It does not read the application controller
  StatefulSet or Deployment. So drydock does not see the switch when the
  chart sets it only through `controller.extraArgs`
  (`--server-side-diff-enabled`) or as a literal value in `controller.env`.
  Earlier drydock releases read the key as metadata only, gave a
  runtime-only warning for it, and used the client-side diff for an
  Application that had no diff option. The mode decides the next two points.
- A resource that only one side has, a created or a deleted resource,
  shows as a change in both modes. Under server-side diff, Argo CD skips the
  whole ignore normalizer and the known-types normalizer for it
  (gitops-engine `pkg/diff/diff.go`). So no Application, annotation, or
  `resource.customizations` rule applies, `status` is not removed, and no
  known type applies, not even the Argo Rollout default. The aggregated-role
  normalization still applies. drydock does the same, and its own display
  steps, the drydock-default ignored fields, `--strip-attr`, the ConfigMap
  `binaryData` summary, and Secret redaction, still run. The client-side diff
  applies the rules. So, under server-side diff, a new or deleted resource
  now shows fields that an ignore rule hides from a changed resource.
  Earlier drydock releases applied the rules in both modes. A resource that
  moves to another Application, such as after a rename or an adoption, is
  not new or deleted: Argo CD finds its live object whatever Application
  tracked it (gitops-engine `pkg/cache/cluster.go`), and compares it as a
  change with the full normalization of the new Application. drydock shows
  it as a removal from one Application and an addition to the other, and
  normalizes it as that change.
- A jq expression that removes the whole object, such as `.`, makes Argo CD
  continue with the object `null`, which it cannot decode. gitops-engine
  only logs that error, and the object stays `null`. For a resource that
  both sides have, the result depends on the diff mode and on which side is
  `null`:
  - Both sides `null`, client-side diff: gitops-engine cannot build a patch
    and fails, so Argo CD fails the comparison of the Application. drydock
    reports a `diff.comparison-failed` error for that Application, also
    when its resource did not change.
  - Both sides `null`, server-side diff: Argo CD compares `null` with
    `null`, so a change to the resource is hidden. drydock does the same.
  - One side `null`: Argo CD reports a change, and drydock shows a change
    with `null` on that side. With the client-side diff, this needs the
    last-applied annotation that a client-side sync leaves; without it,
    the two-way diff hides a `null` desired object.

  A created or deleted resource is a change in both modes. The client-side
  diff shows `null` for it. Server-side diff shows the whole object, because
  it does not run the ignore rules for it (see above).
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
- A known-types normalizer error, such as a `core/v1/ResourceRequirements`
  field whose `limits` is not a map, is only logged by Argo CD, which then
  compares the object as it is. The normalizer stops at the first field that
  fails, after it changed the fields before it. drydock does the same. For a
  changed resource that the diff shows, drydock gives the advisory
  `diff.known-type-skipped`, which names the Application, the resource, and
  the field. This also covers the built-in Argo Rollout known type. An
  Application that only the baseline side has gets no advisory. `--strict` does
  not fail on it, because the diff matches Argo CD either way.
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
  out-of-bounds symlink check above). `application.namespaces` and
  `applicationsetcontroller.namespaces` decide which Applications and
  ApplicationSets drydock discovers (see "Applications And
  ApplicationSets"). drydock also applies
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
  drydock applies `controller.diff.server.side` to diffs too: it decides
  the diff mode of every Application that does not have the
  `ServerSideDiff=false` compare option (see "Diff, Images, And Output").
  drydock parses it as the controller does, with `env.ParseBoolFromEnv`. Two
  settings sources with different parsed values conflict. The key changes no
  render, so it is not in the render cache keys. It is in the changed-only
  settings signature as the parsed value, because the mode can change the
  diff of every Application.
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
- `argocd-cm` `kustomize.version.<name>` and `kustomize.path.<name>`, as
  Argo CD v3.6.1 reads them (`GetKustomizeSettings`,
  `util/settings/settings.go:1346-1390`). A Kustomize source whose
  `spec.source.kustomize.version` names no registered version fails with
  `error getting kustomize binary path: kustomize version <name> is not
  registered`, before drydock reads its kube version. A name that two keys
  register fails the settings with `failed to add kustomize version from
  "<key>": found duplicate kustomize version: <name>`. Without an
  `argocd-cm`, drydock cannot tell the versions, so it accepts every name.
  drydock renders a registered version with its embedded kustomize v5.8.3,
  and both key forms give a warning that drydock does not run the
  registered binary. The render cache key holds whether drydock found an
  `argocd-cm`, because that decides which versions are registered.
- `kustomize.buildOptions.<name>` is not applied, as in Argo CD v3.6.1:
  Argo CD stores it with the version, but kustomize build gets only
  `kustomize.buildOptions`, whichever kustomize binary runs
  (`util/kustomize/kustomize.go:391-392`). drydock warns that it parsed the
  key and did not apply it.
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
| Missing non-default project | `ValidateApplications`, `ValidateGeneratedApplications`, `projectIndex`, `applicationProject` | `TestValidateApplicationsCurrentBehaviorReportsMissingNonDefaultProject`, `TestDiagStaticKeepsGeneratedApplicationWithMissingProject` | Generated Applications are kept, a recorded divergence |
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
