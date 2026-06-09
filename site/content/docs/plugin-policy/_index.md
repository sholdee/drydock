---
title: Plugin Policy Reference
---

`drydock` plugin policy is the trusted, drydock-specific contract for Argo CD
config management plugin compatibility beyond drydock's built-in native
adapters. It is not Argo CD repo-server sidecar discovery, and it does not make
arbitrary discovered CMP commands trusted for execution.

Operators usually do not need a policy for Kustomize wrapper plugins. When
drydock discovers a CMP command that safely normalizes to `kustomize build`, it
uses the native Kustomize renderer automatically.

Use plugin policy for these cases:

- Deterministic argocd-vault-plugin placeholder redaction with
  `engine: avp-compat`.
- Explicit native Kustomize overrides with `engine: native-kustomize`.
- Trusted host-process compatibility with `engine: exec` and
  `--enable-plugins`.
- Trusted Docker-backed compatibility with `engine: container` and
  `--enable-plugins`.
- Plugin-rendered bootstrap discovery with `bootstrap.entrypoints`.

## Command Execution Gate

Exec and container plugins run only when the Application source matches trusted
policy, the matched entry uses `engine: exec` or `engine: container`, the caller
passes `--enable-plugins`, and the command-backed policy came from trusted
provenance. See [trust and command execution](/docs/plugin-policy/trust/#command-execution-gate).

## Bootstrap Entrypoints

`bootstrap.entrypoints` render trusted plugin sources during fleet discovery so
generated Argo CD objects become normal discovered inputs. See
[bootstrap entrypoints](/docs/plugin-policy/bootstrap/#bootstrap-entrypoints).

## Trusted Provenance

Native policy can come from the working tree. Command-backed policy must come
from a trusted baseline or an explicit `--plugin-policy-ref`. See
[trusted provenance](/docs/plugin-policy/trust/#trusted-provenance).

## Local Validation

Validate command-backed policy with the same trusted ref or policy repository
that CI will use. See [local validation](/docs/plugin-policy/trust/#local-validation).

## Schema

Plugin policies are strict single-document YAML files. See the
[schema reference](/docs/plugin-policy/schema/#schema).

## Trusted CMP Compatibility Descriptors

Policy entries may include a narrow trusted `configManagementPlugin` seed for
static discovery and native compatibility metadata. See
[trusted CMP compatibility descriptors](/docs/plugin-policy/schema/#trusted-cmp-compatibility-descriptors).

## Command Security Model

Command-backed engines use argv-only lifecycle commands, temporary workspaces,
controlled environments, path validation, and explicit parameter allowlists.
See [command security](/docs/plugin-policy/trust/#command-security-model).

## Engines

Plugin policy supports `avp-compat`, `native-kustomize`, `exec`, and
`container`. See [engines](/docs/plugin-policy/engines/#engines).

## Selective Native Engines

Native engines should stay narrow, deterministic compatibility paths. See
[selective native engines](/docs/plugin-policy/engines/#selective-native-engines).

## Examples

See [plugin policy examples](/docs/plugin-policy/examples/#examples) for AVP,
native Kustomize, trusted container, bootstrap, exec, and post-renderer policy
snippets.
