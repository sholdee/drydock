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
Kustomize CLI. Every placeholder AVP would resolve becomes a deterministic
`drydock-redacted-<12hex>` marker derived from the placeholder's path and key.
drydock substitutes exactly where argocd-vault-plugin (v1.18.1) substitutes,
using AVP's own matching rules:

- Without `metadata.annotations["avp.kubernetes.io/path"]` only inline
  `<path:PATH#KEY>` and `<path:PATH#KEY#VERSION>` tokens are placeholders
  (AVP's `(?mU)<path:([^#]+)#([^#]+)(?:#([^#]+))?>`). Any other `<...>` text
  is left alone.
- When the annotation key is present, every `<...>` span on a line is a
  placeholder (AVP's `(?mU)<(.*)>`): the match runs from the first `<` on the
  line to the next `>`, never crosses a newline, and is replaced whole. The
  body is the key, whatever characters it contains; `|modifiers` are split
  off; an inline `path:PATH#KEY` inside the body resolves to its own path.
  With an empty annotation value AVP has no backend data, so only inline
  tokens are substituted and other spans stay in place (AVP fails the render
  there; drydock does not).
- The walk starts at the manifest root, so metadata, labels, annotation
  values, `stringData`, `data` and spec fields are all visited. Lists are
  walked one level deep: string elements and mapping elements are processed,
  a list nested directly in a list is skipped together with everything in it.
- For `kind: Secret` every string that decodes as standard, padded base64 to
  text containing a `<...>` span is substituted in the decoded text and
  re-encoded, so markers in `data:` come out base64-encoded as they do from
  AVP. ConfigMap `data:`/`binaryData:` and every other kind are never decoded.
- Annotations are read the way AVP's pinned Kubernetes client (apimachinery
  v0.29.1) reads them: a YAML-null value under any annotation key (such as
  `avp.kubernetes.io/path:` with nothing after the colon) makes AVP see no
  annotations on that object at all, so the inline-only rule applies.
- `avp.kubernetes.io/ignore: "true"` (any value `strconv.ParseBool` accepts)
  leaves the whole object untouched, as AVP's `generate` does.
- A `kind: List` is one object to AVP: its kind and annotations govern every
  item, so a Secret item is not base64-decoded and an item's own
  `avp.kubernetes.io/path` annotation has no effect. drydock reads the same
  context from the List it flattened the items out of.

Helm `values`/`valuesObject` are additionally substituted before templating;
AVP only sees rendered manifests, so this pass is a drydock extension.

`ksops-compat` renders the source with drydock's native Kustomize renderer and
replaces `apiVersion: viaduct.ai/v1 / kind: ksops` kustomize generator entries
with deterministic placeholder manifests. Encrypted values become
`drydock-ksops-redacted-<12hex>` markers (base64-encoded in Secret
`data:`/`binaryData:` and ConfigMap `binaryData:` fields; plain elsewhere).
No decryption key, KMS network call, or exec is involved. Enable the mode
globally with `--enable-ksops-compat` (CLI) or `enable-ksops-compat: true`
(GitHub Action). Builtin generator configs render normally without this flag.
Exec transformers and validators remain unsupported and fail closed.
Note that `ksops-compat` is a native compatibility mode enabled only by the
`--enable-ksops-compat` flag (or the action input); it is not a valid policy
`engine:` value.

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
rendered sources, and `--enable-ksops-compat` enables the KSOPS generator
placeholder path. Neither flag is support for arbitrary sidecar CMPs or
arbitrary plugin commands. Use `engine: exec` or
`engine: container` for trusted command-backed plugins.
