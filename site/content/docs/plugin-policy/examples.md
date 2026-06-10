---
title: Examples
---

## Examples

Native argocd-vault-plugin (AVP) placeholder compatibility:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  argocd-vault-plugin:
    engine: avp-compat
```

AVP CMP aliases use the same native compatibility engine:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp-directory-include:
    engine: avp-compat
```

Native Kustomize compatibility copied from trusted Docker or sidecar CMP
descriptor metadata:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  kustomize-build-with-helm:
    engine: native-kustomize
    configManagementPlugin:
      discover:
        fileName: kustomization.yaml
      generate:
        command: ["kustomize", "build"]
        args: ["--enable-helm"]
```

The policy keeps only trusted static descriptor metadata. drydock uses the
native Kustomize renderer and native option validation; it does not run a Docker
sidecar, shell wrapper, or the copied CMP command.

Trusted Docker-backed renderer policy:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: container
    image: registry.example.com/drydock/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    configManagementPlugin:
      discover:
        fileName: PklProject
    copy:
      scope: repository
      include:
        - packages/**
        - personal-cluster/**
    generate:
      command: ["pkl", "eval", "{{param:path}}"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            base: repository
            allow:
              - personal-cluster/**/*.pkl
```

Plugin-rendered Pkl bootstrap discovery with a trusted container image:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: cluster-root
      plugin: pkl
      sourcePath: personal-cluster
      parameters:
        - name: path
          string: index.pkl
plugins:
  pkl:
    engine: container
    image: registry.example.com/drydock/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    network: default
    configManagementPlugin:
      discover:
        fileName: PklProject
    copy:
      scope: repository
      include:
        - packages/**
        - personal-cluster/**
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache/pkl-cache
    init:
      command: ["pkl", "project", "resolve", "--cache-dir", "/drydock-cache/pkl-cache"]
    generate:
      command: ["pkl", "eval", "--cache-dir", "/drydock-cache/pkl-cache", "{{param:path}}"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            base: source
            allow:
              - index.pkl
              - components/*.pkl
        - name: pkl_modules
          type: array
```

The bootstrap entrypoint renders `personal-cluster` during fleet discovery,
scans the output for Argo CD `Application`, `ApplicationSet`, `AppProject`, and
settings objects, then treats those objects like other discovered desired
state. The referenced source must match the plugin's trusted static discovery
rule before drydock runs the command-backed plugin. Use repository-scoped copy
when the plugin source imports sibling directories such as shared Pkl packages.
Use `network: default` only when trusted package resolution needs network
access; it is rejected with `--offline`.

Trusted Pkl exec policy using a workdir-relative cache and a repository-scoped
path parameter:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: PklProject
    copy:
      scope: repository
      include:
        - packages/**
        - pkl-packages/**
    init:
      command: ["pkl", "project", "resolve", "--cache-dir", ".drydock-pkl-cache"]
    generate:
      command: ["pkl", "eval", "--cache-dir", ".drydock-pkl-cache", "{{param:path}}"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            base: repository
            allow:
              - personal-cluster/**/*.pkl
```

This replaces sidecar patterns such as `/tmp/pkl-cache`, `sh -c`, or Docker
execution with trusted argv. `configManagementPlugin.generate`, if copied into
the policy for compatibility metadata, still remains metadata only. The
top-level exec `generate.command` is the command that runs.

When an Application already supplies source-relative `path` values, use
`path.base: source` with source-relative `path.allow` patterns instead.

Exec policy for a trusted non-native renderer with a post-renderer:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  ytt-render:
    engine: exec
    generate:
      command: ["/usr/local/bin/ytt", "-f", "."]
      timeout: 45s
    postRenderers:
      - command: ["/usr/local/bin/kbld", "-f", "-"]
        timeout: 15s
    env:
      allow: ["CLUSTER_NAME", "ENVIRONMENT"]
    output:
      maxStdoutBytes: 10485760
      maxStderrBytes: 65536
```

The checked fixtures in `testdata/plugin-policy/` are parsed and fingerprinted
by unit tests; keep documentation examples aligned with those parser rules.

Scaffold a starting policy from committed Applications and CMP settings:

```bash
drydock plugin-policy init --path .
drydock plugin-policy init --path . --write
drydock plugin-policy init --path . --engine exec --output .drydock/plugins.exec.yaml
```

`init` prints YAML to stdout by default. `--write` writes
`.drydock/plugins.yaml`; `--output` writes a repository-relative path. Existing
files require `--overwrite`. The generated YAML includes the schema modeline.
When trusted CMP evidence is clear, `init` can scaffold `avp-compat` or
`native-kustomize` entries. Other plugins use `container` as the fallback
scaffold engine. Digest-pinned sidecar images may be inferred. Tag-only image
candidates stay as placeholders unless `--allow-mutable-image-tags` is passed.

Check onboarding gates without executing plugins:

```bash
drydock plugin-policy doctor --path .
drydock plugin-policy doctor --path . --plugin-policy-ref main --enable-plugins -o json
drydock plugin-policy doctor --path . --plugin-policy-ref main --enable-plugins --strict
```

`doctor` reports readiness for policy presence, trust provenance,
`--enable-plugins`, image placeholders, mutable image tags, parameters, env,
and bootstrap hints. A missing default policy is a readiness failure; an
explicit missing `--plugin-policy-path` is a command error.

For a single-tree command, run command-backed plugins from an explicit trusted
ref:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

For a pull request diff, the baseline policy is the trusted source:

```bash
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```
