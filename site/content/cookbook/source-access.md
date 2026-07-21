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

## Render OCI Artifact Sources

```yaml
source:
  repoURL: oci://ghcr.io/example/config-artifact
  targetRevision: 1.2.3
  path: .
```

First-class OCI artifact sources render with normal commands — no extra flags:

```bash
drydock test apps --path .
drydock build apps --path . --oci-cache-dir /var/cache/drydock-oci
```

The revision may be a digest (`sha256:...`), an exact tag, or a semver
constraint such as `1.x`. Tags re-resolve against the registry on every online
run, so a re-pushed tag is picked up automatically; pin a digest when the
render must not move. For `--offline` runs, warm the cache with one online run
first — digests render from the cached image, and tags or constraints resolve
from records captured online. Private registries requiring authentication use
the explicit `--oci-*` credential flags — see
[Render Private OCI Artifact Registries](#render-private-oci-artifact-registries).

## Render Private OCI Artifact Registries

```bash
drydock test apps --path . \
  --oci-username "$OCI_USER" \
  --oci-password "$OCI_PASSWORD"
drydock test apps --path . \
  --oci-username "$OCI_USER" \
  --oci-password "$OCI_PASSWORD" \
  --oci-ca-file ./ci/registry-ca.pem
```

The `--oci-*` flags are one global set: the same username/password pair and
TLS configuration are presented to every OCI artifact registry the run
touches. `--oci-ca-file` replaces the system trust pool for all OCI artifact
registries in the run, and setting any TLS-implying flag (`--oci-ca-file`,
`--oci-client-cert-file`, `--oci-client-key-file`,
`--oci-insecure-skip-verify`) switches loopback registries from their
plain-HTTP development default to TLS. Credentials embedded in the `oci://`
repository URL are rejected; pass them through the flags. Details are in
[source acquisition](/concepts/source-acquisition/#authentication-and-tls).

In the GitHub PR action, pass the same flags through the trusted `extra-*`
inputs with repository secrets. The action defines no OCI credential inputs;
both diff sides share the extra diff arguments, and the action never echoes
its argument list, so secret values stay out of workflow logs:

```yaml
- uses: sholdee/drydock/pr-action@main
  with:
    extra-test-args: |
      --oci-username=${{ secrets.OCI_REGISTRY_USER }}
      --oci-password=${{ secrets.OCI_REGISTRY_PASSWORD }}
    extra-diff-args: |
      --oci-username=${{ secrets.OCI_REGISTRY_USER }}
      --oci-password=${{ secrets.OCI_REGISTRY_PASSWORD }}
```

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
