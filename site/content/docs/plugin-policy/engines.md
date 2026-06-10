---
title: Engines
---

## Engines

`avp-compat` renders the source with drydock's native renderer and replaces
supported argocd-vault-plugin placeholders with deterministic redacted values.
Explicit Application plugin sources named `argocd-vault-plugin` use the same
native compatibility path by default and do not require a policy entry.
Discovered CMP aliases also use this path by default when their generate
command safely normalizes to `argocd-vault-plugin generate .`.

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
`postRenderers` commands under the plugin execution gates and process controls.
It supports path-based plugin sources only; chart plugin sources fail closed.

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
`--enable-avp-compat` forces the same redaction pass for ordinary native
rendered sources, but it is not support for arbitrary sidecar CMPs or
arbitrary plugin commands. Use `engine: exec` or
`engine: container` for trusted command-backed plugins.
