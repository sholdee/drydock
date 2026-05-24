# Release And Upgrade Notes

`drydock` is a single static Go binary and embeddable Go module. Release
artifacts should preserve the offline core contract: render and diff from
checked-out files plus explicit caches, without requiring a cluster, Argo CD
server, `kubectl`, `argocd`, Helm/Kustomize command-line tools, or external
rendering processes.

## Argo CD Dependency Upgrades

When upgrading the Argo CD module:

1. Review API changes for `Application`, `ApplicationSet`, `AppProject`,
   source rendering options, and diff customization semantics.
2. Run `go test ./...`, `go vet ./...`, and
   `golangci-lint run --allow-parallel-runners`.
3. Run focused compatibility tests for ApplicationSet generators, global
   settings parsing, AppProject validation, and source acquisition.
4. Update `docs/compatibility.md` in the same change.

## Cache Compatibility

Remote Git, Helm chart, and remote Kustomize cache keys must continue to avoid
credential material. If cache key formats change, document whether existing
cache entries are ignored, migrated, or pruned. Do not write cache data under a
GitOps repository tree.

Cache event reporting is a compatibility surface for source type, action,
redacted target, revision, cache hit, offline, refresh, and error fields.
Events must not expose credentials, query tokens, SSH private keys,
passphrases, or raw repository URLs with embedded secrets.

## GitHub Action

A composite install action is deferred until release artifact names and version
channels are stable. Until then, CI should build or test from source.
