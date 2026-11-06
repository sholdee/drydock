---
title: Argo CD Render Parity
---

Argo CD is the semantic reference for generated desired manifests. drydock
keeps normal render, test, diff, image, and diagnostic commands
runtime-offline, then validates covered rendering semantics against real Argo
CD in isolated maintainer smoke tests.

The goal is not to replace Argo CD as the source of truth. The goal is to make
repository analysis fast, portable, and repeatable without requiring every
local command or pull request check to stand up Argo CD and Kubernetes.

## Why Not Call Argo CD Every Time?

Calling Argo CD for every diff can be accurate, but it makes each run depend on
a live Argo CD installation, Kubernetes access, runtime credentials, API
availability, controller readiness, and more CI setup.

drydock takes a different tradeoff. Maintainer validation proves covered
rendering semantics against real Argo CD, while normal operator workflows use
the native runtime-offline engine.

## What This Buys Operators

- **Performance:** normal commands avoid live Argo CD startup, controller, API,
  and cluster costs.
- **Portability:** one static binary works locally and in CI without `kubectl`,
  `argocd`, Helm CLI, Kustomize CLI, or repo-server wrappers.
- **Repeatability:** results come from repository inputs, explicit source maps,
  and drydock caches instead of live runtime state.
- **Lower CI burden:** consuming repositories can run fast offline checks while
  drydock maintainers front-load Argo CD semantic validation.

## How Validation Works

The render parity smoke starts a kind cluster, installs the pinned Argo CD
version, serves a fixture Git repository to Argo CD, and applies fixture
`Application` and `ApplicationSet` objects.

For each fixture Application, the smoke captures Argo CD rendered manifests,
renders the same Application with drydock, canonicalizes both manifest streams
by Kubernetes resource identity, and compares the results. A mismatch is
treated as a rendering regression for the covered fixture set.

The smoke also compares the Application inventories. drydock must discover
and generate exactly the Applications that Argo CD holds, with the same
names, project, destination, and sources. This catches an ApplicationSet
that generates an extra Application in drydock only.

Some fixture Applications must fail. Most hold a directory file that the
Argo CD repo-server refuses to decode, such as invalid JSON, two objects in one
`.json` file, a broken YAML document after a valid one, or a Jsonnet object
without a `kind`. Others hold a Helm chart, a Kustomize base, or a plugin
program whose output Argo CD refuses to decode: a document without a `kind`,
an empty object `{}`, the `apiVersion` typo `apps/v1/`, or a List item
without a `kind`. The smoke checks that Argo CD reports a `ComparisonError`
condition, that drydock fails to render, and that both errors name the same
file. A `.json` object without a `kind`, such as `package.json` or `{}`,
fails later in the Argo CD controller, and so does a list item without a
`kind`. That error quotes the object instead of the file, so the smoke
matches the quoted object. The error for generated output without a `kind`
also quotes the object. For a plugin case, drydock names the plugin output
instead of the plugin program. A list item whose annotations are not a map fails
when the repo-server sets the tracking annotation. That error names the
object instead of the file, so the smoke matches the object name. Two
Applications hold two failing files each, and prove that a later file that
fails an earlier stage is reported first. One more Application renders from
a second Git repository that holds a symlink to `/etc/hosts`. Argo CD fails
every Application from such a repository with `repository contains
out-of-bounds symlinks`, so this repository holds nothing else. These
Applications live in their own namespace, so they do not affect the manifest
comparison.

The smoke also checks diff normalization against Argo CD sync status. A
third Git repository holds two tagged commits. Argo CD syncs some
Applications at the first commit, then compares the second commit with the
live objects. drydock diffs the same two commits. In the second commit, some
resources change only fields that an ignore rule removes, and Argo CD must
report them `Synced` while drydock reports no change. Other resources also
change a field that no rule removes, and both sides must report a change.
The rules come from Application `spec.ignoreDifferences`, argocd-cm
`resource.customizations` and `resource.compareoptions`, and the
`argocd.argoproj.io/ignore-differences` annotation. Most of these
Applications use server-side diff, and one uses the client-side diff. One
Application changes its own rules in the second commit, and Argo CD must use
the new rules. One Application has a jq rule that does not parse. Argo CD
must fail its comparison, and drydock must fail its diff.

## Covered Areas

| Area | Covered examples |
| --- | --- |
| Directory | recurse, include/exclude handling, skip files, non-recursive auto-detected directories, multi-document YAML files, `.yml`/`.json` extensions, non-Kubernetes YAML silently skipped, null-items Lists as no-ops, shape-based (kind-agnostic) list flattening including `ConfigMapList`, explicit directory-mode forcing on a kustomize path, `**` include globs with path-scoped excludes, case-sensitive extension filter (`.YAML` skipped), `disableExtensionFilter` with custom extensions selected by `include`, file text decoding (UTF-8 and UTF-16 byte order marks, invalid UTF-8 replaced, UTF-16 without a byte order mark skipped), skipped non-manifest files (values files, `{}`, YAML lists, syntax errors without manifest markers, a first document with an `apiVersion` but no `kind`, `kind: ""`, an `apiVersion` with two or more slashes), documents and Lists with a `kind` but no `apiVersion`, objects whose `items` field is not a list, Jsonnet Lists split into items and Jsonnet arrays with `null` and List elements, files that fail the source (invalid, empty, YAML, array, multi-object, or kind-less object `.json` such as `package.json` and `{}`, a list item without a `kind` in a `.json`, YAML, or Jsonnet file, a Jsonnet object without a `kind`, an `apiVersion` with two or more slashes in a `.json` or YAML file, a list item whose annotations are not a map, a broken or kindless YAML document after a valid one, a kindless first document in a file that looks like a manifest), the stage order that decides which of several failing files is reported, file symlinks (a target outside the source path rendered, a symlink chain, a symlink left out by `exclude` or the extension filter on its own name, a missing target, a symlink to a directory, a target with the skip marker, a Jsonnet file and a Jsonnet import reached through a symlink), and an out-of-bounds symlink in a second repository that fails its Application |
| Helm | release name and namespace, values, parameters, file parameters, capabilities, value-file globs, skip CRDs/tests, vendored subchart dependencies with `condition`/`alias`/global values propagation, default CRD inclusion, `values`/`valuesObject`/`valueFiles`/`parameters` precedence chain (`valuesObject` wholly replaces the `values` string), `helm.namespace` override distinct from destination namespace, `skipSchemaValidation`, `kubeVersion` `+suffix` normalization, env-var substitution in parameter values and valueFile names (`$ARGOCD_APP_NAME`), value files and `fileParameters` outside the chart directory bounded at the repo root, comma in parameter values, `.argocd-source.yaml` and `.argocd-source-<app>.yaml` overrides (JSON merge patch, arrays replaced wholesale), `$ref` fileParameters, `helm template` output read with `kube.SplitYAML` (Lists split into items, a null-items List, an object whose `items` field is a map, empty and comment-only documents, a file whose name ends in `NOTES.txt` dropped, a test hook List dropped as a whole under `skipTests`, documents with an unknown or empty `helm.sh/hook` event such as `crd-install` and `test-failure` dropped, repeated resources resolved in `helm template` order so that a List item and a hook copy win over a plain copy in a later file, white space trimmed at the end of the last document before the hooks), and charts that fail the source (a template document without a `kind`, `{}`, the `apiVersion` typo `apps/v1/`, a List item without a `kind`) |
| Kustomize | base rendering, namespace, images, replicas, labels, annotations, components, patches, Helm charts, `kustomization.yml` and bare `Kustomization` filename variants, `nameSuffix`, image digest replacement, strategic-merge patches without explicit target, `labelWithoutSelector`, `labelIncludeTemplates`, `forceCommonLabels`, `forceCommonAnnotations`, `commonAnnotationsEnvsubst`, `configMapGenerator` hash suffixes, `ignoreMissingComponents` (missing directories and directories without a kustomization file skipped, and only the first valid component added, with a warning for each dropped valid component), `kubeVersion`/`apiVersions` pass-through to `helmCharts` inflation, build output read with `kube.SplitYAML` (a resource with the `apiVersion` typo `apps/v1/` fails the source) |
| Jsonnet | ext vars, top-level arguments, code mode, libraries |
| Multi-source | `$ref` values, ref-only sources, source precedence, last-wins resources |
| ApplicationSet | git directories, git files, list, matrix, merge, Go templates, `missingkey=error`, selectors, generator template overrides, `templatePatch`, fasttemplate (default) mode with `path`/`path.basename`/`path[N]`/`values.*` params, `elementsYaml` generator input, `normalize` and `upper` template functions, template render error scoping (comment-only git files match under `missingkey=error` contributes zero Applications), the OCI generator against an in-cluster registry (directories and files with include and exclude patterns, an empty file pattern, hidden and symlinked directories, JSON and YAML param files including a list of objects, `pathParamPrefix` and `values` in Go template and fasttemplate mode, nested in a matrix with a list generator), generated Application name validation (an empty name drops that Application, a duplicated name drops every Application that shares it, and the rest still generate), cluster generator over cluster Secrets with reserved annotations hidden in Go-template and fasttemplate modes, `hasKey` on the `map[string]string` cluster annotations failing the render so that neither side creates Applications, generator selectors on the `[]string` `path.segments` param flattened to one `[a b c]` value, RollingSync strategies with progressive syncs on (the ApplicationSet `InvalidRolloutConfig` or `ErrorOccurred` condition compared with drydock's rollout warnings for valid steps, an unsupported `matchExpressions` operator, an Application selected by two steps, a step with no Applications, an invalid `maxUpdate`, and an empty steps list, for which neither side creates Applications) |
| Apps in any namespace | Application outside the controller namespace: instance-name `ARGOCD_APP_NAME` (`<namespace>_<name>`), destination `ARGOCD_APP_NAMESPACE`, `.argocd-source-<namespace>_<name>.yaml` override with the bare-name file ignored, AppProject `sourceNamespaces`, tracking metadata compared without ignore rules |
| Config management plugins | Named CMP sidecar with no `discover` block against a trusted `engine: exec` policy: the `ARGOCD_APP_*` build environment, `spec.source.plugin.env` delivered as `ARGOCD_ENV_<name>` after build-environment substitution and never under its bare name, the exact `ARGOCD_APP_PARAMETERS` JSON, and the `PARAM_*` surface with no extra names, plugin output read with `kube.SplitYAML` (an empty object `{}` fails the source) |
| Tracking and resources | Argo CD tracking metadata, repeated-resource last-wins behavior, CRDs excluded from tracking metadata, hook-annotated resources excluded from the manifests view across all source types (helm test hooks, Argo CD sync hooks; `crd-install` kept by the hook rules, although `helm template` drops it from Helm output) but still validated against AppProject resource policy, as Argo CD validates hook tasks at sync time and delete hooks at deletion |
| Diff normalization | Argo CD sync status after a change between two commits: Application `jsonPointers` (including a pointer that is not valid and only warns), `jqPathExpressions` with a select, a `jq` expression that fails to run and is skipped, a `jq` expression that does not parse and fails the comparison, rules that change with the commit, `managedFieldsManagers` that own no field, argocd-cm split keys (a core kind without a slash, `all`) and the block form, `ignoreAggregatedRoles`, and the `argocd.argoproj.io/ignore-differences` annotation read from the target commit with JSON-patch path rules, under server-side diff and the client-side diff |

## AppProject Policy Check

The same smoke also checks AppProject Application-spec policy against the live
Argo CD. It compares the policy conditions Argo CD sets on each fixture
Application with the diagnostics `drydock diag` reports. The cases cover
source repositories, source namespaces, and destinations. The destination cases
cover these rules:

- A deny entry (`!`) wins over an allow entry, listed before or after it.
- A destination named `in-cluster`, or at server
  `https://kubernetes.default.svc`, reaches the built-in cluster when no cluster
  Secret claims it.
- A cluster Secret named `in-cluster` takes that name from the built-in cluster.
- argocd-cm `cluster.inClusterEnabled: "false"` rejects the built-in cluster.
- A project-scoped cluster Secret that exists only in the live Argo CD adds a
  destination that drydock cannot see. drydock must report a deferred
  diagnostic, not a denial.

The source of truth for the active fixture inventory is the parity smoke script;
this table summarizes the covered semantics rather than enumerating every
fixture name.

## How To Read The Coverage

The parity smoke is not a proof that every possible Argo CD repository shape is
identical. It is a regression harness for the rendering semantics drydock
implements. If a semantic gap is found, the fix should add or extend a parity
fixture.
