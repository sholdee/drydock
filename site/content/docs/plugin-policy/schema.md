---
title: Schema
---

## Schema

Policy files are strict single-document YAML. Unknown fields, duplicate mapping
keys, YAML aliases, merge keys, custom tags, invalid scalar types, and multiple
documents are rejected.

An editor JSON Schema is available at
[`schemas/plugin-policy.schema.json`](/schemas/plugin-policy.schema.json). Use
a YAML language-server comment rather than a top-level `$schema` field, because
drydock rejects unknown policy fields:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/sholdee/drydock/main/schemas/plugin-policy.schema.json
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
```

The schema is an authoring aid. The Go parser remains the authoritative
security boundary and may enforce checks that JSON Schema cannot fully express.

Top-level fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `apiVersion` | Yes | None | Must be `drydock.sholdee.dev/v1alpha1`. |
| `kind` | Yes | None | Must be `PluginPolicy`. |
| `plugins` | No | `{}` | Mapping from Argo CD plugin name to policy entry. |
| `bootstrap` | No | None | Plugin-rendered discovery entrypoints for Argo CD bootstrap objects. |

`bootstrap` fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `entrypoints` | Yes | None | Non-empty list of bootstrap entrypoints. |

Bootstrap entrypoint fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `name` | Yes | None | DNS-label-like identifier, maximum 63 characters. |
| `plugin` | Yes | None | Plugin policy key to render. |
| `sourcePath` | Yes | None | Repository-relative plugin source path. `.` is allowed. |
| `parameters` | No | `[]` | Argo-style plugin parameters supplied by trusted policy. |

Each `plugins` key is the `spec.source.plugin.name` value that drydock should
match. Names are trimmed and must be non-empty and unique after trimming.

Plugin entry fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `engine` | Yes | None | One of `avp-compat`, `native-kustomize`, `exec`, or `container`. |
| `match.discover` | No | None | Trusted static discovery rule for matching unnamed Application plugin sources. |
| `configManagementPlugin` | No | None | Optional trusted CMP compatibility seed. |

`avp-compat` and `native-kustomize` entries accept `engine`, optional `match`,
and optional `configManagementPlugin`.

`exec` entries support:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `workdir` | No | `source` | Only `source` is supported. Commands run from a temporary copy of the source path. |
| `copy.scope` | No | `source` | Use `repository` only when a trusted plugin needs allowlisted sibling repository paths. |
| `copy.include` | No | `[]` | Repository-relative glob allowlist. Required when `copy.scope: repository`. |
| `init` | No | None | Optional command run before `generate`. |
| `generate` | Yes | None | Command that writes Kubernetes manifests to stdout. |
| `postRenderers` | No | None | Non-empty list when present. Chains stdout through stdin. |
| `env.allow` | No | `[]` | Up to 64 environment variable names copied from the caller environment. |
| `parameters.allow` | No | `[]` | Application plugin parameter allowlist for `engine: exec` and `engine: container`. |
| `output.maxStdoutBytes` | No | `10485760` | Per-command stdout limit. |
| `output.maxStderrBytes` | No | `65536` | Per-command stderr limit. Stderr is not printed in failure messages. |

`container` entries support the same lifecycle fields as `exec`, plus:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `runtime` | No | `docker` | Only Docker is supported. |
| `image` | Yes | None | Fully qualified image reference. Digest required unless `allowMutableImageTag: true`. |
| `allowMutableImageTag` | No | `false` | Allows tag-only image references for local/trusted workflows. |
| `network` | No | `none` | `none` or `default`. `default` is rejected when `--offline` is set. |
| `cacheMounts` | No | `[]` | Policy-managed durable cache mounts under reserved container paths below `/drydock-cache`. |

Command fields:

| Field | Required | Default |
| --- | --- | --- |
| `command` | Yes | None |
| `timeout` | No | `10s` for `init`, `60s` for `generate`, `30s` for each post-renderer |

`timeout` uses Go duration syntax such as `2s`, `500ms`, or `1m30s`.

## Trusted CMP Compatibility Descriptors

Policy entries may include a small `configManagementPlugin` seed copied from a
trusted Argo CD `ConfigManagementPlugin` descriptor. This is compatibility
metadata, not a full Argo CD object. The `plugins` map key is the Argo CD plugin
name. drydock does not support, parse, or infer `metadata.name` inside the seed.

The supported seed subset is intentionally narrow:

| Seed field | Notes |
| --- | --- |
| `discover.fileName` | Static source-relative file glob. |
| `discover.find.glob` | Static source-relative find glob. |
| `generate.command` | Optional argv metadata. |
| `generate.args` | Optional argv metadata. |

`configManagementPlugin.generate` is optional compatibility metadata and is
never executed by seed handling. For `engine: exec` and `engine: container`,
only the top-level trusted policy `generate.command` authorizes execution. A
seed `generate` block cannot make a command runnable and cannot replace the
required lifecycle `generate`.

Unnamed Application plugin sources may match trusted static discovery from
policy. drydock checks `match.discover` first, then
`configManagementPlugin.discover`. If both forms are present, they must describe
the same normalized static rule. Only `discover.fileName` and
`discover.find.glob` are supported; `discover.find.command` is not supported and
is never executed.

For `engine: native-kustomize`, drydock may use seed `generate.command` plus
`generate.args` to choose native Kustomize build options. The configured CMP
command still does not run. For command-backed engines, seed `generate` remains
metadata only; execution always comes from the top-level trusted lifecycle
policy.
