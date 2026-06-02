---
title: Repository Topologies
---

Match the command to the shape of the repository before tuning flags.

## Committed Applications

```bash
drydock get apps --path .
drydock test apps --path .
drydock diff apps --repo . --ref HEAD --ref-orig main
```

Use this when Argo CD `Application` manifests are committed directly.

## App-Of-Apps

```bash
drydock get apps --path .
drydock test apps --path .
```

By default, fleet commands recursively render bootstrap Applications and
app-of-apps sources to discover rendered `Application`, `ApplicationSet`,
`AppProject`, and Argo CD settings objects until discovery converges. Add
`--discovery-mode static` when only committed objects should count.

## Kustomize Bootstrap

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

Use this when Argo CD objects live behind Kustomize overlays instead of
committed inflated YAML.

## Multi-Repository Sources

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
drydock test apps --path . --offline
```

Use repo maps for adjacent checkouts and `--offline` for cache-only runs.

## ApplicationSets

```bash
drydock get apps --path .
drydock get apps --path . \
  --appset-provider-fixture fixtures/appset-providers.yaml
```

Local generators expand offline. Provider-backed generators need explicit
fixture files.

## Plugin Sources

```bash
drydock test apps --path .
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

Start without plugin execution. Add trusted policy flags only when the
repository depends on non-native plugin rendering.

For deeper guidance, see the canonical
[repository topology guide](/docs/topologies/),
[source acquisition](/docs/source-acquisition/),
[ApplicationSet reference](/docs/applicationsets/), and
[plugin policy](/docs/plugin-policy/).
