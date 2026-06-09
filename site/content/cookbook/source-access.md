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

## Prove Cache-Only Runs

```bash
drydock test apps --path .
drydock test apps --path . --offline
```

The first command can populate drydock caches. The second command requires
local files, repo maps, or existing cache entries.

## Inspect Cache Events

```bash
drydock diag --path . --cache-events -o json
drydock cache list -o json
```

Use cache events to see which source acquisitions were used while rendering
Applications for diagnostics, then inspect recognized cache entries.

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
