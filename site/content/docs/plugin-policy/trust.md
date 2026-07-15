---
title: Trust and Command Execution
---

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
explicit `argocd-vault-plugin` sources, `--enable-avp-compat`, and
`--enable-ksops-compat`, do not execute plugin commands.

Discovered Argo CD CMP definitions that normalize to a safe `kustomize build`
command are interpreted by drydock's native Kustomize renderer by default.
`native-kustomize` policy entries remain available as explicit overrides.
For native argocd-vault-plugin (AVP) compatibility, `avp-compat` performs
deterministic placeholder redaction with drydock native renderers. Explicit
Application plugin sources named `argocd-vault-plugin` use the same native
compatibility path by default. Discovered CMP aliases use it when their
generate command safely normalizes to `argocd-vault-plugin generate .`. The
`--enable-avp-compat` flag forces the same redaction pass for ordinary
native-rendered sources. The `--enable-ksops-compat` flag enables the same
opt-in, fail-closed native path for KSOPS kustomize generators.

When a discovered sidecar CMP has static discovery rules and an Application
does not name a plugin, drydock may warn that Argo CD sidecar auto-discovery
would be required. drydock evaluates only `discover.fileName` and
`discover.find.glob` against the local Application source directory. It never
executes or emulates `discover.find.command`.

## Onboarding Commands

`drydock plugin-policy init` and `drydock plugin-policy doctor` are static
onboarding commands. They inspect Applications, CMP settings, and readable
sidecar image candidates, but they do not render Applications or execute
plugin commands. Passing `--plugin-policy-ref` and `--enable-plugins` to
`doctor` checks whether those gates are satisfied for future command-backed
rendering; it does not run the command-backed policy.

For normal render commands, a missing default `.drydock/plugins.yaml` is
ignored. For `plugin-policy doctor`, the same missing default policy is a
readiness `FAIL` with code `policy.missing`; an explicitly selected missing
`--plugin-policy-path` is a command error. Use `-o json` for deterministic
`status` and issue `code` fields, and `--strict` when readiness `FAIL` should
exit nonzero after the report is rendered.

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

Diff commands load default policy from the left or baseline side, such as
`--path-orig` or the `--ref-orig` snapshot, and use that one policy for both
sides of the diff. Command-backed policy from that baseline provenance may run
only when `--enable-plugins` is passed. Policy command definitions,
post-renderers, container image references, and bootstrap definitions are never
sourced from the proposed side of a pull request.

`--plugin-policy-ref` means "load the policy from this explicit trusted Git ref
or source." When `--plugin-policy-repo` is set, drydock resolves the ref in
that local Git repository; otherwise it uses the selected repository. The ref
is a trust assertion by the operator or CI job. Policy paths remain relative to
the policy root snapshot and may not escape it.

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
- `container command failed` during the policy lifecycle `init` phase: check
  copied sibling paths, package/cache requirements, and whether the policy
  needs `network: default`.
- `network: default` with `--offline`: container network access is rejected in
  offline mode; use local caches or run without `--offline`.

## Command Security Model

Exec and container policy lifecycle commands are argv-only. `command` must be a
YAML sequence of strings; shell strings such as `ytt -f .` are rejected. Empty
argv tokens are rejected.

For `engine: exec`, the command executable may be either a basename resolved on
drydock's controlled `PATH` (`/usr/local/bin:/usr/bin:/bin`) or an absolute path
to an executable outside protected roots. Relative executable paths such as
`./render.sh` are rejected. Shells and common interpreters are rejected as
`argv[0]`, including `sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`, `env`,
`python`, `python3`, `node`, `ruby`, `perl`, `pwsh`, and `powershell`.

Exec commands run from a temporary copy of the resolved Application source
path. Symlinks inside that source copy are rejected. Exec binaries must not
resolve inside protected roots, and command arguments must not point back into
protected roots except for files inside the temporary workdir. Credential-bearing
URLs in arguments are rejected.

For `engine: container`, drydock prepares the same temporary source or
repository-scoped workspace, makes it writable for container users, and mounts
it at `/work`. Container lifecycle commands run inside the configured image
with Docker `run --rm --interactive`, the configured network, and an env file
generated from policy-allowed values.

In offline mode, drydock adds `--pull never`, sets Docker client `HOME` and
`DOCKER_CONFIG` to an empty temporary directory, rejects `network: default`,
and rejects caller-provided remote Docker client configuration such as
non-local `DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`,
`DOCKER_TLS_VERIFY`, or `DOCKER_CERT_PATH`.

For `engine: exec`, the command environment starts with only drydock's
controlled `PATH`. For `engine: container`, the Docker client process uses a
minimal controlled environment, while the process inside the image keeps the
image-defined environment and receives policy-allowed environment values,
Argo-style plugin parameter values, and drydock extras through `--env-file`.

For both command-backed engines, allowed environment names must be valid
environment identifiers, cannot be duplicated, and cannot be reserved
loader/interpreter variables such as `PATH`, `LD_*`, `DYLD_*`, `PYTHONPATH`, or
`NODE_OPTIONS`. Each copied value is capped at 16 KiB. Application-authored
plugin env is rejected for policy-backed plugin sources.

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
beyond the command basename, environment values, or rendered manifests.
