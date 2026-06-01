# Security Policy

## Reporting a Vulnerability

Please use GitHub's private vulnerability reporting feature to report security
issues:

Repository > Security tab > Advisories > Report a vulnerability

Do not open public issues for security vulnerabilities.

## Scope

The following areas are in scope for vulnerability reports:

- Credential redaction and source authentication handling.
- Repository, Helm, OCI, remote Kustomize, and cache acquisition behavior.
- Trusted plugin policy, opt-in exec plugin behavior, and plugin sandbox
  boundaries.
- Cache path containment, symlink handling, and filesystem safety.
- GitHub Actions, setup action, PR action, release artifacts, and container
  image distribution.
- Dependency supply chain for Go modules, tools, actions, and container base
  images.

## Dependency Management

[Renovate](https://docs.renovatebot.com/) automates dependency updates for Go
modules, tools, container images, and GitHub Actions. GitHub Actions are pinned
by commit SHA with version comments, and release artifacts are built through
the repository release workflows.
