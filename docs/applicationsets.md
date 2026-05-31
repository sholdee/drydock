---
title: ApplicationSet Reference
---

`drydock` expands a deterministic local subset of Argo CD `ApplicationSet`
generators. Unsupported generators emit diagnostics; non-strict commands keep
supported generated Applications, while `--strict` promotes those diagnostics
to errors.

Provider-backed generators are fixture-backed only. The CLI never contacts
Kubernetes, Argo CD, SCM provider, pull-request, cloud, or plugin-service APIs
for ApplicationSet generation.

## Supported Generators

Supported local generators:

- Git directories
- Git files
- List, including `elementsYaml`
- Matrix
- Merge
- Fixture-backed provider generators for clusters, clusterDecisionResource,
  SCM provider, pull requests, and plugin generators

Supported template behavior:

- `spec.goTemplate: true`
- `spec.goTemplateOptions`, including `missingkey=error`
- `spec.templatePatch` rendered from generator params, strategic-merge-applied
  to the generated `Application`, with `spec.project` preserved
- Sprig-compatible functions used by Argo CD, including `regexReplaceAll`
- generator-level selectors
- generator-level template overrides
- generated `Application.metadata.namespace` set to the ApplicationSet
  namespace
- Argo CD's default generated Application finalizer where applicable
- multiple supported top-level generators evaluated independently and
  concatenated in manifest order

## Git Generators

Git directories and Git files matches are sorted by normalized relative path.
Include and exclude patterns are deterministic, and `exclude: true` removes a
path even when another pattern includes it.
When a Git generator defines both `directories` and `files`, Argo CD's
directory-first dispatch is used and `files` are ignored.

Git files must remain under the repository root and must not traverse symlinks.
YAML and JSON files may decode to a mapping document, an array of mapping
documents, an empty mapping, or an empty file. Scalars, invalid YAML/JSON, and
arrays with non-mapping entries produce diagnostics.

Git files `values` use the same `values.*` and `.values.*` behavior as Git
directories. `pathParamPrefix` applies to all path-related params. For example,
`pathParamPrefix: myRepo` produces `.myRepo.path.path` in Go templates and
`myRepo.path` in non-Go-template mode.

In PR diff mode, mapped repository URLs use the local `--path` or
`--path-orig` trees for directory discovery and rendering, even when the
ApplicationSet declares `revision: master`.

## List, Matrix, And Merge

List generators support `elements` and `elementsYaml`. For non-Go-template
ApplicationSets, `elements` scalar fields must be strings and nested `values`
are flattened to `values.<key>`. `elementsYaml` is kept unflattened so
matrix-interpolated YAML follows Argo CD's behavior.

Matrix generators combine exactly two child generators and interpolate the
second child from first-child params, including templated `elementsYaml`.

Merge generators overlay two or more child generators by `mergeKeys` in base
generator order.

Matrix and merge children may use list, Git directories, Git files,
fixture-backed provider generators, and nested matrix/merge combinations where
the Argo CD v3 nested JSON API permits them.

## Provider Fixtures

Provider-backed generators use explicit local fixture files supplied with the
repeatable `--appset-provider-fixture` flag:

```bash
drydock get apps --path . --appset-provider-fixture fixtures/appset-providers.yaml
drydock diff apps --path . --path-orig ../base --appset-provider-fixture fixtures/appset-providers.yaml
```

Fixture files are strict YAML or JSON documents. Unknown fields, duplicate
identities, URL-like fixture paths, and malformed files produce
`appset.provider-fixture-invalid`. If fixtures are supplied but no entries
match a provider generator, drydock emits `appset.provider-no-match`. Filters
that cannot be evaluated from fixture data fail closed with
`appset.provider-unsupported-filter`.

### Fixture Schema

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
    repository: example-repo
    repositoryID: repo-123
    branch: main
    sha: abcdef1234567890
    url: https://github.com/example-org/example-repo
    labels:
      - ops
    paths:
      - deploy/app.yaml
    values:
      tier: ops

pullRequests:
  - provider: github
    organization: example-org
    repository: example-repo
    number: 42
    title: Update chart
    branch: renovate/chart
    targetBranch: main
    headSHA: abcdef1234567890
    author: renovate
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

## Template Parameters

Provider fixtures emit the same stable template parameter names that Argo CD
uses for each supported provider family:

| Generator | Stable template parameters |
| --- | --- |
| `clusters` | `name`, `nameNormalized`, `server`, `project`, metadata labels/annotations, `values` |
| `clusterDecisionResource` | `name`, `server`, decision fields, `values` |
| `scmProvider` | `organization`, `repository`, `repository_id`, `url`, `branch`, `branchNormalized`, `sha`, `short_sha`, `short_sha_7`, `labels`, `values` |
| `pullRequest` | `number`, `title`, `branch`, `branch_slug`, `target_branch`, `target_branch_slug`, `head_sha`, `head_short_sha`, `head_short_sha_7`, `author`, `labels`, `values` |
| `plugin` | fixture output fields, `generator.input.parameters`, `values` |

For non-Go-template ApplicationSets, nested maps are flattened with dot
notation, including `metadata.labels.<key>`, `metadata.annotations.<key>`, and
`values.<key>`.

For Go-template ApplicationSets, nested values remain available as maps or
arrays, such as `.metadata.labels`, `.metadata.annotations`, `.labels`, and
`.values`.
