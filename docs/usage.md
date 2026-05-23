# Usage

List Applications:

```bash
argocd-local get apps --path .
```

Build all Applications:

```bash
argocd-local build apps --path .
```

Diff two repository trees:

```bash
argocd-local diff apps --path ./current --path-orig ../base
```

Map a repository URL to a local tree:

```bash
argocd-local diff apps \
  --path ./current \
  --path-orig ../base \
  --repo-map https://github.com/example/repo=./current
```

Use network fetches only when explicitly allowed:

```bash
argocd-local build apps --path . --allow-network
```
