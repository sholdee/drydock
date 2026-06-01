# Contributing

Thanks for your interest in drydock. Pull requests are welcome for focused bug
fixes, compatibility improvements, documentation, and features that preserve
the runtime-offline product contract.

## Setup

Install the pinned toolchain with mise:

```bash
mise install
```

Useful local checks:

```bash
mise run test
mise run test-race
mise run lint
mise run markdownlint
mise run docs-check
mise run ci
```

Use the smallest check that proves your change. Run `mise run ci` before larger
or risky pull requests.

## Pull Request Guidelines

- Keep pull requests focused and explain the operator-facing impact.
- Follow [Conventional Commits](https://www.conventionalcommits.org/) for
  commit messages.
- Update documentation when changing CLI behavior, compatibility, output,
  actions, source acquisition, plugin policy, or release behavior.
- Run `go mod tidy` when changing Go dependencies.
- Pin GitHub Actions by commit SHA with a version comment when workflows
  change.
- Do not add default shellouts to `kubectl`, `argocd`, Helm, Kustomize, or
  config management plugins.
- Do not add live Argo CD or Kubernetes runtime requirements to default render,
  test, diff, image, or diagnostic paths.
- Keep credentials, repository auth, Secret values, and credential-bearing URLs
  redacted from logs and diagnostics.

## Runtime-Offline Contract

drydock's default workflows analyze Argo CD desired state without a running
Argo CD instance or Kubernetes cluster. Source acquisition can still fetch
declared Git, HTTP Helm, OCI Helm, or remote Kustomize inputs unless
`--offline` is set.

Changes that alter this boundary need explicit design discussion before
implementation. Start with the design and compatibility docs:

- [Design](docs/design.md)
- [Compatibility](docs/compatibility.md)
- [Source acquisition](docs/source-acquisition.md)
- [Plugin policy](docs/plugin-policy.md)

## Maintainer Workflows

release-please manages releases from Conventional Commit history. The
documentation site deploys through GitHub Pages. The Argo CD render parity
smoke is manual maintainer validation and a selective CI gate for render parity
fixtures or semantic-rendering dependency changes.

These workflows are described in [release notes](docs/release.md) and the
repository workflows.

## References

- See [SECURITY.md](SECURITY.md) for vulnerability reporting.
- See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards.
- See the documentation site for operator workflows and reference material.
