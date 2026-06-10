---
title: Plugin Policy
aliases:
  - /docs/plugin-policy/
---

drydock plugin policy is the trusted, drydock-specific gate for config
management plugin compatibility beyond drydock's built-in native adapters. It
is not Argo CD repo-server sidecar auto-discovery, and it does not make
arbitrary discovered commands safe to execute.

Operators usually do not need policy for Kustomize wrapper plugins. If drydock
discovers a config management plugin command that safely normalizes to
`kustomize build`, it uses the native Kustomize renderer without shelling out.

## Start Here

- Onboarding an existing repo? Start with
  [`plugin-policy init`](#onboard-existing-repositories), then check readiness
  with `plugin-policy doctor`.
- Need to run a trusted plugin command? Use
  [Trust and Command Execution](/docs/plugin-policy/trust/).
- Choosing between native, exec, and container engines? Use
  [Engines](/docs/plugin-policy/engines/).
- Need plugin-rendered bootstrap Applications or ApplicationSets? Use
  [Bootstrap Entrypoints](/docs/plugin-policy/bootstrap/).
- Need Docker package or tool caches? Use
  [Container Plugin Caches](/docs/plugin-policy/cache/).
- Need field names and defaults? Use [Schema](/docs/plugin-policy/schema/).
- Need working YAML? Use [Examples](/docs/plugin-policy/examples/).

## Onboard Existing Repositories

`drydock plugin-policy init` is a static scaffold. It inspects Applications,
supported static ApplicationSet inputs, Argo CD CMP settings, and sidecar image
candidates that drydock can read from the repository. It does not render
Applications, run plugins, or make the generated policy trusted.

If a repository's Applications are visible only after plugin-rendered
bootstrap, `init` may have no static Application evidence to scaffold from.
Start from the matching engine example or existing sidecar/CMP policy, then
use `plugin-policy doctor` to check the policy gates.

Review YAML on stdout:

```bash
drydock plugin-policy init --path .
```

Write the default policy or a repository-relative path:

```bash
drydock plugin-policy init --path . --write
drydock plugin-policy init --path . --output .drydock/plugins.container.yaml
```

Existing files require `--overwrite`. Generated YAML includes the
`yaml-language-server` schema modeline. The default scaffold engine is
`container`; use `--engine exec` only when you intend trusted host-process
execution. Digest-pinned sidecar images may be inferred. Tag-only image
candidates remain placeholders unless `--allow-mutable-image-tags` is passed.

Check readiness before using command-backed plugins:

```bash
drydock plugin-policy doctor --path .
drydock plugin-policy doctor --path . --plugin-policy-ref main --enable-plugins -o json
```

`doctor` is also static: it reports policy, trust, image, parameter, env, and
execution gates without running plugin commands. A missing default
`.drydock/plugins.yaml` is a readiness `FAIL`; an explicitly selected missing
`--plugin-policy-path` is a command error. Use `--strict` when CI should exit
nonzero on readiness `FAIL` after the report is rendered.

## Use Policy For

- Deterministic argocd-vault-plugin placeholder redaction with
  `engine: avp-compat`.
- Explicit native Kustomize overrides with `engine: native-kustomize`.
- Trusted host-process compatibility with `engine: exec` and
  `--enable-plugins`.
- Trusted container compatibility with `engine: container` and
  `--enable-plugins`.
- Plugin-rendered `Application` and `ApplicationSet` discovery with
  `bootstrap.entrypoints`.

## Command Execution Gate

The CLI and default Go client run exec or container plugin commands only when
all of these are true:

- The Application source names a plugin that matches a drydock policy entry,
  or an unnamed plugin source matches trusted static discovery from policy.
- The matched entry uses `engine: exec` or `engine: container`.
- The caller passes `--enable-plugins`.
- The command-backed policy comes from trusted policy provenance.

For a single-tree command, use an explicit trusted ref:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

For pull request diffs, drydock loads policy from the trusted baseline side:

```bash
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```

## Container Cache Mounts

Container `cacheMounts` are policy-managed durable tool caches mounted only
under reserved `/drydock-cache` paths. `--plugin-cache-dir` is a render-time
override for the host root backing those mounts; it does not change trusted
policy target paths. Cache lifecycle commands still manage only Git, chart, and
remote-resource cache entry roots for now.

In the GitHub PR action, trusted plugin cache mounts live under the action cache
root as `${cache-path}/plugin` when trusted plugins are enabled.

## Bootstrap Entrypoints

`bootstrap.entrypoints` are fleet discovery inputs for repositories whose
plugin-rendered output contains Argo CD `Application` or `ApplicationSet`
objects. Static discovery mode disables them; `--max-discovery-depth 0` does
not. Each entrypoint must match trusted static discovery for its plugin.

## Detailed Reference

- [Trust and command execution](/docs/plugin-policy/trust/)
- [Engines](/docs/plugin-policy/engines/)
- [Bootstrap entrypoints](/docs/plugin-policy/bootstrap/)
- [Container plugin caches](/docs/plugin-policy/cache/)
- [Schema and CMP descriptors](/docs/plugin-policy/schema/)
- [Examples](/docs/plugin-policy/examples/)
