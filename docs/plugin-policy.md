---
title: Plugin Policy
---

`drydock` plugin policy is the trusted, drydock-specific contract for Argo CD
config management plugin (CMP) compatibility beyond drydock's built-in native
adapters. It is not Argo CD repo-server sidecar discovery, and it does not make
arbitrary discovered CMP commands trusted for execution.

Operators usually do not need a policy for Kustomize wrapper plugins. When
drydock discovers a CMP command that safely normalizes to `kustomize build`, it
uses the native Kustomize renderer automatically.

Use plugin policy for these cases:

- Deterministic argocd-vault-plugin (AVP) placeholder redaction with
  `engine: avp-compat`. For explicit Application plugin sources named
  `argocd-vault-plugin`, the CLI can use the same native compatibility behavior
  with `--enable-avp-compat` and no policy.
- Explicit native Kustomize overrides with `engine: native-kustomize`.
- Trusted host-process compatibility with `engine: exec` and
  `--enable-plugins`.
- Trusted Docker-backed compatibility with `engine: container` and
  `--enable-plugins`.
- Plugin-rendered bootstrap discovery with `bootstrap.entrypoints`.

## Command Execution Gate

The CLI and default Go client do not run plugin commands unless all of these
are true:

- The Application source names a plugin that matches a drydock plugin policy
  entry, or an unnamed Application plugin source matches trusted static
  discovery from policy.
- The matched policy entry uses `engine: exec` or `engine: container`.
- The caller passes `--enable-plugins`.
- The command-backed policy came from trusted policy provenance.

No plugin command execution occurs unless `--enable-plugins` is passed. Native
rendering paths, including `engine: avp-compat`, `engine: native-kustomize`,
and `--enable-avp-compat`, do not execute plugin commands.

Discovered Argo CD CMP definitions that normalize to a safe `kustomize build`
command are interpreted by drydock's native Kustomize renderer by default.
`native-kustomize` policy entries remain available as explicit overrides.
For native argocd-vault-plugin (AVP) compatibility, `avp-compat` performs
deterministic placeholder redaction with drydock native renderers. The
`--enable-avp-compat` flag enables the same behavior only for explicit
Application plugin sources named `argocd-vault-plugin`.

When a discovered sidecar CMP has static discovery rules and an Application
does not name a plugin, drydock may warn that Argo CD sidecar auto-discovery
would be required. This check is intentionally bounded: drydock only evaluates
`discover.fileName` and `discover.find.glob` against the local Application
source directory. It never executes or emulates `discover.find.command`.

## Bootstrap Entrypoints

Some repositories keep Argo CD bootstrap objects behind a config management
plugin rather than committing `Application` or `ApplicationSet` YAML directly.
Policy bootstrap entrypoints let drydock render those trusted plugin sources
during fleet discovery so their generated Argo CD objects become normal
discovered inputs.

Bootstrap discovery runs after committed and explicit Kustomize discovery and
the first ApplicationSet expansion, then drydock expands any ApplicationSets
produced by bootstrap output before recursive rendered fleet discovery. It runs
only in fleet discovery mode. `--discovery-mode static` disables it;
`--max-discovery-depth 0` does not. Static committed objects take precedence
over bootstrap-rendered duplicates, and bootstrap-rendered objects take
precedence over recursive rendered fleet duplicates.

Each bootstrap entrypoint creates an internal, hidden synthetic Application
with namespace `argocd`, project `default`, destination name `in-cluster`, and
destination namespace `argocd`. The synthetic Application is used only to
render and scan the entrypoint output; it is not returned as a discovered
Application.

Bootstrap entrypoints fail closed. The `sourcePath` must be repository-local,
exist, be a directory, avoid symlink components, and match the referenced
plugin's trusted static `match.discover` or `configManagementPlugin.discover`
rule. Bootstrap render failures are discovery errors rather than skipped
recursive app-of-apps candidates.

## Trusted Provenance

The default local policy path is `.drydock/plugins.yaml`. Use
`--plugin-policy-path` to select a different policy path relative to the
selected policy root. Missing default policy is ignored; an explicitly selected
policy path must exist. `--disable-plugin-policy` disables drydock policy
loading only; it does not disable built-in native Kustomize interpretation for
safe discovered CMP definitions.

Default local policy is trusted for native policy only. For single-tree
commands such as `build`, `test`, and `diag`, a policy loaded from the current
working tree may authorize `avp-compat` and `native-kustomize`. It is not
trusted to execute `engine: exec` or `engine: container` entries. Even with
`--enable-plugins`, a matching command-backed entry from the current tree fails
closed unless the caller also selects trusted policy provenance with
`--plugin-policy-ref`.

Diff commands load default policy from the left/baseline side, such as
`--path-orig` or the `--ref-orig` snapshot, and use that one policy for both
sides of the diff. Command-backed policy from that baseline provenance may run
only when `--enable-plugins` is passed. Policy command definitions,
post-renderers, container image references, and bootstrap definitions are never
sourced from the proposed/current side of a pull request.

`--plugin-policy-ref` means "load the policy from this explicit trusted Git ref
or source." When `--plugin-policy-repo` is set, drydock resolves the ref in
that local Git repository; otherwise it uses the selected repository. The ref
is a trust assertion by the operator or CI job, not an arbitrary escape hatch
for untrusted working-tree policy. Policy paths remain relative to the policy
root snapshot and may not escape it.

## Local Validation

For a committed command-backed policy, validate with the trusted policy ref that
CI will use:

```sh
drydock get apps \
  --path . \
  --enable-plugins \
  --plugin-policy-ref main
```

When generated Applications point at the canonical Git remote URL, add a
repository map so recursive rendering uses the local checkout:

```sh
drydock test apps \
  --path . \
  --enable-plugins \
  --plugin-policy-ref main \
  --repo-map https://github.com/example/cluster="$PWD"
```

To validate an uncommitted policy without making the working tree policy
trusted directly, copy the policy into a small temporary Git repository and
trust that repository's `HEAD`:

```sh
mkdir -p /tmp/drydock-policy-trust/.drydock
cp .drydock/plugins.yaml /tmp/drydock-policy-trust/.drydock/plugins.yaml
git -C /tmp/drydock-policy-trust init
git -C /tmp/drydock-policy-trust add .drydock/plugins.yaml
git -C /tmp/drydock-policy-trust commit -m "Trust local drydock plugin policy"

drydock test apps \
  --path . \
  --enable-plugins \
  --plugin-policy-ref HEAD \
  --plugin-policy-repo /tmp/drydock-policy-trust \
  --repo-map https://github.com/example/cluster="$PWD"
```

For diff validation, use the same trusted policy source and render all apps
when there may be no local Git changes:

```sh
drydock diff apps \
  --path-orig . \
  --path . \
  --enable-plugins \
  --plugin-policy-ref HEAD \
  --plugin-policy-repo /tmp/drydock-policy-trust \
  --repo-map https://github.com/example/cluster="$PWD" \
  --changed-only=false \
  --exit-code=false
```

Common validation failures:

- `requires --enable-plugins`: pass `--enable-plugins`; drydock never runs
  command-backed policies by default.
- `untrusted policy source`: use `--plugin-policy-ref`, or run a diff where the
  policy comes from the trusted baseline side.
- `no trusted plugin policy match.discover`: ensure the Application plugin name
  matches a policy key, or add trusted static `match.discover` or
  `configManagementPlugin.discover` metadata.
- `Application plugin parameter ... is not allowed`: add a narrow
  `parameters.allow` entry with the expected type and path allowlist.
- `container command failed` during `init`: check copied sibling paths,
  package/cache requirements, and whether the policy needs `network: default`.
- `network: default` with `--offline`: container network access is rejected in
  offline mode; use local caches or run without `--offline`.

## Schema

Policy files are strict single-document YAML. Unknown fields, duplicate mapping
keys, YAML aliases, merge keys, custom tags, invalid scalar types, and multiple
documents are rejected.

An editor JSON Schema is available at
[`schemas/plugin-policy.schema.json`](../schemas/plugin-policy.schema.json).
Use a YAML language-server comment rather than a top-level `$schema` field,
because drydock rejects unknown policy fields:

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

`avp-compat` and `native-kustomize` entries accept `engine`, optional
`match`, and optional `configManagementPlugin`.

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

## Command Security Model

Exec and container policy lifecycle commands are argv-only. `command` must be a
YAML sequence of strings; shell strings such as `ytt -f .` are rejected. Empty
argv tokens are rejected.

For `engine: exec`, the command executable may be either:

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

For `engine: container`, drydock prepares the same temporary source or
repository-scoped workspace, makes it writable for container users, and mounts
it into the container at `/work`. Container lifecycle commands run inside the
configured image with Docker `run --rm --interactive`, the configured network,
and an env file generated from policy-allowed environment values. The Docker
client itself runs with a minimal client environment. In offline mode, drydock
adds `--pull never`, sets Docker client `HOME` and `DOCKER_CONFIG` to an empty
temporary directory, rejects `network: default`, and rejects caller-provided
remote Docker client configuration such as non-local `DOCKER_HOST`,
`DOCKER_CONTEXT`, `DOCKER_CONFIG`, `DOCKER_TLS_VERIFY`, or
`DOCKER_CERT_PATH`. Timed-out or canceled container commands are removed with
`docker rm -f` when a container ID is available.

Container `cacheMounts` let trusted plugins keep durable tool caches without
granting policy authors a host path escape hatch. Each entry names a cache and
chooses only a container target path under reserved `/drydock-cache`, not
`/drydock-cache` itself. drydock selects the host cache directory under its user
cache root and scopes it by policy fingerprint, plugin name, and cache name.
Targets cannot overlap `/work`, contain traversal, commas, control characters,
backslashes, duplicate paths, or ancestor/descendant overlaps.

`--plugin-cache-dir PATH` overrides the host root for these policy-managed
container plugin cache mounts at render time. It does not change the trusted
policy target paths, and it does not make plugin caches part of `drydock cache`
lifecycle commands; those commands still manage only Git, chart, and
remote-resource cache entry roots for now. The GitHub PR action sets the plugin
cache directory under the action cache root, so trusted plugin cache mounts can
be restored and saved with the render cache when trusted plugins are enabled.

For `engine: exec`, the command environment starts with only drydock's
controlled `PATH`. `env.allow` names additional caller environment variables
that may be copied in.

For `engine: container`, the Docker client process uses a minimal controlled
environment. The process inside the configured image keeps the image-defined
environment and receives policy-allowed environment values, Argo plugin
parameter environment values, and drydock extras through `--env-file`.

For both command-backed engines, allowed environment names must be valid
environment identifiers, cannot be duplicated, and cannot be reserved
loader/interpreter variables such as `PATH`, `LD_*`, `DYLD_*`, `PYTHONPATH`,
`NODE_OPTIONS`, or similar runtime injection names. Each copied value is capped
at 16 KiB. Application-authored plugin env is rejected for policy-backed plugin
sources.

### Application Parameters

Application plugin parameters are accepted only for `engine: exec` and
`engine: container`, and only when allowlisted by trusted policy. Native engines
reject Application plugin env and parameters.

`parameters.allow[]` entries:

| Field | Required | Notes |
| --- | --- | --- |
| `name` | Yes | Application plugin parameter name. |
| `type` | Yes | `string`, `array`, or `map`. |
| `required` | No | When `true`, the Application must provide the parameter. |
| `path.base` | No | For string path parameters only. Defaults to `source`; may be `repository`. |
| `path.allow` | No | Slash-normalized relative glob allowlist for path values. |

String parameters may be substituted into argv tokens with `{{param:name}}`.
Parameter templates are not allowed in the executable position. Array and map
parameters are exposed only through Argo-style `PARAM_*` and
`ARGOCD_APP_PARAMETERS` environment values.

Path parameters may be constrained to paths under the Application source or the
repository. Repository-scoped path parameters require `copy.scope: repository`.
Values under the Application source are copied by default; trusted sibling
repository paths must also match `copy.include`.

Command-backed runs keep structured execution metadata per phase: phase name,
engine, sanitized command basename, elapsed duration, and for container policy
the runtime and image. Metadata does not include plugin stdout, stderr, argv
beyond the command basename, environment values, or rendered manifests. If an
exec basename executable is missing from drydock's controlled `PATH`, install
it there or configure an absolute trusted executable path outside protected
roots.

## Engines

`avp-compat` renders the source with drydock's native renderer and replaces
supported AVP placeholders with deterministic redacted values. For explicit
Application plugin sources named `argocd-vault-plugin`, `--enable-avp-compat`
uses the same native compatibility path without requiring a policy entry.

Native renderer selection follows drydock's normal source detection: Kustomize
when a `kustomization` file exists, Helm when `Chart.yaml` exists, and
Directory otherwise. Chart-only plugin sources use native chart rendering.

AVP compatibility does not contact a secret backend and does not execute the
AVP binary, the config-management plugin command, a shell, the Helm CLI, or the
Kustomize CLI. Generic `<KEY>` placeholders are redacted only when the rendered
manifest has `metadata.annotations["avp.kubernetes.io/path"]`; inline
`<path:...#...>` placeholders remain supported.

`native-kustomize` explicitly permits a named plugin to use drydock's native
Kustomize adapter. The same adapter also runs by default when drydock discovers
a compatible Kustomize build CMP definition for that plugin from Argo CD
settings such as Helm values or rendered `argocd-cmp-cm` ConfigMaps. The
configured CMP command is not executed; drydock validates the command shape and
uses its Go-native Kustomize renderer.

`exec` runs the policy-defined `init`, `generate`, and optional
`postRenderers` commands under the gates and process controls above. It
supports path-based plugin sources only; chart plugin sources fail closed.

`container` runs the same lifecycle inside a trusted image through the Docker
runtime. It supports path-based plugin sources only; chart plugin sources fail
closed.

## Selective Native Engines

Additional native engines should be added only when they clearly improve speed,
determinism, security, or setup burden compared with trusted command-backed
engines. CUE or Jsonnet are the most plausible next candidates if stable Go
APIs and real repository demand line up. ytt and Tanka need separate design
review because their import, environment, and convention surfaces are broader.

Native engines must remain narrow compatibility paths. drydock may interpret
discovered CMP definitions only when they map to a known in-process renderer
with a fail-closed validator. Discovered CMP definitions are never ambient
permission to execute commands or emulate arbitrary plugin behavior.
`--enable-avp-compat` has the same boundary: it is not support for arbitrary
sidecar CMPs or arbitrary plugin commands. Use `engine: exec` or
`engine: container` for trusted command-backed plugins.

## Examples

Native argocd-vault-plugin (AVP) placeholder compatibility:

```yaml
apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  argocd-vault-plugin:
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
        command: ["kustomize"]
        args: ["build", "--enable-helm", "."]
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

For a single-tree command, run command-backed plugins from an explicit trusted
ref:

```bash
drydock test apps --path . --plugin-policy-ref main --enable-plugins
```

For a pull request diff, the baseline policy is the trusted source:

```bash
drydock diff apps --path-orig ../baseline --path . --enable-plugins
```
