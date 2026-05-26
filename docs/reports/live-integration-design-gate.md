# Live Runtime Boundary

Date: 2026-05-24

## Decision

`drydock` intentionally does not integrate with live Kubernetes or Argo CD
runtime in its core execution path. The default product is a native Go
renderer/differ for Argo CD GitOps repositories that works from checked-out
files and explicit caches. It must not require a Kubernetes cluster, Argo CD
server, `kubectl`, `argocd`, Helm/Kustomize command-line tools, or any
external render service to produce PR diffs.

This offline-runtime contract is separate from network source acquisition.
Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources may be fetched
into explicit caches unless `--offline` is set. Future live behavior may be
researched only behind an explicit design update, and that work must not weaken
`--offline` behavior, introduce ambient credential reads, or make live access
part of normal verification.

## Runtime Boundary

Future live-runtime decisions remain owned by this boundary document. No live
adapter module, fake live client, defaulting simulator, admission simulator,
server-side diff path, Kubernetes client, or Argo CD client is part of the
default product. Those remain exceptions to the core runtime contract and must
satisfy the acceptance gates below before implementation.

## Why This Boundary Exists

Live runtime access changes the threat model and correctness model:

- A Kubernetes API request can expose cluster identity, resources, and
  credentials.
- Argo CD API calls can expose repository, project, cluster, and account
  metadata.
- Kubernetes defaulting and admission mutation are versioned and
  cluster-specific.
- Server-side apply ownership depends on live managed fields and field-manager
  history.
- Argo CD server-side diff behavior is coupled to repo-server, cluster cache,
  Kubernetes discovery, and Argo CD settings.

The offline-runtime engine should report these as outside its boundary rather
than silently approximating them.

## Exception Guardrails

Future live-runtime exceptions must pass these checks before code changes:

- Update this design gate or an equivalent accepted design document first.
- Keep default commands and package-level APIs offline from live Kubernetes and
  Argo CD runtime.
- Put every live credential, endpoint, and mode behind explicit opt-in
  configuration.
- Keep external command execution out of default render/diff paths; any
  compatibility shellout must be separately approved and opt-in.
- Test default workflows without a cluster, Argo CD server, `kubectl`,
  `argocd`, Helm CLI, or Kustomize CLI. Use `--offline` when tests must prove
  cache-only behavior without network access.
- Document any remaining divergence instead of approximating Kubernetes
  defaulting, admission mutation, Argo CD server-side diff, or live managed
  fields silently.

## Default Mode Contract

Default commands and package-level public APIs are offline-runtime
desired-vs-desired workflows:

- They read the current tree, the baseline tree, and explicit local caches.
- They may fetch declared Git, HTTP Helm, OCI Helm, and remote Kustomize
  sources into explicit caches unless `--offline` is set.
- They do not read kubeconfig, Argo CD CLI config, ambient Git credential
  helpers, ambient Helm registry config, or Docker credential config.
- They do not contact Kubernetes or an Argo CD server.
- They do not shell out to `kubectl`, `argocd`, `helm`, or `kustomize`.
- They do not mutate clusters, Argo CD instances, or GitOps repository trees.

## Live Boundary Classification

| Feature | Status | Required Gate Before Implementation |
| --- | --- | --- |
| Live-cluster diffing | Outside core runtime | Explicit opt-in command/API, Kubernetes auth model, fake-client tests, read-only request list, redaction review, and no default-mode calls. |
| Argo CD API/server integration | Outside core runtime | Explicit server configuration, non-ambient credentials, fake server tests, rate/error model, redaction review, and no default-mode calls. |
| Argo CD server-side diff parity | Design-gated research | Decide between delegated live server calls and a separate local simulation model; document limitations before implementation. |
| Kubernetes defaulting | Design-gated research | Kubernetes-version contract, schema/defaulting source, fixture matrix, and clear separation from live cluster calls. |
| Admission mutation | Design-gated research | Mutating webhook model or explicit live dry-run model; never approximate silently. |
| Server-side apply ownership prediction | Design-gated research | Field-manager assumptions, schema requirements, managed-field source, and documented divergence from live ownership. |

## Acceptance Gates For Future Exceptions

Any future live-runtime implementation plan must include:

1. A new or updated design spec approved before code changes.
2. A CLI/API surface that is separate from default render/diff commands.
3. Explicit opt-in flags or constructors for all live credentials and endpoints.
4. A rule forbidding ambient kubeconfig or Argo CD config reads unless a design
   update explicitly permits a named flag and documents the risk.
5. Fake Kubernetes or fake Argo CD server tests that run without network.
6. A read-only operation inventory for every live client call.
7. Redaction tests for server URLs, bearer tokens, client certificates, cluster
   names when sensitive, and returned API errors.
8. Exit-code and diagnostic behavior that keeps CI output parseable.
9. Documentation updates in `AGENTS.md`, `README.md`, `docs/usage.md`, and
   `docs/compatibility.md`.
10. Review-agent approval that confirms default behavior still has no
    cluster, Argo CD server, or shellout dependency.

## Explicit Non-Goals

- No live behavior without an approved design update.
- No hidden validation against the user's current cluster.
- No server-side diff approximation that claims parity without either a live
  server call or a separately specified simulation model.
- No mutation of Kubernetes resources, Argo CD resources, or GitOps repository
  files.
- No required live runtime access in CI.
