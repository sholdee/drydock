---
title: Source Acquisition
---

drydock renders from local files and explicit source caches. It may fetch
declared Git, HTTP Helm, OCI Helm, and remote Kustomize inputs unless
`--offline` is set.

## Resolution Order

For repository sources, drydock resolves deterministically:

1. Explicit `--repo-map URL=PATH`.
2. Existing local source path under the selected repository tree.
3. Declared Git cache or fetch behavior for unmapped external repositories.
4. Clear failure.

`--repo-map` wins over local fallback and network fetching. Use it for adjacent
local checkouts or CI jobs that prepare dependencies explicitly:

```bash
drydock test apps --path . \
  --repo-map https://github.com/example/platform-config=../platform-config
```

## Caches And Offline Runs

Render-time Git, chart, and remote-resource caches must stay outside the
current and baseline repository trees. Populate caches with a non-offline run,
then use `--offline` for cache-only validation:

```bash
drydock test apps --path .
drydock test apps --path . --offline
```

Cache lifecycle commands are local filesystem operations only:

```bash
drydock cache path
drydock cache list -o json
drydock cache prune --older-than 720h --dry-run
```

## Credentials

Credentials are explicit and non-interactive. drydock does not read ambient Git
credential helpers, ambient Helm registry config, or credential fields from
discovered repository Secrets.

Use the canonical [source acquisition guide](/docs/source-acquisition/) for
the complete flag list, Helm dependency behavior, remote Kustomize forms, and
credential details.
