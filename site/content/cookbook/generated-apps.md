---
title: Generated Applications
---

## List Generated Apps

```bash
drydock get apps --path .
```

Use this first for app-of-apps and `ApplicationSet` repositories. It shows the
Applications drydock can discover before rendering tests or diffs.

## Render Kustomize Bootstrap Objects

```bash
drydock get apps --path . --discover-kustomize clusters/prod/argocd
drydock test apps --path . --discover-kustomize clusters/prod/argocd
```

Use this when committed files are Kustomize inputs that render Argo CD objects.

## Supply Provider Fixtures

```bash
drydock get apps --path . \
  --appset-provider-fixture fixtures/appset-providers.yaml
drydock test apps --path . \
  --appset-provider-fixture fixtures/appset-providers.yaml
```

Provider-backed `ApplicationSet` generators are fixture-backed offline. Keep
fixtures in the repository or prepare them in CI before running drydock.

## Disable Recursive Rendered Discovery

```bash
drydock get apps --path . --discovery-mode static
drydock get apps --path . --max-discovery-depth 0
```

Use static discovery when only committed Argo CD objects should be considered.

Details live in [repository topologies](/docs/topologies/) and the
[ApplicationSet reference](/docs/applicationsets/).
