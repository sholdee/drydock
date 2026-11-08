---
title: ApplicationSet Reference
---

drydock expands a deterministic local subset of Argo CD `ApplicationSet`
generators. Unsupported generators emit diagnostics; non-strict commands keep
supported generated Applications, while `--strict` promotes those diagnostics to
errors.

Provider-backed generators are fixture-backed only. drydock does not contact
Kubernetes, Argo CD, SCM provider, pull-request, cloud, or plugin-service APIs
while generating Applications. The only generator that reaches the network is
the OCI generator, which pulls its artifact from the registry exactly as an
`oci://` Application source does, and not at all under `--offline`.

## Supported Generators

| Generator | Support |
| --- | --- |
| Git directories | Native. Matches keep the Argo CD v3.6.1 repo-server walk order. |
| Git files | Native. Patterns match as `git ls-files` pathspecs, and matches are sorted by path. |
| List | Native, including `elementsYaml`. |
| Matrix | Native for two child generators, including interpolated child params. |
| Merge | Native for two or more child generators over `mergeKeys`. |
| Provider-backed generators | Fixture-backed for clusters, clusterDecisionResource, SCM provider, pull requests, and plugin generators. |
| OCI directories | Native over the pulled artifact. Matches keep the Argo CD v3.6.1 repo-server walk order. |
| OCI files | Native over the pulled artifact. Matches are sorted by path. |

Supported ApplicationSet behavior includes:

- `spec.goTemplate: true`
- `spec.goTemplateOptions`, including `missingkey=error`
- generator-level selectors and template overrides
- `spec.templatePatch` rendered from generator params and strategic-merge-applied
  to generated Applications, with `spec.project` preserved
- Sprig-compatible template functions used by Argo CD, plus Argo CD v3.6.1's
  `tpl` (nested template rendering with the same options, functions, and
  three-level recursion limit) in templates, `templatePatch`, generator
  `values` (except `clusterDecisionResource`, which does not render values),
  and matrix/merge child interpolation
- generated `Application.metadata.namespace` set to the ApplicationSet
  namespace
- Argo CD's default generated Application finalizer where applicable
- multiple supported top-level generators evaluated independently and
  concatenated in manifest order

## External ApplicationSet Files

An ApplicationSet can live in one repository while the manifests it renders
live in another. Pass that file with repeatable `--discover-manifest <path>` on
the discovery commands (`build`, `get`, `test`, `diff`, `diag`). The path can be
absolute and outside the repository and outside any `--ref` snapshot. drydock
loads the file once and adds its `Application` and `ApplicationSet` documents
to discovery on both diff sides, so a pull request diff of the manifest
repository renders the generated Applications from each side's commit tree.

- Sources whose `repoURL` names the repository under diff resolve to each
  side's tree through the usual self-repository rules (`HEAD`, a diffed ref
  name, or the default-branch name).
- `--ref` snapshots never read the dirty checkout; without the flag, an
  uncommitted ApplicationSet in the working tree stays invisible.
- Changed-only selection ignores the external file itself. A generated
  Application owns its source paths in the repository under diff.
- Git generators always scan the repository under analysis, whatever the
  ApplicationSet's generator `repoURL` and `revision` say. An external
  ApplicationSet whose Git generator points at its own repository produces no
  Applications, or the wrong ones.
- A missing file, `..` path components, a symlink, or a document that is not
  an `Application` or `ApplicationSet` is an error.

In the GitHub pr-action, list the files one per line in the
`discover-manifest` input, for example `/home/runner/work/platform/appset.yaml`.
The test, diff, and image diff steps all receive them.

## Git Generators

drydock lists the repository as the Argo CD v3.6.1 repo-server sees its
checkout of a commit. A checkout holds no `.git` entry below the top level.
It holds an empty directory only for a gitlink (a submodule). When drydock
reads a working tree, it also skips every path that Git ignores and does not
track. It keeps an untracked path that Git does not ignore, because a commit
can hold it. A `--ref` snapshot holds committed files only, and an empty
directory for each gitlink.

drydock finds the gitlinks in the index of a working tree, in the commit of
a `--ref` snapshot, and as directories that hold their own `.git`. A gitlink
is a directory of the checkout, also when it is not on disk. When the
repository has a `.gitmodules` file, Argo CD fetches submodule content by
default (`ARGOCD_GIT_MODULES_ENABLED`), and drydock does not. drydock then
warns and sees each submodule as the local tree holds it.

When a Git generator defines both `directories` and `files`, Argo CD's
directory-first dispatch is used and `files` are ignored. `exclude: true`
removes a path even when another pattern includes it.

Before a Git generator lists anything, Argo CD gets the AppProject that the
`spec.template.spec.project` of the ApplicationSet names. It uses the template
of the ApplicationSet, not the template of the generator. When that AppProject
does not exist, the whole ApplicationSet fails. An empty name never exists. A
name that holds `{{` skips the lookup. Argo CD looks only in the namespace of
its controller.

drydock fails the ApplicationSet for an empty name. For any other name it
keeps the Applications, because it cannot know the AppProjects of the
cluster offline. They can come from another repository, a chart, a remote
Kustomize base, or the cluster itself. When the repository declares
AppProjects but not that one, and it is not `default`, drydock warns
`project.missing` once for each Application that names it. The warning says
that the Git generator looks that AppProject up first, so Argo CD fails the
whole ApplicationSet when it does not exist in the cluster. When no
Application of the ApplicationSet names it, for example because a generator
template sets another project, drydock warns once for the ApplicationSet.
`--project-diagnostics` filters these warnings like the other project
diagnostics. When the repository declares no AppProject at all, drydock does
not warn.

A Git generator failure that fails the ApplicationSet in Argo CD gives the
warning `appset.generator-failed`. The ApplicationSet then contributes no
Applications, also none from its other generators. Argo CD shows the same
failure as an `ErrorOccurred` condition. drydock evaluates no generator after
the one that failed.

### Directories

The directory list follows the repo-server walk: walk order, never through a
symlink, and without hidden directories and the directories below them.
`reposerver.include.hidden.directories` in `argocd-cmd-params-cm` adds the
hidden directories. With it, Argo CD also lists the `.git` directory of its
checkout and the directories in it. drydock lists `.git` but nothing in it,
and it warns about an include pattern that can match a directory below `.git`.
An Application whose source path is `.git` has no manifests in Argo CD.
drydock renders it empty and warns `render.git-directory-source`.

Each pattern matches with `path.Match` exactly as written. So `./apps/*` and
`apps/*/` match nothing. Argo CD skips a pattern that `path.Match` refuses,
such as `apps/[`, and the other patterns still apply.

### Files

By default, Argo CD lists the files of each pattern with
`git ls-files -- <pattern>`. So a pattern is a git pathspec, not a shell glob:

- `*` and `?` also match `/`. `apps/*/config.yaml` matches
  `apps/a/config.yaml` and `apps/a/b/config.yaml`.
- A pattern that names a directory matches every file below it. `apps`
  matches every file in `apps`, at any depth.
- `./` and repeated slashes are removed, and `..` goes up one directory.
- Pathspec magic works, for example `:(glob)apps/*/config.yaml`, where `*`
  does not match `/`, or `:!apps/legacy` to match everything except a
  directory. drydock does not evaluate the `attr` magic. A pattern with it
  makes the ApplicationSet unsupported (`appset.unsupported-generator`):
  drydock cannot tell what Argo CD generates, so the ApplicationSet
  contributes no Applications.
- An empty pattern matches every file. Hidden files match.
- A gitlink is one entry. A pattern can match it, also with a trailing
  slash such as `params/sub/`, but never a file in it. The repo-server then
  fails, because it cannot read a directory as a param file.

A pattern that git refuses, such as `../outside.yaml` or an absolute path,
fails the ApplicationSet.

`applicationsetcontroller.enable.new.git.file.globbing: "true"` in
`argocd-cmd-params-cm` changes the matching to doublestar globs over the
checkout: `*` does not match `/`, and `**` matches any number of directories.
A doublestar glob also matches directories. Argo CD then fails the
ApplicationSet, because it cannot read a directory as a param file. The glob
follows symlinks to directories, up to the 40 symlinks that Linux follows in
one path. drydock looks paths up as the glob asks for them. When a pattern
needs more than 131072 lookups, for example through two symlinks to `.`,
the ApplicationSet is unsupported. A symlink that does not resolve, such as
a dangling one, does not exist for a literal pattern part. So
`dangling.yaml` and `{x,dangling}.yaml` do not match it. A wildcard part
lists it, and reading it then fails the ApplicationSet. The glob also sees
the `.git` directory of the checkout. A pattern that matches `.git` or a
directory in it fails the ApplicationSet, as in Argo CD. A pattern that
matches a file in it makes the ApplicationSet unsupported: Argo CD reads
the file, and drydock never reads `.git`. drydock knows the names that a
checkout's `.git` directory holds, such as `.git/config` and
`.git/refs/heads`, but not branch names. drydock matches a pattern that
leaves the repository with nothing and warns. In Argo CD, `../*` can match
the checkout itself and then fails. drydock uses the doublestar release that
Argo CD v3.6.1 uses (v4.10.0), because releases parse some patterns
differently. For example, v4.10.0 refuses `{c,[a,b]}.yaml`.

The repo-server reads each listed file. It follows a symlink to its target,
as the kernel does. A symlink to a directory, to a target that does not exist,
or to a path through a file, such as `t.yaml/`, fails the ApplicationSet. In
a working tree, a target that Git ignores and does not track does not exist
either, and neither does a FIFO. drydock never reads a target outside the
repository or in `.git`, also when a `..` leaves `.git` again. It warns about
that one file and skips it. Argo CD reads such a target. A generated
Application owns the paths that its param file came from: the listed path,
each symlink followed, and the file read. So a changed-only diff renders it
when only the target of a symlink changes.

YAML and JSON files can decode to a mapping document, an array of mapping
documents, an empty mapping, or an empty file. A scalar, invalid YAML or JSON,
or an array with an entry that is not a mapping fails the ApplicationSet, as in
Argo CD.

Empty, comment-only, and bare `---` or `...` files decode to one empty param
set, matching Argo CD. An empty list (`[]`) decodes to zero param sets. Use it
when a param file exists but must generate nothing yet. Multi-document files
decode only the first document, also matching Argo CD.

Git files `values` use the same `values.*` and `.values.*` behavior as Git
directories. `pathParamPrefix` applies to all path-related params. For example,
`pathParamPrefix: myRepo` produces `.myRepo.path.path` in Go templates and
`myRepo.path` in non-Go-template mode.

In PR diff mode, mapped repository URLs use the local `--path` or
`--path-orig` trees for directory discovery and rendering, even when the
ApplicationSet declares `revision: master`.

### Settings

drydock reads `applicationsetcontroller.enable.new.git.file.globbing` and
`reposerver.include.hidden.directories` from a discovered or rendered
`argocd-cmd-params-cm`, the same way as
`applicationsetcontroller.enable.progressive.syncs`. Argo CD reads `true` and
`false` in any case, and any other value as `false`. When fleet discovery
finds a value late, after it expanded the ApplicationSets, drydock runs
discovery a second time with the final value. It does this only when an
ApplicationSet uses a generator that the value changes. When the second run
ends with yet another value, drydock warns `settings.appset-generation-cycle`.
drydock does not read a value that is set only on the Deployment.

## OCI Generator

The Argo CD v3.6.1 `oci` generator works like the Git generator over the
content of an OCI artifact instead of the repository under analysis. It is
supported top-level and as a matrix or merge child.

```yaml
generators:
  - oci:
      repoURL: oci://ghcr.io/my-org/platform
      revision: v1.2.3
      directories:
        - path: platform/*
        - path: platform/legacy
          exclude: true
```

- drydock pulls `repoURL` at `revision` through the same path as an `oci://`
  Application source: a `--repo-map` entry for the URL wins, tags and semver
  constraints resolve to a digest, content comes from the OCI cache when it
  is there, `--offline` resolves only from earlier online runs, and
  registry credentials come from `--registry-config` or
  `--oci-username`/`--oci-password`. A failed pull fails the run, like other
  source acquisition failures.
- `directories`, `files`, `exclude`, `pathParamPrefix`, `values`, and the
  generator `template` and `selector` behave as in the Git generator, with
  the same `path`, `path.basename`, `path.basenameNormalized`,
  `path.segments`/`path[n]`, `path.filename`, and `path.filenameNormalized`
  params. When both `directories` and `files` are set, `directories` wins.
- Directory patterns match with `path.Match` exactly as written: no
  normalization, so `apps/*/` and `./apps/*` match nothing. Hidden
  directories and everything under them are not listed, unless
  `reposerver.include.hidden.directories` is `true`. A symlink to a
  directory is never listed.
- File patterns are always doublestar globs, and `.` or an empty pattern
  means `**`. Hidden files match. File patterns follow symlinks that stay
  inside the artifact. A symlink that resolves outside the artifact is
  skipped.
- Param files decode like Git files, including the empty-content rules. As
  with Git files, a file that does not decode fails the whole
  ApplicationSet, and so does a file pattern that doublestar refuses.
- Generated Applications own no repository paths from the artifact:
  changed-only selection matches them through the ApplicationSet file and
  their own local sources, and persistent render cache keys follow the
  generated Application spec.
- `requeueAfterSeconds` has no effect; each run resolves `revision` once,
  and a two-sided diff shares one resolution.

## List, Matrix, And Merge

List generators support `elements` and `elementsYaml`. For non-Go-template
ApplicationSets, `elements` scalar fields must be strings and nested `values`
are flattened to `values.<key>`. `elementsYaml` stays unflattened so
matrix-interpolated YAML follows Argo CD behavior.

Matrix generators combine exactly two child generators and interpolate the
second child from first-child params, including templated `elementsYaml`.

Merge generators overlay two or more child generators by `mergeKeys` in base
generator order.

Matrix and merge children may use list, Git directories, Git files, OCI
directories, OCI files, fixture-backed provider generators, and nested
matrix/merge combinations where the Argo CD v3 nested JSON API permits them.

## Generator Selectors

A generator `selector` matches against the generator params flattened into
labels, as Argo CD v3.6.1 does. Nested maps become dot-separated keys, such as
`path.basename`, `metadata.labels.env`, and `values.region`. Lists of untyped
values, such as plugin output arrays, become indexed keys, such as `items.0`.

A list of strings is not split. Git `path.segments` and pull-request `labels`
are each one key whose value is the Go print form of the list, with the items
inside square brackets and separated by spaces. For example, the directory
`apps/alpha` gives `path.segments` the value `[apps alpha]`, so this selector
matches it:

```yaml
selector:
  matchLabels:
    path.segments: "[apps alpha]"
```

There is no `path.segments.0` or `labels.0` key, so a selector on such a key
never matches. The cluster `flatList` `clusters` param is one key in the same
way.

## Template Render Errors

Template execution failures, including `templatePatch` failures, generator
`values` templating, matrix/merge child interpolation, and `missingkey=error`
misses, are scoped to the failing ApplicationSet. drydock emits one warning
diagnostic per failing param set or generator with stable code
`appset.template-render-failed`, and that ApplicationSet contributes zero
generated Applications, including param sets that rendered successfully. Other
ApplicationSets and Applications render normally; `--strict` promotes the
warning to an error and fails the run.

This matches the Argo CD controller, which reports the same failure as an
`ErrorOccurred` condition on the ApplicationSet and reconciles no Applications
for it. A common trigger is a Git files generator matching an empty or
comment-only param file while the template references file params under
`missingkey=error`: the file decodes to one empty param set (Argo CD parity),
and rendering the template against it fails. Use `[]` as the param file
content when a match should generate nothing.

## Generator Errors

Some generator errors are not template errors, but Argo CD v3.6.1 still
reports them as an `ErrorOccurred` condition and generates no Applications
for the whole ApplicationSet. drydock warns `appset.generator-failed` with
the Argo CD message, and that ApplicationSet contributes zero Applications.
Other ApplicationSets render normally. `--strict` promotes the warning to an
error. These errors are:

- A list element that does not decode, or an `elementsYaml` that is not a
  YAML list of mappings.
- A matrix that does not have exactly two children, or a merge with fewer
  than two children, no merge keys, or a repeated merge key value.
- A matrix or merge child that names no generator, such as `{}` or a matrix
  nested one level too deep (`child generator generated no parameters`), or
  that names more than one.
- A generator selector that does not parse.
- A pull request or SCM provider filter regexp that does not compile, and a
  cluster or `clusterDecisionResource` selector that Argo CD rejects. These
  checks need only the spec, so they also run without provider fixtures.

Other generator evaluation failures, such as file IO, source acquisition, and
invalid ApplicationSet YAML, stop the run.

In diffs, a failing ApplicationSet contributes zero desired Applications on
that side. When the failure exists on only one side, the diff reports that
ApplicationSet's Applications as additions or deletions and exits `1`, while
the live controller would abort reconciliation without pruning.

## Generated Application Validation

drydock mirrors the name checks of the Argo CD v3.6.1 ApplicationSet
controller after all templates render:

- A generated Application with an empty `metadata.name`, including one whose
  `templatePatch` sets only `generateName`, is omitted.
- When several generated Applications share a name, every Application with
  that name is omitted, not only the later ones; Argo CD keys validation
  errors by Application name.

The controller then looks up the AppProject of each generated Application
and does not create one whose AppProject does not exist in the cluster.
drydock does not know the AppProjects of the cluster offline, so it keeps
such an Application. When the repository declares other AppProjects but not
the named one, the `project.missing` warning names the ApplicationSet and
says that the ApplicationSet controller does not create the Application
unless that AppProject exists in the cluster.

Each failure emits an `appset.application-validation-failed` warning carrying
Argo CD's message, and the ApplicationSet's other Applications still
generate. `--strict` promotes the warning to an error.

## RollingSync Strategy

The ApplicationSet controller of Argo CD v3.6.1 has a progressive syncs
switch. Without it, the controller does not use
`spec.strategy.type: RollingSync`. drydock reads that switch, `applicationsetcontroller.enable.progressive.syncs`, from
`argocd-cmd-params-cm` (see
[Diagnostics](/reference/cli/#diagnostics)). As in Argo CD, only
`true` in any letter case turns it on, and it is off by default.

With progressive syncs off, Argo CD ignores the strategy. drydock generates
the Applications as usual and gives one `appset.rollout-strategy-ignored`
advisory for each ApplicationSet with a RollingSync strategy. `--strict` does
not fail on it.

With progressive syncs on, drydock checks the steps against the generated
Applications' labels the way the Argo CD controller does:

| Finding | Stable code | Argo CD v3.6.1 behavior | drydock |
| --- | --- | --- | --- |
| Empty `steps` | `appset.rollout-no-steps` | `ErrorOccurred` ("No steps defined for rollout"). No Applications are created, updated, or deleted | The ApplicationSet contributes no Applications |
| `matchExpressions` operator other than `In`/`NotIn` | `appset.invalid-rollout-config` | `InvalidRolloutConfig` condition | Applications generate |
| Application selected by more than one step | `appset.invalid-rollout-config` | `InvalidRolloutConfig` condition | Applications generate |
| Invalid `maxUpdate` value | `appset.invalid-rollout-config` | `InvalidRolloutConfig` condition | Applications generate |
| Step that matches no generated Application | `appset.invalid-rollout-config` | `InvalidRolloutConfig` condition | Applications generate |

Progressive syncs can be on with at least one step. Then Argo CD writes
`spec.syncPolicy.automated.enabled: false` on each generated Application
with an automated sync policy, because the controller starts the syncs
itself. drydock does the same.

drydock reads the switch only from `argocd-cmd-params-cm`. It does not see a
value set only on the ApplicationSet controller Deployment, for example
through the chart's `applicationSet.extraArgs` or `applicationSet.extraEnv`.

An empty steps list differs from Argo CD in one way. Argo CD keeps the
Applications that already exist and leaves them unchanged. drydock generates
none, so `drydock diff` shows them all as removed, and `--strict` fails.

Messages reuse Argo CD's text. drydock reports every finding class, while the
Argo CD condition shows the highest-priority one. The `maxUpdate` check
assumes the cluster already holds the generated Applications. `--strict`
promotes `appset.rollout-no-steps` to an error, because Argo CD stops
reconciling the ApplicationSet. It neither escalates nor fails on
`appset.invalid-rollout-config`, because Argo CD's `InvalidRolloutConfig`
condition never blocks Applications, so a staged rollout whose later step has
no Applications yet still passes `--strict` CI.

The [render parity smoke](/concepts/argocd-render-parity/) runs Argo CD with
progressive syncs on. Argo CD must set the matching condition on the same
ApplicationSets that drydock flags, with the same message. For the empty
steps list, both sides must create no Applications. The smoke covers each
finding class above. Unit tests cover progressive syncs off.

## Provider Fixtures

Use the repeatable `--appset-provider-fixture` flag to provide deterministic
local data for provider-backed generators:

```bash
drydock get apps --path . --appset-provider-fixture fixtures/appset-providers.yaml
drydock diff apps --path . --path-orig ../base --appset-provider-fixture fixtures/appset-providers.yaml
```

Fixture files are strict YAML or JSON documents. Unknown fields, duplicate
identities, URL-like fixture paths, and malformed files produce
`appset.provider-fixture-invalid`. If fixtures are supplied but no entries match
a provider generator, drydock emits `appset.provider-no-match`. Filters that
cannot be evaluated from fixture data fail closed with
`appset.provider-unsupported-filter`. A filter that Argo CD itself rejects,
such as a regexp that does not compile, fails the ApplicationSet with
`appset.generator-failed` (see Generator Errors).

```yaml
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
    project: platform
    labels:
      environment: prod
    annotations:
      owner: platform
    values:
      region: home

clusterDecisions:
  - configMapRef: placement-config
    resourceName: placement-a
    labels:
      placement: edge
    matchKey: clusterName
    statusListKey: clusters
    decisions:
      - clusterName: prod-a
        placement: edge
    values:
      tier: edge

scmRepositories:
  - provider: github
    organization: example-org
    project: platform
    region: us-east-1
    repository: example-repo
    repositoryID: 123456789
    branch: main
    sha: abcdef1234567890
    url: https://github.com/example-org/example-repo
    labels:
      - ops
    tags:
      owner: platform
    paths:
      - deploy/app.yaml
    values:
      tier: ops

pullRequests:
  - provider: github
    organization: example-org
    project: platform
    repository: example-repo
    number: 42
    title: Update chart
    branch: renovate/chart
    targetBranch: main
    headSHA: abcdef1234567890
    author: renovate
    state: open
    labels:
      - dependencies
    values:
      kind: renovate

plugins:
  - configMapRef: generator-plugin
    outputs:
      - environment: prod
        cluster:
          name: prod-a
    values:
      source: fixture
```

Additional provider-specific fixture fields are available where Argo provider
configuration needs scope data that should not alter emitted template params.
SCM repositories accept `project`, `region`, and `tags`; pull requests accept
`project` and `state`. For example, Azure DevOps uses `organization` plus
`project`, AWS CodeCommit requires explicit `region` and can evaluate
`tagFilters` from `tags`, and GitLab `pullRequestState` is evaluated from
`state`.

### SCM repository IDs

Argo CD v3.6.1 passes `repository_id` to templates with a different Go type
for each SCM provider. drydock reads the fixture `repositoryID` and converts it
to the same type:

| Provider | Go type | Fixture `repositoryID` |
| --- | --- | --- |
| `github` | `*int64` | decimal integer |
| `gitlab` | `int64` | decimal integer |
| `gitea`, `bitbucketServer` | `int` | decimal integer |
| `azureDevOps` | `uuid.UUID` | UUID |
| `bitbucket` (Cloud), `awsCodeCommit` | `string` | any text |

In YAML fixtures, write an integer ID with or without quotes. In JSON fixtures,
write it as a string, such as `"repositoryID": "123456789"`. If an ID does not
match its provider type, drydock reports an `appset.provider-fixture-invalid`
error and the `scmProvider` generator produces no params. If you leave
`repositoryID` out, `repository_id` is the zero value of the type: `<nil>` for
GitHub, `0` for the other integer providers, the all-zero UUID for Azure DevOps,
and an empty string for the others.

The type changes the output:

- In Go-template mode, `{{ .repository_id }}` prints the number or the
  lower-case UUID. A comparison with a string, such as
  `{{ eq .repository_id "123" }}`, fails for the integer and UUID types.
- In fasttemplate mode, Argo CD only replaces string params. For the integer and
  UUID providers, `{{repository_id}}` stays in the output as literal text.
- `{{ printf "%v" .repository_id }}` prints a memory address for GitHub,
  because the value is a pointer. The address changes between runs in Argo CD
  and in drydock.

## Template Parameters

Provider fixtures emit the same stable template parameter names that Argo CD
uses for each supported provider family:

| Generator | Stable template parameters |
| --- | --- |
| `clusters` | `name`, `nameNormalized`, `server`, `project`, metadata labels/annotations (see below), `values` |
| `clusterDecisionResource` | `name`, `server`, decision fields, `values` |
| `scmProvider` | `organization`, `repository`, `repository_id`, `url`, `branch`, `branchNormalized`, `sha`, `short_sha`, `short_sha_7`, `labels`, `values` |
| `pullRequest` | `number`, `title`, `branch`, `branch_slug`, `target_branch`, `target_branch_slug`, `head_sha`, `head_short_sha`, `head_short_sha_7`, `author`, `labels`, `values` |
| `plugin` | fixture output fields, `generator.input.parameters`, `values` |

Every generator except `clusterDecisionResource` renders its `values` as
templates with the generator params. As in Argo CD v3.6.1,
`clusterDecisionResource` copies each value as a plain string and does not
render it. A value such as `region: '{{region}}'` stays the literal text
`{{region}}`, and a value with broken template syntax is not an error. To use a
decision field, reference it directly in the Application template, such as
`{{region}}` or `{{ .region }}`. In Go-template mode, a decision field named
`values` together with generator `values` makes Argo CD fail the
ApplicationSet. drydock warns `appset.template-render-failed` and generates no
Applications for it.

For non-Go-template ApplicationSets, nested maps are flattened with dot
notation, including `metadata.labels.<key>`, `metadata.annotations.<key>`, and
`values.<key>`.

For Go-template ApplicationSets, nested values remain available as maps or
arrays, such as `.metadata.labels`, `.metadata.annotations`, `.labels`, and
`.values`.

These params have the same Go types as in Argo CD v3.6.1:

| Param | Go type |
| --- | --- |
| `clusters` `.metadata.annotations` and `.metadata.labels` | `map[string]string` |
| `.values` from any generator that sets `values` | `map[string]string` |
| `clusters` `.clusters` with `flatList: true` | `[]map[string]any` |
| `pullRequest` `.labels` | `[]string` |

The type matters for Sprig map functions. `hasKey`, `keys`, `pick`, `omit`,
`values`, and `dig` need a `map[string]any`, so they fail on a
`map[string]string`. For example, `{{ hasKey .metadata.annotations "team" }}`
fails in Argo CD. drydock fails it too: it warns
`appset.template-render-failed`, and the ApplicationSet generates no
Applications. To test for a key, use `{{ index .metadata.annotations "team" }}`
instead, which returns an empty string when the key is missing, or range over
the map.

As in Argo CD v3.6.1, `clusters` params never expose the reserved annotations
`argocd.argoproj.io/refresh`, `argocd.argoproj.io/refresh-timestamp`,
`argocd.argoproj.io/hydrate`, `argocd.argoproj.io/hydrate-timestamp`,
`argocd.argoproj.io/tracking-id`, `argocd.argoproj.io/installation-id`, and
`kubectl.kubernetes.io/last-applied-configuration`. In Go-template mode,
`.metadata.annotations` exists only when the fixture cluster has annotations
(an empty map when all of them are reserved). `.metadata.labels` is always
present, an empty map when the fixture has no labels, because Argo CD only
reads cluster Secrets that carry the `argocd.argoproj.io/secret-type` label.
