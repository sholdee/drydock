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

## Covered Areas

| Area | Covered examples |
| --- | --- |
| Directory | recurse, include/exclude handling, skip files, non-recursive auto-detected directories, multi-document YAML files, `.yml`/`.json` extensions, non-Kubernetes YAML silently skipped, null-items Lists as no-ops, shape-based (kind-agnostic) list flattening including `ConfigMapList`, explicit directory-mode forcing on a kustomize path, `**` include globs with path-scoped excludes |
| Helm | release name and namespace, values, parameters, file parameters, capabilities, value-file globs, skip CRDs/tests, vendored subchart dependencies with `condition`/`alias`/global values propagation, default CRD inclusion, `values`/`valuesObject`/`valueFiles`/`parameters` precedence chain (`valuesObject` wholly replaces the `values` string), `helm.namespace` override distinct from destination namespace, `skipSchemaValidation`, `kubeVersion` `+suffix` normalization, env-var substitution in parameter values and valueFile names, value files and `fileParameters` outside the chart directory bounded at the repo root, comma in parameter values, `.argocd-source.yaml` and `.argocd-source-<app>.yaml` overrides (JSON merge patch, arrays replaced wholesale), `$ref` fileParameters |
| Kustomize | base rendering, namespace, images, replicas, labels, annotations, components, patches, Helm charts, `kustomization.yml` and bare `Kustomization` filename variants, `nameSuffix`, image digest replacement, strategic-merge patches without explicit target, `labelWithoutSelector`, `labelIncludeTemplates`, `forceCommonLabels`, `forceCommonAnnotations`, `commonAnnotationsEnvsubst`, `configMapGenerator` hash suffixes, `ignoreMissingComponents`, `kubeVersion`/`apiVersions` pass-through to `helmCharts` inflation |
| Jsonnet | ext vars, top-level arguments, code mode, libraries |
| Multi-source | `$ref` values, ref-only sources, source precedence, last-wins resources |
| ApplicationSet | git directories, git files, list, matrix, merge, Go templates, `missingkey=error`, selectors, generator template overrides, `templatePatch`, fasttemplate (default) mode with `path`/`path.basename`/`path[N]`/`values.*` params, `elementsYaml` generator input, `normalize` and `upper` template functions |
| Tracking and resources | Argo CD tracking metadata, repeated-resource last-wins behavior, CRDs excluded from tracking metadata, hook-annotated resources excluded from the manifests view across all source types (helm test hooks, Argo CD sync hooks; `crd-install` kept) |

The source of truth for the active fixture inventory is the parity smoke script;
this table summarizes the covered semantics rather than enumerating every
fixture name.

## How To Read The Coverage

The parity smoke is not a proof that every possible Argo CD repository shape is
identical. It is a regression harness for the rendering semantics drydock
implements. If a semantic gap is found, the fix should add or extend a parity
fixture.
