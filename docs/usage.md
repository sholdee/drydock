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

Planned command surface. The `diff apps` command and `--path-orig` flag are
present, but desired-vs-desired diff execution is not wired in the current E2E
CLI path.

```bash
argocd-local diff apps --path ./current --path-orig ../base
```

Map a repository URL to a local tree:

Planned command surface. `--repo-map` is parsed, but local repository mapping is
not wired through the current E2E build or diff paths yet.

```bash
argocd-local diff apps \
  --path ./current \
  --path-orig ../base \
  --repo-map https://github.com/example/repo=./current
```

Use network fetches only when explicitly allowed:

Planned command surface. `--allow-network` exists, but network fetching is not
wired in the current E2E `build apps` path.

```bash
argocd-local build apps --path . --allow-network
```
