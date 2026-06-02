---
title: GitHub Actions
---

drydock publishes two repository-local composite actions:

- `sholdee/drydock/setup-action`: install a released `drydock` binary.
- `sholdee/drydock/pr-action`: install `drydock`, run PR validation, publish
  diff artifacts, and optionally maintain sticky PR comments.

The PR action is a convenience wrapper around the CLI. It does not run image
pull verification and does not pass GitHub tokens to the `drydock` subprocess.

## Setup Action

Use the setup action when you want to own every drydock command in workflow
YAML:

```yaml
- uses: sholdee/drydock/setup-action@main
  with:
    version: vX.Y.Z
```

The action accepts `latest`, `vX.Y.Z`, or bare `X.Y.Z`. It verifies the release
archive with `checksums.txt` unless `allow-unverified: "true"` is set.

By default, the setup action caches the verified release archive by resolved
version, runner OS/architecture, release repository, and checksum. `latest` is
resolved to a concrete tag before cache lookup, so a new release receives a new
cache key. Binary caching is skipped when `allow-unverified: "true"` is set.
Disable it with `cache-binary: "false"` if a workflow wants every run to
download the archive.

## Manual CLI Workflow

Use `setup-action` directly when you want a minimal workflow and prefer to own
the drydock commands yourself:

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: sholdee/drydock/setup-action@main
        with:
          version: vX.Y.Z
      - run: drydock test apps --path .
      - run: drydock diff apps --path . --ref-orig origin/${{ github.base_ref }}
```

## Pull Request Action

Use the PR action when you want the standard render test, manifest diff, image
diff, source cache, artifacts, and sticky comments:

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read
  pull-requests: write

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: sholdee/drydock/pr-action@main
        with:
          version: vX.Y.Z
```

By default the action:

- checks out the pull request head with credentials not persisted into Git;
- restores or saves a verified drydock binary archive when installation is
  enabled;
- fetches the pull request base branch for ref-based diffs;
- runs `drydock test apps --path .`;
- runs `drydock diff apps --repo . --ref HEAD --ref-orig origin/<base>`;
- records current-only image additions from `drydock diff images` for the added
  image artifact;
- writes image diff comments with drydock markdown output, including removed
  image references when present;
- uses drydock source caches under the runner temp directory;
- uploads full diff artifacts when differences are found;
- writes sticky PR comments for trusted same-repository pull requests.

Fork pull requests do not restore or save drydock source caches by default,
because source caches can contain private repository or chart material. They
also skip PR comments by default. Set `cache-untrusted-restore: "true"` only if
restoring cache contents into fork PR runs is acceptable for that repository.
Cache save remains disabled for fork PRs.

### Reporting and gating

By default, `pr-action` fails render errors and reports manifest or image diffs
through comments and artifacts without failing the workflow. To make diffs a
gate, set `strict`, `strict-changed-only`, `fail-on-diff`, and optionally
`fail-on-image-diff`.

Keep `changed-only-include` and `changed-only-ignore` narrow; ignored paths
cannot trigger Application renders.

## Authentication

The action accepts either a normal token or GitHub App credentials:

```yaml
- uses: sholdee/drydock/pr-action@main
  with:
    version: vX.Y.Z
    github-token: ${{ secrets.DRYDOCK_TOKEN }}
```

```yaml
- uses: sholdee/drydock/pr-action@main
  with:
    version: vX.Y.Z
    github-app-client-id: ${{ secrets.DRYDOCK_APP_CLIENT_ID }}
    github-app-private-key: ${{ secrets.DRYDOCK_APP_PRIVATE_KEY }}
```

`github-app-id` remains available as a legacy fallback for existing workflows,
but new workflows should use the GitHub App client ID.

The token is used for release downloads, checkout, baseline fetch, and PR
comments. It is not exported to `drydock test`, `drydock diff`, cache contents,
or uploaded artifacts.

Binary caching is separate from drydock source caching. Binary cache entries
contain only the released `drydock` archive and are keyed by release checksum.
Source cache entries contain fetched Git, Helm, and remote Kustomize sources for
the repository under test.

## Common Inputs

| Input | Default | Purpose |
| --- | --- | --- |
| `version` | `latest` | Released drydock version to install. |
| `install` | `true` | Install drydock before running. |
| `drydock-bin` | `drydock` | Preinstalled drydock binary when `install` is `false`. |
| `release-repository` | `sholdee/drydock` | Repository that publishes drydock release artifacts. |
| `cache-binary` | `true` | Restore and save the verified release archive when installing drydock. |
| `cache-binary-key-suffix` | `v1` | Invalidate generated binary cache keys. |
| `path` | `.` | Repository path for `drydock test apps`. |
| `repo` | `.` | Local Git repository path for ref-based diffs. |
| `base-ref` | PR base | Baseline branch name for diff commands. |
| `head-ref` | `HEAD` | Current ref for diff commands after checkout. |
| `skip-secrets` | `true` | Omit Secret resources from output and diffs. |
| `strict` | `false` | Promote diagnostics to errors. |
| `strict-changed-only` | `false` | Fail ambiguous changed-only ownership. |
| `changed-only` | drydock default | Override changed-only with `true` or `false`. |
| `changed-only-include` | unset | Newline-delimited changed-only include globs. |
| `changed-only-ignore` | unset | Newline-delimited changed-only ignore globs. |
| `show-ignored-fields` | `false` | Show default ignored diff fields. |
| `comment-mode` | `both` | One of `none`, `diff`, `images`, or `both`. |
| `fail-on-diff` | `false` | Fail when manifest differences are detected. |
| `fail-on-image-diff` | `false` | Fail when image differences are detected. |

Newline-delimited inputs are passed as repeated drydock flags:

- `changed-only-include`
- `changed-only-ignore`
- `discover-kustomize`
- `repo-map`
- `extra-test-args`
- `extra-diff-args`
- `extra-image-diff-args`

`changed-only-include` and `changed-only-ignore` apply only to `diff apps` and
`diff images`; `test apps` still validates the selected repository path. Use
them to keep non-GitOps workflow churn from forcing full-fleet diffs, while
keeping patterns narrow enough that real render inputs are not hidden.

Only use `extra-*` inputs with trusted workflow configuration. The action
passes them as arguments without `eval`, but they still change drydock behavior.

## Outputs

| Output | Meaning |
| --- | --- |
| `has-diff` | Rendered manifest differences were detected. |
| `has-images` | Current-only image references were detected. |
| `has-image-diff` | Any image difference was detected, including removed-only diffs. |
| `render-status` | `passed`, `failed`, or `skipped`. |
| `diff-path` | Local path to the rendered manifest diff file. |
| `images-path` | Local path to the current-only image list. |
| `trusted-context` | Whether cache save and PR comments were allowed. |
