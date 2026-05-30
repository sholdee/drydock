---
title: Plugin Policy
---

drydock plugin policy is the trusted, drydock-specific gate for config
management plugin compatibility. It is not Argo CD sidecar auto-discovery, and
it does not make arbitrary discovered commands safe to execute.

Operators usually do not need policy for Kustomize wrapper plugins. If drydock
discovers a config management plugin command that safely normalizes to
`kustomize build`, it uses the native Kustomize renderer without shelling out.

## Use Policy For

- Deterministic argocd-vault-plugin placeholder redaction with
  `engine: avp-compat`.
- Explicit native Kustomize overrides with `engine: native-kustomize`.
- Trusted shellout compatibility with `engine: exec` and `--enable-plugins`.

## Exec Gate

The CLI and default Go client run plugin commands only when all of these are
true:

- The Application source names a plugin that matches a drydock policy entry.
- The matched entry uses `engine: exec`.
- The caller passes `--enable-plugins`.
- The exec policy comes from trusted policy provenance.

For a single-tree command, use an explicit trusted ref:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

For pull request diffs, drydock loads policy from the trusted baseline side:

```bash
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```

For schema, provenance rules, native engines, and exec security controls, see
the canonical [plugin policy guide](/docs/plugin-policy/).
