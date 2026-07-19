---
title: Source Access
---

## Map Adjacent Repositories

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
```

Use `--repo-map` when CI or a developer workstation already checked out a
source repository.

Self-references need no mapping: a source naming the checkout's own repository
at `HEAD` or its default-branch name resolves to the local tree automatically
on all render surfaces. Keep `--repo-map` for forks, commit-SHA pins, and runs
from a subdirectory of the checkout.

## Prove Cache-Only Runs

```bash
drydock test apps --path .
drydock test apps --path . --offline
```

The first command can populate drydock caches. The second command requires
local files, repo maps, or existing cache entries.

## Inspect Cache Events

```bash
drydock diag --path . --render --cache-events -o json
drydock cache list -o json
```

Use cache events to see which source acquisitions were used while rendering
Applications for diagnostics, then inspect recognized cache entries. Render
cache `skipped` events explain why an Application rendered normally instead of
using a persisted render output.

Dirty local worktrees can still hit persisted render outputs for Applications
whose proven inputs did not change. In `diag --render --cache-events` output,
look for render cache `hit`, `miss`, `store`, and `skipped` actions. Dirty
inputs that are unsafe to hash, such as symlinks or unsupported file types,
render normally and report a `skipped` event for the affected Application.

## Pass Explicit Credentials

```bash
drydock test apps --path . \
  --git-ssh-key-file ./ci/deploy_key \
  --git-known-hosts-file ./ci/known_hosts
drydock test apps --path . \
  --helm-username "$HELM_USER" \
  --helm-password "$HELM_PASSWORD"
drydock test apps --path . --registry-config ./ci/registry-config.json
```

Credential handling is explicit and non-interactive. Complete cache, auth,
Helm, Git, and remote Kustomize behavior is in
[source acquisition](/concepts/source-acquisition/).
