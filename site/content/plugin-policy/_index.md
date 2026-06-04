---
title: Plugin Policy
---

drydock plugin policy is the trusted, drydock-specific gate for config
management plugin compatibility beyond drydock's built-in native adapters. It
is not Argo CD repo-server sidecar auto-discovery, and it does not make
arbitrary discovered commands safe to execute.

Operators usually do not need policy for Kustomize wrapper plugins. If drydock
discovers a config management plugin command that safely normalizes to
`kustomize build`, it uses the native Kustomize renderer without shelling out.

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

## Bootstrap Entrypoints

`bootstrap.entrypoints` are fleet discovery inputs for repositories whose
plugin-rendered output contains Argo CD `Application` or `ApplicationSet`
objects. Static discovery mode disables them; `--max-discovery-depth 0` does
not. Each entrypoint must match trusted static discovery for its plugin.

For schema, provenance rules, native engines, command execution controls, and
bootstrap details, see the canonical
[plugin policy guide](/docs/plugin-policy/).
