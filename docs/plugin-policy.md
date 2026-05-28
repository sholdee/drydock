# Plugin Policy

`drydock` plugin policy is the trusted, drydock-specific contract for Argo CD
config management plugin compatibility. It is not Argo CD repo-server sidecar
discovery, and it does not make discovered CMP commands ambiently trusted.

## Runtime Gate

Plugin sources fail closed by default. The CLI and default Go client do not run
plugin commands unless all of these are true:

- The Application source names a plugin that matches a drydock plugin policy
  entry.
- The matched policy entry uses `engine: exec`.
- The caller passes `--enable-plugins`.
- The exec policy came from trusted policy provenance.

No plugin command execution occurs unless `--enable-plugins` is passed. Native
engines are policy-gated interpretation paths; they do not execute plugin
commands.

For native compatibility, `avp-compat` performs deterministic placeholder
redaction with drydock native renderers, and `native-kustomize` routes
compatible Kustomize build CMP definitions through drydock's native Kustomize
renderer.

## Trusted Provenance

The default local policy path is `.drydock/plugins.yaml`. Use
`--plugin-policy-path` to select a different policy path relative to the
selected policy root. Missing default policy is ignored; an explicitly selected
policy path must exist. `--disable-plugin-policy` disables policy loading and
keeps plugin sources fail-closed.

Default local policy is trusted for native policy only. For single-tree
commands such as `build`, `test`, and `diag`, a policy loaded from the current
working tree may authorize `avp-compat` and `native-kustomize`, but it is not
trusted to execute `engine: exec` entries. Even with `--enable-plugins`, a
matching exec entry from the current tree fails closed unless the caller also
selects trusted policy provenance with `--plugin-policy-ref`.

Diff commands load default policy from the left/baseline side, such as
`--path-orig` or the `--ref-orig` snapshot, and use that one policy for both
sides of the diff. Exec policy from that baseline provenance may run only when
`--enable-plugins` is passed. Policy command definitions and post-renderers are
never sourced from the proposed/current side of a pull request.

`--plugin-policy-ref` means "load the policy from this explicit trusted Git ref
or source." When `--plugin-policy-repo` is set, drydock resolves the ref in
that local Git repository; otherwise it uses the selected repository. The ref
is a trust assertion by the operator or CI job, not an arbitrary escape hatch
for untrusted working-tree policy. Policy paths remain relative to the policy
root snapshot and may not escape it.

## Schema

Policy files are strict single-document YAML. Unknown fields, duplicate mapping
keys, YAML aliases, merge keys, custom tags, invalid scalar types, and multiple
documents are rejected.

Top-level fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `apiVersion` | Yes | None | Must be `drydock.sholdee.dev/v1alpha1`. |
| `kind` | Yes | None | Must be `PluginPolicy`. |
| `plugins` | No | `{}` | Mapping from Argo CD plugin name to policy entry. |

Each `plugins` key is the `spec.source.plugin.name` value that drydock should
match. Names are trimmed and must be non-empty and unique after trimming.

Plugin entry fields:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `engine` | Yes | None | One of `avp-compat`, `native-kustomize`, or `exec`. |

`avp-compat` and `native-kustomize` entries accept only `engine`.

`exec` entries support:

| Field | Required | Default | Notes |
| --- | --- | --- | --- |
| `workdir` | No | `source` | Only `source` is supported. Commands run from a temporary copy of the source path. |
| `init` | No | None | Optional command run before `generate`. |
| `generate` | Yes | None | Command that writes Kubernetes manifests to stdout. |
| `postRenderers` | No | None | Non-empty list when present. Chains stdout through stdin. |
| `env.allow` | No | `[]` | Up to 64 environment variable names copied from the caller environment. |
| `output.maxStdoutBytes` | No | `10485760` | Per-command stdout limit. |
| `output.maxStderrBytes` | No | `65536` | Per-command stderr limit. Stderr is not printed in failure messages. |

Command fields:

| Field | Required | Default |
| --- | --- | --- |
| `command` | Yes | None |
| `timeout` | No | `10s` for `init`, `60s` for `generate`, `30s` for each post-renderer |

`timeout` uses Go duration syntax such as `2s`, `500ms`, or `1m30s`.

## Exec Security Model

Exec policy is argv-only. `command` must be a YAML sequence of strings; shell
strings such as `argocd-vault-plugin generate .` are rejected. Empty argv
tokens are rejected.

The command executable may be either:

- A basename resolved on drydock's controlled PATH:
  `/usr/local/bin:/usr/bin:/bin`.
- An absolute path to an executable outside protected roots.

Relative executable paths such as `./render.sh` are rejected. Shells and common
interpreters are rejected as argv[0], including `sh`, `bash`, `zsh`, `dash`,
`ksh`, `fish`, `env`, `python`, `python3`, `node`, `ruby`, `perl`, `pwsh`, and
`powershell`. Use a trusted executable directly instead of a shell wrapper.

Exec commands run from a temporary copy of the resolved Application source
path. Symlinks inside that source copy are rejected. The original source tree,
selected repository roots, chart cache, Git cache, remote-resource cache,
remote-resource forbidden roots, and the temporary workdir are protected. Exec
binaries must not resolve inside protected roots, and command arguments must
not point back into protected roots except for files inside the temporary
workdir. Credential-bearing URLs in arguments are rejected.

The environment starts with only drydock's controlled `PATH`. `env.allow` names
additional caller environment variables that may be copied in. Names must be
valid environment identifiers, cannot be duplicated, and cannot be reserved
loader/interpreter variables such as `PATH`, `LD_*`, `DYLD_*`, `PYTHONPATH`,
`NODE_OPTIONS`, or similar runtime injection names. Each copied value is capped
at 16 KiB. Application-authored plugin env and parameters are rejected for
policy-backed plugin sources.

Exec runs keep structured execution metadata per phase: phase name, sanitized
executable basename, and elapsed duration. Metadata does not include plugin
stdout, stderr, argv beyond the executable basename, environment values, or
rendered manifests. If a basename executable is missing from drydock's
controlled `PATH`, install it there or configure an absolute trusted executable
path outside protected roots.

## Engines

`avp-compat` renders the source with drydock's native renderer and replaces
supported argocd-vault-plugin placeholders with deterministic redacted values.
It does not contact a secret backend and does not execute the AVP binary.

`native-kustomize` permits a named plugin only when drydock has discovered a
native-compatible Kustomize build CMP definition for that plugin from trusted
Argo CD settings such as Helm values or rendered `argocd-cmp-cm` ConfigMaps.
The configured CMP command is not executed; drydock uses its Go-native
Kustomize renderer.

`exec` runs the policy-defined `init`, `generate`, and optional
`postRenderers` commands under the gates and process controls above. It
supports path-based plugin sources only; chart plugin sources fail closed.

## Selective Native Engines

Additional native engines should be added only when they clearly improve speed,
determinism, security, or setup burden compared with trusted `engine: exec`.
CUE or Jsonnet are the most plausible next candidates if stable Go APIs and
real repository demand line up. ytt and Tanka need separate design review
because their import, environment, and convention surfaces are broader.

Native engines must remain optional, policy-gated compatibility paths. drydock
must not treat discovered Argo CD CMP definitions as ambient permission to
execute or emulate arbitrary plugin behavior.

## Examples

Native AVP placeholder compatibility:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp-directory-include:
    engine: avp-compat
```

Exec policy with a post-renderer:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp-directory-include:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 45s
    postRenderers:
      - command: ["manifest-normalizer", "--mode", "ci"]
        timeout: 15s
    env:
      allow: ["AVP_TYPE", "OP_CONNECT_HOST"]
    output:
      maxStdoutBytes: 10485760
      maxStderrBytes: 65536
```

The checked fixtures in `testdata/plugin-policy/` are parsed and fingerprinted
by unit tests so these examples do not silently drift.

For a single-tree command, run exec plugins from an explicit trusted ref:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

For a pull request diff, the baseline policy is the trusted source:

```bash
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```
