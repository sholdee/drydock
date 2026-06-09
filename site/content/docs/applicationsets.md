---
title: ApplicationSet Reference
---

drydock expands a deterministic local subset of Argo CD `ApplicationSet`
generators. Unsupported generators emit diagnostics; non-strict commands keep
supported generated Applications, while `--strict` promotes those diagnostics to
errors.

Provider-backed generators are fixture-backed only. drydock does not contact
Kubernetes, Argo CD, SCM provider, pull-request, cloud, or plugin-service APIs
while generating Applications.

## Supported Generators

| Generator | Support |
| --- | --- |
| Git directories | Native. Matches are sorted by normalized relative path. |
| Git files | Native. YAML and JSON mapping documents are decoded into params. |
| List | Native, including `elementsYaml`. |
| Matrix | Native for two child generators, including interpolated child params. |
| Merge | Native for two or more child generators over `mergeKeys`. |
| Provider-backed generators | Fixture-backed for clusters, clusterDecisionResource, SCM provider, pull requests, and plugin generators. |

Supported template behavior includes:

- `spec.goTemplate: true`
- `spec.goTemplateOptions`, including `missingkey=error`
- generator-level selectors and template overrides
- `spec.templatePatch` applied to generated Applications
- Sprig-compatible functions used by Argo CD, including `regexReplaceAll`
- generated `Application.metadata.namespace` set to the ApplicationSet
  namespace
- Argo CD's default generated Application finalizer where applicable

## Git Generators

Git directory and file matches are sorted by normalized relative path. Include
and exclude patterns are deterministic, and `exclude: true` removes a path even
when another pattern includes it.

When a Git generator defines both `directories` and `files`, Argo CD's
directory-first dispatch is used and `files` are ignored.

Git files must stay under the repository root and must not traverse symlinks.
YAML and JSON files may decode to a mapping document, an array of mapping
documents, an empty mapping, or an empty file. Scalars, invalid YAML/JSON, and
arrays with non-mapping entries produce diagnostics.

## Provider Fixtures

Use `--appset-provider-fixture` to provide deterministic local data for
provider-backed generators:

```bash
drydock get apps --path . --appset-provider-fixture fixtures/appset-providers.yaml
drydock diff apps --path . --path-orig ../base --appset-provider-fixture fixtures/appset-providers.yaml
```

Fixture files are strict YAML or JSON documents. Unknown fields, duplicate
identities, URL-like fixture paths, and malformed files produce diagnostics. If
fixtures are supplied but no entries match a provider generator, drydock reports
that no fixture matched.

```yaml
clusters:
  - name: prod-a
    server: https://prod-a.example.invalid
    project: platform
    labels:
      environment: prod
    values:
      region: home

scmRepositories:
  - provider: github
    organization: example-org
    repository: example-repo
    branch: main
    sha: abcdef1234567890
    url: https://github.com/example-org/example-repo
    labels:
      - ops

pullRequests:
  - provider: github
    organization: example-org
    repository: example-repo
    number: 42
    branch: renovate/chart
    targetBranch: main
    headSHA: abcdef1234567890
    author: renovate
```

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
notation. For Go-template ApplicationSets, nested values remain available as
maps or arrays.
