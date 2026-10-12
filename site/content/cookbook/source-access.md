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

A Helm chart in a registry is an OCI artifact too: the chart's repository is
the `repoURL`, the chart version is the `targetRevision`, `path: .` is the
chart root, and the `helm` block applies as usual. Do not add `chart:` to an
`oci://` source — Argo CD ignores it there, and drydock warns with
`source.oci-chart-ignored`:

```yaml
source:
  repoURL: oci://ghcr.io/bjw-s-labs/helm/app-template
  targetRevision: 5.2.1
  path: .
  helm:
    valuesObject:
      controllers: {}
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
`--registry-config` or the explicit `--oci-*` credential flags — see
[Render Private OCI Artifact Registries](#render-private-oci-artifact-registries).

## Render Private OCI Artifact Registries

```bash
drydock test apps --path . \
  --registry-config "$HOME/.config/helm/registry/config.json"
drydock test apps --path . \
  --oci-username "$OCI_USER" \
  --oci-password "$OCI_PASSWORD"
drydock test apps --path . \
  --oci-username "$OCI_USER" \
  --oci-password "$OCI_PASSWORD" \
  --oci-ca-file ./ci/registry-ca.pem
```

`--registry-config` points at a Docker/Helm registry config file (what
`helm registry login` or `docker login` writes) and supplies credentials per
registry host, for OCI artifact sources and OCI Helm chart registries alike.
drydock reads and validates it at the first `oci://` acquisition, so passing
it on runs without OCI sources is harmless; hosts without an entry stay
anonymous, and identity-token entries or credential helpers that cannot run
are errors for artifact sources rather than silent anonymous pulls. The
`--oci-username`/`--oci-password` pair is the global override: when either is
set, the file is not opened and the pair is presented to every OCI artifact
registry the run touches. The TLS configuration is one global set:
`--oci-ca-file` replaces the system trust pool for all OCI artifact
registries in the run, and setting any TLS-implying flag (`--oci-ca-file`,
`--oci-client-cert-file`, `--oci-client-key-file`,
`--oci-insecure-skip-verify`) switches loopback registries from their
plain-HTTP development default to TLS. The same four TLS flags also configure
OCI Helm chart pulls, where the bundle is added to the system pool instead of
replacing it. Credentials embedded in the `oci://` repository URL are
rejected; pass them through the flags or the file. Details are in
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

## Use The Runner's SSH Identity

Git SSH sources need no flags when the runner already has an SSH identity.
drydock uses the ssh-agent, then `~/.ssh/config` `IdentityFile` entries, then
`~/.ssh/id_ed25519`, `id_ecdsa`, and `id_rsa`. Host keys are always verified,
so `known_hosts` must contain the host.

Locally, load the key once and render normally:

```bash
ssh-add ~/.ssh/id_ed25519
drydock test apps --path .
```

In GitHub Actions, start an agent before the action runs:

```yaml
- uses: webfactory/ssh-agent@v0.10.0
  with:
    ssh-private-key: ${{ secrets.DEPLOY_KEY }}
- run: ssh-keyscan github.com >> ~/.ssh/known_hosts
- uses: sholdee/drydock/pr-action@main
```

The action inherits `SSH_AUTH_SOCK` from the job, so no drydock input is
needed.

On a Forgejo or self-hosted runner, a key in the runner's `~/.ssh`, or an
`IdentityFile` entry in its `~/.ssh/config`, is picked up the same way. Add the
host to the runner's `~/.ssh/known_hosts` once:

```bash
ssh-keyscan code.example.com >> ~/.ssh/known_hosts
```

Passphrase-protected keys are skipped — drydock never prompts. Pass those with
`--git-ssh-key-file` and `--git-ssh-passphrase`.

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

Explicit credentials win: `--git-ssh-key-file` disables the ambient identity
lookup entirely, and `--registry-config` covers OCI Helm chart registries and
`oci://` artifact sources per registry host. Credential handling is
non-interactive. Complete cache, auth,
Helm, Git, and remote Kustomize behavior is in
[source acquisition](/concepts/source-acquisition/).
