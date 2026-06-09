---
title: Bootstrap Entrypoints
---

## Bootstrap Entrypoints

Some repositories keep Argo CD bootstrap objects behind a config management
plugin rather than committing `Application` or `ApplicationSet` YAML directly.
Policy bootstrap entrypoints let drydock render those trusted plugin sources
during fleet discovery so their generated Argo CD objects become normal
discovered inputs.

Bootstrap discovery runs after committed and explicit Kustomize discovery and
the first ApplicationSet expansion. drydock then expands any ApplicationSets
produced by bootstrap output before recursive rendered fleet discovery.

Bootstrap entrypoints run only in fleet discovery mode. `--discovery-mode
static` disables them; `--max-discovery-depth 0` does not. Static committed
objects take precedence over bootstrap-rendered duplicates, and
bootstrap-rendered objects take precedence over recursive rendered fleet
duplicates.

Each bootstrap entrypoint creates an internal, hidden synthetic Application
with namespace `argocd`, project `default`, destination name `in-cluster`, and
destination namespace `argocd`. The synthetic Application is used only to
render and scan the entrypoint output; it is not returned as a discovered
Application.

Bootstrap entrypoints fail closed. The `sourcePath` must be repository-local,
exist, be a directory, avoid symlink components, and match the referenced
plugin's trusted static `match.discover` or `configManagementPlugin.discover`
rule. Bootstrap render failures are discovery errors rather than skipped
recursive app-of-apps candidates.

See the [schema reference](/docs/plugin-policy/schema/) for
`bootstrap.entrypoints` fields and [examples](/docs/plugin-policy/examples/)
for a trusted container bootstrap policy.
