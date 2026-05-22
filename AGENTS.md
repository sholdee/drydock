# AGENTS.md - argocd-local

## What This Is

`argocd-local` is an independent Go CLI for offline Argo CD GitOps repository
analysis. The first product goal is desired-vs-desired PR diffing: render Argo
CD Applications from a current tree and a baseline tree, then show what desired
Kubernetes manifests changed.

It is not a live-cluster diff tool in the MVP. Do not add Kubernetes API
dependencies, live Argo CD server calls, or shellout-based render paths unless
the design spec is updated first.

## Current Design

Read the design spec before substantive changes:

- `docs/superpowers/specs/2026-05-22-argocd-local-design.md`

## Repository Layout

- `cmd/argocd-local/`: binary entrypoint
- `internal/cli/`: Cobra command tree and exit-code mapping
- `internal/config/`: Argo settings discovery and merge model
- `internal/discovery/`: repository scanning for Applications, ApplicationSets, and settings
- `internal/appset/`: supported local ApplicationSet generators
- `internal/source/`: repository URL normalization, repo maps, and network opt-in
- `internal/render/`: renderer interfaces and source renderers
- `internal/app/`: Application source planning and multi-source combine behavior
- `internal/change/`: changed-file detection and Application input indexing
- `internal/diff/`: parent-aware desired-vs-desired diffs and image diffs
- `internal/diagnostic/`: warnings, provenance, and strict-mode escalation
- `testdata/`: minimal fixtures only

## Validation

Run the smallest check that covers your change:

```bash
go test ./...
go vet ./...
golangci-lint run
markdownlint-cli2 '**/*.md'
```

If a tool is not installed locally, say so in your final response.

## Hard Constraints

- Default workflows must not require `helm`, `kustomize`, `kubectl`, or `argocd` on `PATH`.
- Network access is opt-in through `--allow-network`; prefer `--repo-map`.
- Do not print secret data. Repository Secrets may provide non-sensitive metadata only.
- `spec.sources` takes precedence over `spec.source`.
- Changed-only mode must not use Flux-style "most-specific owner wins"; Argo CD may have overlapping Applications.
- Server-side diff/apply settings are diagnostics in offline mode, not executable behavior.

## Agent Maintenance Rule

When you add, remove, or materially change a package, command, renderer,
setting provider, diagnostic, validation command, or supported Argo CD feature,
update this file in the same change.
