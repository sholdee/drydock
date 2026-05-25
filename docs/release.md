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

The repository includes an optional composite install action at
`.github/actions/setup-drydock`. It is release metadata only; it does not
change the default static binary, `--offline` behavior, or local render/diff
runtime contract.

The action requires an explicit semantic-version `version` tag input and
rejects `latest`. It builds versioned GitHub Release URLs for the current runner
OS/architecture and never uses `curl | sh`. Supported runner pairs are Linux
and macOS on `amd64` and `arm64`.

Expected release artifact names are:

- `drydock_linux-amd64.tar.gz`
- `drydock_linux-arm64.tar.gz`
- `drydock_darwin-amd64.tar.gz`
- `drydock_darwin-arm64.tar.gz`

If the release publishes `checksums.txt`, the action verifies the selected
artifact with `sha256sum --check` when available, or `shasum -a 256 -c` on
runners where `shasum` is the available SHA-256 verifier. Only a 404 for
`checksums.txt` can be treated as an intentionally unpublished checksum
artifact, and only when `allow-unverified: true` is set. By default, missing
checksums and checksum download failures fail the action.

Example:

```yaml
- uses: ./.github/actions/setup-drydock
  with:
    version: v0.1.0
    install-dir: /usr/local/bin
```

Public required CI should continue to build and test from source unless a
workflow intentionally opts into installing a released binary.
