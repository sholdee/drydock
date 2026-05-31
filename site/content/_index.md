---
title: drydock
---

Review Argo CD repository changes before they become controller work.

Inspect your Argo CD fleet without getting wet.

drydock is a fast, single static Go binary and embeddable Go module for
runtime-offline Argo CD desired-state analysis. It discovers, renders, tests,
diffs, and diagnoses GitOps Applications with native Go renderers, no Argo CD
server, no Kubernetes credentials, and no default shellouts.

Use it locally or in CI to answer operator questions that are hard to review in
raw Git: which Applications render, which desired resources changed, which
images moved, and which repository inputs need attention.

**[Get Started](/getting-started/)** | **[Set Up PR Checks](/workflows/github-actions/)**

## Start Here

- [Getting started](/getting-started/) installs drydock and runs the first
  local render test.
- [GitHub Actions](/workflows/github-actions/) sets up pull request checks
  without Kubernetes or Argo CD credentials.
- [Local diffs](/workflows/local-diffs/) covers local tree and Git ref
  comparisons.
- [How it works](/concepts/how-it-works/) explains discovery, source
  acquisition, rendering, normalization, and reporting.
- [Troubleshooting](/troubleshooting/) maps common operator symptoms to the
  first commands to run.

## Core Workflows

```bash
drydock get apps --path .
drydock test apps --path .
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main
drydock diag --path .
```

## Choose Your Workflow

| Need | Start with | Read next |
| --- | --- | --- |
| Confirm the fleet renders | `drydock test apps --path .` | [Getting started](/getting-started/) |
| Review manifest changes | `drydock diff apps --repo . --ref HEAD --ref-orig main -o markdown` | [GitHub Actions](/workflows/github-actions/) |
| Scan image movement | `drydock diff images --repo . --ref HEAD --ref-orig main -o markdown` | [Local diffs](/workflows/local-diffs/) |
| Explain warnings or failures | `drydock diag --path . --cache-events` | [Troubleshooting](/troubleshooting/) |
| Validate cache-only operation | `drydock test apps --path . --offline` | [Source acquisition](/concepts/source-acquisition/) |

## What It Covers

| Capability | Result |
| --- | --- |
| Discover | Find committed and supported generated Argo CD Applications. |
| Render | Inflate directory, Kustomize, Helm, Jsonnet, and supported remote sources. |
| Test | Prove Applications render before Argo CD syncs them. |
| Diff | Compare rendered desired manifests across paths or Git refs. |
| Inspect images | Report rendered image reference additions and removals. |
| Diagnose | Surface repository, project, source, cache, plugin, and settings issues. |

## Operating Model

drydock is runtime-offline: render, test, diff, image, and diagnostic commands
do not call Kubernetes or Argo CD APIs. Declared Git, HTTP Helm, OCI Helm, and
remote Kustomize sources can still be fetched into explicit caches unless
`--offline` is set.

Use these curated pages for day-to-day operation. Use the reference docs for
full compatibility notes, action inputs, source acquisition flags, and plugin
policy schema.
